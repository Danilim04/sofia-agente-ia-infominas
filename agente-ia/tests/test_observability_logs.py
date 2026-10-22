"""Eventos de log estruturado do cérebro (E4.3): nome, campos e PII.

Os módulos de verdade emitem o evento (roteador, nó de tools, server) e o
teste confere nome e campos contra o catálogo `observability/eventos.py`.
"""

from __future__ import annotations

import asyncio
import logging

import pytest
from langchain_core.messages import AIMessage
from langchain_core.tools import tool

import server
from src.agent import nodes
from src.classificacao.fallback_llm import ResultadoFallback
from src.classificacao.roteador import classificar
from src.classificacao.tipos import Mensagem
from src.observability import metricas
from src.observability.eventos import EVENTOS, parse_evento, registrar
from tests.fakes import (
    FakeFallbackLLM,
    FakeJEV,
    config_de_teste,
    resp_jev,
    taxonomia_de_teste,
)

CPF = "529.982.247-25"


def _eventos(caplog, nome: str) -> list[dict[str, str]]:
    achados = []
    for r in caplog.records:
        n, campos = parse_evento(r.getMessage())
        if n == nome:
            achados.append(campos)
    return achados


class _Grafo:
    def __init__(self, valores: dict):
        self._valores = valores

    async def ainvoke(self, inputs, config=None):
        return self._valores


def _classificar(jev, fallback=None):
    return asyncio.run(
        classificar(
            [Mensagem("cliente", "minha internet caiu")],
            None,
            jev,
            fallback,
            taxonomia=taxonomia_de_teste(),
            config=config_de_teste(),
        )
    )


@pytest.fixture
def contador():
    metricas.zerar()
    handler = metricas.instalar_contador()
    yield handler
    logging.getLogger("src").removeHandler(handler)
    metricas.zerar()


# ---------------------------------------------------------------------------
# catálogo e parser
# ---------------------------------------------------------------------------


def test_catalogo_cobre_os_eventos_do_cerebro():
    esperados = {"chat_request", "classificacao", "tool_exec", "transferencia", "jev_indisponivel"}
    assert esperados <= set(EVENTOS)
    for spec in EVENTOS.values():
        assert spec.componente == "cerebro" and spec.campos and spec.descricao


def test_parse_evento_separa_valores_com_espaco_e_lista():
    nome, campos = parse_evento("tool_exec nome=abrir args=['a', 'b']")
    assert nome == "tool_exec" and campos == {"nome": "abrir", "args": "['a', 'b']"}


def test_registrar_recusa_campo_fora_do_contrato():
    with pytest.raises(ValueError, match="fora do contrato"):
        registrar(logging.getLogger("t"), "chat_request", cpf="x")


# ---------------------------------------------------------------------------
# chat_request / transferencia (server.py)
# ---------------------------------------------------------------------------


def test_chat_request_tem_nome_e_campos_do_catalogo_e_nao_leva_a_mensagem(caplog):
    req = server.ChatRequest(
        conversation_id="conv-1",
        mensagem=f"meu cpf é {CPF}",
        telefone="+5531999990001",
        nome_contato="Maria",
        contato={"forma": "telefone"},
    )
    with caplog.at_level(logging.DEBUG, logger="server"):
        asyncio.run(server.processar_chat(_Grafo({"messages": [AIMessage(content="oi")]}), req))
    (campos,) = _eventos(caplog, "chat_request")
    assert set(campos) == set(EVENTOS["chat_request"].campos)
    assert campos["conversation_id"] == "conv-1"
    assert campos["telefone"] == "*" * 11 + "001"
    assert campos["identificado"] == "True" and campos["len_mensagem"] == str(len(req.mensagem))
    assert "+5531999990001" not in caplog.text


def test_transferencia_do_cerebro_sai_com_origem_e_sem_motivo(caplog):
    valores = {
        "messages": [AIMessage(content="vou chamar um atendente")],
        "acoes": [{"tipo": "transferir", "origem": "frustracao", "motivo": "cliente irritado"}],
    }
    req = server.ChatRequest(conversation_id="conv-2", mensagem="que absurdo")
    with caplog.at_level(logging.INFO, logger="server"):
        asyncio.run(server.processar_chat(_Grafo(valores), req))
    (campos,) = _eventos(caplog, "transferencia")
    assert campos == {"conversation_id": "conv-2", "origem": "frustracao"}
    assert "cliente irritado" not in caplog.text


