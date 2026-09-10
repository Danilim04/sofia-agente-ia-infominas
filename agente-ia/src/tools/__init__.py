from src.tools.crm_mocks import (
    CRM_TOOLS,
    abrir_novo_chamado,
    buscar_cliente_por_telefone,
    rastrear_nota_fiscal,
    verificar_chamados_abertos,
)
from src.tools.rag_tool import consultar_base_conhecimento

__all__ = [
    "CRM_TOOLS",
    "abrir_novo_chamado",
    "buscar_cliente_por_telefone",
    "consultar_base_conhecimento",
    "rastrear_nota_fiscal",
    "verificar_chamados_abertos",
]
