"""Tools mockadas que simulam o CRM/backend da Azapfy.

Estas tools são decoradas com `@tool` do LangChain e suas docstrings são lidas
pelo LLM para escolher qual chamar — por isso elas descrevem com clareza
*quando* usar cada uma e *quais* parâmetros enviar.

As respostas são determinísticas em função do input (2–3 variações por tool),
o que permite exercitar múltiplos caminhos do agente sem depender de dados
reais ou aleatoriedade.

Segurança (F7/IDOR — laudo 2026-08-27): NENHUMA tool exposta ao agente aceita
argumento de identidade/escopo vindo do LLM. O escopo de `rastrear_nota_fiscal`
(`grupos_emp_sessao`) é um `InjectedToolArg` preenchido pelo `tools_node` a
partir da identidade resolvida pelo gate (ver `src/agent/tool_policy.py`) —
não existe sequência de tokens que consulte NF de outro cliente.

`buscar_cliente_por_telefone`, `verificar_chamados_abertos` e
`abrir_novo_chamado` são LEGADO (Épicos 2-4): não entram em
`get_default_tools()` — chamados reais são as SAC tools via gateway Go.
"""

from __future__ import annotations

import re
from datetime import datetime, timedelta, timezone
from typing import Annotated, Any, Optional

from langchain_core.tools import InjectedToolArg, tool


# ---------------------------------------------------------------------------
# "Banco de dados" mockado
# ---------------------------------------------------------------------------

# `grupo_emp` espelha o `grupo_empresa` da identidade real (Contrato A). O
# telefone 11999990001 usa AZAPERS para casar com `identidade_mock` (harness).
_CLIENTES: dict[str, dict[str, Any]] = {
    "11999990001": {
        "id_cliente": "CLI-1001",
        "grupo_emp": "AZAPERS",
        "nome": "Mariana Souza",
        "plano": "Pro",
        "status_conta": "ativo",
    },
    "11999990002": {
        "id_cliente": "CLI-1002",
        "grupo_emp": "ALMEIDA LOG",
        "nome": "Ricardo Almeida",
        "plano": "Business",
        "status_conta": "inadimplente",
    },
    "11999990003": {
        "id_cliente": "CLI-1003",
        "grupo_emp": "PEREIRA CARGAS",
        "nome": "Júlia Pereira",
        "plano": "Starter",
        "status_conta": "ativo",
    },
}

_CHAMADOS: dict[str, list[dict[str, Any]]] = {
    "CLI-1001": [],
    "CLI-1002": [
        {
            "id": "TCK-7781",
            "assunto": "Falha intermitente na captura de etiqueta",
            "status": "em_andamento",
            "criado_em": "2026-04-29T10:14:00Z",
        }
    ],
    "CLI-1003": [
        {
            "id": "TCK-7790",
            "assunto": "Erro 500 ao gerar relatório de coletas",
            "status": "aberto",
            "criado_em": "2026-05-02T16:42:00Z",
        },
        {
            "id": "TCK-7795",
            "assunto": "Integração Bling parou de sincronizar",
            "status": "aguardando_cliente",
            "criado_em": "2026-05-04T09:05:00Z",
        },
        {
            "id": "TCK-7801",
            "assunto": "Solicitação de novo usuário no painel",
            "status": "aberto",
            "criado_em": "2026-05-05T11:20:00Z",
        },
    ],
}

