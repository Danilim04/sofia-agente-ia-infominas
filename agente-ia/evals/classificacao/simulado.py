# mypy: disallow-untyped-defs, disallow-incomplete-defs, check-untyped-defs, disallow-any-generics, warn-return-any, strict-equality
"""JEV e fallback LLM SIMULADOS para o modo offline (`--offline`): sem rede, sem chave, determinísticos.

NÃO É O JEV. É um classificador lexical de brinquedo (casamento de radicais com a
descrição/exemplos que a própria taxonomia manda ao JEV + regras de palavra-chave
para intenção, frustração e segurança) que existe para duas coisas:

1. exercitar o `classificar()` REAL de ponta a ponta no CI (estado montado por
   `estado_para_jev` com a janela pedida, mascaramento de PII, roteador,
   limiares, fallback, esclarecimento) e o relatório (calibração em 10 bins,
   cobertura/acurácia@limiar, matriz de confusão);
2. fazer a comparação de janelas (1/3/6/10) mexer de verdade: ele só enxerga o
   que o `estado` enviado contém, então janela menor = menos contexto.

Os números que sai dele medem o ENCANAMENTO do runner, nunca o desempenho do
JEV (P8) — todo relatório offline carrega essa faixa no topo. Ele não vê o
`esperado` do dataset (nada de gabarito vazando) e usa as MESMAS classes `FakeJEV`
e `FakeFallbackLLM` de `tests/fakes.py` como base.
"""

from __future__ import annotations

import math
import re
import unicodedata
import zlib
from collections.abc import Mapping
from typing import Any, Final

from src.classificacao.fallback_llm import ResultadoFallback
from src.classificacao.tipos import (
    INTENCAO_CONFIRMACAO,
    INTENCAO_DUVIDA,
    INTENCAO_OUTRO,
    INTENCAO_PEDIR_ATENDENTE,
    INTENCAO_RECLAMACAO,
    INTENCAO_SOCIAL,
    INTENCAO_STATUS,
    INTENCOES,
    Pergunta,
    RespostaChoice,
    RespostaJEV,
    RespostaScore,
)
from tests.fakes import FakeFallbackLLM, FakeJEV

CATEGORIA_INDEFINIDA: Final = "indefinida"
MODELO_SIMULADO: Final = "jev-simulado-lexical"

_STOP: Final[frozenset[str]] = frozenset(
    "para como mais muito esta esse essa isso aqui você voce meu minha seu sua dele dela "
    "uma uns umas que com por nos nas dos das mas tem estou estão estao tenho ter foi "
    "ser são sao vcs vocês pra pro não nao sim já ja só so até ate quando onde qual".split()
)
_STOP_SEM_ACENTO: Final[frozenset[str]] = frozenset(
    unicodedata.normalize("NFKD", w).encode("ascii", "ignore").decode() for w in _STOP
)


def _sem_acento(texto: str) -> str:
    base = unicodedata.normalize("NFKD", texto.lower())
    return "".join(c for c in base if not unicodedata.combining(c))


def _radicais(texto: str) -> list[str]:
    """Palavras úteis reduzidas aos 5 primeiros caracteres (absorve plural, flexão e parte dos erros de digitação)."""
    palavras = re.findall(r"[a-z0-9]{3,}", _sem_acento(texto))
    return [p[:5] for p in palavras if p not in _STOP_SEM_ACENTO]


def _softmax(logits: Mapping[str, float]) -> dict[str, float]:
    topo = max(logits.values())
    exps = {k: math.exp(v - topo) for k, v in logits.items()}
    total = sum(exps.values())
    return {k: v / total for k, v in exps.items()}


def _escolha(logits: Mapping[str, float], escala: float = 1.0) -> RespostaChoice:
    """Choice com a confiança da doc do JEV: (p_max − 1/n) / (1 − 1/n)."""
    probs = _softmax({k: escala * v for k, v in logits.items()})
    escolha = max(probs, key=lambda k: probs[k])
    n = len(probs)
    confianca = max(0.0, (probs[escolha] - 1 / n) / (1 - 1 / n))
    return RespostaChoice(
        escolha=escolha,
        confianca=round(confianca, 4),
        probabilidades={k: round(v, 4) for k, v in probs.items()},
    )


