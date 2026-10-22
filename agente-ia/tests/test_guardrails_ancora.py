"""Guardrail de saída ancorado por TIPO (E4.5b, B2).

Antes: qualquer tool com sucesso no turno ancorava qualquer afirmação, e a regex só
pegava "abri o chamado". Agora "chamado aberto" exige `abrir_chamado` ok, "te
transferi" exige `transferir_para_atendente` (ou ação de transferência), "cadastro
concluído" exige `cadastrar_solicitante`, e todo número de chamado citado tem de vir
de uma tool ou do cliente. A incerteza honesta da E3.8 segue passando.
"""

from __future__ import annotations

import json

import pytest
from langchain_core.messages import AIMessage, HumanMessage, ToolMessage

from src.agent import nodes
from src.agent.prompts import RESPOSTA_ACAO_SEM_FERRAMENTA
from src.security.output_guardrails import afirmacoes_tipadas, numeros_de_chamado_citados


def _tm(nome: str, payload: dict) -> ToolMessage:
    return ToolMessage(content=json.dumps(payload), tool_call_id=f"tc-{nome}", name=nome)


def _guardrail(resposta: str, *tools: ToolMessage, cliente: str = "sim", acoes=None) -> str:
    msgs = [HumanMessage(content=cliente), *tools, AIMessage(content=resposta)]
    out = nodes.output_guardrail_node({"messages": msgs, "fontes_usadas": [], "acoes": acoes or []})
    return out["messages"][0].content if out else resposta


ABRIR_OK = _tm("abrir_chamado", {"status": True, "numero": "4821"})
PREPARAR_OK = _tm("preparar_abertura_chamado", {"status": True, "proposta": {"titulo": "x"}})
LISTAR_OK = _tm("listar_meus_chamados", {"status": True, "chamados": [{"numero": "4821"}]})
CONSULTAR_OK = _tm("consultar_status_chamado", {"status": True, "numero": "4821", "aberto_em": "x"})
TRANSFERIR_OK = _tm("transferir_para_atendente", {"status": True})
CADASTRAR_OK = _tm("cadastrar_solicitante", {"status": True, "cadastrado": True})

# As 16 frases da sonda da revisão: nenhuma deixa de ser trocada sem a tool certa.
FRASES_SEM_ANCORA = [
    ("Seu chamado nº 4821 foi aberto", [PREPARAR_OK]),
    (
        "O chamado 4821 foi aberto e a equipe já assume",
        [LISTAR_OK.model_copy(update={"content": json.dumps({"status": True, "chamados": []})})],
    ),
    ("Chamado 4821 criado", [PREPARAR_OK]),
    ("Já transferi você para um atendente", [PREPARAR_OK]),
    ("Transferi a conversa para o atendente", [LISTAR_OK]),
    ("Seu cadastro foi concluído", [PREPARAR_OK]),
    ("Protocolo 4821 gerado", [PREPARAR_OK]),
    ("Pronto, abri o chamado 4821", [PREPARAR_OK]),
    ("Foi aberto o chamado 4821", [PREPARAR_OK]),
    ("Estou te transferindo para um atendente agora", [PREPARAR_OK]),
    ("Seu chamado foi aberto com sucesso", [PREPARAR_OK]),
    ("Te cadastrei aqui, pode seguir", [PREPARAR_OK]),
    ("Seu cadastro está concluído", []),
    ("Criei o chamado para você", [CONSULTAR_OK]),
    ("A conversa foi transferida para o time de suporte", [PREPARAR_OK]),
    ("Seu ticket foi criado", []),
]


@pytest.mark.parametrize(("frase", "tools"), FRASES_SEM_ANCORA)
def test_afirmacao_sem_a_tool_do_efeito_e_trocada(frase, tools):
    assert _guardrail(frase, *tools) == RESPOSTA_ACAO_SEM_FERRAMENTA, frase