# Notas fiscais da MERCADORIA transportada (não é cobrança/assinatura). Cada NF
# tem uma posição no ciclo de entrega da Azapfy (expedição → rota → transbordo →
# entrega) e o status da comprovação de entrega. Indexadas pelo número da NF;
# `grupo_emp` é o dono da NF — a checagem de posse compara com o escopo
# INJETADO da sessão, nunca com valor vindo do LLM (F7/LLM06).
_NOTAS_FISCAIS: dict[str, dict[str, Any]] = {
    # AZAPERS: uma a caminho, uma já entregue e validada.
    "NF-1042": {
        "grupo_emp": "AZAPERS",
        "etapa": "em_rota",
        "comprovacao": "pendente",
        "ocorrencia": None,
        "atualizado_em": "2026-06-15T09:12:00Z",
    },
    "NF-1043": {
        "grupo_emp": "AZAPERS",
        "etapa": "entregue",
        "comprovacao": "validada",
        "ocorrencia": None,
        "atualizado_em": "2026-06-12T17:40:00Z",
    },
    # ALMEIDA LOG: parada em transbordo por divergência de endereço.
    "NF-2001": {
        "grupo_emp": "ALMEIDA LOG",
        "etapa": "transbordo",
        "comprovacao": "pendente",
        "ocorrencia": "endereco_divergente",
        "atualizado_em": "2026-06-14T11:05:00Z",
    },
    # PEREIRA CARGAS: entregue, mas a comprovação foi rejeitada na auditoria.
    "NF-3001": {
        "grupo_emp": "PEREIRA CARGAS",
        "etapa": "entregue",
        "comprovacao": "rejeitada",
        "ocorrencia": "foto_ilegivel",
        "atualizado_em": "2026-06-10T08:22:00Z",
    },
}


def _normalizar_telefone(telefone: str) -> str:
    """Mantém apenas os dígitos do telefone (ex.: `(11) 99999-0001` → `11999990001`)."""
    return re.sub(r"\D", "", telefone or "")


def _mascarar_telefone(telefone: str) -> str:
    """Mascara o telefone deixando visíveis apenas os 4 últimos dígitos (LLM06)."""
    digitos = _normalizar_telefone(telefone)
    if len(digitos) <= 4:
        return "*" * len(digitos)
    return "*" * (len(digitos) - 4) + digitos[-4:]


def grupos_emp_por_telefone(telefone: str) -> list[str]:
    """Resolve o escopo mock (grupos de empresa) a partir do telefone da sessão.

    Fallback usado pela política de tools quando a sessão não trouxe identidade
    completa do gate (harness Chainlit/testes). Telefone desconhecido → escopo
    vazio → toda consulta escopada falha fechada (nada é vazado).
    """
    cliente = _CLIENTES.get(_normalizar_telefone(telefone))
    if cliente is None:
        return []
    grupo = cliente.get("grupo_emp")
    return [grupo] if grupo else []


# ---------------------------------------------------------------------------
# Tools expostas para o agente
# ---------------------------------------------------------------------------


@tool
def buscar_cliente_por_telefone(telefone: str) -> dict[str, Any]:
    """Identifica o cliente Azapfy a partir do telefone informado.

    Use esta tool no início da sessão (ou sempre que o telefone do cliente
    mudar) para obter `id_cliente`, `nome`, `plano` e `status_conta`. O
    `id_cliente` retornado é exigido pelas demais tools de CRM.

    Args:
        telefone: Telefone do cliente, com ou sem máscara.

    Returns:
        dict: id_cliente, nome, plano, status_conta, encontrado (bool).
    """
    digitos = _normalizar_telefone(telefone)
    cliente = _CLIENTES.get(digitos)
    if cliente is None:
        return {
            "id_cliente": None,
            "nome": None,
            "plano": None,
            "status_conta": None,
            "encontrado": False,
            "telefone_consultado": _mascarar_telefone(digitos),
        }
    return {**cliente, "encontrado": True}


@tool
def verificar_chamados_abertos(id_cliente: str) -> dict[str, Any]:
    """Lista chamados em aberto (tickets) do cliente no sistema de suporte.

    Use quando o cliente perguntar sobre chamados, tickets, status de
    atendimento ou andamento de problemas reportados. Sempre passe o
    `id_cliente` retornado por `buscar_cliente_por_telefone`.

    Args:
        id_cliente: Identificador interno do cliente (ex.: "CLI-1001").

    Returns:
        dict: id_cliente, total (int), chamados (list com id, assunto, status,
        criado_em).
    """
    chamados = _CHAMADOS.get(id_cliente, [])
    return {
        "id_cliente": id_cliente,
        "total": len(chamados),
        "chamados": [dict(c) for c in chamados],
    }