# ---------------------------------------------------------------------------
# Leitura do estado enviado
# ---------------------------------------------------------------------------


def _falas_do_cliente(estado: str) -> tuple[list[str], str]:
    """(histórico do cliente, mensagem atual) a partir do texto de `estado_para_jev`."""
    historico: list[str] = []
    atual = ""
    for linha in estado.splitlines():
        if linha.startswith("Mensagem atual:"):
            atual = linha.removeprefix("Mensagem atual:").strip()
        elif linha.startswith("Cliente:"):
            historico.append(linha.removeprefix("Cliente:").strip())
    return historico, atual


# ---------------------------------------------------------------------------
# Regras de palavra-chave (intenção, prioridade, frustração, segurança)
# ---------------------------------------------------------------------------

_RE_ATENDENTE = re.compile(
    r"atendente|humano|pessoa de verdade|gente de verdade|falar com (?:alguem|uma pessoa)|"
    r"me liga|ligar pra mim|nao quero (?:falar com )?robo|passa pra (?:um )?(?:atendente|alguem)"
)
_RE_STATUS = re.compile(
    r"protocolo|chamado|andamento|ninguem (?:veio|apareceu|retornou|me retornou)|ja abri|"
    r"abri um|cade o tecnico|nenhum retorno|sem retorno|previsao de (?:retorno|visita)"
)
_RE_DUVIDA = re.compile(
    r"^(?:como|quanto|qual|quais|quando|onde|tem |tem$|da pra|posso|pode |vcs vendem|voces vendem|"
    r"existe|o que|e possivel)|\?"
)
_RE_SOCIAL = re.compile(
    r"\b(?:oi|ola|opa|eai|e ai|bom dia|boa tarde|boa noite|obrigad[oa]|valeu|vlw|tchau|ate mais|"
    r"abraco|brigad[oa]|show)\b"
)
_RE_CONFIRMACAO = re.compile(
    r"^(?:sim|nao|ok|okay|pode|pode sim|isso|isso mesmo|certo|beleza|blz|claro|aham|uhum|"
    r"positivo|negativo|fechado|combinado|pode ser)[ .!]*$"
)
_RE_PROBLEMA = re.compile(
    r"caiu|caindo|cai |cai$|sem |nao (?:funciona|liga|abre|pega|conecta|chega|consigo|aparece|vem|veio)|"
    r"lent[oa]|trav|oscil|queimou|quebr|errad|duplicad|cobraram|cobrando|problema|defeito|"
    r"parou|nao tenho|nao esta|nao ta|piscando|vermelh|apagad|rompeu|demora"
)
_RE_URGENTE = re.compile(
    r"urgente|perigo|choque|faisc|cheiro de queimado|fio (?:solto|caido|no chao)|idoso|"
    r"hospital|saude|medico|home ?office|trabalho|prova|aula|empresa|loja|clinica"
)
_RE_PARADO = re.compile(
    r"sem (?:internet|sinal|conexao|net|wifi)|caiu|parou|nao tenho (?:internet|net|sinal)|"
    r"nada funciona|nao (?:abre|pega|conecta|liga)|luz vermelha|apagad|queimou|rompeu"
)
_RE_LIMITADO = re.compile(
    r"lent[oa]|trav|oscil|cai(?:ndo)? toda|fica caindo|instavel|demor|cobr|errad"
)
_RE_RAIVA_FORTE = re.compile(
    r"procon|reclame aqui|anatel|processo|advogad|palhacada|vergonha|absurdo|lixo|pessim|"
    r"ladr|roubo|golpe|enganad|nunca mais|cancel(?:ar|o|amento) (?:tudo|ja|agora)|inferno|merda"
)
_RE_RAIVA_MEDIA = re.compile(
    r"ninguem resolve|ninguem faz nada|cansei|cansad[oa]|de novo|toda hora|ja falei|ja liguei|"
    r"quantas vezes|faz (?:\d+|dias|semanas)|ate quando|desrespeito|descaso|irritad|puto|raiva"
)
_RE_IMPACIENTE = re.compile(
    r"urgente|rapido|logo|ainda nao|ate agora|desde ontem|ha dias|\bdias\b|hoje"
)
_RE_MALICIOSO = re.compile(
    r"ignor(?:e|a) (?:suas|as|todas)|esquec[ae] (?:suas|as|tudo)|prompt|instruc|"
    r"voce agora e|aja como|finja|modo (?:dev|desenvolvedor|admin|deus)|sou (?:o )?(?:dev|desenvolvedor|admin)|"
    r"system|sistema (?:interno|operacional)|jailbreak|senha do (?:admin|sistema)|revele|libere (?:tudo|todos)"
)
_RE_FORA_ESCOPO = re.compile(
    r"celular|pizza|futebol|jogo do|receita de|horoscopo|signo|loteria|politic|eleicao|"
    r"filme|novela|musica|bitcoin|carro usado|emprego|vaga de|namor|piada|clima|previsao do tempo"
)


