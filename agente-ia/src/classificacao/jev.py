"""Cliente do JEV (TypeSafe System One) e a montagem do estado enviado a ele.

- `estado_para_jev()`: transcrição compacta, com PII MASCARADA (CPF, telefone,
  e-mail, nome) — regra 5 do CLAUDE.md: nada em claro sai para a TypeSafe.
- `ClienteJEV`: protocolo mínimo (`perguntar`). `JEVTypeSafe` é a implementação
  real (SDK `typesafe-sdk`, importado só na primeira chamada); `FakeJEV`
  (tests/fakes.py) é a dos testes. O roteador só conhece o protocolo.
- `Disjuntor` e `JanelaDisponibilidade`: o JEV fora do ar nunca derruba o turno
  (fail-soft → fallback LLM) e a taxa de indisponibilidade vira alerta.

TODO(P8): chave `TYPESAFE_API_KEY`, limites e desempenho em pt-BR — o cliente
real só é exercitado em testes `-m jev`.
"""

from __future__ import annotations

import asyncio
import logging
import re
import time
from collections import deque
from collections.abc import Callable, Mapping, Sequence
from typing import Any, Protocol

from src.classificacao.config import ConfigClassificacao
from src.classificacao.tipos import (
    Choice,
    Mensagem,
    Noul,
    Pergunta,
    RespostaChoice,
    RespostaJEV,
    RespostaScore,
    Score,
)

logger = logging.getLogger(__name__)


class JEVIndisponivel(RuntimeError):
    """O JEV não respondeu (sem chave, timeout, 5xx, 429 esgotado, disjuntor aberto)."""


class ClienteJEV(Protocol):
    async def perguntar(self, estado: str, perguntas: Mapping[str, Pergunta]) -> RespostaJEV: ...


# ---------------------------------------------------------------------------
# Estado enviado ao JEV (PII mascarada)
# ---------------------------------------------------------------------------

#: Partes limitadas (RFC: local ≤ 64, rótulo ≤ 63): sem isso a varredura é quadrática
#: em palavras longas sem espaço e trava o event loop com texto hostil.
_EMAIL = re.compile(r"[\w.+-]{1,64}@[\w-]{1,63}(?:\.[\w-]{1,63}){1,8}")
#: Palavras do domínio que um nome de contato do WhatsApp pode conter ("Net Fibra",
#: "Luz"): mascará-las apagaria o próprio assunto do cliente no estado enviado ao JEV.
_PALAVRAS_DO_DOMINIO = frozenset(
    {
        "net",
        "internet",
        "fibra",
        "wifi",
        "wi-fi",
        "luz",
        "sinal",
        "plano",
        "tv",
        "telecom",
        "conexão",
        "conexao",
        "rede",
        "link",
        "digital",
        "online",
    }
)
#: Telefone brasileiro: DDI/DDD opcionais, 9º dígito opcional, separadores livres.
_TELEFONE = re.compile(r"(?<!\d)(?:\+?55[\s-]?)?\(?\d{2}\)?[\s-]?9?\d{4}[-\s]?\d{4}(?!\d)")
#: Sequência de ≥ 9 dígitos com um separador opcional entre eles (CPF, CNPJ,
#: nº de contrato, telefone sem formato). Protocolos curtos (4 dígitos) passam.
_SEQUENCIA_NUMERICA = re.compile(r"(?<!\d)(?:\d[ .\-/]?){8,}\d(?!\d)")
_CPF = re.compile(r"\d{3}\.?\d{3}\.?\d{3}-?\d{2}")
#: "meu nome é Maria Souza", "me chamo João" — o nome após a apresentação.
_APRESENTACAO = re.compile(
    r"(?i:\b(?:meu nome [ée]|me chamo|aqui [ée] (?:o|a)|sou (?:o|a))\s+)"
    r"[A-ZÀ-Ý][\wÀ-ÿ'-]+(?:\s+[A-ZÀ-Ý][\wÀ-ÿ'-]+){0,3}"
)
_DOCUMENTO_EXTERNO = re.compile(
    r"<documento_externo>.*?</documento_externo>", re.IGNORECASE | re.DOTALL
)
_ESPACOS = re.compile(r"\s+")

#: Aproximação de caracteres por token para o corte de 2.000 tokens (pt-BR ≈ 3,5–4).
CARACTERES_POR_TOKEN = 4


