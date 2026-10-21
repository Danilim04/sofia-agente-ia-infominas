"""Fakes compartilhados pelos testes — tudo sem rede.

- `fake_llm(scripted)`: LLM falso cujo `invoke` segue um roteiro de
  `AIMessage`s (com ou sem `tool_calls`); `bind_tools` devolve ele mesmo.
- `sem_rag`: retriever nulo para o nó `retrieve`.
- `passa_seguranca(monkeypatch)`: guardrail de entrada sempre "suporte".
- `_texto_de`: texto de um `content` que pode ser str ou lista de blocos.
- `FakeJEV`/`resp_jev`/`FakeFallbackLLM`/`servico_fake`: classificação (E2.2) sem rede —
  respostas do JEV scriptadas por pergunta e por turno.
- Tools de teste com `InjectedToolArg` + políticas/efeitos correspondentes:
  `tool_de_teste` (injeção simples) e o par `tool_preparar_de_teste` /
  `tool_abrir_de_teste` (duas fases). `registrar_duas_fases_de_teste`
  instala as políticas em `POLITICAS` e os efeitos em `EFEITOS` via
  monkeypatch — assim os testes de COMPORTAMENTO do grafo (duas fases,
  política, carimbo, consumo da proposta) continuam valendo com `POLITICAS`
  e `EFEITOS` vazios no código de produção (E2.3 os preenche).
"""

from __future__ import annotations

import json
from collections.abc import Mapping, Sequence
from pathlib import Path
from types import SimpleNamespace
from typing import Annotated, Any
from unittest.mock import MagicMock

import httpx
from langchain_core.messages import AIMessage
from langchain_core.tools import InjectedToolArg, tool

from src.agent import nodes, tool_policy
from src.agent.tool_policy import (
    PoliticaTool,
    _conversation_id_da_sessao,
    _exigir_proposta_confirmada,
    _proposta_da_sessao,
    _telefone_da_sessao,
    carimbar_proposta,
)
from src.classificacao.config import ConfigClassificacao
from src.classificacao.fallback_llm import ResultadoFallback
from src.classificacao.roteador import ServicoClassificacao
from src.classificacao.taxonomia import Taxonomia, carregar_taxonomia
from src.classificacao.tipos import (
    Pergunta,
    RespostaChoice,
    RespostaJEV,
    RespostaScore,
)
from src.tools import efeitos

# ---------------------------------------------------------------------------
# LLM, RAG e guardrail
# ---------------------------------------------------------------------------


def fake_llm(scripted: list[AIMessage] | None = None) -> MagicMock:
    """LLM falso: `bind_tools` retorna ele mesmo; `invoke` segue o roteiro.

    Sem roteiro, qualquer `invoke` estoura (`StopIteration`) — útil para
    provar que o LLM NÃO foi chamado.
    """
    fake = MagicMock(name="FakeLLM")
    fake.bind_tools.return_value = fake
    fake.invoke.side_effect = list(scripted) if scripted is not None else []
    return fake


def _texto_de(content: Any) -> str:
    """Texto de um `content` que pode ser str ou lista de blocos (cache_control)."""
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return "".join(b.get("text", "") if isinstance(b, dict) else str(b) for b in content)
    return str(content)


def sem_rag(pergunta: str) -> dict:
    """Retriever nulo p/ o nó `retrieve` — testes não tocam índice real."""
    return {"encontrado": False, "total": 0, "chunks": []}


def rag_com(chunks: list[dict]):
    """Retriever fake que devolve sempre os mesmos chunks."""

    def _buscar(pergunta: str) -> dict:
        return {"encontrado": bool(chunks), "total": len(chunks), "chunks": list(chunks)}

    return _buscar


def passa_seguranca(monkeypatch) -> None:
    monkeypatch.setattr(
        nodes,
        "avaliar_entrada",
        lambda texto, **_: {"is_safe": True, "categoria": "suporte", "motivo": "test"},
    )


# ---------------------------------------------------------------------------
# Tools de teste com argumentos de sessão injetados
# ---------------------------------------------------------------------------

#: Registro das execuções das tools de teste (zerado pelo conftest).
CHAMADAS: list[dict[str, Any]] = []