def _logits_intencao(historico: list[str], atual: str) -> dict[str, float]:
    """Intenção pela mensagem atual; o histórico só desempata (o cliente já vinha reclamando)."""
    a = _sem_acento(atual)
    h = _sem_acento(" ".join(historico))
    curta = len(a.split()) <= 4
    logits = dict.fromkeys(INTENCOES, 0.0)
    logits[INTENCAO_OUTRO] = 1.0
    if _RE_ATENDENTE.search(a):
        logits[INTENCAO_PEDIR_ATENDENTE] += 4.0
    if _RE_STATUS.search(a):
        logits[INTENCAO_STATUS] += 3.0
    if _RE_CONFIRMACAO.match(a.strip()):
        logits[INTENCAO_CONFIRMACAO] += 4.5
    if _RE_SOCIAL.search(a) and len(a.split()) <= 6 and not _RE_PROBLEMA.search(a):
        logits[INTENCAO_SOCIAL] += 4.0
    if _RE_DUVIDA.search(a):
        logits[INTENCAO_DUVIDA] += 2.5
    if _RE_PROBLEMA.search(a):
        logits[INTENCAO_RECLAMACAO] += 3.5
        logits[INTENCAO_DUVIDA] -= 1.0
    elif _RE_PROBLEMA.search(h) and not curta and not _RE_DUVIDA.search(a):
        logits[INTENCAO_RECLAMACAO] += 1.5
    if not any(v > 1.0 for v in logits.values()):
        logits[INTENCAO_OUTRO] += 1.5
    return logits


def _nivel_prioridade(texto: str) -> tuple[int, float]:
    t = _sem_acento(texto)
    if _RE_URGENTE.search(t) and _RE_PARADO.search(t):
        return 3, 0.72
    if _RE_PARADO.search(t):
        return 2, 0.8
    if _RE_LIMITADO.search(t):
        return 1, 0.7
    return 0, 0.62


def _nivel_frustracao(atual: str) -> tuple[int, float]:
    t = _sem_acento(atual)
    letras = [c for c in atual if c.isalpha()]
    caixa_alta = len(letras) >= 8 and sum(c.isupper() for c in letras) / len(letras) > 0.6
    pontos = atual.count("!") + atual.count("?") // 2
    if _RE_RAIVA_FORTE.search(t):
        return 3, 0.82
    if _RE_RAIVA_MEDIA.search(t) or (caixa_alta and pontos >= 1) or pontos >= 3:
        return 2, 0.75
    if caixa_alta or _RE_IMPACIENTE.search(t) or pontos >= 1:
        return 1, 0.66
    return 0, 0.9


def _latencia_simulada_ms(estado: str) -> int:
    """80–299 ms, determinística pelo conteúdo (o relatório offline não pode mudar a cada rodada)."""
    return 80 + zlib.crc32(estado.encode("utf-8")) % 220


# ---------------------------------------------------------------------------
# Categoria (casamento de radicais com os critérios da taxonomia)
# ---------------------------------------------------------------------------


