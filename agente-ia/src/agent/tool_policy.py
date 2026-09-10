"""Política de autorização das tools — "o LLM propõe, o código dispõe".

Princípio (F7/F9 do laudo 2026-08-27): o LLM NUNCA faz parte da base confiável
do sistema. Toda tool call que ele emite é tratada como input não-confiável —
como se tivesse vindo do próprio usuário — e passa por esta política antes de
executar. Identidade e escopo vêm SEMPRE do estado da sessão (resolvido pelo
gate Go via Contrato A), nunca de argumento escolhido pelo modelo.

Este módulo é a fonte única de verdade sobre o que o agente pode fazer:

- ``injetar``: args preenchidos pelo ``tools_node`` a partir do ESTADO da
  sessão. Nas tools eles são ``InjectedToolArg`` — ficam FORA do schema que o
  modelo vê, então não existe sequência de tokens capaz de controlá-los.
  Qualquer valor que o modelo tente passar nesses nomes é sobrescrito.
- ``validar``: args que o LLM fornece legitimamente (ex.: desambiguação de
  empresa) mas que só passam se condizerem com a sessão. Defesa em
  profundidade — o gateway Go revalida server-side.

Uma tool ausente deste mapa não recebe nem valida nada da sessão (tools puras
de conteúdo). Auditoria de segurança das tools começa e termina neste arquivo.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Callable, Mapping, Optional


Resolver = Callable[[Mapping[str, Any]], Any]
Validador = Callable[[Any, Mapping[str, Any]], Optional[str]]
Precondicao = Callable[[Mapping[str, Any]], Optional[str]]


@dataclass(frozen=True)
class PoliticaTool:
    """Política de uma tool: o que injetar da sessão e o que validar do LLM.

    ``exigir`` é uma precondição de SESSÃO (não de argumento): se devolver um
    erro, a tool não executa — independente do que o modelo mandou. É o que
    torna o fluxo em duas fases determinístico (ex.: abrir chamado exige
    proposta preparada no estado).
    """

    injetar: dict[str, Resolver] = field(default_factory=dict)
    validar: dict[str, Validador] = field(default_factory=dict)
    exigir: Optional[Precondicao] = None


# ---------------------------------------------------------------------------
# Resolvers — derivam valores do estado da sessão (nunca do LLM)
# ---------------------------------------------------------------------------


def _telefone_da_sessao(state: Mapping[str, Any]) -> str:
    return state.get("telefone") or ""


def _proposta_da_sessao(state: Mapping[str, Any]) -> Optional[dict]:
    """Proposta de chamado validada pelo dry-run do gateway (estado do grafo)."""
    return state.get("proposta_chamado") or None


def grupos_da_sessao(state: Mapping[str, Any]) -> list[str]:
    """Grupos de empresa com acesso ativo na sessão (escopo de dados).

    Fonte primária: a identidade resolvida pelo gate (Contrato A). Fallback
    para o harness de dev/testes: o mapeamento mock telefone→grupo. Sessão sem
    escopo devolve lista vazia — as tools escopadas falham FECHADAS.
    """
    identidade = state.get("identidade") or {}
    grupos = [
        emp.get("grupo_empresa")
        for emp in (identidade.get("empresas") or [])
        if emp.get("grupo_empresa")
    ]
    if grupos:
        return grupos
    from src.tools.crm_mocks import grupos_emp_por_telefone

    return grupos_emp_por_telefone(state.get("telefone") or "")


# ---------------------------------------------------------------------------
# Validadores — recusam args do LLM incompatíveis com a sessão
# ---------------------------------------------------------------------------


def _validar_empresa_da_sessao(
    valor: Any, state: Mapping[str, Any]
) -> Optional[str]:
    """`empresa` (desambiguação de chamado) precisa pertencer à sessão."""
    if not valor:
        return None  # vazio = gateway resolve pela identidade; nada a validar
    grupos = {g.strip().upper() for g in grupos_da_sessao(state) if g}
    if str(valor).strip().upper() in grupos:
        return None
    return (
        "empresa fora do escopo desta sessão — o chamado só pode ser aberto "
        "para uma empresa do próprio cliente"
    )


def _exigir_proposta_preparada(state: Mapping[str, Any]) -> Optional[str]:
    """Abrir chamado sem dry-run aprovado não é representável (duas fases)."""
    if _proposta_da_sessao(state):
        return None
    return (
        "não há proposta de chamado preparada — chame preparar_abertura_chamado, "
        "apresente o resumo ao cliente e só abra após a confirmação dele"
    )


# ---------------------------------------------------------------------------
# A política em si — uma linha por tool exposta ao agente
# ---------------------------------------------------------------------------

POLITICAS: dict[str, PoliticaTool] = {
    # SAC (gateway Go): o relator vem da identidade do gate via `telefone`.
    "consultar_tipos_de_chamado": PoliticaTool(
        injetar={"telefone": _telefone_da_sessao},
    ),
    "listar_chamados_abertos": PoliticaTool(
        injetar={"telefone": _telefone_da_sessao},
    ),
    # Duas fases (dry-run → confirmação → execução): `preparar` valida os
    # campos no gateway e a proposta aprovada fica no estado; `abrir` não
    # recebe NADA do modelo — executa exatamente a proposta preparada.
    "preparar_abertura_chamado": PoliticaTool(
        injetar={"telefone": _telefone_da_sessao},
        validar={"empresa": _validar_empresa_da_sessao},
    ),
    "abrir_chamado_suporte": PoliticaTool(
        injetar={
            "telefone": _telefone_da_sessao,
            "proposta": _proposta_da_sessao,
        },
        exigir=_exigir_proposta_preparada,
    ),
    # Rastreio de NF (mock, futura MCP): escopo = grupos da sessão, injetado.
    "rastrear_nota_fiscal": PoliticaTool(
        injetar={"grupos_emp_sessao": grupos_da_sessao},
    ),
}


def aplicar_politica(
    nome: str, args: Mapping[str, Any], state: Mapping[str, Any]
) -> tuple[dict[str, Any], Optional[str]]:
    """Aplica a política de uma tool call proposta pelo LLM.

    Returns:
        `(args_finais, erro)`. Com `erro != None` a tool NÃO deve ser executada
        — o texto vira o `ToolMessage` devolvido ao agente, que explica a
        recusa ao cliente sem inventar dados.
    """
    politica = POLITICAS.get(nome)
    final = dict(args)
    if politica is None:
        return final, None

    if politica.exigir is not None:
        erro = politica.exigir(state)
        if erro:
            return final, erro

    for arg, validador in politica.validar.items():
        if arg in final:
            erro = validador(final[arg], state)
            if erro:
                return final, erro

    # Injeção por último: sobrescreve QUALQUER valor que o LLM tenha tentado
    # passar num arg de sessão (o schema não os expõe, mas modelos podem
    # alucinar args — a sobrescrita torna a tentativa inócua).
    for arg, resolver in politica.injetar.items():
        final[arg] = resolver(state)
    return final, None
