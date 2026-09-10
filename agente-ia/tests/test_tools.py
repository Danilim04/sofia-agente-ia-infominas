"""Testes unitários das tools mockadas do CRM (Épico 2)."""

from __future__ import annotations

import re

import pytest

from src.tools.crm_mocks import (
    CRM_TOOLS,
    abrir_novo_chamado,
    buscar_cliente_por_telefone,
    rastrear_nota_fiscal,
    verificar_chamados_abertos,
)


# ---------------------------------------------------------------------------
# buscar_cliente_por_telefone
# ---------------------------------------------------------------------------


def test_buscar_cliente_telefone_conhecido_retorna_schema_completo():
    resultado = buscar_cliente_por_telefone.invoke({"telefone": "11999990001"})
    assert resultado["encontrado"] is True
    assert resultado["id_cliente"] == "CLI-1001"
    assert resultado["nome"] == "Mariana Souza"
    assert resultado["plano"] in {"Starter", "Pro", "Business"}
    assert resultado["status_conta"] in {"ativo", "inadimplente", "suspenso"}


def test_buscar_cliente_aceita_telefone_com_mascara():
    resultado = buscar_cliente_por_telefone.invoke(
        {"telefone": "(11) 99999-0002"}
    )
    assert resultado["encontrado"] is True
    assert resultado["id_cliente"] == "CLI-1002"


def test_buscar_cliente_telefone_desconhecido_retorna_nao_encontrado():
    resultado = buscar_cliente_por_telefone.invoke({"telefone": "11000000000"})
    assert resultado["encontrado"] is False
    assert resultado["id_cliente"] is None
    # Telefone deve aparecer mascarado (LLM06 — sensitive info)
    assert resultado["telefone_consultado"].endswith("0000")
    assert resultado["telefone_consultado"].startswith("*")


# ---------------------------------------------------------------------------
# verificar_chamados_abertos — variações: 0, 1 e múltiplos chamados
# ---------------------------------------------------------------------------


def test_verificar_chamados_cliente_sem_tickets():
    resultado = verificar_chamados_abertos.invoke({"id_cliente": "CLI-1001"})
    assert resultado["total"] == 0
    assert resultado["chamados"] == []


def test_verificar_chamados_cliente_com_um_ticket():
    resultado = verificar_chamados_abertos.invoke({"id_cliente": "CLI-1002"})
    assert resultado["total"] == 1
    chamado = resultado["chamados"][0]
    assert set(chamado.keys()) >= {"id", "assunto", "status", "criado_em"}


def test_verificar_chamados_cliente_com_multiplos_tickets():
    resultado = verificar_chamados_abertos.invoke({"id_cliente": "CLI-1003"})
    assert resultado["total"] >= 2
    assert all("id" in c for c in resultado["chamados"])


# ---------------------------------------------------------------------------
# rastrear_nota_fiscal — variações no ciclo de entrega da mercadoria.
# O escopo (`grupos_emp_sessao`) é InjectedToolArg: em produção quem o
# preenche é a política (`tool_policy`), nunca o LLM. Aqui invocamos direto.
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "grupo_emp,numero_nota,etapa,comprovacao",
    [
        ("AZAPERS", "NF-1042", "em_rota", "pendente"),
        ("AZAPERS", "NF-1043", "entregue", "validada"),
        ("ALMEIDA LOG", "NF-2001", "transbordo", "pendente"),
        ("PEREIRA CARGAS", "NF-3001", "entregue", "rejeitada"),
    ],
)
def test_rastrear_nota_fiscal_variacoes_no_ciclo(
    grupo_emp, numero_nota, etapa, comprovacao
):
    resultado = rastrear_nota_fiscal.invoke(
        {"numero_nota": numero_nota, "grupos_emp_sessao": [grupo_emp]}
    )
    assert resultado["encontrado"] is True
    assert resultado["etapa"] == etapa
    assert resultado["comprovacao"] == comprovacao
    assert re.match(r"\d{4}-\d{2}-\d{2}T", resultado["atualizado_em"])