class _VocabularioCategorias:
    """Radicais por categoria (descrição + exemplos) com peso idf entre as categorias."""

    def __init__(self, criterios: Mapping[str, Any]) -> None:
        self.por_categoria: dict[str, set[str]] = {}
        for nome, criterio in criterios.items():
            if nome == CATEGORIA_INDEFINIDA:
                continue
            textos: list[str] = []
            if isinstance(criterio, Mapping):
                textos.append(str(criterio.get("what", "")))
                textos.extend(str(e) for e in criterio.get("examples", []) or [])
            elif isinstance(criterio, str):
                textos.append(criterio)
            self.por_categoria[nome] = set(_radicais(" ".join(textos)))
        n = max(1, len(self.por_categoria))
        contagem: dict[str, int] = {}
        for radicais in self.por_categoria.values():
            for r in radicais:
                contagem[r] = contagem.get(r, 0) + 1
        self.idf = {r: math.log(1 + n / c) for r, c in contagem.items()}

    def pontuar(self, historico: list[str], atual: str, peso_historico: float) -> dict[str, float]:
        pontos = dict.fromkeys(self.por_categoria, 0.0)
        for texto, peso in [(h, peso_historico) for h in historico] + [(atual, 3.0)]:
            for radical in set(_radicais(texto)):
                for nome, vocabulario in self.por_categoria.items():
                    if radical in vocabulario:
                        pontos[nome] += peso * self.idf[radical]
        return pontos

    def logits(
        self, historico: list[str], atual: str, peso_historico: float = 1.0
    ) -> dict[str, float]:
        pontos = self.pontuar(historico, atual, peso_historico)
        logits = {nome: 1.4 * p for nome, p in pontos.items()}
        logits[CATEGORIA_INDEFINIDA] = 2.0  # sem evidência suficiente, "indefinida" vence
        return logits


def _vocabulario(perguntas: Mapping[str, Pergunta]) -> _VocabularioCategorias:
    categoria = perguntas["categoria"]
    criterios = getattr(categoria, "criterios", {})
    return _VocabularioCategorias(criterios)


# ---------------------------------------------------------------------------
# Os dois fakes
# ---------------------------------------------------------------------------


class JEVSimulado(FakeJEV):
    """`ClienteJEV` que responde a partir do `estado` recebido (e só dele)."""

    async def perguntar(self, estado: str, perguntas: Mapping[str, Pergunta]) -> RespostaJEV:
        self.chamadas.append((estado, perguntas))
        historico, atual = _falas_do_cliente(estado)
        vocab = _vocabulario(perguntas)
        tudo = " ".join([*historico, atual])
        prio_nivel, prio_conf = _nivel_prioridade(tudo)
        fru_nivel, fru_conf = _nivel_frustracao(atual)
        a = _sem_acento(atual)
        return RespostaJEV(
            choices={
                "intencao": _escolha(_logits_intencao(historico, atual), escala=1.2),
                "categoria": _escolha(vocab.logits(historico, atual)),
            },
            scores={
                "prioridade": RespostaScore(nivel=prio_nivel, confianca=prio_conf),
                "frustracao": RespostaScore(nivel=fru_nivel, confianca=fru_conf),
            },
            nouls={
                "malicioso": 0.95 if _RE_MALICIOSO.search(a) else 0.04,
                "fora_de_escopo": 0.92 if _RE_FORA_ESCOPO.search(a) else 0.05,
            },
            modelo=MODELO_SIMULADO,
            latencia_ms=_latencia_simulada_ms(estado),
        )


class FallbackSimulado(FakeFallbackLLM):
    """Fallback que escolhe a opção mais pontuada (sem noção de confiança), dando pouco peso ao histórico."""

    def __init__(self, perguntas: Mapping[str, Pergunta]) -> None:
        super().__init__()
        self._perguntas = perguntas

    async def classificar(self, estado: str) -> ResultadoFallback:
        self.chamadas.append(estado)
        historico, atual = _falas_do_cliente(estado)
        logits_i = _logits_intencao(historico, atual)
        intencao = max(logits_i, key=lambda k: logits_i[k])
        logits_c = _vocabulario(self._perguntas).logits(historico, atual, peso_historico=0.5)
        categoria = max(logits_c, key=lambda k: logits_c[k])
        return ResultadoFallback(intencao, categoria)


def latencia_simulada_ms(estado: str) -> int:
    """Exposta para o adaptador somar a latência do fallback quando ele é chamado."""
    return _latencia_simulada_ms(estado)
