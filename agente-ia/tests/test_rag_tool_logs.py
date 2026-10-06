"""ZG-2 (LGPD): o texto do cliente não pode aparecer nos logs de nível INFO.

`buscar_chunks` logava a pergunta inteira em `rag_query` (INFO) e em
`rag_falhou` (WARNING). O texto vai só para DEBUG; INFO fica com o tamanho.
"""

from __future__ import annotations

import logging

import pytest

PERGUNTA = "como rastreio a nota fiscal 12345 da transportadora Silva?"
LOGGER = "src.tools.rag_tool"


class _Doc:
    def __init__(self, texto: str) -> None:
        self.page_content = texto
        self.metadata = {"secao": "Rastreamento", "source": "rastreamento.md"}


class _RetrieverOK:
    def invoke(self, pergunta: str):
        return [_Doc("Para rastrear, abra o módulo de rastreamento.")]


class _RetrieverQuebrado:
    def invoke(self, pergunta: str):
        raise RuntimeError("pgvector fora do ar")


def _mensagens(caplog, nivel_minimo: int) -> list[str]:
    return [r.getMessage() for r in caplog.records if r.levelno >= nivel_minimo]


@pytest.fixture
def rag_tool(monkeypatch):
    from src.tools import rag_tool as mod

    monkeypatch.setattr(mod, "get_retriever", lambda k: _RetrieverOK())
    return mod


def test_rag_query_info_nao_contem_a_pergunta(rag_tool, caplog):
    with caplog.at_level(logging.DEBUG, logger=LOGGER):
        out = rag_tool.buscar_chunks(PERGUNTA)

    assert out["encontrado"] is True
    info = _mensagens(caplog, logging.INFO)
    assert any(m.startswith("rag_query ") for m in info), info
    assert not any(PERGUNTA in m for m in info), info
    assert not any("Silva" in m for m in info), info
    # Em DEBUG a query continua disponível para diagnóstico.
    debug = [m for m in _mensagens(caplog, logging.DEBUG) if m.startswith("rag_query_texto")]
    assert debug and PERGUNTA in debug[0]


def test_rag_falha_warning_nao_contem_a_pergunta(rag_tool, monkeypatch, caplog):
    monkeypatch.setattr(rag_tool, "get_retriever", lambda k: _RetrieverQuebrado())
    with caplog.at_level(logging.DEBUG, logger=LOGGER):
        out = rag_tool.buscar_chunks(PERGUNTA)

    assert out["encontrado"] is False
    assert out["erro"] == "base de conhecimento indisponível no momento"
    avisos = [r for r in caplog.records if r.levelno >= logging.WARNING]
    assert avisos and "rag_falhou" in avisos[0].getMessage()
    assert not any(PERGUNTA in m for m in _mensagens(caplog, logging.INFO))
    assert any(PERGUNTA in m for m in _mensagens(caplog, logging.DEBUG))
