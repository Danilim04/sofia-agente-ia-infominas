"""Langfuse (E4.3): tags intencao/categoria/origem, desligado sem chave, sem PII em span.

Cliente falso para as tags e o fail-safe; para a máscara, o SDK REAL do Langfuse
com exportador em memória (sem rede) lendo os atributos que iriam ao painel.
"""

from __future__ import annotations

import asyncio
import contextlib
import logging
from types import SimpleNamespace

import pytest
from langchain_core.messages import AIMessage
from langchain_core.tools import tool

import server
from src.observability import tracing
from src.observability.pii import esquecer_nomes, mascarar_dado, registrar_nomes
from src.observability.tracing import TracoDoTurno, tags_do_turno

CPF = "529.982.247-25"
TELEFONE = "5531999990001"
CLASSIFICACAO = {
    "intencao": "reclamacao_problema",
    "categoria": "sem_conexao",
    "prioridade": "alta",
    "confianca": 0.9,
    "origem": "jev",
}


class SpanFalso:
    def __init__(self, falhar_em: str | None = None):
        self.updates: list[dict] = []
        self.marcas: list[dict] = []
        self.falhar_em = falhar_em

    def update(self, **kwargs):
        self.marcas.append(kwargs)

    def update_trace(self, **kwargs):
        if self.falhar_em == "update_trace":
            raise RuntimeError("langfuse fora")
        self.updates.append(kwargs)


class ClienteFalso:
    """Imita `Langfuse.start_as_current_span`."""

    def __init__(self, falhar_em: str | None = None):
        self.span = SpanFalso(falhar_em)
        self.falhar_em = falhar_em
        self.nomes: list[str] = []
        self.aberto = False
        self.fechou_com: list = []

    @contextlib.contextmanager
    def start_as_current_span(self, *, name: str):
        self.nomes.append(name)
        if self.falhar_em == "enter":
            raise RuntimeError("langfuse fora")
        self.aberto = True
        tipo = None
        try:
            yield self.span
        except BaseException as exc:
            tipo = type(exc)
            raise
        finally:
            self.aberto = False
            self.fechou_com.append(tipo)
            if self.falhar_em == "exit":
                raise RuntimeError("langfuse fora")


# ---------------------------------------------------------------------------
# tags
# ---------------------------------------------------------------------------


def test_tags_levam_canal_intencao_categoria_e_origem():
    assert tags_do_turno("whatsapp", CLASSIFICACAO) == [
        "canal:whatsapp",
        "intencao:reclamacao_problema",
        "categoria:sem_conexao",
        "origem:jev",
    ]


def test_tags_sem_classificacao_so_levam_o_canal():
    assert tags_do_turno("whatsapp", None) == ["canal:whatsapp"]
    assert tags_do_turno("whatsapp", {"categoria": None, "origem": ""}) == ["canal:whatsapp"]


def test_tag_com_texto_livre_ou_pii_e_omitida():
    c = {"intencao": "ok", "categoria": f"cpf {CPF}", "origem": "meu nome é Joana"}
    assert tags_do_turno("whatsapp", c) == ["canal:whatsapp", "intencao:ok"]


def test_traco_marca_o_trace_com_sessao_usuario_mascarado_e_depois_as_tags():
    cliente = ClienteFalso()
    with TracoDoTurno(
        conversation_id="conv-1", canal="whatsapp", telefone=TELEFONE, cliente=cliente
    ) as traco:
        assert cliente.aberto
        traco.marcar_classificacao(CLASSIFICACAO)
    assert not cliente.aberto and cliente.nomes == ["chat"]
    abertura, depois = cliente.span.updates
    assert abertura["session_id"] == "conv-1"
    assert abertura["user_id"] == "*" * 10 + "001" and TELEFONE not in str(cliente.span.updates)
    assert abertura["tags"] == ["canal:whatsapp"]
    assert depois["tags"] == [
        "canal:whatsapp",
        "intencao:reclamacao_problema",
        "categoria:sem_conexao",
        "origem:jev",
    ]


def test_traco_sem_telefone_nao_manda_user_id():
    cliente = ClienteFalso()
    with TracoDoTurno(conversation_id="c", canal="whatsapp", cliente=cliente):
        pass
    assert cliente.span.updates[0]["user_id"] is None


