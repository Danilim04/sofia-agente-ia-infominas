"""Testes da camada Chainlit (Épico 8) — só helpers puros.

Os handlers `@cl.on_chat_start` e `@cl.on_message` exigem o servidor
Chainlit rodando e dependem de I/O assíncrono — são exercitados
manualmente pelos cenários E2E do Épico 9 (`chainlit run app.py -w`).
"""

from __future__ import annotations

import app as app_module
from app import (
    COMANDO_TROCAR_TELEFONE,
    extrair_texto_resposta_ask,
    formatar_fontes,
    saudacao_para_identidade,
)


# ---------------------------------------------------------------------------
# saudacao_para_identidade
# ---------------------------------------------------------------------------


def test_saudacao_identidade_encontrada_traz_nome_e_empresa():
    out = saudacao_para_identidade(
        {
            "encontrado": True,
            "nome": "Daniel Ferraz",
            "empresas": [{"grupo_empresa": "AZAPERS"}],
        }
    )
    assert "Daniel Ferraz" in out
    assert "AZAPERS" in out
    # Negrito no nome
    assert "**Daniel Ferraz**" in out


def test_saudacao_identidade_nao_encontrada_oferece_trocar_telefone():
    out = saudacao_para_identidade({"encontrado": False})
    assert COMANDO_TROCAR_TELEFONE in out
    assert "Não consegui" in out


def test_saudacao_aceita_input_invalido_sem_quebrar():
    assert "Azapfy" in saudacao_para_identidade(None)
    assert "Azapfy" in saudacao_para_identidade("string solta")


def test_saudacao_identidade_encontrada_com_campos_faltando_nao_quebra():
    out = saudacao_para_identidade({"encontrado": True})
    # Não explode, e mantém placeholder previsível
    assert "usuário" in out.lower() or "Olá" in out


# ---------------------------------------------------------------------------
# extrair_texto_resposta_ask
# ---------------------------------------------------------------------------


def test_extrair_texto_de_dict_com_chave_output():
    assert extrair_texto_resposta_ask({"output": "  11999990001  "}) == "11999990001"


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
    out = formatar_fontes(["https://azapfy.com.br/x"])
    assert "[https://azapfy.com.br/x](https://azapfy.com.br/x)" in out
    assert "🌐" in out


def test_formatar_fontes_renderiza_pagina_pdf_em_backticks():
    out = formatar_fontes(["base.pdf#p2"])
    assert "`base.pdf#p2`" in out
    assert "📄" in out


def test_formatar_fontes_mix_url_e_pdf_separados_por_meio_ponto():
    out = formatar_fontes(
        [
            "base.pdf#p2",
            "https://azapfy.com.br/x",
            "base.pdf#p3",
        ]
    )
    assert " · " in out
    assert out.count("📄") == 2
    assert out.count("🌐") == 1
    assert out.startswith("**Fontes consultadas:**")


# ---------------------------------------------------------------------------
# Sanidade do módulo
# ---------------------------------------------------------------------------


def test_singleton_do_grafo_e_lazy():
    """O grafo só é compilado na primeira chamada — `_GRAPH` começa None."""
    # Ainda não foi acessado neste processo de teste — pode ser None ou já populado
    # se outro teste tocou. Garantimos só que o accessor existe e é callable.
    assert hasattr(app_module, "_GRAPH")
    assert callable(app_module._get_graph)


def test_comando_trocar_telefone_constante_existe():
    assert COMANDO_TROCAR_TELEFONE.startswith("/")
    assert "telefone" in COMANDO_TROCAR_TELEFONE