@tool
def tool_de_teste(
    x: str,
    telefone: Annotated[str, InjectedToolArg] = "",
    conversation_id: Annotated[str, InjectedToolArg] = "",
) -> dict[str, Any]:
    """Tool de teste: ecoa o argumento e os valores injetados pela política."""
    CHAMADAS.append({"tool": "tool_de_teste", "x": x, "telefone": telefone})
    return {"status": True, "x": x, "telefone": telefone, "conversation_id": conversation_id}


@tool
def tool_preparar_de_teste(
    titulo: str,
    descricao: str = "",
    telefone: Annotated[str, InjectedToolArg] = "",
    conversation_id: Annotated[str, InjectedToolArg] = "",
) -> dict[str, Any]:
    """Dry-run de teste: devolve a proposta canônica sem criar nada."""
    CHAMADAS.append({"tool": "tool_preparar_de_teste", "titulo": titulo})
    return {
        "status": True,
        "proposta": {
            "titulo": titulo.strip().upper(),
            "descricao": descricao,
            "categoria": "teste",
        },
    }


@tool
def tool_abrir_de_teste(
    proposta: Annotated[dict | None, InjectedToolArg] = None,
    telefone: Annotated[str, InjectedToolArg] = "",
    conversation_id: Annotated[str, InjectedToolArg] = "",
) -> dict[str, Any]:
    """Abertura de teste: executa EXATAMENTE a proposta injetada."""
    CHAMADAS.append({"tool": "tool_abrir_de_teste", "proposta": proposta})
    return {"status": True, "numero": "4821", "proposta": proposta or {}}


POLITICAS_DE_TESTE: dict[str, PoliticaTool] = {
    "tool_de_teste": PoliticaTool(
        injetar={"telefone": _telefone_da_sessao, "conversation_id": _conversation_id_da_sessao},
    ),
    "tool_preparar_de_teste": PoliticaTool(
        injetar={"telefone": _telefone_da_sessao, "conversation_id": _conversation_id_da_sessao},
    ),
    "tool_abrir_de_teste": PoliticaTool(
        injetar={
            "telefone": _telefone_da_sessao,
            "conversation_id": _conversation_id_da_sessao,
            "proposta": _proposta_da_sessao,
        },
        exigir=_exigir_proposta_confirmada,
    ),
}


def _efeito_preparar(nome: str, resultado: Any, state: Mapping[str, Any]) -> dict[str, Any]:
    if isinstance(resultado, dict) and resultado.get("status") and resultado.get("proposta"):
        return {"proposta_chamado": carimbar_proposta(resultado["proposta"], state)}
    return {}


def _efeito_abrir(nome: str, resultado: Any, state: Mapping[str, Any]) -> dict[str, Any]:
    if isinstance(resultado, dict) and resultado.get("status"):
        return {"proposta_chamado": None}
    return {}


EFEITOS_DE_TESTE: dict[str, Any] = {
    "tool_preparar_de_teste": _efeito_preparar,
    "tool_abrir_de_teste": _efeito_abrir,
}

TOOLS_DUAS_FASES = [tool_preparar_de_teste, tool_abrir_de_teste]


def registrar_duas_fases_de_teste(monkeypatch) -> None:
    """Instala políticas e efeitos das tools de teste (desfeito pelo monkeypatch)."""
    for nome, politica in POLITICAS_DE_TESTE.items():
        monkeypatch.setitem(tool_policy.POLITICAS, nome, politica)
    for nome, efeito in EFEITOS_DE_TESTE.items():
        monkeypatch.setitem(efeitos.EFEITOS, nome, efeito)


def aberturas() -> int:
    """Quantas vezes `tool_abrir_de_teste` EXECUTOU (recusa da política não conta)."""
    return sum(1 for c in CHAMADAS if c["tool"] == "tool_abrir_de_teste")


def tc_abrir(id_: str = "tc-abrir") -> AIMessage:
    return AIMessage(
        content="", tool_calls=[{"id": id_, "name": "tool_abrir_de_teste", "args": {}}]
    )


def tc_preparar(id_: str = "tc-prep", titulo: str = "Sem conexão desde cedo") -> AIMessage:
    return AIMessage(
        content="",
        tool_calls=[
            {
                "id": id_,
                "name": "tool_preparar_de_teste",
                "args": {"titulo": titulo, "descricao": "Cliente relata queda total"},
            }
        ],
    )


