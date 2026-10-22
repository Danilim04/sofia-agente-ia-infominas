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
from src.agent.nodes import PREFIXO_SESSAO_NO_CONFIG


@pytest.fixture(autouse=True)
def _cerebro_sem_autenticacao_nos_testes_da_rota(monkeypatch):
    """Estes testes exercitam a rota, não a auth (E4.5b: token vazio recusa tudo)."""
    from src.config import get_settings

    monkeypatch.setenv("BRAIN_SEM_AUTENTICACAO", "true")
    get_settings.cache_clear()
    yield
    get_settings.cache_clear()


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
        "fontes_usadas": ["internet-residencial.md — Reiniciar o roteador"],
    }
    g = _FakeGraph(valores)
    contato = {"forma": "cpf", "valor_mascarado": "***.***.789-**", "cliente_id": None}
    req = ChatRequest(
        conversation_id="conv-123",
        mensagem="como reinicio o roteador?",
        telefone="5531999990001",
        nome_contato="Maria",
        contato=contato,
    )

    resp = asyncio.run(processar_chat(g, req))

    assert resp.reply == "resposta do agente"
    assert resp.fontes == ["internet-residencial.md — Reiniciar o roteador"]
    assert resp.acoes == []
    assert resp.classificacao is None  # E2.2 preenche
    # thread_id = conversation_id (isolamento por conversa)
    assert g.last_config["configurable"]["thread_id"] == "conv-123"
    # E4.5b: telefone e nomes NÃO vão ao estado persistido (o gateway os reenvia a cada
    # turno): viajam por `config["configurable"]`; o estado recebe `None` (limpa legado).
    cfg = g.last_config["configurable"]
    assert cfg[PREFIXO_SESSAO_NO_CONFIG + "nome_contato"] == "Maria"
    assert cfg[PREFIXO_SESSAO_NO_CONFIG + "telefone"] == "5531999990001"
    assert g.last_inputs["nome_contato"] is None
    assert g.last_inputs["telefone"] is None
    # o contato (forma, valor mascarado, cliente_id) segue no estado — sem o nome do cadastro
    assert {k: v for k, v in contato.items() if k != "nome_cadastro"} == g.last_inputs["contato"]
    if contato.get("nome_cadastro"):
        assert cfg[PREFIXO_SESSAO_NO_CONFIG + "nome_cadastro"] == contato["nome_cadastro"]
        assert "nome_cadastro" not in g.last_inputs["contato"]
    # conversation_id entra no estado (as tools o repassam ao gateway)
    assert g.last_inputs["conversation_id"] == "conv-123"
    assert isinstance(g.last_inputs["messages"][0], HumanMessage)
    assert g.last_inputs["messages"][0].content == "como reinicio o roteador?"


def test_chat_request_e_compativel_com_campos_antigos_e_novos():
    """Contrato A só cresce: campos desconhecidos são ignorados e os novos são
    opcionais — um gateway antigo ou novo fala com este cérebro."""
    req = ChatRequest.model_validate(
        {"conversation_id": "c", "mensagem": "oi", "identidade": {"x": 1}, "session_token": "t"}
    )
    assert req.contato is None and req.nome_contato is None and req.estado_conversa == "bot"


def test_processar_chat_devolve_classificacao_do_estado():
    classificacao = {"intencao": "reclamacao", "categoria": "sem_conexao", "confianca": 0.9}
    g = _FakeGraph({"messages": [AIMessage(content="ok")], "classificacao": classificacao})
    resp = asyncio.run(processar_chat(g, ChatRequest(conversation_id="c", mensagem="oi")))
    assert resp.classificacao == classificacao


def test_processar_chat_mascara_telefone_no_log(caplog):
    g = _FakeGraph({"messages": [AIMessage(content="ok")]})
    req = ChatRequest(conversation_id="c", mensagem="oi", telefone="5531999990001")
    with caplog.at_level(logging.INFO, logger="server"):
        asyncio.run(processar_chat(g, req))
    linha = next(
        r.getMessage() for r in caplog.records if r.getMessage().startswith("chat_request")
    )
    assert "5531999990001" not in linha and "telefone=**********001" in linha


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
            AIMessage(
                content=[{"type": "text", "text": "parte 1 "}, {"type": "text", "text": "parte 2"}]
            )
        ],
    }
    g = _FakeGraph(valores)
    req = ChatRequest(conversation_id="c2", mensagem="oi")
    resp = asyncio.run(processar_chat(g, req))
    assert resp.reply == "parte 1 parte 2"


# ---------------------------------------------------------------------------
# Falha do grafo é CLASSIFICADA e devolvida ao gateway (classe + ID) —
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


_RE_ID = re.compile(r"^SOF-\d{8}-\d{4}-\d{2}$")


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
        processar_chat(
            _GrafoQueExplode(embrulhada), ChatRequest(conversation_id="c", mensagem="oi")
        )
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
# Prazo do turno no cérebro — o grafo que não termina a tempo vira falha
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


# ---------------------------------------------------------------------------
# Conversa ENCERRADA no Infodesk = fim do episódio também no cérebro —
# `POST /encerrar-episodio` limpa proposta pendente, classificação e
# identificação da conversa (uma proposta antiga não pode ser "confirmada"
# por uma mensagem qualquer do episódio seguinte).
# ---------------------------------------------------------------------------


