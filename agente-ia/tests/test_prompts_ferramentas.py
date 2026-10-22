"""Seção de ferramentas do system prompt (E2.3): o texto tem de bater com o que as tools fazem."""

from __future__ import annotations

from src.agent.prompts import SYSTEM_PROMPT_AGENTE


def test_listar_meus_chamados_diz_abertos_no_infodesk_mais_os_abertos_por_aqui():
    """Revisão do PR 19: a lista deixou de ser só 'os abertos por aqui'."""
    assert "chamados abertos no Infodesk mais os abertos por aqui" in SYSTEM_PROMPT_AGENTE
    assert "a lista inclui só os chamados abertos por aqui" not in SYSTEM_PROMPT_AGENTE
    assert "só os chamados abertos por aqui" not in SYSTEM_PROMPT_AGENTE


def test_prompt_manda_repetir_a_observacao_da_lista():
    trecho = SYSTEM_PROMPT_AGENTE.split("listar_meus_chamados", 1)[1][:400]
    assert "observação" in trecho


def test_prompt_cita_as_8_tools_do_plano():
    for nome in (
        "informar_identificacao",
        "preparar_abertura_chamado",
        "abrir_chamado",
        "consultar_status_chamado",
        "listar_meus_chamados",
        "transferir_para_atendente",
        "preparar_cadastro_solicitante",
        "cadastrar_solicitante",
    ):
        assert f"`{nome}`" in SYSTEM_PROMPT_AGENTE, nome


def test_prompt_nao_deixa_o_modelo_escolher_mesa_prioridade_nem_confirmacao():
    assert "Você NÃO escolhe fila, prioridade nem destino" in SYSTEM_PROMPT_AGENTE
    assert "NÃO confirmam" in SYSTEM_PROMPT_AGENTE  # 'esse', número, pergunta
    assert "`abrir_chamado` (sem argumentos" in SYSTEM_PROMPT_AGENTE
