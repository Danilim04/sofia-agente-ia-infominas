"""Testes da política de autorização de tools ("o LLM propõe, o código dispõe").

As entradas de produção de `POLITICAS` são de E2.3; aqui o mecanismo é
exercitado com as políticas de teste de `tests/fakes.py` (instaladas por
monkeypatch): injeção de args de sessão, precondição de duas fases,
confirmação explícita checada por código, carimbo e validade da proposta.
"""

from __future__ import annotations

from datetime import UTC, datetime, timedelta

import pytest
from langchain_core.messages import AIMessage, HumanMessage

from src.agent import tool_policy
from src.agent.nodes import make_tools_node
from src.agent.tool_policy import (
    PROPOSTA_VALIDADE,
    PoliticaTool,
    aplicar_politica,
    carimbar_proposta,
    eh_confirmacao_explicita,
)
from tests.fakes import (
    CHAMADAS,
    registrar_duas_fases_de_teste,
    tool_abrir_de_teste,
    tool_de_teste,
)

_CONTATO = {"forma": "cpf", "valor_mascarado": "***.***.789-**"}


@pytest.fixture
def politicas(monkeypatch):
    registrar_duas_fases_de_teste(monkeypatch)


# ---------------------------------------------------------------------------
# Estado de produção nesta etapa
# ---------------------------------------------------------------------------


def test_politicas_de_producao_vazias_ate_e23():
    """Nenhuma tool exposta, nenhuma política: E2.3 preenche os dois juntos."""
    from src.agent.graph import get_default_tools

    assert tool_policy.POLITICAS == {}
    assert get_default_tools() == []


def test_tool_sem_politica_passa_intocada():
    args, erro = aplicar_politica("qualquer_tool", {"a": 1}, {})
    assert (args, erro) == ({"a": 1}, None)


# ---------------------------------------------------------------------------
# aplicar_politica — injeção sobrescreve o LLM; precondição recusa
# ---------------------------------------------------------------------------


def test_injecao_sobrescreve_arg_alucinado_pelo_llm(politicas):
    """Mesmo que o modelo alucine um arg de sessão, a sessão manda."""
    state = {"telefone": "5531999990000", "conversation_id": "conv-1", "contato": _CONTATO}
    args, erro = aplicar_politica(
        "tool_de_teste",
        {"x": "oi", "telefone": "5500000000000", "conversation_id": "outra"},
        state,
    )
    assert erro is None
    assert args == {"x": "oi", "telefone": "5531999990000", "conversation_id": "conv-1"}


def test_injecao_sem_valor_na_sessao_da_vazio(politicas):
    args, erro = aplicar_politica("tool_de_teste", {"x": "oi"}, {})
    assert erro is None and args["telefone"] == "" and args["conversation_id"] == ""


def test_validador_recusa_arg_do_llm(monkeypatch):
    """`validar` barra um argumento legítimo do modelo quando ele não bate
    com a sessão — a tool não executa e o motivo vira o erro."""

    def _so_digitos(valor, state):
        return None if str(valor).isdigit() else "numero precisa ter só dígitos"

    monkeypatch.setitem(
        tool_policy.POLITICAS, "tool_de_teste", PoliticaTool(validar={"x": _so_digitos})
    )
    _, erro = aplicar_politica("tool_de_teste", {"x": "48a21"}, {})
    assert erro == "numero precisa ter só dígitos"
    args, erro = aplicar_politica("tool_de_teste", {"x": "4821"}, {})
    assert erro is None and args == {"x": "4821"}


def test_precondicao_roda_antes_de_validar_e_injetar(monkeypatch):
    monkeypatch.setitem(
        tool_policy.POLITICAS,
        "tool_de_teste",
        PoliticaTool(
            injetar={"telefone": lambda s: "nunca"},
            validar={"x": lambda v, s: "nunca"},
            exigir=lambda s: "precondição falhou",
        ),
    )
    args, erro = aplicar_politica("tool_de_teste", {"x": "1"}, {})
    assert erro == "precondição falhou"
    assert "telefone" not in args  # nada injetado numa chamada recusada