def _grafo_real_com_proposta(monkeypatch, thread_id: str, proposta, **extra):
    """Grafo real (LLM falso, MemorySaver) com um turno rodado e a proposta
    (e campos extras) no estado persistido do `thread_id`."""
    from unittest.mock import MagicMock

    from src.agent import nodes
    from src.agent.graph import build_graph

    monkeypatch.setattr(
        nodes,
        "avaliar_entrada",
        lambda texto, **_: {"is_safe": True, "categoria": "suporte", "motivo": "t"},
    )
    fake = MagicMock(name="FakeLLM")
    fake.bind_tools.return_value = fake
    fake.invoke.side_effect = [AIMessage(content="Posso abrir?"), AIMessage(content="ok")]
    g = build_graph(
        llm=fake, tools=[], buscar_chunks=lambda q: {"encontrado": False, "total": 0, "chunks": []}
    )
    cfg = {"configurable": {"thread_id": thread_id}}
    g.invoke(
        {
            "telefone": "1",
            "proposta_chamado": proposta,
            "messages": [HumanMessage(content="oi")],
            **extra,
        },
        config=cfg,
    )
    return g, cfg, fake


def test_encerrar_episodio_limpa_proposta_pendente_e_preserva_historico(monkeypatch):
    proposta = {"resumo": "X", "criada_em": "2026-10-06T12:00:00+00:00", "turno": 1}
    g, cfg, fake = _grafo_real_com_proposta(monkeypatch, "conv-45", proposta)
    assert g.get_state(cfg).values["proposta_chamado"] == proposta

    resp = asyncio.run(server.encerrar_episodio(g, "conv-45"))

    assert resp.status == "ok" and resp.proposta_limpa is True
    assert resp.campos_limpos == ["proposta_chamado"]
    estado = g.get_state(cfg)
    assert estado.values["proposta_chamado"] is None
    assert estado.next == ()  # nada pendente para o próximo turno
    assert len(estado.values["messages"]) == 2  # histórico intacto

    # O turno seguinte roda normal (1 chamada de LLM) e segue sem proposta.
    out = g.invoke({"messages": [HumanMessage(content="Esse")]}, config=cfg)
    assert fake.invoke.call_count == 2
    assert out["proposta_chamado"] is None
    assert len(out["messages"]) == 4


def test_encerrar_episodio_limpa_classificacao_e_identificacao(monkeypatch):
    g, cfg, _ = _grafo_real_com_proposta(
        monkeypatch,
        "conv-45b",
        None,
        classificacao={"categoria": "sem_conexao"},
        identificacao={"forma": "cpf", "valor_mascarado": "***.***.789-**"},
        contato={"forma": "cpf", "valor_mascarado": "***.***.789-**"},
    )
    resp = asyncio.run(server.encerrar_episodio(g, "conv-45b"))
    assert resp.proposta_limpa is False
    assert resp.campos_limpos == ["classificacao", "identificacao"]
    valores = g.get_state(cfg).values
    assert valores["classificacao"] is None and valores["identificacao"] is None
    # O cache do gateway (`contato`) NÃO é do episódio: fica.
    assert valores["contato"] == {"forma": "cpf", "valor_mascarado": "***.***.789-**"}


def test_encerrar_episodio_sem_proposta_nao_mexe_no_estado(monkeypatch):
    g, cfg, _ = _grafo_real_com_proposta(monkeypatch, "conv-46", None)
    antes = g.get_state(cfg)

    resp = asyncio.run(server.encerrar_episodio(g, "conv-46"))

    assert resp.proposta_limpa is False
    assert g.get_state(cfg).config == antes.config  # nenhum checkpoint novo


def test_encerrar_episodio_conversa_desconhecida_e_inocuo(monkeypatch):
    g, _, _ = _grafo_real_com_proposta(monkeypatch, "conv-47", None)
    resp = asyncio.run(server.encerrar_episodio(g, "nunca-existiu"))
    assert resp.status == "ok" and resp.proposta_limpa is False
    assert g.get_state({"configurable": {"thread_id": "nunca-existiu"}}).values == {}


def test_endpoint_encerrar_episodio(monkeypatch):
    from fastapi.testclient import TestClient

    proposta = {"resumo": "X", "criada_em": "2026-10-06T12:00:00+00:00", "turno": 1}
    g, cfg, _ = _grafo_real_com_proposta(monkeypatch, "48", proposta)
    monkeypatch.setattr(server, "_GRAPH", g)
    client = TestClient(server.app)

    r = client.post("/encerrar-episodio", json={"conversation_id": "48"})
    assert r.status_code == 200
    assert r.json() == {
        "status": "ok",
        "proposta_limpa": True,
        "campos_limpos": ["proposta_chamado"],
    }
    assert g.get_state(cfg).values["proposta_chamado"] is None

    # Segunda vez: nada a limpar, continua 200.
    r = client.post("/encerrar-episodio", json={"conversation_id": "48"})
    assert r.status_code == 200 and r.json()["proposta_limpa"] is False


def test_endpoint_encerrar_episodio_falha_vira_500_com_corpo(monkeypatch, caplog):
    from fastapi.testclient import TestClient

    class _GrafoQuebrado:
        async def aget_state(self, config):
            raise RuntimeError("checkpointer fora")

    monkeypatch.setattr(server, "_GRAPH", _GrafoQuebrado())
    client = TestClient(server.app)
    with caplog.at_level(logging.ERROR):
        r = client.post("/encerrar-episodio", json={"conversation_id": "49"})
    assert r.status_code == 500
    assert r.json() == {"status": "erro", "proposta_limpa": False, "campos_limpos": []}
    assert any("episodio_erro conversation_id=49" in m for m in caplog.messages)
