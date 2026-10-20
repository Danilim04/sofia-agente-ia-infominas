"""Testes da camada Chainlit (harness de dev) — só helpers puros.

Os handlers `@cl.on_chat_start` e `@cl.on_message` exigem o servidor
Chainlit rodando e dependem de I/O assíncrono — são exercitados
manualmente (`chainlit run app.py -w`).
"""

from __future__ import annotations

import app as app_module
from app import (
    COMANDO_TROCAR_TELEFONE,
    extrair_texto_resposta_ask,
    formatar_fontes,
    montar_contato_dev,
    normalizar_telefone,
    saudacao_dev,
)

# ---------------------------------------------------------------------------
# montar_contato_dev / normalizar_telefone / saudacao_dev
# ---------------------------------------------------------------------------


def test_normalizar_telefone_mantem_so_digitos():
    assert normalizar_telefone("(31) 99999-0001") == "31999990001"
    assert normalizar_telefone("+55 31 99999-0001") == "5531999990001"
    assert normalizar_telefone(None) == ""
    assert normalizar_telefone("abc") == ""


def test_montar_contato_dev_segue_o_contrato_a_sem_cache_de_identificacao():
    dados = montar_contato_dev("(31) 99999-0001", " Maria ")
    assert dados == {"telefone": "31999990001", "nome_contato": "Maria", "contato": None}


def test_montar_contato_dev_sem_nome():
    assert montar_contato_dev("31999990001", "")["nome_contato"] is None
    assert montar_contato_dev("31999990001", None)["nome_contato"] is None


def test_saudacao_dev_traz_nome_e_comando_de_troca():
    out = saudacao_dev("Maria")
    assert "**Maria**" in out
    assert "Sofia" in out
    assert COMANDO_TROCAR_TELEFONE in out


def test_saudacao_dev_aceita_input_invalido_sem_quebrar():
    assert "cliente" in saudacao_dev(None)
    assert "cliente" in saudacao_dev("   ")


# ---------------------------------------------------------------------------
# extrair_texto_resposta_ask
# ---------------------------------------------------------------------------


def test_extrair_texto_de_dict_com_chave_output():
    assert extrair_texto_resposta_ask({"output": "  31999990001  "}) == "31999990001"


def test_extrair_texto_de_dict_com_chave_content_alternativa():
    assert extrair_texto_resposta_ask({"content": "abc"}) == "abc"


def test_extrair_texto_de_objeto_com_output():
    class FakeMsg:
        output = "  zxy  "

    assert extrair_texto_resposta_ask(FakeMsg()) == "zxy"


def test_extrair_texto_aceita_none_dict_vazio_e_object_sem_campos():
    class Vazio:
        pass

    assert extrair_texto_resposta_ask(None) == ""
    assert extrair_texto_resposta_ask({}) == ""
    assert extrair_texto_resposta_ask(Vazio()) == ""


# ---------------------------------------------------------------------------
# formatar_fontes
# ---------------------------------------------------------------------------


def test_formatar_fontes_devolve_none_para_lista_vazia_ou_none():
    assert formatar_fontes([]) is None
    assert formatar_fontes(None) is None


def test_formatar_fontes_renderiza_url_como_link_clicavel():
    out = formatar_fontes(["https://exemplo.com.br/x"])
    assert "[https://exemplo.com.br/x](https://exemplo.com.br/x)" in out
    assert "🌐" in out


def test_formatar_fontes_renderiza_arquivo_em_backticks():
    out = formatar_fontes(["internet-residencial.md — Reiniciar o roteador"])
    assert "`internet-residencial.md — Reiniciar o roteador`" in out
    assert "📄" in out


def test_formatar_fontes_mix_url_e_arquivo_separados_por_meio_ponto():
    out = formatar_fontes(["a.md — X", "https://exemplo.com.br/x", "b.md — Y"])
    assert " · " in out
    assert out.count("📄") == 2
    assert out.count("🌐") == 1
    assert out.startswith("**Fontes consultadas:**")


# ---------------------------------------------------------------------------
# Sanidade do módulo
# ---------------------------------------------------------------------------


def test_singleton_do_grafo_e_lazy():
    """O grafo só é compilado na primeira chamada — `_GRAPH` começa None."""
    assert hasattr(app_module, "_GRAPH")
    assert callable(app_module._get_graph)


def test_comando_trocar_telefone_constante_existe():
    assert COMANDO_TROCAR_TELEFONE.startswith("/")
    assert "telefone" in COMANDO_TROCAR_TELEFONE
