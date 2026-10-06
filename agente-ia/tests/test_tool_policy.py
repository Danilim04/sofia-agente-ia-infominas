"""Testes da política de autorização de tools (F7/F9 — "o LLM propõe, o código dispõe")."""

from __future__ import annotations

from datetime import datetime, timedelta, timezone

import pytest
from langchain_core.messages import AIMessage, HumanMessage

from src.agent.nodes import make_tools_node
from src.agent.tool_policy import (
    PROPOSTA_VALIDADE,
    aplicar_politica,
    carimbar_proposta,
    eh_confirmacao_explicita,
    grupos_da_sessao,
)
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
    passar outro conteúdo, a injeção sobrescreve. (Proposta carimbada no
    turno anterior e cliente confirmando neste — ZB-1.)"""
    proposta = carimbar_proposta({"resumo": "App travando", "categoria": "APLICATIVO"}, {})
    state = {
        "identidade": _IDENTIDADE_AZAPERS,
        "telefone": "5531999990000",
        "proposta_chamado": proposta,
        "messages": [HumanMessage(content="sim")],
    }
    args, erro = aplicar_politica(
        "abrir_chamado_suporte",
        {"proposta": {"resumo": "OUTRA COISA alucinada"}},
        state,
    )
    assert erro is None
    assert args["proposta"] == proposta
    assert args["telefone"] == "5531999990000"


# ---------------------------------------------------------------------------
# ZB-1 — confirmação explícita checada por código (nunca pelo modelo)
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "texto",
    ["sim", "Sim!", "SIM", "ok", "Ok.", "pode abrir", "Pode abrir, sim", "pode", "confirmo",
     "Pode abrir 👍", "tá bom", "beleza", "sim, por favor", "pode abrir o chamado"],
)
def test_confirmacao_explicita_aceita(texto):
    assert eh_confirmacao_explicita(texto) is True


@pytest.mark.parametrize(
    "texto",
    [
        "Esse",  # conversa #45
        "ZPRS25208011",  # conversa #45
        "isso",
        "esse mesmo",
        "não",
        "sim, mas muda a prioridade",
        "pode abrir? qual o prazo?",
        "obrigado",
        "",
        None,
        "sim e também abre outro pro app",
    ],
)
def test_confirmacao_explicita_recusa_o_que_nao_e_so_confirmacao(texto):
    assert eh_confirmacao_explicita(texto) is False


def _estado_abrir(proposta: dict, mensagens: list) -> dict:
    return {
        "identidade": _IDENTIDADE_AZAPERS,
        "telefone": "5531999990000",
        "proposta_chamado": proposta,
        "messages": mensagens,
    }


def test_carimbar_proposta_registra_criada_em_utc_e_turno():
    agora = datetime(2026, 10, 6, 12, 0, tzinfo=timezone.utc)
    msgs = [HumanMessage(content="a"), AIMessage(content="b"), HumanMessage(content="c")]
    proposta = carimbar_proposta({"resumo": "x"}, {"messages": msgs}, agora=agora)
    assert proposta == {"resumo": "x", "criada_em": "2026-10-06T12:00:00+00:00", "turno": 2}


def test_abrir_recusado_quando_proposta_venceu():
    velha = carimbar_proposta(
        {"resumo": "x"}, {}, agora=datetime.now(timezone.utc) - PROPOSTA_VALIDADE - timedelta(seconds=1)
    )
    _, erro = aplicar_politica("abrir_chamado_suporte", {}, _estado_abrir(velha, [HumanMessage(content="sim")]))
    assert erro is not None and "venceu" in erro


def test_abrir_recusado_no_mesmo_turno_da_proposta():
    msgs = [HumanMessage(content="abre um chamado")]
    proposta = carimbar_proposta({"resumo": "x"}, {"messages": msgs})
    _, erro = aplicar_politica("abrir_chamado_suporte", {}, _estado_abrir(proposta, msgs))
    assert erro is not None and "NESTE turno" in erro


def test_abrir_recusado_quando_a_conversa_seguiu_sem_confirmar():
    proposta = carimbar_proposta({"resumo": "x"}, {"messages": [HumanMessage(content="1")]})
    msgs = [HumanMessage(content="1"), HumanMessage(content="2"), HumanMessage(content="sim")]
    _, erro = aplicar_politica("abrir_chamado_suporte", {}, _estado_abrir(proposta, msgs))
    assert erro is not None and "SEGUINTE" in erro


@pytest.mark.parametrize("mensagem", ["Esse", "ZPRS25208011", "não", "sim, mas muda a prioridade"])
def test_abrir_recusado_sem_confirmacao_explicita_no_turno_seguinte(mensagem):
    proposta = carimbar_proposta({"resumo": "x"}, {"messages": [HumanMessage(content="1")]})
    msgs = [HumanMessage(content="1"), HumanMessage(content=mensagem)]
    _, erro = aplicar_politica("abrir_chamado_suporte", {}, _estado_abrir(proposta, msgs))
    assert erro is not None and "confirmação explícita" in erro


def test_abrir_recusado_quando_proposta_sem_carimbo():
    _, erro = aplicar_politica(
        "abrir_chamado_suporte", {}, _estado_abrir({"resumo": "x"}, [HumanMessage(content="sim")])
    )
    assert erro is not None and "preparar_abertura_chamado" in erro


def test_abrir_aceito_com_proposta_valida_e_sim_no_turno_seguinte():
    proposta = carimbar_proposta({"resumo": "x"}, {"messages": [HumanMessage(content="1")]})
    msgs = [HumanMessage(content="1"), AIMessage(content="Posso abrir?"), HumanMessage(content="Sim!")]
    args, erro = aplicar_politica("abrir_chamado_suporte", {}, _estado_abrir(proposta, msgs))
    assert erro is None and args["proposta"] == proposta


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


# ---------------------------------------------------------------------------
# ZB-2 — chamado novo só com pedido explícito do cliente (checado por código);
# protocolo citado pelo cliente nunca vira chamado novo.
# ---------------------------------------------------------------------------

_ARGS_PREPARAR = {"resumo": "x", "descricao": "y", "categoria": "C", "ocorrencia": "O"}


def _estado_preparar(mensagens: list) -> dict:
    return {"identidade": _IDENTIDADE_AZAPERS, "telefone": "5531999990000", "messages": mensagens}


def test_novo_chamado_false_nao_exige_nada():
    args, erro = aplicar_politica(
        "preparar_abertura_chamado",
        {**_ARGS_PREPARAR, "novo_chamado": False},
        _estado_preparar([HumanMessage(content="o app trava, abre um chamado")]),
    )
    assert erro is None and args["novo_chamado"] is False


@pytest.mark.parametrize(
    "mensagem",
    ["abre um chamado", "Esse", "o app trava de novo", "sim", "pode abrir", "preciso de ajuda"],
)
def test_novo_chamado_true_sem_pedido_do_cliente_e_recusado(mensagem):
    """O modelo decidiu sozinho que é um chamado novo: a política recusa e
    manda mostrar os abertos."""
    _, erro = aplicar_politica(
        "preparar_abertura_chamado",
        {**_ARGS_PREPARAR, "novo_chamado": True},
        _estado_preparar(
            [HumanMessage(content="o app trava"), AIMessage(content="Entendi. Em qual tela?"), HumanMessage(content=mensagem)]
        ),
    )
    assert erro is not None and "novo_chamado=true" in erro and "chamados abertos" in erro


@pytest.mark.parametrize(
    "mensagem",
    [
        "quero abrir outro chamado",
        "pode abrir um novo chamado, esse é outro problema",
        "Abre outro, por favor",
        "preciso de mais um chamado",
        "cria um chamado novo pra isso",
        "não é esse, abre um novo",
    ],
)
def test_novo_chamado_true_com_pedido_explicito_na_mensagem_passa(mensagem):
    args, erro = aplicar_politica(
        "preparar_abertura_chamado",
        {**_ARGS_PREPARAR, "novo_chamado": True},
        _estado_preparar([HumanMessage(content=mensagem)]),
    )
    assert erro is None and args["novo_chamado"] is True


def test_novo_chamado_true_passa_quando_cliente_confirma_pergunta_sobre_chamado_novo():
    """O agente mostrou o aberto e perguntou se quer um novo mesmo assim; o
    'sim' do cliente é o pedido explícito."""
    msgs = [
        HumanMessage(content="o app trava"),
        AIMessage(
            content="Você já tem o chamado ZPRS25208011 (APP TRAVANDO) em aberto: https://sac/x. "
            "É sobre ele, ou quer abrir um novo chamado mesmo assim?"
        ),
        HumanMessage(content="Sim, pode abrir"),
    ]
    _, erro = aplicar_politica(
        "preparar_abertura_chamado", {**_ARGS_PREPARAR, "novo_chamado": True}, _estado_preparar(msgs)
    )
    assert erro is None


def test_novo_chamado_true_com_sim_a_outra_pergunta_e_recusado():
    """'sim' responde a outra pergunta do agente — não é pedido de chamado novo."""
    msgs = [
        HumanMessage(content="o app trava"),
        AIMessage(content="O erro aparece ao bipar a nota?"),
        HumanMessage(content="sim"),
    ]
    _, erro = aplicar_politica(
        "preparar_abertura_chamado", {**_ARGS_PREPARAR, "novo_chamado": True}, _estado_preparar(msgs)
    )
    assert erro is not None


@pytest.mark.parametrize(
    "mensagem, protocolo",
    [
        ("ZPRS25208011", "ZPRS25208011"),
        ("o chamado zprs25208011 ainda não foi atendido", "ZPRS25208011"),
        ("Esse aqui: ZPRS25208011, abre outro igual", "ZPRS25208011"),
    ],
)
def test_preparar_recusado_quando_cliente_cita_protocolo(mensagem, protocolo):
    """Protocolo citado é chamado EXISTENTE: preparar não roda (nem com
    novo_chamado=true) e o modelo é mandado listar."""
    for novo in (False, True):
        _, erro = aplicar_politica(
            "preparar_abertura_chamado",
            {**_ARGS_PREPARAR, "novo_chamado": novo},
            _estado_preparar([HumanMessage(content=mensagem)]),
        )
        assert erro is not None, mensagem
        assert protocolo in erro and "listar_chamados_abertos" in erro


@pytest.mark.parametrize(
    "mensagem",
    ["abre um chamado, o app trava", "romaneio 4-123456 não aparece", "RCF-123456 sumiu", "NF 35210912345678 não baixou"],
)
def test_preparar_passa_sem_protocolo_na_mensagem(mensagem):
    """Romaneio, NF e números soltos não são protocolo de chamado."""
    _, erro = aplicar_politica(
        "preparar_abertura_chamado", dict(_ARGS_PREPARAR), _estado_preparar([HumanMessage(content=mensagem)])
    )
    assert erro is None, mensagem


def test_protocolo_citado_normaliza_para_maiusculo():
    from src.agent.tool_policy import protocolo_citado

    assert protocolo_citado("protocolo zprs25208011, por favor") == "ZPRS25208011"
    assert protocolo_citado("4-123456") is None
    assert protocolo_citado(None) is None