def test_abrir_sem_proposta_preparada_e_recusado(politicas):
    """Duas fases: abrir sem dry-run aprovado no estado não é representável."""
    _, erro = aplicar_politica("tool_abrir_de_teste", {}, {"contato": _CONTATO})
    assert erro is not None
    assert "preparar_abertura_chamado" in erro


def test_abrir_injeta_a_proposta_do_estado(politicas):
    """A abertura executa a proposta preparada — mesmo que o modelo tente
    passar outro conteúdo, a injeção sobrescreve. (Proposta carimbada no
    turno anterior e cliente confirmando neste.)"""
    proposta = carimbar_proposta({"titulo": "Sem conexão", "categoria": "sem_conexao"}, {})
    state = {
        "contato": _CONTATO,
        "telefone": "5531999990000",
        "proposta_chamado": proposta,
        "messages": [HumanMessage(content="sim")],
    }
    args, erro = aplicar_politica(
        "tool_abrir_de_teste", {"proposta": {"titulo": "OUTRA COISA alucinada"}}, state
    )
    assert erro is None
    assert args["proposta"] == proposta
    assert args["telefone"] == "5531999990000"


# ---------------------------------------------------------------------------
# Confirmação explícita checada por código (nunca pelo modelo)
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "texto",
    [
        "sim",
        "Sim!",
        "SIM",
        "ok",
        "Ok.",
        "pode abrir",
        "Pode abrir, sim",
        "pode",
        "confirmo",
        "Pode abrir 👍",
        "tá bom",
        "beleza",
        "sim, por favor",
        "pode abrir o chamado",
        "sim, Sofia",
        "pode abrir, Sofia",
    ],
)
def test_confirmacao_explicita_aceita(texto):
    assert eh_confirmacao_explicita(texto) is True


@pytest.mark.parametrize(
    "texto",
    [
        "Esse",  # deítico: pode apontar um chamado existente
        "4821",  # número de chamado
        "isso",
        "esse mesmo",
        "não",
        "sim, mas muda a prioridade",
        "pode abrir? qual o prazo?",
        "obrigado",
        "",
        None,
        "sim e também abre outro pro roteador",
        "sim maria",  # dirigido a outra pessoa não é confirmação à Sofia
    ],
)
def test_confirmacao_explicita_recusa_o_que_nao_e_so_confirmacao(texto):
    assert eh_confirmacao_explicita(texto) is False


def test_confirmacoes_nao_contem_deiticos():
    assert not {"esse", "isso", "aquele", "esse mesmo"} & tool_policy.CONFIRMACOES_EXPLICITAS


def _estado_abrir(proposta: dict, mensagens: list) -> dict:
    return {
        "contato": _CONTATO,
        "telefone": "5531999990000",
        "proposta_chamado": proposta,
        "messages": mensagens,
    }


def test_carimbar_proposta_registra_criada_em_utc_e_turno():
    agora = datetime(2026, 10, 6, 12, 0, tzinfo=UTC)
    msgs = [HumanMessage(content="a"), AIMessage(content="b"), HumanMessage(content="c")]
    proposta = carimbar_proposta({"titulo": "x"}, {"messages": msgs}, agora=agora)
    assert proposta == {"titulo": "x", "criada_em": "2026-10-06T12:00:00+00:00", "turno": 2}


def test_carimbar_proposta_nao_muta_a_original():
    original = {"titulo": "x"}
    carimbar_proposta(original, {})
    assert original == {"titulo": "x"}


def test_abrir_recusado_quando_proposta_venceu(politicas):
    velha = carimbar_proposta(
        {"titulo": "x"}, {}, agora=datetime.now(UTC) - PROPOSTA_VALIDADE - timedelta(seconds=1)
    )
    _, erro = aplicar_politica(
        "tool_abrir_de_teste", {}, _estado_abrir(velha, [HumanMessage(content="sim")])
    )
    assert erro is not None and "venceu" in erro


