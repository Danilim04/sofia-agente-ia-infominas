"""Testes do Contrato A (server.py) — função pura `processar_chat`.

Roda sem servidor/rede: injeta um grafo fake com `ainvoke` async e valida o
mapeamento request → estado do grafo → response.
"""

from __future__ import annotations

import asyncio
import logging
import re

import pytest
from langchain_core.messages import AIMessage, HumanMessage

import server
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
    # conversation_id entra no estado (as tools do SAC o repassam ao gateway)
    assert g.last_inputs["conversation_id"] == "conv-123"
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


# ---------------------------------------------------------------------------
# ZC-2: falha do grafo é CLASSIFICADA e devolvida ao gateway (classe + ID) —
# nunca uma exceção solta virando 500 mudo no uvicorn.
# ---------------------------------------------------------------------------


class _GrafoQueExplode:
    def __init__(self, exc: BaseException):
        self._exc = exc

    async def ainvoke(self, inputs, config=None):
        raise self._exc


class _ErroProvedor(Exception):
    """Imita `openai.APIStatusError`: carrega `status_code` (duck typing)."""

    def __init__(self, status_code: int, msg: str = "provedor"):
        super().__init__(msg)
        self.status_code = status_code


_RE_ID = re.compile(r"^ZPF-\d{8}-\d{4}-\d{2}$")


def test_processar_chat_402_nao_explode_e_devolve_definitiva(caplog):
    g = _GrafoQueExplode(_ErroProvedor(402, "Insufficient credits"))
    req = ChatRequest(conversation_id="conv-402", mensagem="oi")
    with caplog.at_level(logging.ERROR, logger="server"):
        resp = asyncio.run(processar_chat(g, req))

    assert resp.reply == ""
    assert resp.erro is not None
    assert resp.erro.classe == "definitiva"
    assert _RE_ID.match(resp.erro.id_ocorrencia), resp.erro.id_ocorrencia
    assert "402" in resp.erro.causa
    # O log `chat_erro` leva o conversation_id, a classe e o MESMO id.
    linha = next(r for r in caplog.records if r.getMessage().startswith("chat_erro"))
    msg = linha.getMessage()
    assert "conversation_id=conv-402" in msg
    assert "classe=definitiva" in msg
    assert f"id_ocorrencia={resp.erro.id_ocorrencia}" in msg


@pytest.mark.parametrize(
    "exc, classe",
    [
        (_ErroProvedor(429), "transitoria"),
        (_ErroProvedor(503), "transitoria"),
        (TimeoutError("read timed out"), "transitoria"),
        (ConnectionError("conexão recusada"), "transitoria"),
        (_ErroProvedor(401), "definitiva"),
        (_ErroProvedor(400), "definitiva"),
        (TypeError("bug no código"), "definitiva"),
    ],
)
def test_processar_chat_classifica_transitoria_x_definitiva(exc, classe):
    g = _GrafoQueExplode(exc)
    resp = asyncio.run(processar_chat(g, ChatRequest(conversation_id="c", mensagem="oi")))
    assert resp.erro is not None and resp.erro.classe == classe


def test_processar_chat_classifica_pela_causa_encadeada():
    """O SDK pode embrulhar o erro HTTP: a classe vem da cadeia de causas."""
    try:
        try:
            raise _ErroProvedor(402)
        except _ErroProvedor as inner:
            raise RuntimeError("embrulho") from inner
    except RuntimeError as exc:
        embrulhada = exc
    resp = asyncio.run(
        processar_chat(_GrafoQueExplode(embrulhada), ChatRequest(conversation_id="c", mensagem="oi"))
    )
    assert resp.erro is not None and resp.erro.classe == "definitiva"


def test_endpoint_chat_devolve_status_por_classe_com_corpo(monkeypatch):
    """O gateway Go lê classe/ID do corpo: 500 = definitiva, 503 = transitória."""
    from fastapi.testclient import TestClient

    client = TestClient(server.app)
    corpo = {"conversation_id": "c9", "mensagem": "oi"}

    monkeypatch.setattr(server, "_GRAPH", _GrafoQueExplode(_ErroProvedor(402)))
    r = client.post("/chat", json=corpo)
    assert r.status_code == 500
    assert r.json()["erro"]["classe"] == "definitiva"
    assert _RE_ID.match(r.json()["erro"]["id_ocorrencia"])

    monkeypatch.setattr(server, "_GRAPH", _GrafoQueExplode(_ErroProvedor(503)))
    r = client.post("/chat", json=corpo)
    assert r.status_code == 503
    assert r.json()["erro"]["classe"] == "transitoria"

    # Turno normal continua 200 sem `erro`.
    monkeypatch.setattr(server, "_GRAPH", _FakeGraph({"messages": [AIMessage(content="ok")]}))
    r = client.post("/chat", json=corpo)
    assert r.status_code == 200
    assert r.json()["reply"] == "ok" and r.json()["erro"] is None


# ---------------------------------------------------------------------------
# ZC-6: prazo do turno no cérebro — o grafo que não termina a tempo vira falha
# TRANSITÓRIA classificada (o gateway repete ou transfere), não um turno que
# conclui depois de o cliente já ter recebido erro.
# ---------------------------------------------------------------------------


class _GrafoLento:
    def __init__(self):
        self.concluiu = False

    async def ainvoke(self, inputs, config=None):
        await asyncio.sleep(5)
        self.concluiu = True
        return {"messages": [AIMessage(content="tarde demais")]}


def test_processar_chat_respeita_prazo_do_turno(monkeypatch):
    from src.config import get_settings

    monkeypatch.setenv("BRAIN_TURNO_PRAZO", "0.05")
    get_settings.cache_clear()
    try:
        g = _GrafoLento()
        resp = asyncio.run(processar_chat(g, ChatRequest(conversation_id="lento", mensagem="oi")))
    finally:
        get_settings.cache_clear()

    assert resp.reply == ""
    assert resp.erro is not None
    assert resp.erro.classe == "transitoria"
    assert "prazo" in resp.erro.causa.lower()
    assert g.concluiu is False  # o grafo foi interrompido, não seguiu sozinho
