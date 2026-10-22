"""Tool calls do MESMO lote (E3.8): a política de cada chamada lê o estado já
atualizado pelos efeitos das anteriores, então um efeito colateral repetido no
lote não executa duas vezes. LLM scriptado, tools de teste, sem rede."""

from __future__ import annotations

from datetime import UTC, datetime, timedelta

from langchain_core.messages import AIMessage, HumanMessage, ToolMessage

from src.agent.graph import build_graph
from tests.fakes import (
    CHAMADAS,
    TOOLS_DUAS_FASES,
    aberturas,
    fake_llm,
    passa_seguranca,
    registrar_duas_fases_de_teste,
    sem_rag,
    tc_preparar,
)

_PROPOSTA = {
    "titulo": "SEM CONEXÃO",
    "descricao": "Cliente relata queda total",
    "categoria": "teste",
}


def _lote(*chamadas: tuple[str, str]) -> AIMessage:
    return AIMessage(content="", tool_calls=[{"id": i, "name": n, "args": {}} for i, n in chamadas])


def _grafo(monkeypatch, scripted):
    passa_seguranca(monkeypatch)
    registrar_duas_fases_de_teste(monkeypatch)
    return build_graph(
        llm=fake_llm(scripted=scripted), tools=TOOLS_DUAS_FASES, buscar_chunks=sem_rag
    )


def _estado_com_proposta() -> dict:
    criada = datetime.now(UTC) - timedelta(minutes=1)
    return {
        "telefone": "5531999990000",
        "contato": {"forma": "cpf", "valor_mascarado": "***.***.789-**"},
        "proposta_chamado": {**_PROPOSTA, "criada_em": criada.isoformat(), "turno": 0},
        "messages": [HumanMessage(content="sim")],
    }


def _toolmessages(out: dict, nome: str) -> list[ToolMessage]:
    return [m for m in out["messages"] if isinstance(m, ToolMessage) and m.name == nome]


def test_duas_aberturas_no_mesmo_lote_executam_uma_vez(monkeypatch):
    g = _grafo(
        monkeypatch,
        [
            _lote(("a1", "tool_abrir_de_teste"), ("a2", "tool_abrir_de_teste")),
            AIMessage(content="Pronto! Abri o chamado 4821."),
        ],
    )
    out = g.invoke(_estado_com_proposta(), config={"configurable": {"thread_id": "lote-2x"}})

    assert aberturas() == 1
    primeira, segunda = _toolmessages(out, "tool_abrir_de_teste")
    assert '"status": true' in primeira.content
    assert "recusado_pela_politica" in segunda.content  # sem proposta: já consumida
    assert out["proposta_chamado"] is None


def test_tres_aberturas_no_lote_tambem_executam_uma_vez(monkeypatch):
    g = _grafo(
        monkeypatch,
        [
            _lote(*[(f"a{i}", "tool_abrir_de_teste") for i in range(3)]),
            AIMessage(content="Pronto, chamado aberto."),
        ],
    )
    g.invoke(_estado_com_proposta(), config={"configurable": {"thread_id": "lote-3x"}})
    assert aberturas() == 1


def test_preparar_e_abrir_no_mesmo_lote_nao_abre(monkeypatch):
    """O preparo do lote carimba a proposta com ESTE turno: o `abrir` seguinte
    do mesmo lote é recusado (a confirmação vale só no turno seguinte)."""
    preparar = tc_preparar().tool_calls[0]
    abrir = {"id": "a1", "name": "tool_abrir_de_teste", "args": {}}
    g = _grafo(
        monkeypatch,
        [AIMessage(content="", tool_calls=[preparar, abrir]), AIMessage(content="Posso abrir?")],
    )
    out = g.invoke(
        {
            "telefone": "5531999990000",
            "contato": {"forma": "cpf", "valor_mascarado": "***.***.789-**"},
            "messages": [HumanMessage(content="caiu a internet, abre um chamado")],
        },
        config={"configurable": {"thread_id": "lote-prep-abrir"}},
    )
    assert aberturas() == 0
    assert [c["tool"] for c in CHAMADAS] == ["tool_preparar_de_teste"]
    assert "recusado_pela_politica" in _toolmessages(out, "tool_abrir_de_teste")[0].content
    assert out["proposta_chamado"]["turno"] == 1