# ---------------------------------------------------------------------------
# Classificação (E2.2): JEV e fallback LLM falsos, scriptados por pergunta
# ---------------------------------------------------------------------------

#: Escolha, confiança[, segunda opção mais provável].
EscolhaFalsa = tuple[str, float] | tuple[str, float, str]


def _choice(valor: EscolhaFalsa) -> RespostaChoice:
    escolha, confianca = valor[0], valor[1]
    probs = {escolha: round(0.5 + confianca / 2, 4)}
    if len(valor) == 3:
        probs[valor[2]] = round(max(0.0, 0.5 - confianca / 2), 4) or 0.01
    return RespostaChoice(escolha=escolha, confianca=confianca, probabilidades=probs)


def resp_jev(
    *,
    intencao: EscolhaFalsa = ("reclamacao_problema", 0.92),
    categoria: EscolhaFalsa = ("sem_conexao", 0.88),
    prioridade: tuple[int, float] = (2, 0.8),
    frustracao: tuple[int, float] = (0, 0.9),
    malicioso: float = 0.02,
    fora_de_escopo: float = 0.02,
    modelo: str = "jev-fake",
) -> RespostaJEV:
    """Resposta do JEV scriptada POR PERGUNTA (defaults: reclamação clara, calmo, seguro)."""
    return RespostaJEV(
        choices={"intencao": _choice(intencao), "categoria": _choice(categoria)},
        scores={
            "prioridade": RespostaScore(nivel=prioridade[0], confianca=prioridade[1]),
            "frustracao": RespostaScore(nivel=frustracao[0], confianca=frustracao[1]),
        },
        nouls={"malicioso": malicioso, "fora_de_escopo": fora_de_escopo},
        modelo=modelo,
        latencia_ms=5,
    )


class FakeJEV:
    """`ClienteJEV` falso: devolve (ou levanta) o próximo item do roteiro por chamada.

    - `por_turno`: lista consumida em ordem — uma `RespostaJEV` ou uma exceção
      (JEV fora do ar naquele turno);
    - `padrao`: usado quando o roteiro acaba (sem ele, chamada extra falha o teste);
    - `chamadas`: `(estado, perguntas)` de cada chamada, para os asserts de
      mascaramento de PII e de "uma chamada por turno".
    """

    def __init__(
        self,
        por_turno: Sequence[RespostaJEV | BaseException] = (),
        padrao: RespostaJEV | BaseException | None = None,
    ) -> None:
        self._fila: list[RespostaJEV | BaseException] = list(por_turno)
        self._padrao = padrao
        self.chamadas: list[tuple[str, Mapping[str, Pergunta]]] = []

    async def perguntar(self, estado: str, perguntas: Mapping[str, Pergunta]) -> RespostaJEV:
        self.chamadas.append((estado, perguntas))
        item = self._fila.pop(0) if self._fila else self._padrao
        if item is None:
            raise AssertionError("FakeJEV sem resposta scriptada para esta chamada")
        if isinstance(item, BaseException):
            raise item
        return item


class FakeFallbackLLM:
    """`FallbackLLM` falso: roteiro de `ResultadoFallback` (ou exceção) por chamada."""

    def __init__(
        self,
        respostas: Sequence[ResultadoFallback | BaseException] = (),
        padrao: ResultadoFallback | None = None,
    ) -> None:
        self._fila: list[ResultadoFallback | BaseException] = list(respostas)
        self._padrao = padrao
        self.chamadas: list[str] = []

    async def classificar(self, estado: str) -> ResultadoFallback:
        self.chamadas.append(estado)
        item = self._fila.pop(0) if self._fila else self._padrao
        if item is None:
            raise AssertionError("FakeFallbackLLM sem resposta scriptada para esta chamada")
        if isinstance(item, BaseException):
            raise item
        return item


def config_de_teste(**mudancas: Any) -> ConfigClassificacao:
    """Config com os defaults do plano, sem `.env` nem chave real."""
    base: dict[str, Any] = {"TYPESAFE_API_KEY": ""}
    base.update(mudancas)
    return ConfigClassificacao(_env_file=None, **base)  # type: ignore[call-arg]