@pytest.mark.parametrize(
    ("frase", "tools", "acoes"),
    [
        ("Pronto! Abri o chamado 4821. A equipe assume daqui.", [ABRIR_OK], None),
        ("Seu chamado nº 4821 foi aberto.", [ABRIR_OK], None),
        ("Chamado 4821 criado, a equipe já assume.", [ABRIR_OK], None),
        ("Já transferi você para um atendente.", [TRANSFERIR_OK], None),
        # a transferência é uma ação do turno decidida por código (JEV): o sistema a executa
        (
            "Estou te transferindo para um atendente agora.",
            [],
            [{"tipo": "transferir", "origem": "frustracao"}],
        ),
        ("Pronto, seu cadastro foi concluído!", [CADASTRAR_OK], None),
        ("Te cadastrei, agora seguimos com o chamado.", [CADASTRAR_OK], None),
        # consulta que traz o número: "foi aberto" descreve o chamado, não uma ação
        ("O chamado 4821 foi aberto ontem e está em análise.", [CONSULTAR_OK], None),
        ("Seus chamados: o 4821 foi aberto ontem.", [LISTAR_OK], None),
    ],
)
def test_afirmacao_com_a_tool_do_efeito_passa(frase, tools, acoes):
    assert _guardrail(frase, *tools, acoes=acoes) == frase


def test_numero_de_chamado_inventado_e_trocado_mesmo_com_a_tool():
    resposta = "Pronto! Abri o chamado 9999. A equipe assume daqui."
    assert _guardrail(resposta, ABRIR_OK) == RESPOSTA_ACAO_SEM_FERRAMENTA


def test_numero_de_outro_chamado_na_consulta_nao_ancora_um_numero_diferente():
    resposta = "O chamado 7777 foi aberto ontem."
    assert _guardrail(resposta, CONSULTAR_OK) == RESPOSTA_ACAO_SEM_FERRAMENTA


def test_numero_dito_pelo_cliente_pode_ser_repetido_sem_tool():
    resposta = "Sobre o chamado 4821, vou pedir para um atendente conferir."
    assert _guardrail(resposta, cliente="como está o chamado 4821?") == resposta


def test_numero_vindo_do_erro_chamado_aberto_do_preparo_pode_ser_citado():
    erro = _tm(
        "preparar_abertura_chamado",
        {"status": False, "motivo": "chamado_aberto", "chamados_abertos": [{"numero": "48213"}]},
    )
    resposta = "Você já tem o chamado 48213 aberto sobre isso. É o mesmo problema?"
    assert _guardrail(resposta, erro) == resposta


@pytest.mark.parametrize(
    "resposta",
    [
        "Não consegui confirmar se o chamado foi registrado, então não vou abrir outro. Um atendente vai conferir.",
        "Não tenho certeza de que o chamado foi aberto; um atendente vai conferir.",
        "Vou te passar para um atendente.",
        "Posso fazer o seu cadastro? Preciso do seu nome completo.",
        "Você tem algum chamado aberto sobre isso?",
        "Não abri nenhum chamado ainda; quer que eu prepare um?",
    ],
)
def test_incerteza_promessa_e_pergunta_nao_sao_afirmacao(resposta):
    assert _guardrail(resposta, PREPARAR_OK) == resposta


def test_recusa_da_politica_nao_ancora():
    recusa = _tm("abrir_chamado", {"erro": "x", "recusado_pela_politica": True})
    assert _guardrail("Pronto, abri o chamado 4821", recusa) == RESPOSTA_ACAO_SEM_FERRAMENTA


def test_ferramenta_de_turno_anterior_nao_ancora():
    msgs = [
        HumanMessage(content="abre"),
        ABRIR_OK,
        AIMessage(content="Abri o chamado 4821."),
        HumanMessage(content="e agora?"),
        AIMessage(content="Já abri o chamado 4821, fique tranquilo."),
    ]
    out = nodes.output_guardrail_node({"messages": msgs, "fontes_usadas": []})
    assert out["messages"][0].content == RESPOSTA_ACAO_SEM_FERRAMENTA


