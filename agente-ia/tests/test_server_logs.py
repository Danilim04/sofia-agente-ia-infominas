"""server.py não loga a mensagem do cliente em claro em nenhum nível (E4.3, item 7 da revisão do PR 30).

Antes: `logger.debug("chat_request_mensagem … mensagem=%r")` levava o CPF/telefone
da mensagem (e o nome do contato) ao DEBUG. Aqui o DEBUG liga e a saída que o
operador veria (handler com a máscara instalada, como o `_setup_logging`) é
inspecionada por CPF, telefone e nome.
"""

from __future__ import annotations

import asyncio
import io
import logging

import pytest
from langchain_core.messages import AIMessage

import server
from src.observability.pii import esquecer_nomes, instalar_mascara_pii

CPF = "529.982.247-25"
TELEFONE_NA_MENSAGEM = "(31) 99999-0001"


class _Grafo:
    def __init__(self, reply: str):
        self._reply = reply

    async def ainvoke(self, inputs, config=None):
        return {"messages": [AIMessage(content=self._reply)]}


@pytest.fixture
def saida_debug():
    """O que o operador veria com LOG_LEVEL=DEBUG: handler com o formatter do `_setup_logging`."""
    esquecer_nomes()
    saida = io.StringIO()
    handler = logging.StreamHandler(saida)
    handler.setFormatter(logging.Formatter("%(asctime)s [%(levelname)s] %(name)s: %(message)s"))
    instalar_mascara_pii([handler])
    logger = logging.getLogger("server")
    antes = (logger.level, logger.propagate)
    logger.addHandler(handler)
    logger.setLevel(logging.DEBUG)
    logger.propagate = False
    yield saida
    logger.removeHandler(handler)
    logger.setLevel(antes[0])
    logger.propagate = antes[1]
    esquecer_nomes()


def _turno(mensagem: str, reply: str = "Certo, anotei.", nome: str | None = "Joana Prado"):
    req = server.ChatRequest(
        conversation_id="conv-dbg",
        mensagem=mensagem,
        telefone="+5531988887777",
        nome_contato=nome,
    )
    return asyncio.run(server.processar_chat(_Grafo(reply), req))


def test_debug_nao_leva_cpf_telefone_nem_nome_da_mensagem(saida_debug):
    _turno(f"Oi, sou Joana Prado, meu cpf é {CPF} e meu telefone {TELEFONE_NA_MENSAGEM}")
    texto = saida_debug.getvalue()
    assert "chat_request_mensagem" in texto  # o DEBUG continua útil…
    for proibido in (CPF, "529.982.247", "52998224725", "99999-0001", "Joana", "Prado", "7777"):
        assert proibido not in texto, proibido
    assert "meu cpf" in texto  # …com o texto do cliente já sem PII


def test_debug_da_resposta_da_sofia_tambem_nao_ecoa_pii(saida_debug):
    _turno("qual meu protocolo?", reply=f"Joana, confirmo o CPF {CPF}.")
    texto = saida_debug.getvalue()
    assert "chat_response_full" in texto
    assert CPF not in texto and "Joana" not in texto


def test_nome_do_contato_nunca_e_logado_em_nenhum_nivel(saida_debug):
    _turno("bom dia", nome="Bartolomeu Quintanilha")
    assert "Bartolomeu" not in saida_debug.getvalue()
    assert "Quintanilha" not in saida_debug.getvalue()


def test_info_so_leva_o_tamanho_da_mensagem(saida_debug):
    logging.getLogger("server").setLevel(logging.INFO)
    mensagem = f"meu cpf {CPF}"
    _turno(mensagem)
    texto = saida_debug.getvalue()
    assert f"len_mensagem={len(mensagem)}" in texto
    assert "chat_request_mensagem" not in texto and "meu cpf" not in texto


def test_falha_do_turno_nao_vaza_cpf_da_excecao(saida_debug):
    class _GrafoQuebrado:
        async def ainvoke(self, inputs, config=None):
            raise ValueError(f"cpf inválido {CPF}")

    req = server.ChatRequest(conversation_id="conv-err", mensagem="oi")
    resposta = asyncio.run(server.processar_chat(_GrafoQuebrado(), req))
    assert resposta.erro is not None
    texto = saida_debug.getvalue()
    assert "chat_erro" in texto and CPF not in texto
