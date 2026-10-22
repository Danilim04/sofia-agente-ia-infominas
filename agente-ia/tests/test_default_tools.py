"""Testes de prevenção da lista canônica de tools (`get_default_tools`).

Lição herdada: uma tool de TESTE (mock) entrou na lista oficial do agente e
respondeu dados fictícios a cliente real. Daqui em diante nenhuma tool de
módulo de mock/harness/teste pode entrar na lista canônica, e o system prompt
não pode anunciar capacidade que o agente não tem.

Nesta etapa (E1.3) a lista está VAZIA: as tools do domínio antigo saíram e as
do Infodesk entram em E2.3 — os testes abaixo já valem para elas.
"""

from __future__ import annotations

from langchain_core.tools import BaseTool

from src.agent.graph import get_default_tools
from src.agent.prompts import SYSTEM_PROMPT_AGENTE
from src.agent.tool_policy import POLITICAS

#: Tools que o plano prevê expor ao modelo (docs/plano/01-ferramentas.md, A1–A6 e A8).
TOOLS_DO_PLANO = {
    "informar_identificacao",
    "preparar_abertura_chamado",
    "abrir_chamado",
    "consultar_status_chamado",
    "listar_meus_chamados",
    "transferir_para_atendente",
    # E5.3, ADR 0009: cadastro de solicitante em duas fases (A8).
    "preparar_cadastro_solicitante",
    "cadastrar_solicitante",
}


def _modulo_de_origem(tool: BaseTool) -> str:
    """Módulo da função embrulhada pelo `@tool` (`StructuredTool.func`/`coroutine`).

    Fallback: módulo da classe (tools definidas como subclasse de `BaseTool`).
    """
    for attr in ("func", "coroutine"):
        fn = getattr(tool, attr, None)
        if fn is not None:
            return getattr(fn, "__module__", "") or ""
    return type(tool).__module__


def test_lista_canonica_tem_as_8_tools_do_infodesk_e_cada_uma_tem_politica():
    nomes = [t.name for t in get_default_tools()]
    assert nomes == [
        "informar_identificacao",
        "preparar_abertura_chamado",
        "abrir_chamado",
        "consultar_status_chamado",
        "listar_meus_chamados",
        "transferir_para_atendente",
        "preparar_cadastro_solicitante",
        "cadastrar_solicitante",
    ]
    assert set(nomes) == TOOLS_DO_PLANO == set(POLITICAS)


def test_toda_tool_oficial_vem_de_modulo_de_producao():
    """Nenhuma tool canônica pode vir de módulo de mock/harness/teste."""
    for t in get_default_tools():
        modulo = _modulo_de_origem(t)
        assert modulo.startswith("src.tools."), f"{t.name} vem de fora de src.tools: {modulo}"
        assert not any(p in modulo for p in ("mock", "fake", "tests")), (
            f"{t.name} vem de módulo de teste: {modulo}"
        )


def test_toda_tool_oficial_esta_no_plano():
    fora = {t.name for t in get_default_tools()} - TOOLS_DO_PLANO
    assert not fora, f"tools fora do catálogo de docs/plano/01-ferramentas.md: {fora}"


def test_prompt_nao_anuncia_capacidade_que_o_agente_nao_tem():
    sp = SYSTEM_PROMPT_AGENTE
    for proibido in ("rastrear_nota_fiscal", "buscar_na_web", "pesquisar na web", "rastreei aqui"):
        assert proibido not in sp
    # Diz explicitamente que não acessa a internet e que ferramenta ausente
    # não pode ser fingida.
    assert "NÃO tem acesso à internet" in sp
    assert "não finja tê-la usado" in sp


def test_prompt_so_cita_tools_do_plano():
    """Toda tool mencionada em crase no prompt pertence ao catálogo.

    Nome de tool = verbo_objeto (tem `_`); `titulo`/`descricao`/`proposta` em
    crase são argumentos/campos, não tools.
    """
    import re

    citadas = set(re.findall(r"`([a-z]+_[a-z_]+)`", SYSTEM_PROMPT_AGENTE))
    assert citadas, "o prompt precisa descrever o fluxo pelas tools do plano"
    assert citadas <= TOOLS_DO_PLANO, citadas - TOOLS_DO_PLANO