def test_abrir_recusado_no_mesmo_turno_da_proposta(politicas):
    msgs = [HumanMessage(content="abre um chamado")]
    proposta = carimbar_proposta({"titulo": "x"}, {"messages": msgs})
    _, erro = aplicar_politica("tool_abrir_de_teste", {}, _estado_abrir(proposta, msgs))
    assert erro is not None and "NESTE turno" in erro


def test_abrir_recusado_quando_a_conversa_seguiu_sem_confirmar(politicas):
    proposta = carimbar_proposta({"titulo": "x"}, {"messages": [HumanMessage(content="1")]})
    msgs = [HumanMessage(content="1"), HumanMessage(content="2"), HumanMessage(content="sim")]
    _, erro = aplicar_politica("tool_abrir_de_teste", {}, _estado_abrir(proposta, msgs))
    assert erro is not None and "SEGUINTE" in erro


@pytest.mark.parametrize("mensagem", ["Esse", "4821", "não", "sim, mas muda a prioridade"])
def test_abrir_recusado_sem_confirmacao_explicita_no_turno_seguinte(politicas, mensagem):
    proposta = carimbar_proposta({"titulo": "x"}, {"messages": [HumanMessage(content="1")]})
    msgs = [HumanMessage(content="1"), HumanMessage(content=mensagem)]
    _, erro = aplicar_politica("tool_abrir_de_teste", {}, _estado_abrir(proposta, msgs))
    assert erro is not None and "confirmação explícita" in erro


def test_abrir_recusado_quando_proposta_sem_carimbo(politicas):
    _, erro = aplicar_politica(
        "tool_abrir_de_teste", {}, _estado_abrir({"titulo": "x"}, [HumanMessage(content="sim")])
    )
    assert erro is not None and "preparar_abertura_chamado" in erro


def test_abrir_recusado_quando_carimbo_e_invalido(politicas):
    proposta = {"titulo": "x", "criada_em": "ontem", "turno": 0}
    _, erro = aplicar_politica(
        "tool_abrir_de_teste", {}, _estado_abrir(proposta, [HumanMessage(content="sim")])
    )
    assert erro is not None and "versão anterior" in erro


def test_abrir_aceito_com_proposta_valida_e_sim_no_turno_seguinte(politicas):
    proposta = carimbar_proposta({"titulo": "x"}, {"messages": [HumanMessage(content="1")]})
    msgs = [
        HumanMessage(content="1"),
        AIMessage(content="Posso abrir?"),
        HumanMessage(content="Sim!"),
    ]
    args, erro = aplicar_politica("tool_abrir_de_teste", {}, _estado_abrir(proposta, msgs))
    assert erro is None and args["proposta"] == proposta


def test_confirmacao_tolera_content_em_blocos(politicas):
    proposta = carimbar_proposta({"titulo": "x"}, {"messages": [HumanMessage(content="1")]})
    msgs = [HumanMessage(content="1"), HumanMessage(content=[{"type": "text", "text": "sim"}])]
    _, erro = aplicar_politica("tool_abrir_de_teste", {}, _estado_abrir(proposta, msgs))
    assert erro is None


# ---------------------------------------------------------------------------
# tools_node — a recusa vira ToolMessage e a tool NÃO executa
# ---------------------------------------------------------------------------


def test_tools_node_recusa_da_politica_vira_toolmessage_sem_executar(politicas):
    node = make_tools_node([tool_abrir_de_teste])
    ai = AIMessage(
        content="", tool_calls=[{"id": "tc1", "name": "tool_abrir_de_teste", "args": {}}]
    )
    out = node({"messages": [HumanMessage(content="abre"), ai], "contato": _CONTATO})
    content = out["messages"][0].content
    assert "recusado_pela_politica" in content
    assert "preparar_abertura_chamado" in content
    assert CHAMADAS == []  # a tool não executou
    assert "proposta_chamado" not in out  # nenhum efeito aplicado


def test_tools_node_schema_do_modelo_nao_expoe_args_injetados():
    for t in (tool_de_teste, tool_abrir_de_teste):
        props = set(t.tool_call_schema.model_json_schema().get("properties", {}))
        assert not props & {"telefone", "conversation_id", "proposta"}, t.name
