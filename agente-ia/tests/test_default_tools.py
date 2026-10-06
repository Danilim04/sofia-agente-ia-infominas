"""Testes de prevenção da lista canônica de tools (`get_default_tools`) — ZE-1.

Incidente (rodada 1): a tool de TESTE `rastrear_nota_fiscal` (mock em
`src/tools/crm_mocks.py`) estava na lista oficial do agente e respondeu "nota
não encontrada" a cliente real — 14 chamadas com números de nota verdadeiros
contra um dicionário fictício. Daqui em diante NENHUMA tool do módulo
`src.tools.crm_mocks` pode entrar na lista canônica: mock é só para
testes/harness, até existir integração real.
"""

from __future__ import annotations

from langchain_core.tools import BaseTool

from src.agent.graph import get_default_tools
from src.agent.prompts import SYSTEM_PROMPT_AGENTE
from src.tools import crm_mocks
from src.tools.sac_tools import SAC_TOOLS

MODULO_MOCKS = crm_mocks.__name__  # "src.tools.crm_mocks"


def _modulo_de_origem(tool: BaseTool) -> str | None:
    """Módulo da função embrulhada pelo `@tool` (`StructuredTool.func`/`coroutine`).

    Fallback: módulo da classe (tools definidas como subclasse de `BaseTool`).
    """
    for attr in ("func", "coroutine"):
        fn = getattr(tool, attr, None)
        if fn is not None:
            return getattr(fn, "__module__", None)
    return type(tool).__module__


def _tools_definidas_em_crm_mocks() -> dict[int, str]:
    """Toda instância de `BaseTool` presente no namespace do módulo mock."""
    return {
        id(obj): nome
        for nome, obj in vars(crm_mocks).items()
        if isinstance(obj, BaseTool)
    }


def test_nenhuma_tool_da_lista_oficial_vem_de_crm_mocks():
    """Falha se QUALQUER tool de `get_default_tools()` vier de `src.tools.crm_mocks`.

    Três detectores independentes (módulo da função, identidade do objeto e
    nome), para pegar também re-exports e aliases.
    """
    tools = get_default_tools()
    assert tools, "lista canônica vazia"

    por_identidade = _tools_definidas_em_crm_mocks()
    nomes_mock = {t.name for t in crm_mocks.CRM_TOOLS} | set(por_identidade.values())

    infratoras = sorted(
        t.name
        for t in tools
        if _modulo_de_origem(t) == MODULO_MOCKS
        or id(t) in por_identidade
        or t.name in nomes_mock
    )
    assert infratoras == [], (
        f"tool(s) de TESTE ({MODULO_MOCKS}) na lista oficial do agente: "
        f"{infratoras} — mock não pode atender cliente real (ZE-1)"
    )


def test_lista_oficial_e_exatamente_as_sac_tools():
    """Hoje a lista canônica são só os chamados reais (SAC via gateway Go)."""
    assert [t.name for t in get_default_tools()] == [t.name for t in SAC_TOOLS]


def test_rastrear_nota_fiscal_fora_da_lista_oficial():
    assert "rastrear_nota_fiscal" not in {t.name for t in get_default_tools()}


def test_toda_tool_oficial_vem_de_modulo_de_producao():
    """Nenhuma tool canônica pode vir de módulo de mock/harness."""
    for t in get_default_tools():
        modulo = _modulo_de_origem(t) or ""
        assert "mock" not in modulo, f"{t.name} vem de módulo de mock: {modulo}"


def test_prompt_nao_anuncia_rastreio_de_nota_fiscal_ao_modelo():
    """O system prompt não pode prometer uma capacidade que o agente não tem."""
    sp = SYSTEM_PROMPT_AGENTE
    assert "rastrear_nota_fiscal" not in sp
    assert "rastrear ela aqui" not in sp
    assert "rastreei aqui" not in sp
    # Diz explicitamente que não há ferramenta de consulta operacional e para
    # onde orientar (plataforma / chamado).
    assert "não tem ferramenta" in sp.lower()
    assert "Histórico/Tracking" in sp