def test_rastrear_nota_fiscal_normaliza_numero_minusculo_e_espacos():
    resultado = rastrear_nota_fiscal.invoke(
        {"numero_nota": "  nf-1042 ", "grupos_emp_sessao": ["AZAPERS"]}
    )
    assert resultado["encontrado"] is True
    assert resultado["numero_nota"] == "NF-1042"


def test_rastrear_nota_fiscal_numero_desconhecido_nao_encontrado():
    resultado = rastrear_nota_fiscal.invoke(
        {"numero_nota": "NF-0000", "grupos_emp_sessao": ["AZAPERS"]}
    )
    assert resultado["encontrado"] is False


def test_rastrear_nota_fiscal_nao_vaza_nf_de_outro_grupo():
    # NF-2001 existe, mas é da ALMEIDA LOG — sessão de outro grupo não a vê
    # nem tem a existência confirmada (F7/LLM06).
    resultado = rastrear_nota_fiscal.invoke(
        {"numero_nota": "NF-2001", "grupos_emp_sessao": ["AZAPERS"]}
    )
    assert resultado["encontrado"] is False


def test_rastrear_nota_fiscal_sem_escopo_falha_fechada():
    # Sessão sem grupos (não identificado/telefone desconhecido) → nada sai.
    resultado = rastrear_nota_fiscal.invoke(
        {"numero_nota": "NF-1042", "grupos_emp_sessao": []}
    )
    assert resultado["encontrado"] is False


def test_rastrear_nota_fiscal_schema_do_llm_nao_expoe_escopo():
    """O IDOR é irrepresentável: o schema visto pelo modelo só tem o número."""
    schema = rastrear_nota_fiscal.tool_call_schema.model_json_schema()
    assert "numero_nota" in schema["properties"]
    assert "grupos_emp_sessao" not in schema["properties"]
    assert "id_cliente" not in schema["properties"]


# ---------------------------------------------------------------------------
# abrir_novo_chamado
# ---------------------------------------------------------------------------


def test_abrir_novo_chamado_retorna_ticket_aberto():
    resultado = abrir_novo_chamado.invoke(
        {
            "id_cliente": "CLI-1001",
            "resumo": "Painel não carrega após login",
        }
    )
    assert resultado["status"] == "aberto"
    assert resultado["ticket_id"].startswith("TCK-")
    assert resultado["assunto"] == "Painel não carrega após login"


def test_abrir_novo_chamado_sanitiza_resumo_longo_e_quebras():
    resumo = "  linha 1\n\n   linha 2  " + ("x" * 400)
    resultado = abrir_novo_chamado.invoke(
        {"id_cliente": "CLI-1001", "resumo": resumo}
    )
    assert "\n" not in resultado["assunto"]
    assert len(resultado["assunto"]) <= 280


def test_abrir_novo_chamado_resumo_vazio_e_rejeitado():
    resultado = abrir_novo_chamado.invoke(
        {"id_cliente": "CLI-1001", "resumo": "   "}
    )
    assert resultado["status"] == "rejeitado"
    assert resultado["ticket_id"] is None


def test_abrir_novo_chamado_id_deterministico_para_mesmo_input():
    payload = {"id_cliente": "CLI-1001", "resumo": "Erro X"}
    a = abrir_novo_chamado.invoke(payload)
    b = abrir_novo_chamado.invoke(payload)
    assert a["ticket_id"] == b["ticket_id"]


# ---------------------------------------------------------------------------
# Sanidade do agregado exportado
# ---------------------------------------------------------------------------


def test_crm_tools_lista_contem_as_quatro_tools():
    nomes = {t.name for t in CRM_TOOLS}
    assert nomes == {
        "buscar_cliente_por_telefone",
        "verificar_chamados_abertos",
        "rastrear_nota_fiscal",
        "abrir_novo_chamado",
    }


def test_todas_as_tools_tem_docstring_para_o_llm():
    for t in CRM_TOOLS:
        assert t.description and len(t.description.strip()) > 30
