"""Fakes compartilhados pelos testes — tudo sem rede.

- `fake_llm(scripted)`: LLM falso cujo `invoke` segue um roteiro de
  `AIMessage`s (com ou sem `tool_calls`); `bind_tools` devolve ele mesmo.
- `sem_rag`: retriever nulo para o nó `retrieve`.
- `passa_seguranca(monkeypatch)`: guardrail de entrada sempre "suporte".
- `_texto_de`: texto de um `content` que pode ser str ou lista de blocos.
- Tools de teste com `InjectedToolArg` + políticas/efeitos correspondentes:
  `tool_de_teste` (injeção simples) e o par `tool_preparar_de_teste` /
  `tool_abrir_de_teste` (duas fases). `registrar_duas_fases_de_teste`
  instala as políticas em `POLITICAS` e os efeitos em `EFEITOS` via
  monkeypatch — assim os testes de COMPORTAMENTO do grafo (duas fases,
  política, carimbo, consumo da proposta) continuam valendo com `POLITICAS`
  e `EFEITOS` vazios no código de produção (E2.3 os preenche).
"""

from __future__ import annotations

from collections.abc import Mapping
from typing import Annotated, Any
from unittest.mock import MagicMock

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