def taxonomia_de_teste() -> Taxonomia:
    """A taxonomia versionada (placeholder P1) — a mesma que o cérebro carrega."""
    return carregar_taxonomia(ConfigClassificacao(_env_file=None).taxonomia_path)  # type: ignore[call-arg]


def servico_fake(
    jev: FakeJEV | None = None,
    fallback: FakeFallbackLLM | None = None,
    **config: Any,
) -> ServicoClassificacao:
    """`ServicoClassificacao` ligado a fakes (sem rede)."""
    return ServicoClassificacao(
        taxonomia=taxonomia_de_teste(),
        config=config_de_teste(**config),
        jev=jev,
        fallback=fallback,
    )


# ---------------------------------------------------------------------------
# Gateway falso das tools do Infodesk (E2.3): `httpx.post` monkeypatchado,
# respondendo com os JSON de `contratos/tools-infodesk/`
# ---------------------------------------------------------------------------

RAIZ_CONTRATOS_TOOLS = Path(__file__).resolve().parents[2] / "contratos" / "tools-infodesk"

#: URL base e token que a tool usa nos testes (`get_settings` trocado).
GATEWAY_URL_DE_TESTE = "http://gateway.test"
GATEWAY_TOKEN_DE_TESTE = "token-de-teste"


def contrato_tools(caminho: str, arquivo: str) -> dict[str, Any]:
    """JSON de `contratos/tools-infodesk/<caminho>/<arquivo>.json` (ex.: `ticket/abrir`, `ok`)."""
    texto = (RAIZ_CONTRATOS_TOOLS / caminho / f"{arquivo}.json").read_text(encoding="utf-8")
    return json.loads(texto)


class FakeGateway:
    """`httpx.post` falso para `src.tools.infodesk_tools._post`.

    `respostas` mapeia o caminho (`ticket/preparar`) para: um `dict` (corpo, HTTP
    200), uma exceção (levantada), `(status, texto)` (resposta crua) ou uma função
    `payload -> dict`. Caminho sem resposta falha o teste (tool chamou o que não devia).
    """

    def __init__(self, respostas: Mapping[str, Any] | None = None) -> None:
        self.respostas: dict[str, Any] = dict(respostas or {})
        self.chamadas: list[dict[str, Any]] = []

    def post(self, url: str, *, json: Any = None, headers: Any = None, timeout: Any = None):
        prefixo = f"{GATEWAY_URL_DE_TESTE}/tools/infodesk/"
        assert url.startswith(prefixo), f"URL fora do gateway: {url}"
        caminho = url.removeprefix(prefixo)
        self.chamadas.append(
            {"caminho": caminho, "json": json, "headers": dict(headers or {}), "timeout": timeout}
        )
        if caminho not in self.respostas:
            raise AssertionError(f"chamada inesperada ao gateway: {caminho}")
        resposta = self.respostas[caminho]
        if callable(resposta) and not isinstance(resposta, BaseException):
            resposta = resposta(json)
        if isinstance(resposta, BaseException):
            raise resposta
        pedido = httpx.Request("POST", url)
        if isinstance(resposta, tuple):
            status, texto = resposta
            return httpx.Response(status, text=texto, request=pedido)
        return httpx.Response(200, json=resposta, request=pedido)

    def em(self, caminho: str) -> list[dict[str, Any]]:
        """As chamadas feitas a `caminho`, em ordem."""
        return [c for c in self.chamadas if c["caminho"] == caminho]


def instalar_gateway_fake(monkeypatch, respostas: Mapping[str, Any] | None = None) -> FakeGateway:
    """Troca `httpx.post` e `get_settings` das tools do Infodesk (desfeito pelo monkeypatch)."""
    from src.tools import infodesk_tools

    gateway = FakeGateway(respostas)
    monkeypatch.setattr(infodesk_tools.httpx, "post", gateway.post)
    monkeypatch.setattr(
        infodesk_tools,
        "get_settings",
        lambda: SimpleNamespace(
            infodesk_tools_base_url=GATEWAY_URL_DE_TESTE,
            infodesk_tools_token=GATEWAY_TOKEN_DE_TESTE,
            infodesk_tools_timeout=30.0,
        ),
    )
    return gateway