def _substituir_sequencia(m: re.Match[str]) -> str:
    bruto = m.group(0)
    digitos = re.sub(r"\D", "", bruto)
    if len(digitos) == 14:
        return "<CNPJ>"
    if len(digitos) == 11 and _CPF.fullmatch(bruto.replace(" ", "")):
        return "<CPF>"
    return "<NUM>"


def mascarar_pii(texto: str, nomes: Sequence[str] = (), *, apresentacao: bool = True) -> str:
    """Troca e-mail, telefone, CPF/CNPJ/nº longos e nomes por tokens (`<CPF>`, `<TEL>`…).

    `nomes`: nomes conhecidos do contato (ex.: nome no WhatsApp); cada palavra
    com 3+ letras vira `<NOME>`. Com `apresentacao=True` também mascara o nome
    após "meu nome é …" (só faz sentido na fala do cliente: a Sofia se apresenta
    como "sou a Sofia"). Determinístico; não há como o LLM influenciar.
    """
    if not texto:
        return ""
    saida = _DOCUMENTO_EXTERNO.sub("", texto)
    saida = _EMAIL.sub("<EMAIL>", saida)
    saida = _TELEFONE.sub("<TEL>", saida)
    saida = _SEQUENCIA_NUMERICA.sub(_substituir_sequencia, saida)
    if apresentacao:
        saida = _APRESENTACAO.sub(_mascarar_apresentacao, saida)
    palavras = {
        p
        for nome in nomes
        for p in re.split(r"\s+", nome.strip())
        if len(p) >= 3 and p.lower() not in _PALAVRAS_DO_DOMINIO
    }
    for palavra in sorted(palavras, key=len, reverse=True):
        saida = re.sub(rf"(?<!\w){re.escape(palavra)}(?!\w)", "<NOME>", saida, flags=re.IGNORECASE)
    return saida


def _mascarar_apresentacao(m: re.Match[str]) -> str:
    """Mantém a frase de apresentação ("meu nome é") e troca só o nome."""
    trecho = m.group(0)
    prefixo = re.match(
        r"(?i:\b(?:meu nome [ée]|me chamo|aqui [ée] (?:o|a)|sou (?:o|a))\s+)", trecho
    )
    return (prefixo.group(0) if prefixo else "") + "<NOME>"


def _linha(m: Mensagem, nomes: Sequence[str], limite: int) -> str:
    rotulo = "Cliente" if m.autor == "cliente" else "Sofia"
    # Corta ANTES de mascarar: o custo das regex não pode depender do tamanho que o cliente mandar.
    bruto = m.texto[:limite]
    texto = _ESPACOS.sub(" ", mascarar_pii(bruto, nomes, apresentacao=m.autor == "cliente")).strip()
    return f"{rotulo}: {texto}"


def estado_para_jev(
    mensagens: Sequence[Mensagem],
    *,
    janela: int = 6,
    max_tokens: int = 2000,
    nomes: Sequence[str] = (),
) -> str:
    """Transcrição compacta que vai ao JEV (docs/plano/02, "As perguntas").

    - últimas `janela` mensagens (C-JEV1 — hipótese; INCLUI a atual), prefixadas
      `Cliente:`/`Sofia:`; a última aparece como `Mensagem atual:`;
    - PII mascarada (`mascarar_pii`); nunca o system prompt nem conteúdo de
      `<documento_externo>` (removido);
    - truncada a `max_tokens` (≈ 4 caracteres/token): descarta as mensagens mais
      antigas primeiro e, em último caso, corta a própria mensagem atual.
    """
    recentes = [m for m in mensagens if m.texto and m.texto.strip()][-max(1, janela) :]
    if not recentes:
        return "Mensagem atual: "
    *historico, atual = recentes
    limite = max_tokens * CARACTERES_POR_TOKEN
    linhas = [_linha(m, nomes, limite) for m in historico]
    texto_atual = _linha(atual, nomes, limite).split(": ", 1)[1]

    orcamento = max_tokens * CARACTERES_POR_TOKEN
    rotulo_atual = "Mensagem atual: "

    def montar() -> str:
        return "\n".join([*linhas, rotulo_atual + texto_atual])

    while linhas and len(montar()) > orcamento:
        linhas.pop(0)
    if len(montar()) > orcamento:
        texto_atual = texto_atual[: max(0, orcamento - len(rotulo_atual))]
    return montar()