@tool
def rastrear_nota_fiscal(
    numero_nota: str,
    grupos_emp_sessao: Annotated[Optional[list[str]], InjectedToolArg] = None,
) -> dict[str, Any]:
    """Rastreia uma nota fiscal (NF da mercadoria) no ciclo de entrega da Azapfy.

    Use quando o cliente quiser saber EM QUE PONTO está uma NF específica da qual
    ele já tem o número: etapa do transporte (expedição → rota → transbordo →
    entrega), se a comprovação de entrega foi validada/rejeitada e se há ocorrência.
    A busca é sempre restrita às empresas do cliente desta sessão — não é
    possível (nem necessário) informar cliente ou empresa.

    NÃO use para dúvidas do tipo "como/onde encontro a NF no painel", "a nota não
    aparece na Pesquisa" ou "como funciona o módulo X" — isso é how-to e a base de
    conhecimento já responde. Esta tool também não trata cobrança/fatura da
    assinatura Azapfy (a Azapfy não vende isso ao cliente final aqui).

    Args:
        numero_nota: Número da nota fiscal, ex.: "NF-1042".

    Returns:
        dict: numero_nota, etapa ("expedicao"|"em_rota"|"transbordo"|"entregue"),
        comprovacao ("pendente"|"validada"|"rejeitada"), ocorrencia (str | None),
        atualizado_em (ISO 8601 UTC), encontrado (bool).
    """
    numero = (numero_nota or "").strip().upper()
    registro = _NOTAS_FISCAIS.get(numero)
    # Posse validada contra o escopo INJETADO da sessão (nunca argumento do
    # LLM): NF de outra empresa não é devolvida nem tem a existência confirmada
    # (F7/LLM06). Sem escopo na sessão → falha fechada.
    grupos = {g.strip().upper() for g in (grupos_emp_sessao or []) if g}
    if registro is None or registro["grupo_emp"].upper() not in grupos:
        return {
            "numero_nota": numero or numero_nota,
            "encontrado": False,
        }

    return {
        "numero_nota": numero,
        "etapa": registro["etapa"],
        "comprovacao": registro["comprovacao"],
        "ocorrencia": registro["ocorrencia"],
        "atualizado_em": registro["atualizado_em"],
        "encontrado": True,
    }


@tool
def abrir_novo_chamado(id_cliente: str, resumo: str) -> dict[str, Any]:
    """Abre um novo chamado (ticket) de suporte técnico para o cliente.

    AÇÃO COM EFEITO COLATERAL: cria um registro no sistema de tickets.
    Só chame esta tool após o cliente ter confirmado explicitamente que
    deseja abrir o chamado e qual é o resumo do problema (LLM08 — Excessive
    Agency).

    Args:
        id_cliente: Identificador interno do cliente (ex.: "CLI-1001").
        resumo: Descrição curta do problema (1–2 frases). Será sanitizada
            (limite de 280 caracteres, sem quebras de linha excessivas).

    Returns:
        dict: ticket_id, id_cliente, assunto (resumo sanitizado), status
        ("aberto"), criado_em (ISO 8601 UTC).
    """
    resumo_limpo = (resumo or "").strip()
    resumo_limpo = re.sub(r"\s+", " ", resumo_limpo)
    if len(resumo_limpo) > 280:
        resumo_limpo = resumo_limpo[:277] + "..."

    if not resumo_limpo:
        return {
            "ticket_id": None,
            "id_cliente": id_cliente,
            "status": "rejeitado",
            "erro": "resumo do chamado não pode ser vazio",
        }

    # ID determinístico em função do (cliente, resumo) — facilita testes e
    # evita "duplicatas" quando o agente reexecuta a tool no mesmo turno.
    seed = abs(hash((id_cliente, resumo_limpo))) % 9000 + 1000
    ticket_id = f"TCK-{seed}"

    chamados_existentes = _CHAMADOS.setdefault(id_cliente, [])
    novo = {
        "id": ticket_id,
        "assunto": resumo_limpo,
        "status": "aberto",
        "criado_em": datetime.now(timezone.utc).replace(microsecond=0).isoformat(),
    }
    chamados_existentes.append(novo)

    return {
        "ticket_id": ticket_id,
        "id_cliente": id_cliente,
        "assunto": resumo_limpo,
        "status": "aberto",
        "criado_em": novo["criado_em"],
    }


CRM_TOOLS = [
    buscar_cliente_por_telefone,
    verificar_chamados_abertos,
    rastrear_nota_fiscal,
    abrir_novo_chamado,
]