def test_traco_nao_engole_excecao_do_corpo_mas_so_o_nome_do_tipo_vai_ao_sdk():
    cliente = ClienteFalso()
    with pytest.raises(ValueError, match="do turno"):
        with TracoDoTurno(conversation_id="c", canal="whatsapp", cliente=cliente):
            raise ValueError(f"do turno {CPF}")
    # a exceção sobe para o chamador, mas o SDK a vê limpa: só o NOME do tipo
    assert cliente.fechou_com == [None]
    assert cliente.span.marcas == [{"level": "ERROR", "status_message": "ValueError"}]


# ---------------------------------------------------------------------------
# desligado e fail-safe
# ---------------------------------------------------------------------------


def _settings(monkeypatch, **campos):
    base = {
        "langfuse_enabled": False,
        "langfuse_public_key": "",
        "langfuse_secret_key": "",
        "langfuse_host": "https://langfuse.test",
    }
    base.update(campos)
    monkeypatch.setattr("src.config.get_settings", lambda: SimpleNamespace(**base))
    tracing.get_langfuse.cache_clear()
    tracing.get_tracing_handler.cache_clear()


@pytest.fixture(autouse=True)
def _limpa_cache():
    yield
    tracing.get_langfuse.cache_clear()
    tracing.get_tracing_handler.cache_clear()
    esquecer_nomes()


def test_sem_chaves_o_tracing_fica_desligado_e_o_traco_e_no_op(monkeypatch):
    _settings(monkeypatch)
    assert tracing.get_langfuse() is None
    assert tracing.get_tracing_handler() is None
    with TracoDoTurno(conversation_id="c", canal="whatsapp", telefone=TELEFONE) as traco:
        traco.marcar_classificacao(CLASSIFICACAO)  # no-op, sem erro


def test_cliente_langfuse_nasce_com_a_mascara_de_pii(monkeypatch):
    _settings(
        monkeypatch,
        langfuse_enabled=True,
        langfuse_public_key="pk-lf-teste",
        langfuse_secret_key="sk-lf-teste",
    )
    recebido: dict = {}

    class LangfuseFalso:
        def __init__(self, **kwargs):
            recebido.update(kwargs)

    monkeypatch.setattr("langfuse.Langfuse", LangfuseFalso)
    cliente = tracing.get_langfuse()
    assert isinstance(cliente, LangfuseFalso)
    assert recebido["mask"] is mascarar_dado
    assert recebido["host"] == "https://langfuse.test"


def test_credencial_invalida_vira_sem_tracing_sem_expor_a_chave(monkeypatch, caplog):
    _settings(
        monkeypatch,
        langfuse_enabled=True,
        langfuse_public_key="pk-lf-teste",
        langfuse_secret_key="sk-lf-segredo",
    )

    def _quebra(**kwargs):
        raise RuntimeError("credencial sk-lf-segredo inválida")

    monkeypatch.setattr("langfuse.Langfuse", _quebra)
    with caplog.at_level(logging.WARNING, logger="src.observability.tracing"):
        assert tracing.get_langfuse() is None
    assert "langfuse_tracing_falhou" in caplog.text and "sk-lf-segredo" not in caplog.text


@pytest.mark.parametrize("falhar_em", ["enter", "update_trace", "exit"])
def test_falha_do_sdk_nunca_derruba_o_turno(falhar_em, caplog):
    cliente = ClienteFalso(falhar_em)
    with caplog.at_level(logging.WARNING, logger="src.observability.tracing"):
        with TracoDoTurno(conversation_id="c", canal="whatsapp", cliente=cliente) as traco:
            traco.marcar_classificacao(CLASSIFICACAO)
            resultado = "turno seguiu"
    assert resultado == "turno seguiu"
    assert "langfuse_tracing_falhou" in caplog.text


# ---------------------------------------------------------------------------
# server.py usa o traço e marca a classificação do turno
# ---------------------------------------------------------------------------


def test_processar_chat_marca_as_tags_da_classificacao_no_trace(monkeypatch):
    cliente = ClienteFalso()
    monkeypatch.setattr(tracing, "get_langfuse", lambda: cliente)
    monkeypatch.setattr(server, "get_tracing_handler", object)

    class _Grafo:
        async def ainvoke(self, inputs, config=None):
            self.config = config
            return {"messages": [AIMessage(content="oi")], "classificacao": CLASSIFICACAO}

    grafo = _Grafo()
    req = server.ChatRequest(conversation_id="conv-7", mensagem="caiu", telefone=TELEFONE)
    asyncio.run(server.processar_chat(grafo, req))
    assert cliente.span.updates[-1]["tags"][1:] == [
        "intencao:reclamacao_problema",
        "categoria:sem_conexao",
        "origem:jev",
    ]
    # sessão/usuário/tags vão no trace raiz; o metadata do handler não repete telefone
    assert "metadata" not in grafo.config and len(grafo.config["callbacks"]) == 1


