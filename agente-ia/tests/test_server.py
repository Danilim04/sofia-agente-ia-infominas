"""Testes do Contrato A (server.py) — função pura `processar_chat`.

Roda sem servidor/rede: injeta um grafo fake com `ainvoke` async e valida o
mapeamento request → estado do grafo → response.
"""

from __future__ import annotations

import asyncio

from langchain_core.messages import AIMessage, HumanMessage

from server import ChatRequest, processar_chat


class _FakeGraph:
    """Grafo mínimo: registra o que recebeu e devolve `valores` fixos."""

    def __init__(self, valores: dict):
        self._valores = valores
        self.last_inputs = None
        self.last_config = None

    async def ainvoke(self, inputs, config=None):
        self.last_inputs = inputs
        self.last_config = config
        return self._valores


def test_processar_chat_extrai_reply_e_fontes_e_isola_por_conversa():
    valores = {
        "messages": [HumanMessage(content="oi"), AIMessage(content="resposta do agente")],
        "fontes_usadas": ["azapfy-web.md — Módulo: Pesquisa"],
    }
    g = _FakeGraph(valores)
    req = ChatRequest(
        conversation_id="conv-123",
        mensagem="como funciona a Pesquisa?",
        identidade={"encontrado": True, "login": "10596693664"},
    )

    resp = asyncio.run(processar_chat(g, req))

    assert resp.reply == "resposta do agente"
    assert resp.fontes == ["azapfy-web.md — Módulo: Pesquisa"]
    assert resp.acoes == []
    # thread_id = conversation_id (isolamento por conversa)
    assert g.last_config["configurable"]["thread_id"] == "conv-123"
    # identidade repassada ao estado; mensagem vira HumanMessage
    assert g.last_inputs["identidade"] == {"encontrado": True, "login": "10596693664"}
    assert isinstance(g.last_inputs["messages"][0], HumanMessage)
    assert g.last_inputs["messages"][0].content == "como funciona a Pesquisa?"


def test_processar_chat_reply_vazio_quando_sem_aimessage():
    g = _FakeGraph({"messages": [HumanMessage(content="oi")]})
    req = ChatRequest(conversation_id="c1", mensagem="oi")
    resp = asyncio.run(processar_chat(g, req))
    assert resp.reply == ""
    assert resp.fontes == []


def test_processar_chat_tolera_content_em_blocos():
    """`content` pode ser lista de blocos (Anthropic/cache_control)."""
    valores = {
        "messages": [
            AIMessage(content=[{"type": "text", "text": "parte 1 "}, {"type": "text", "text": "parte 2"}])
        ],
    }
    g = _FakeGraph(valores)
    req = ChatRequest(conversation_id="c2", mensagem="oi")
    resp = asyncio.run(processar_chat(g, req))
    assert resp.reply == "parte 1 parte 2"