def test_sem_acao_de_transferir_nao_ha_evento_de_transferencia(caplog):
    req = server.ChatRequest(conversation_id="conv-3", mensagem="oi")
    with caplog.at_level(logging.INFO, logger="server"):
        asyncio.run(server.processar_chat(_Grafo({"messages": [AIMessage(content="oi")]}), req))
    assert _eventos(caplog, "transferencia") == []


# ---------------------------------------------------------------------------
# classificacao / tool_exec / jev_indisponivel
# ---------------------------------------------------------------------------


def test_classificacao_tem_os_campos_do_catalogo(caplog):
    with caplog.at_level(logging.INFO, logger="src.classificacao.roteador"):
        _classificar(FakeJEV(padrao=resp_jev()))
    (campos,) = _eventos(caplog, "classificacao")
    assert set(campos) == set(EVENTOS["classificacao"].campos)
    assert campos["origem"] == "jev" and campos["jev"] == "True"
    assert campos["categoria"] == "sem_conexao"


@tool
def _eco(x: str) -> str:
    """eco"""
    return x


def test_tool_exec_leva_so_os_nomes_dos_args(caplog):
    tn = nodes.make_tools_node([_eco])
    estado = {
        "messages": [
            AIMessage(content="", tool_calls=[{"id": "1", "name": "_eco", "args": {"x": CPF}}])
        ]
    }
    with caplog.at_level(logging.INFO, logger="src.agent.nodes"):
        tn(estado)
    (campos,) = _eventos(caplog, "tool_exec")
    assert set(campos) == set(EVENTOS["tool_exec"].campos)
    assert campos == {"nome": "_eco", "args": "['x']"}
    assert CPF not in caplog.text


def test_jev_indisponivel_loga_so_o_tipo_da_excecao(caplog):
    fb = FakeFallbackLLM(padrao=ResultadoFallback("outro", "indefinida"))
    with caplog.at_level(logging.WARNING, logger="src.classificacao.roteador"):
        _classificar(FakeJEV(padrao=RuntimeError(f"503 para o cpf {CPF}")), fb)
    (campos,) = _eventos(caplog, "jev_indisponivel")
    assert campos == {"erro": "RuntimeError"}
    assert CPF not in caplog.text


# ---------------------------------------------------------------------------
# métrica jev_indisponivel (fail-soft)
# ---------------------------------------------------------------------------


def test_metrica_jev_indisponivel_conta_cada_turno_em_fail_soft(contador, caplog):
    fb = FakeFallbackLLM(padrao=ResultadoFallback("outro", "indefinida"))
    with caplog.at_level(logging.INFO):
        for _ in range(3):
            c = _classificar(FakeJEV(padrao=RuntimeError("503")), fb)
            assert c.jev_disponivel is False  # fail-soft: o turno seguiu
    assert metricas.contagem("jev_indisponivel") == 3
    metricas_logadas = _eventos(caplog, "sofia_metrica")
    assert [e["total"] for e in metricas_logadas] == ["1", "2", "3"]
    assert {e["metrica"] for e in metricas_logadas} == {"jev_indisponivel"}


def test_jev_saudavel_nao_conta(contador):
    _classificar(FakeJEV(padrao=resp_jev()))
    assert metricas.contagem("jev_indisponivel") == 0


def test_alerta_de_taxa_nao_conta_como_indisponibilidade(contador):
    logging.getLogger("src.classificacao.roteador").error("jev_indisponivel_alerta taxa=1.00")
    assert metricas.contagem("jev_indisponivel") == 0


def test_contador_e_idempotente(contador):
    assert metricas.instalar_contador() is contador
    handlers = logging.getLogger("src").handlers
    assert sum(isinstance(h, metricas.ContadorDeEventos) for h in handlers) == 1