def mensagens_de_linhas(linhas: Sequence[str]) -> list[Mensagem]:
    """`["Cliente: oi", "Sofia: olá"]` → `[Mensagem]` (formato dos datasets de eval).

    Linhas sem prefixo conhecido são tratadas como fala do cliente.
    """
    saida: list[Mensagem] = []
    for linha in linhas:
        bruta = linha.strip()
        if bruta.lower().startswith("sofia:"):
            saida.append(Mensagem("sofia", bruta[6:].strip()))
        elif bruta.lower().startswith("cliente:"):
            saida.append(Mensagem("cliente", bruta[8:].strip()))
        elif bruta:
            saida.append(Mensagem("cliente", bruta))
    return saida


# ---------------------------------------------------------------------------
# Disjuntor e janela de disponibilidade
# ---------------------------------------------------------------------------


class Disjuntor:
    """Após `falhas_max` falhas seguidas, recusa chamadas por `pausa_s` segundos.

    Passada a pausa deixa UMA tentativa passar (meio-aberto): se falhar, reabre
    na hora; se der certo, fecha. Evita pagar 2 s × retentativas por turno
    enquanto o JEV está fora.
    """

    def __init__(
        self,
        falhas_max: int = 5,
        pausa_s: float = 30.0,
        relogio: Callable[[], float] = time.monotonic,
    ) -> None:
        self._falhas_max = falhas_max
        self._pausa_s = pausa_s
        self._relogio = relogio
        self._falhas = 0
        self._aberto_ate = 0.0

    @property
    def aberto(self) -> bool:
        return self._falhas >= self._falhas_max and self._relogio() < self._aberto_ate

    def registrar_sucesso(self) -> None:
        self._falhas = 0
        self._aberto_ate = 0.0

    def registrar_falha(self) -> None:
        self._falhas += 1
        if self._falhas >= self._falhas_max:
            self._aberto_ate = self._relogio() + self._pausa_s


class JanelaDisponibilidade:
    """Fração de turnos sem JEV nos últimos `janela_s` segundos (métrica `jev_indisponivel`)."""

    #: Amostras mínimas antes de alertar (evita alarme com 1 turno de 1).
    MINIMO_AMOSTRAS = 20

    def __init__(
        self,
        janela_s: float = 900.0,
        limite: float = 0.05,
        relogio: Callable[[], float] = time.monotonic,
    ) -> None:
        self._janela_s = janela_s
        self._limite = limite
        self._relogio = relogio
        self._amostras: deque[tuple[float, bool]] = deque()
        self.total_indisponivel = 0

    def registrar(self, disponivel: bool) -> bool:
        """Registra o turno; devolve True quando a taxa passou do limite (alerta)."""
        agora = self._relogio()
        self._amostras.append((agora, disponivel))
        if not disponivel:
            self.total_indisponivel += 1
        while self._amostras and agora - self._amostras[0][0] > self._janela_s:
            self._amostras.popleft()
        return self.taxa_indisponivel > self._limite and len(self._amostras) >= self.MINIMO_AMOSTRAS

    @property
    def taxa_indisponivel(self) -> float:
        if not self._amostras:
            return 0.0
        return sum(1 for _, ok in self._amostras if not ok) / len(self._amostras)


# ---------------------------------------------------------------------------
# Cliente real (SDK da TypeSafe)
# ---------------------------------------------------------------------------


def normalizar_resposta(resposta: Any, modelo: str, latencia_ms: int) -> RespostaJEV:
    """`SystemOneResponse` do SDK → `RespostaJEV` (duck-typed; testável sem o SDK).

    Score: o nível é o de MAIOR probabilidade (o `score` do SDK é a média
    ponderada e pode cair entre níveis); sem probabilidades, `round(score)`.
    """
    choices = {
        nome: RespostaChoice(
            escolha=str(r.choice),
            confianca=float(r.confidence),
            probabilidades={str(k): float(v) for k, v in (r.probabilities or {}).items()},
        )
        for nome, r in (getattr(resposta, "choices", None) or {}).items()
    }
    scores: dict[str, RespostaScore] = {}
    for nome, r in (getattr(resposta, "scores", None) or {}).items():
        probs = {int(k): float(v) for k, v in (r.probabilities or {}).items()}
        nivel = max(probs, key=lambda k: probs[k]) if probs else round(float(r.score))
        scores[nome] = RespostaScore(
            nivel=int(nivel), confianca=float(r.confidence), score=float(r.score)
        )
    nouls = {nome: float(r.noul) for nome, r in (getattr(resposta, "nouls", None) or {}).items()}
    return RespostaJEV(
        choices=choices,
        scores=scores,
        nouls=nouls,
        modelo=str(getattr(resposta, "model", None) or modelo),
        latencia_ms=latencia_ms,
    )