# ---------------------------------------------------------------------------
# máscara no span real (SDK Langfuse + exportador em memória, sem rede)
# ---------------------------------------------------------------------------


@pytest.fixture(scope="module")
def _langfuse_do_modulo():
    """Um só cliente real por módulo (o SDK mantém singleton por chave pública)."""
    pytest.importorskip("langfuse")
    from langfuse import Langfuse
    from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter
    from opentelemetry.sdk.trace import TracerProvider
    from opentelemetry.sdk.trace.export import SimpleSpanProcessor, SpanExportResult
    from opentelemetry.sdk.trace.export.in_memory_span_exporter import InMemorySpanExporter

    memoria = InMemorySpanExporter()
    provedor = TracerProvider()
    provedor.add_span_processor(SimpleSpanProcessor(memoria))
    with pytest.MonkeyPatch.context() as mp:
        # O SDK também exporta por HTTP para o host: aqui nada sai da máquina.
        mp.setattr(OTLPSpanExporter, "export", lambda self, spans: SpanExportResult.SUCCESS)
        cliente = Langfuse(
            public_key="pk-lf-teste",
            secret_key="sk-lf-teste",
            host="http://127.0.0.1:9",
            mask=mascarar_dado,
            tracer_provider=provedor,
        )
        yield cliente, memoria
        cliente.shutdown()


@pytest.fixture
def langfuse_real(_langfuse_do_modulo):
    cliente, memoria = _langfuse_do_modulo
    memoria.clear()
    return cliente, memoria


@tool
def _tool_com_pii(cpf: str, telefone: str = "") -> str:
    """Tool de teste com PII nos args."""
    return f"ok {cpf}"


@tool
def _tool_sem_pii(assunto: str, telefone: str = "") -> str:
    """Tool de teste sem PII proposta pelo modelo (só o telefone injetado)."""
    return "ok"


def _atributos(memoria) -> str:
    return " | ".join(f"{s.name}: {dict(s.attributes)}" for s in memoria.get_finished_spans())


def test_span_de_tool_sem_pii_nos_args_nao_leva_o_telefone_injetado(langfuse_real):
    """Item 3 da revisão do PR 30: o `telefone` da política ia ao span de entrada."""
    from langfuse.langchain import CallbackHandler

    cliente, memoria = langfuse_real
    with cliente.start_as_current_span(name="chat"):
        _tool_sem_pii.invoke(
            {"assunto": "internet caiu", "telefone": f"+55{TELEFONE}"},
            config={"callbacks": [CallbackHandler()]},
        )
    atributos = _atributos(memoria)
    assert "_tool_sem_pii" in atributos and "internet caiu" in atributos
    assert TELEFONE not in atributos and "31999990001" not in atributos


def test_span_de_tool_com_pii_nao_leva_cpf_nem_telefone_nem_nome(langfuse_real):
    from langfuse.langchain import CallbackHandler

    registrar_nomes("conv-1", ["Marlene"])
    cliente, memoria = langfuse_real
    with cliente.start_as_current_span(name="chat") as raiz:
        raiz.update_trace(input={"mensagem": f"Marlene aqui, cpf {CPF}"})
        _tool_com_pii.invoke(
            {"cpf": CPF, "telefone": f"+55{TELEFONE}"},
            config={"callbacks": [CallbackHandler()]},
        )
    atributos = _atributos(memoria)
    for proibido in (CPF, "52998224725", TELEFONE, "Marlene"):
        assert proibido not in atributos, proibido
    assert "_tool_com_pii" in atributos


def test_excecao_do_turno_com_pii_nao_chega_ao_span_exportado(langfuse_real):
    """Bloqueante da revisão: o OTel grava `exception.message`/`stacktrace` em claro."""
    cliente, memoria = langfuse_real
    with pytest.raises(RuntimeError):
        with TracoDoTurno(
            conversation_id="conv-x", canal="whatsapp", telefone=TELEFONE, cliente=cliente
        ):
            raise RuntimeError(f"falha cpf {CPF} tel +55{TELEFONE}")
    spans = memoria.get_finished_spans()
    assert spans
    tudo = " | ".join(
        f"{dict(s.attributes)} {[dict(e.attributes) for e in s.events]} {s.status.description}"
        for s in spans
    )
    for proibido in (CPF, "52998224725", TELEFONE):
        assert proibido not in tudo, proibido
    assert "RuntimeError" in tudo
