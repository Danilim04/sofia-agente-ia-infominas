"""Testes da política de autorização de tools (F7/F9 — "o LLM propõe, o código dispõe")."""

from __future__ import annotations

from langchain_core.messages import AIMessage

from src.agent.nodes import make_tools_node
from src.agent.tool_policy import aplicar_politica, grupos_da_sessao
from src.tools.crm_mocks import rastrear_nota_fiscal


_IDENTIDADE_AZAPERS = {
    "encontrado": True,
    "login": "10596693664",
    "nome": "Daniel",
    "empresas": [
        {
            "grupo_empresa": "AZAPERS",
            "grupo_user": "COLABORADOR",
            "area": "SAC",
            "bases": [],
        }
    ],
}


# ---------------------------------------------------------------------------
# grupos_da_sessao — resolução do escopo
# ---------------------------------------------------------------------------


def test_grupos_vem_da_identidade_do_gate():
    state = {"identidade": _IDENTIDADE_AZAPERS, "telefone": "5531999990000"}
    assert grupos_da_sessao(state) == ["AZAPERS"]


def test_grupos_fallback_mock_por_telefone():
    state = {"identidade": None, "telefone": "11999990002"}
    assert grupos_da_sessao(state) == ["ALMEIDA LOG"]


def test_grupos_sessao_desconhecida_escopo_vazio():
    assert grupos_da_sessao({"telefone": "11000000000"}) == []


# ---------------------------------------------------------------------------
# aplicar_politica — injeção sobrescreve o LLM; validação recusa fora da sessão
# ---------------------------------------------------------------------------


def test_injecao_sobrescreve_arg_alucinado_pelo_llm():
    """Mesmo que o modelo alucine um escopo, a sessão manda."""
    state = {"identidade": _IDENTIDADE_AZAPERS}
    args, erro = aplicar_politica(
        "rastrear_nota_fiscal",
        {"numero_nota": "NF-2001", "grupos_emp_sessao": ["ALMEIDA LOG"]},
        state,
    )
    assert erro is None
    assert args["grupos_emp_sessao"] == ["AZAPERS"]


def test_preparar_chamado_empresa_da_sessao_passa():
    state = {"identidade": _IDENTIDADE_AZAPERS}
    args, erro = aplicar_politica(
        "preparar_abertura_chamado",
        {"resumo": "x", "empresa": "azapers"},
        state,
    )
    assert erro is None
    assert args["telefone"] == ""  # injetado (sessão sem telefone)


def test_preparar_chamado_empresa_alheia_e_recusada():
    """F9/B4: chamado 'em nome de' empresa fora da sessão não executa."""
    state = {"identidade": _IDENTIDADE_AZAPERS}
    _, erro = aplicar_politica(
        "preparar_abertura_chamado",
        {"resumo": "x", "empresa": "TRANSPORTADORA XPTO LTDA"},
        state,
    )
    assert erro is not None
    assert "escopo" in erro


def test_abrir_sem_proposta_preparada_e_recusado():
    """Duas fases: abrir sem dry-run aprovado no estado não é representável."""
    state = {"identidade": _IDENTIDADE_AZAPERS}
    _, erro = aplicar_politica("abrir_chamado_suporte", {}, state)
    assert erro is not None
    assert "preparar_abertura_chamado" in erro


def test_abrir_injeta_a_proposta_do_estado():
    """A abertura executa a proposta preparada — mesmo que o modelo tente
    passar outro conteúdo, a injeção sobrescreve."""
    proposta = {"resumo": "App travando", "categoria": "APLICATIVO"}
    state = {
        "identidade": _IDENTIDADE_AZAPERS,
        "telefone": "5531999990000",
        "proposta_chamado": proposta,
    }
    args, erro = aplicar_politica(
        "abrir_chamado_suporte",
        {"proposta": {"resumo": "OUTRA COISA alucinada"}},
        state,
    )
    assert erro is None
    assert args["proposta"] == proposta
    assert args["telefone"] == "5531999990000"


def test_tool_sem_politica_passa_intocada():
    args, erro = aplicar_politica("qualquer_tool", {"a": 1}, {})
    assert (args, erro) == ({"a": 1}, None)


# ---------------------------------------------------------------------------
# tools_node E2E — IDOR do laudo (F7) morre no grafo real
# ---------------------------------------------------------------------------


def test_idor_do_laudo_nao_e_mais_representavel():
    """Replay do F7: sessão AZAPERS pedindo a NF-2001 (de outro cliente).

    Mesmo que o modelo emita a tool call, o escopo injetado é o da sessão —
    a NF alheia volta como não-encontrada, sem confirmar existência.
    """
    node = make_tools_node([rastrear_nota_fiscal])
    ai = AIMessage(
        content="",
        tool_calls=[
            {
                "id": "tc1",
                "name": "rastrear_nota_fiscal",
                "args": {"numero_nota": "NF-2001"},
            }
        ],
    )
    out = node(
        {
            "messages": [ai],
            "identidade": _IDENTIDADE_AZAPERS,
            "telefone": "5531983857490",
        }
    )
    content = out["messages"][0].content
    assert '"encontrado": false' in content
    assert "transbordo" not in content  # nenhum dado da NF alheia vaza


def test_tools_node_recusa_da_politica_vira_toolmessage():
    from src.tools.sac_tools import preparar_abertura_chamado

    node = make_tools_node([preparar_abertura_chamado])
    ai = AIMessage(
        content="",
        tool_calls=[
            {
                "id": "tc1",
                "name": "preparar_abertura_chamado",
                "args": {
                    "resumo": "x",
                    "descricao": "y",
                    "categoria": "C",
                    "ocorrencia": "O",
                    "empresa": "XPTO ALHEIA",
                },
            }
        ],
    )
    out = node({"messages": [ai], "identidade": _IDENTIDADE_AZAPERS})
    content = out["messages"][0].content
    assert "recusado_pela_politica" in content
    # A tool NÃO executou (nenhuma chamada HTTP aconteceu — sem stub, teria
    # falhado com erro de rede genérico em vez da recusa).
    assert "escopo" in content