def test_detectores_tipados_e_numeros():
    assert set(afirmacoes_tipadas("Já abri o chamado 4821 e te transferi você")) == {
        "abertura",
        "transferencia",
    }
    assert afirmacoes_tipadas("Vou abrir o chamado e te transferir") == {}
    assert numeros_de_chamado_citados(
        "O chamado nº 4821 e o ticket #5555; ligue 31 99999-0001"
    ) == [
        "4821",
        "5555",
    ]


# ---------------------------------------------------------------------------
# Achados da revisão do PR 44
# ---------------------------------------------------------------------------

# I2: formas que passavam mesmo com `preparar` ok no turno (ou sem nenhuma tool)
FRASES_SEM_ANCORA_2 = [
    ("Chamado aberto com sucesso!", [PREPARAR_OK]),
    ("Pronto, seu chamado está aberto e a equipe já foi avisada.", [PREPARAR_OK]),
    ("Já abri pra você.", [PREPARAR_OK]),
    ("Abri uma solicitação para a equipe técnica.", [PREPARAR_OK]),
    ("O número do seu chamado é 48213.", [PREPARAR_OK]),
    ("Seu protocolo é 99887.", [PREPARAR_OK]),
    ("Te transferi para um atendente.", []),
    ("Já te passei para um atendente.", [PREPARAR_OK]),
    ("Te encaminhei para um atendente.", []),
    ("Um atendente já foi acionado.", []),
    ("Transferência concluída.", [PREPARAR_OK]),
    ("Pronto, você já está cadastrado!", [PREPARAR_OK]),
]


@pytest.mark.parametrize(("frase", "tools"), FRASES_SEM_ANCORA_2)
def test_mais_formas_de_afirmar_sem_a_tool_do_efeito_sao_trocadas(frase, tools):
    assert _guardrail(frase, *tools) == RESPOSTA_ACAO_SEM_FERRAMENTA, frase


@pytest.mark.parametrize(
    "resposta",
    [
        "Seu chamado foi aberto ontem às 14h e está em análise.",
        "Seu chamado foi criado em 20/10 e segue em andamento.",
        "O chamado 4821 foi aberto ontem e está em análise.",
    ],
)
def test_status_via_consulta_nao_precisa_repetir_o_numero(resposta):
    """Caso mais comum da consulta: a resposta pode não citar o número."""
    assert _guardrail(resposta, CONSULTAR_OK) == resposta
    assert _guardrail(resposta, LISTAR_OK) == resposta


def test_chamado_de_dois_digitos_consultado_nao_e_barrado():
    consulta = _tm("consultar_status_chamado", {"status": True, "numero": "42"})
    assert (
        _guardrail("O chamado 42 foi aberto ontem.", consulta) == "O chamado 42 foi aberto ontem."
    )


def test_status_sem_consulta_nenhuma_continua_barrado():
    assert _guardrail("Seu chamado foi aberto ontem e está em análise.") == (
        RESPOSTA_ACAO_SEM_FERRAMENTA
    )


def test_cliente_ja_identificado_pode_ouvir_que_esta_cadastrado():
    msgs = [HumanMessage(content="oi"), AIMessage(content="Você já está cadastrado, Maria.")]
    ident = {"forma": "cadastro", "valor_mascarado": "Maria S."}
    com = nodes.output_guardrail_node(
        {"messages": msgs, "fontes_usadas": [], "identificacao": ident}
    )
    sem = nodes.output_guardrail_node({"messages": msgs, "fontes_usadas": []})
    assert com == {}
    assert sem["messages"][0].content == RESPOSTA_ACAO_SEM_FERRAMENTA


def test_numero_anunciado_tambem_precisa_vir_de_uma_tool():
    assert numeros_de_chamado_citados("O número do seu chamado é 48213. Seu protocolo: 99887") == [
        "48213",
        "99887",
    ]
    assert _guardrail("O número do seu chamado é 99999.", ABRIR_OK) == RESPOSTA_ACAO_SEM_FERRAMENTA