class JEVTypeSafe:
    """`ClienteJEV` sobre o `AsyncTypeSafeClient` (timeout 2 s, `RetryPolicy(max_retries=2)`).

    - Modelo PINADO (`JEV_MODEL`): os limiares foram calibrados para ele.
    - Qualquer falha (sem chave, SDK ausente, timeout, 5xx, 429 esgotado,
      disjuntor aberto) vira `JEVIndisponivel` — quem chama cai no fallback.
    - O cliente HTTP é criado sob demanda e recriado se o event loop mudar
      (o `httpx` async não atravessa loops).
    """

    def __init__(
        self,
        config: ConfigClassificacao,
        *,
        disjuntor: Disjuntor | None = None,
        cliente_factory: Callable[[], Any] | None = None,
    ) -> None:
        self._config = config
        self._disjuntor = disjuntor or Disjuntor(
            config.jev_disjuntor_falhas, config.jev_disjuntor_pausa
        )
        self._cliente_factory = cliente_factory
        self._cliente: Any = None
        self._loop: asyncio.AbstractEventLoop | None = None

    def _obter_cliente(self) -> Any:
        loop = asyncio.get_running_loop()
        if self._cliente is None or self._loop is not loop:
            if self._cliente_factory is not None:
                self._cliente = self._cliente_factory()
            else:
                try:
                    from typesafe_sdk import AsyncTypeSafeClient, RetryPolicy
                except ImportError as exc:
                    raise JEVIndisponivel("typesafe-sdk não instalado") from exc
                self._cliente = AsyncTypeSafeClient(
                    api_key=self._config.typesafe_api_key,
                    model=self._config.jev_model,
                    timeout=self._config.jev_timeout,
                    retry=RetryPolicy(
                        max_retries=self._config.jev_max_retries,
                        timeout=self._config.jev_prazo_total,
                    ),
                )
            self._loop = loop
        return self._cliente

    @staticmethod
    def _perguntas_sdk(perguntas: Mapping[str, Pergunta]) -> dict[str, Any]:
        try:
            from typesafe_sdk import Choice as SdkChoice
            from typesafe_sdk import Noul as SdkNoul
            from typesafe_sdk import Score as SdkScore
        except ImportError as exc:
            raise JEVIndisponivel("typesafe-sdk não instalado") from exc
        convertidas: dict[str, Any] = {}
        for nome, p in perguntas.items():
            if isinstance(p, Choice):
                convertidas[nome] = SdkChoice(instructions=p.instrucoes, criteria=dict(p.criterios))
            elif isinstance(p, Score):
                convertidas[nome] = SdkScore(instructions=p.instrucoes, criteria=list(p.criterios))
            elif isinstance(p, Noul):
                convertidas[nome] = SdkNoul(instructions=p.instrucoes)
        return convertidas

    async def perguntar(self, estado: str, perguntas: Mapping[str, Pergunta]) -> RespostaJEV:
        if not self._config.typesafe_api_key:
            raise JEVIndisponivel("TYPESAFE_API_KEY não configurada (P8)")
        if self._disjuntor.aberto:
            raise JEVIndisponivel("disjuntor aberto")
        inicio = time.perf_counter()
        try:
            cliente = self._obter_cliente()
            resposta = await asyncio.wait_for(
                cliente.system_one(state=estado, questions=self._perguntas_sdk(perguntas)),
                timeout=self._config.jev_prazo_total,
            )
        except JEVIndisponivel:
            raise
        except Exception as exc:
            self._disjuntor.registrar_falha()
            raise JEVIndisponivel(f"{type(exc).__name__}: {exc}") from exc
        self._disjuntor.registrar_sucesso()
        latencia_ms = int((time.perf_counter() - inicio) * 1000)
        return normalizar_resposta(resposta, self._config.jev_model, latencia_ms)
