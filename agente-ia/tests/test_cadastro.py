"""Cadastro de solicitante em duas fases (E5.3, ADR 0009, `docs/plano/01` A8).

Valida: as regras de entrada (nome, e-mail, empresa), as duas tools contra os JSON de
`contratos/tools-infodesk/cadastro/**`, a política (cada recusa, injeção do telefone da
sessão, proposta lida do histórico) e o efeito no estado. No fim, o e2e de três turnos
com LLM scriptado: preparar → "sim" → cadastrar → o cliente já está identificado e
prepara o chamado. Sem rede: o gateway é `tests/fakes.py::FakeGateway`.
"""

from __future__ import annotations

import json

import httpx
import pytest
from langchain_core.messages import AIMessage, HumanMessage, ToolMessage

from src.agent import tool_policy
from src.agent.graph import build_graph, get_default_tools
from src.agent.tool_policy import aplicar_politica
from src.identity.cadastro import email_valido, empresa_valida, linha_unica, nome_valido
from src.tools import efeitos
from src.tools.infodesk_tools import cadastrar_solicitante, preparar_cadastro_solicitante
from tests.fakes import (
    RAIZ_CONTRATOS_TOOLS,
    FakeJEV,
    contrato_tools,
    fake_llm,
    instalar_gateway_fake,
    resp_jev,
    sem_rag,
    servico_fake,
)

CONVERSA = "6f1c2a3e-9b4d-4e7a-8c21-5d3f0a9b7e10"
TELEFONE = "+5531999990001"
PREPARAR = "preparar_cadastro_solicitante"
CADASTRAR = "cadastrar_solicitante"


def _motivos(caminho: str) -> list[str]:
    return [
        a.stem.removeprefix("erro-")
        for a in sorted((RAIZ_CONTRATOS_TOOLS / caminho).glob("erro-*.json"))
    ]


# ---------------------------------------------------------------------------
# Regras de entrada (as mesmas do gateway Go)
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    ("nome", "valido"),
    [
        ("Maria Souza", True),
        ("  maria   souza  ", True),
        ("João da Silva", True),
        ("Ana-Paula D'Ávila", True),
        ("Dr. José Neto", True),
        ("Maria\nSouza", True),  # controle vira espaço
        ("Maria", False),
        ("", False),
        ("   ", False),
        (None, False),
        ("Maria 123", False),
        ("maria@acme.com Souza", False),
        ("1 2", False),
        ("- -", False),
        ("a" * 70 + " " + "b" * 70, False),
    ],
)
def test_nome_valido(nome, valido):
    assert nome_valido(nome) is valido


@pytest.mark.parametrize(
    ("email", "valido"),
    [
        ("", True),
        ("   ", True),
        (None, True),
        ("maria@acme.com.br", True),
        (" maria@acme.com ", True),
        ("maria", False),
        ("maria@acme", False),
        ("@acme.com", False),
        ("maria@@acme.com", False),
        ("mar ia@acme.com", False),
        ("a" * 250 + "@b.co", False),
    ],
)
def test_email_valido(email, valido):
    assert email_valido(email) is valido


@pytest.mark.parametrize(
    ("empresa", "valida"),
    [("ACME Ltda", True), ("", False), ("   ", False), (None, False), ("x" * 121, False)],
)
def test_empresa_valida(empresa, valida):
    assert empresa_valida(empresa) is valida


def test_linha_unica_tira_controle_e_colapsa_espacos():
    assert linha_unica("  ACME \n\t Ltda\x00 ") == "ACME Ltda"
    assert linha_unica(None) == ""


# ---------------------------------------------------------------------------
# As tools contra os contratos
# ---------------------------------------------------------------------------


def _gateway(monkeypatch, **respostas):
    return instalar_gateway_fake(monkeypatch, respostas)


def test_preparar_envia_exatamente_o_request_do_contrato(monkeypatch):
    gw = _gateway(monkeypatch, **{"cadastro/preparar": contrato_tools("cadastro/preparar", "ok")})
    req = contrato_tools("cadastro/preparar", "request")
    saida = preparar_cadastro_solicitante.invoke(
        {
            "nome": req["nome"],
            "empresa": req["empresa"],
            "email": req["email"],
            "telefone": req["telefone"],
            "conversation_id": req["conversation_id"],
        }
    )
    assert gw.chamadas[0]["json"] == req
    assert saida == contrato_tools("cadastro/preparar", "ok")
    assert gw.chamadas[0]["headers"]["X-Tools-Token"]


def test_preparar_limpa_o_texto_do_cliente_antes_de_enviar(monkeypatch):
    gw = _gateway(monkeypatch, **{"cadastro/preparar": contrato_tools("cadastro/preparar", "ok")})
    preparar_cadastro_solicitante.invoke(
        {
            "nome": " Maria \n Souza ",
            "empresa": "ACME\tLtda ",
            "email": " m@a.co ",
            "telefone": TELEFONE,
        }
    )
    corpo = gw.chamadas[0]["json"]
    assert (corpo["nome"], corpo["empresa"], corpo["email"]) == (
        "Maria Souza",
        "ACME Ltda",
        "m@a.co",
    )


@pytest.mark.parametrize("motivo", _motivos("cadastro/preparar"))
def test_preparar_repassa_cada_recusa_do_gateway(monkeypatch, motivo):
    esperado = contrato_tools("cadastro/preparar", f"erro-{motivo}")
    _gateway(monkeypatch, **{"cadastro/preparar": esperado})
    saida = preparar_cadastro_solicitante.invoke(
        {
            "nome": "Maria Souza",
            "empresa": "ACME Ltda",
            "telefone": TELEFONE,
            "conversation_id": CONVERSA,
        }
    )
    assert saida == esperado


def test_empresa_ambigua_so_leva_nomes_para_o_modelo():
    erro = contrato_tools("cadastro/preparar", "erro-empresa_ambigua")
    assert all(set(c) == {"nome"} for c in erro["candidatos"])


def test_cadastrar_envia_a_proposta_do_contrato_e_o_ok_chega_ao_modelo(monkeypatch):
    gw = _gateway(monkeypatch, **{"cadastro/cadastrar": contrato_tools("cadastro/cadastrar", "ok")})
    req = contrato_tools("cadastro/cadastrar", "request")
    saida = cadastrar_solicitante.invoke(
        {
            "proposta": req["proposta"],
            "telefone": req["telefone"],
            "conversation_id": req["conversation_id"],
        }
    )
    assert gw.chamadas[0]["json"] == req
    assert saida == contrato_tools("cadastro/cadastrar", "ok")


@pytest.mark.parametrize("motivo", _motivos("cadastro/cadastrar"))
def test_cadastrar_repassa_cada_recusa_do_gateway(monkeypatch, motivo):
    esperado = contrato_tools("cadastro/cadastrar", f"erro-{motivo}")
    _gateway(monkeypatch, **{"cadastro/cadastrar": esperado})
    req = contrato_tools("cadastro/cadastrar", "request")
    saida = cadastrar_solicitante.invoke({"proposta": req["proposta"], "telefone": TELEFONE})
    assert saida == esperado


@pytest.mark.parametrize("proposta", [None, {}, {"nome": "x"}])
def test_cadastrar_sem_proposta_nem_chama_o_gateway(monkeypatch, proposta):
    gw = _gateway(monkeypatch)
    saida = cadastrar_solicitante.invoke({"proposta": proposta, "telefone": TELEFONE})
    assert saida["status"] is False and saida["motivo"] == "sem_proposta"
    assert gw.chamadas == []


@pytest.mark.parametrize(
    "falha",
    [
        httpx.ReadTimeout("lento"),
        httpx.ConnectError("caiu"),
        (500, "<html>erro</html>"),
        (200, "nao json"),
    ],
)
@pytest.mark.parametrize("nome_da_tool", [PREPARAR, CADASTRAR])
def test_falha_de_transporte_vira_indisponivel_sem_texto_cru(monkeypatch, nome_da_tool, falha):
    caminho = "cadastro/preparar" if nome_da_tool == PREPARAR else "cadastro/cadastrar"
    gw = _gateway(monkeypatch, **{caminho: falha})
    args = (
        {"nome": "Maria Souza", "empresa": "ACME", "telefone": TELEFONE}
        if nome_da_tool == PREPARAR
        else {"proposta": {"chave": "k"}, "telefone": TELEFONE}
    )
    tool = preparar_cadastro_solicitante if nome_da_tool == PREPARAR else cadastrar_solicitante
    saida = tool.invoke(args)
    assert saida["status"] is False and saida["motivo"] == "infodesk_indisponivel"
    assert "html" not in json.dumps(saida) and "caiu" not in json.dumps(saida)
    assert len(gw.chamadas) == 1  # nunca repete


# ---------------------------------------------------------------------------
# Política
# ---------------------------------------------------------------------------


def _tool_msg(nome: str, dado: dict, id_: str = "t") -> ToolMessage:
    return ToolMessage(content=json.dumps(dado, ensure_ascii=False), tool_call_id=id_, name=nome)


def _ai(tool: str, /, **args) -> AIMessage:
    return AIMessage(content="", tool_calls=[{"id": "x", "name": tool, "args": args}])


def _estado(*mensagens, **extra) -> dict:
    return {"telefone": TELEFONE, "conversation_id": CONVERSA, "messages": list(mensagens), **extra}


def _preparado(turno_do_preparo: int = 1) -> list:
    """Histórico: o cliente falou, o modelo preparou e o gateway aceitou."""
    msgs: list = []
    for i in range(turno_do_preparo):
        msgs.append(HumanMessage(content=f"mensagem {i + 1}"))
    msgs += [_ai(PREPARAR), _tool_msg(PREPARAR, contrato_tools("cadastro/preparar", "ok"))]
    return msgs


ARGS_OK = {"nome": "Maria Souza", "empresa": "ACME Ltda", "email": "maria@acme.com.br"}


def test_politica_tem_as_duas_tools_com_args_de_sessao_injetados():
    assert set(tool_policy.POLITICAS) >= {PREPARAR, CADASTRAR}
    assert set(tool_policy.POLITICAS[PREPARAR].injetar) == {"telefone", "conversation_id"}
    assert set(tool_policy.POLITICAS[CADASTRAR].injetar) == {
        "telefone",
        "conversation_id",
        "proposta",
    }
    assert set(tool_policy.POLITICAS[PREPARAR].validar) == {"nome", "empresa", "email"}


def test_preparar_injeta_o_telefone_da_sessao_e_ignora_o_do_llm():
    args, erro = aplicar_politica(
        PREPARAR,
        {
            **ARGS_OK,
            "telefone": "+5500000000000",
            "conversation_id": "outra",
            "cliente_id": "k-forjado",
        },
        _estado(HumanMessage(content="oi")),
    )
    assert erro is None
    assert args["telefone"] == TELEFONE and args["conversation_id"] == CONVERSA


@pytest.mark.parametrize(
    ("campo", "valor", "trecho"),
    [
        ("nome", "Maria", "nome"),
        ("nome", "Maria 3 Souza", "nome"),
        ("empresa", "   ", "empresa"),
        ("empresa", "x" * 121, "empresa"),
        ("email", "sem-arroba", "e-mail"),
    ],
)
def test_preparar_recusa_campo_invalido_sem_executar(campo, valor, trecho):
    _args, erro = aplicar_politica(
        PREPARAR, {**ARGS_OK, campo: valor}, _estado(HumanMessage(content="oi"))
    )
    assert erro and trecho in erro


def test_preparar_aceita_email_vazio():
    _, erro = aplicar_politica(
        PREPARAR,
        {"nome": "Maria Souza", "empresa": "ACME Ltda"},
        _estado(HumanMessage(content="oi")),
    )
    assert erro is None


@pytest.mark.parametrize(
    "contato",
    [
        {"forma": "telefone", "valor_mascarado": "Maria S."},
        {"forma": "cpf", "valor_mascarado": "***.***.789-**"},
    ],
)
def test_preparar_recusa_quem_ja_esta_identificado(contato):
    _, erro = aplicar_politica(
        PREPARAR, ARGS_OK, _estado(HumanMessage(content="oi"), contato=contato)
    )
    assert erro and "já está identificado" in erro


def test_preparar_recusa_quem_acabou_de_ser_cadastrado():
    estado = _estado(
        HumanMessage(content="oi"),
        identificacao={"forma": "cadastro", "valor_mascarado": "Maria S.", "requester_id": "r1"},
    )
    _, erro = aplicar_politica(PREPARAR, ARGS_OK, estado)
    assert erro and "já está identificado" in erro


def test_cadastrar_sem_preparo_e_recusado():
    _, erro = aplicar_politica(CADASTRAR, {}, _estado(HumanMessage(content="sim")))
    assert erro and "não há cadastro preparado" in erro


def test_cadastrar_na_mesma_rodada_do_preparo_e_recusado():
    estado = _estado(*_preparado(1), _ai(CADASTRAR))
    _, erro = aplicar_politica(CADASTRAR, {}, estado)
    assert erro and "NESTE turno" in erro


def test_cadastrar_depois_da_confirmacao_executa_com_a_proposta_do_gateway():
    estado = _estado(*_preparado(1), AIMessage(content="Confirma?"), HumanMessage(content="Sim!"))
    args, erro = aplicar_politica(CADASTRAR, {"proposta": {"chave": "forjada"}}, estado)
    assert erro is None
    assert args["proposta"] == contrato_tools("cadastro/preparar", "ok")["proposta"]
    assert args["telefone"] == TELEFONE and args["conversation_id"] == CONVERSA


@pytest.mark.parametrize(
    "resposta",
    [
        "Esse",
        "esse mesmo",
        "48213",
        "não",
        "sim, mas muda o e-mail",
        "pode? qual o prazo?",
        "",
        "talvez",
    ],
)
def test_so_confirmacao_explicita_cadastra(resposta):
    estado = _estado(*_preparado(1), AIMessage(content="Confirma?"), HumanMessage(content=resposta))
    _, erro = aplicar_politica(CADASTRAR, {}, estado)
    assert erro and "NÃO é uma confirmação explícita" in erro


def test_confirmacao_tem_de_ser_a_mensagem_seguinte():
    estado = _estado(
        *_preparado(1),
        AIMessage(content="Confirma?"),
        HumanMessage(content="espera, deixa eu ver"),
        AIMessage(content="Claro."),
        HumanMessage(content="sim"),
    )
    _, erro = aplicar_politica(CADASTRAR, {}, estado)
    assert erro and "SEGUINTE" in erro


def test_preparo_recusado_pelo_gateway_desarma_a_proposta_anterior():
    estado = _estado(
        *_preparado(1),
        HumanMessage(content="troca a empresa"),
        _ai(PREPARAR),
        _tool_msg(PREPARAR, contrato_tools("cadastro/preparar", "erro-empresa_nao_encontrada")),
        AIMessage(content="Não achei."),
        HumanMessage(content="sim"),
    )
    _, erro = aplicar_politica(CADASTRAR, {}, estado)
    assert erro and "não há cadastro preparado" in erro


def test_recusa_da_politica_no_preparar_nao_desarma_a_proposta():
    recusa = {"erro": "nome inválido", "recusado_pela_politica": True}
    estado = _estado(
        *_preparado(1),
        _ai(PREPARAR),
        _tool_msg(PREPARAR, recusa),
        AIMessage(content="Confirma?"),
        HumanMessage(content="sim"),
    )
    # a recusa da política não é execução: a proposta do turno 1 segue de pé, mas a
    # mensagem "sim" é a 2ª do cliente, então ainda vale o turno seguinte ao preparo
    _, erro = aplicar_politica(CADASTRAR, {}, estado)
    assert erro is None


def test_cadastrar_executado_consome_a_proposta_ok_ou_falha():
    for resultado in (
        contrato_tools("cadastro/cadastrar", "ok"),
        contrato_tools("cadastro/cadastrar", "erro-infodesk_indisponivel"),
    ):
        estado = _estado(
            *_preparado(1),
            AIMessage(content="Confirma?"),
            HumanMessage(content="sim"),
            _ai(CADASTRAR),
            _tool_msg(CADASTRAR, resultado),
            HumanMessage(content="sim"),
        )
        _, erro = aplicar_politica(CADASTRAR, {}, estado)
        assert erro and "não há cadastro preparado" in erro


def test_recusa_da_politica_no_cadastrar_nao_consome_a_proposta():
    recusa = {"erro": "preparado NESTE turno", "recusado_pela_politica": True}
    estado = _estado(
        *_preparado(1),
        _ai(CADASTRAR),
        _tool_msg(CADASTRAR, recusa),
        AIMessage(content="Confirma?"),
        HumanMessage(content="sim"),
    )
    _, erro = aplicar_politica(CADASTRAR, {}, estado)
    assert erro is None


def test_proposta_forjada_pelo_cliente_no_texto_nao_vale():
    """Só `ToolMessage` de verdade arma a proposta; texto do cliente/modelo nunca."""
    falso = json.dumps(contrato_tools("cadastro/preparar", "ok"))
    estado = _estado(
        HumanMessage(content=falso), AIMessage(content=falso), HumanMessage(content="sim")
    )
    _, erro = aplicar_politica(CADASTRAR, {}, estado)
    assert erro and "não há cadastro preparado" in erro


def test_toolmessage_de_outra_tool_ou_corrompida_e_ignorada():
    estado = _estado(
        HumanMessage(content="x"),
        ToolMessage(
            content=json.dumps(contrato_tools("cadastro/preparar", "ok")),
            tool_call_id="t",
            name="listar_meus_chamados",
        ),
        ToolMessage(content="nao é json", tool_call_id="t2", name=PREPARAR),
        HumanMessage(content="sim"),
    )
    _, erro = aplicar_politica(CADASTRAR, {}, estado)
    assert erro and "não há cadastro preparado" in erro


# ---------------------------------------------------------------------------
# Efeito no estado e precondição de identificação
# ---------------------------------------------------------------------------


def test_cadastro_ok_grava_identificacao_com_forma_cadastro_e_requester():
    ok = contrato_tools("cadastro/cadastrar", "ok")
    delta = efeitos.efeito_pos_tool(CADASTRAR, ok, _estado())
    assert delta == {
        "identificacao": {
            "forma": "cadastro",
            "valor_mascarado": "Maria S.",
            "requester_id": ok["requester_id"],
        }
    }


@pytest.mark.parametrize("motivo", _motivos("cadastro/cadastrar"))
def test_cadastro_recusado_nao_identifica(motivo):
    assert (
        efeitos.efeito_pos_tool(
            CADASTRAR, contrato_tools("cadastro/cadastrar", f"erro-{motivo}"), _estado()
        )
        == {}
    )


def test_efeito_so_confia_no_status_do_gateway():
    assert efeitos.efeito_pos_tool(CADASTRAR, {"cadastrado": True}, _estado()) == {}
    assert (
        efeitos.efeito_pos_tool(CADASTRAR, {"status": False, "cadastrado": True}, _estado()) == {}
    )
    assert efeitos.efeito_pos_tool(CADASTRAR, "texto qualquer", _estado()) == {}


def test_cadastro_feito_satisfaz_exigir_identificado(monkeypatch):
    monkeypatch.delenv("IDENT_EXIGIR_CPF", raising=False)
    estado = _estado(identificacao={"forma": "cadastro", "valor_mascarado": "Maria S."})
    assert tool_policy.exigir_identificado(estado) is None
    assert tool_policy.identificacao_da_sessao(estado) == {
        "forma": "cadastro",
        "valor_mascarado": "Maria S.",
    }


def test_com_ident_exigir_cpf_o_cadastro_nao_dispensa_o_cpf(monkeypatch):
    """`IDENT_EXIGIR_CPF` segue sem decisão (N3): o cadastro vale como o lookup, nada além."""
    monkeypatch.setenv("IDENT_EXIGIR_CPF", "true")
    estado = _estado(identificacao={"forma": "cadastro", "valor_mascarado": "Maria S."})
    assert "CPF" in (tool_policy.exigir_identificado(estado) or "")


def test_o_schema_do_modelo_nao_tem_telefone_cliente_nem_proposta():
    from langchain_core.utils.function_calling import convert_to_openai_tool

    por_nome = {
        t.name: convert_to_openai_tool(t)["function"]["parameters"] for t in get_default_tools()
    }
    assert set(por_nome[PREPARAR]["properties"]) == {"nome", "empresa", "email"}
    assert por_nome[PREPARAR]["required"] == ["nome", "empresa"]
    assert not por_nome[CADASTRAR].get("properties")


# ---------------------------------------------------------------------------
# E2E de três turnos: preparar → "sim" → cadastrar → chamado
# ---------------------------------------------------------------------------


def _chamada(tool: str, id_: str, /, **args) -> AIMessage:
    return AIMessage(content="", tool_calls=[{"id": id_, "name": tool, "args": args}])


def _grafo(llm):
    servico = servico_fake(FakeJEV(padrao=resp_jev()))
    return build_graph(
        llm=llm, tools=get_default_tools(), buscar_chunks=sem_rag, classificador=servico
    )


def _turno(grafo, texto: str, **estado):
    return grafo.invoke(
        {"messages": [HumanMessage(content=texto)], **estado},
        config={"configurable": {"thread_id": CONVERSA}},
    )


def _gw(monkeypatch):
    return instalar_gateway_fake(
        monkeypatch,
        {
            "cadastro/preparar": contrato_tools("cadastro/preparar", "ok"),
            "cadastro/cadastrar": contrato_tools("cadastro/cadastrar", "ok"),
            "ticket/preparar": contrato_tools("ticket/preparar", "ok"),
        },
    )


def _primeiro(grafo, texto: str):
    return _turno(
        grafo,
        texto,
        telefone=TELEFONE,
        conversation_id=CONVERSA,
        nome_contato="Maria",
        contato=None,
    )


def test_tres_turnos_preparar_sim_cadastrar_e_ja_prepara_o_chamado(monkeypatch):
    monkeypatch.delenv("IDENT_EXIGIR_CPF", raising=False)
    gw = _gw(monkeypatch)
    llm = fake_llm(
        [
            _chamada(PREPARAR, "tc-1", **ARGS_OK),
            AIMessage(
                content="Maria Souza, da ACME Ltda, e-mail maria@acme.com.br. Posso cadastrar?"
            ),
            _chamada(CADASTRAR, "tc-2"),  # args == {}: o modelo não escolhe nada
            AIMessage(content="Cadastro feito! Me conta o que está acontecendo."),
            _chamada(
                "preparar_abertura_chamado",
                "tc-3",
                titulo="Internet sem conexão",
                descricao="Cliente sem conexão desde ontem.",
            ),
            AIMessage(content="Vou registrar: internet sem conexão. Posso abrir?"),
        ]
    )
    grafo = _grafo(llm)

    # Turno 1: só o preparo; nada é cadastrado; o cliente ainda não está identificado.
    out1 = _primeiro(grafo, "Oi! Sou Maria Souza, da ACME Ltda, meu e-mail é maria@acme.com.br")
    assert [c["caminho"] for c in gw.chamadas] == ["cadastro/preparar"]
    assert gw.chamadas[0]["json"] == contrato_tools("cadastro/preparar", "request")
    assert "identificacao" not in out1 or not out1.get("identificacao")

    # Turno 2: "sim" → cadastra, sem argumentos do modelo, com a proposta do gateway.
    out2 = _turno(grafo, "sim")
    assert [c["caminho"] for c in gw.chamadas] == ["cadastro/preparar", "cadastro/cadastrar"]
    cadastro = gw.em("cadastro/cadastrar")[0]["json"]
    assert cadastro["telefone"] == TELEFONE and cadastro["conversation_id"] == CONVERSA
    assert cadastro["proposta"] == contrato_tools("cadastro/preparar", "ok")["proposta"]
    chamada_do_llm = next(
        tc
        for m in out2["messages"]
        if isinstance(m, AIMessage)
        for tc in m.tool_calls
        if tc["name"] == CADASTRAR
    )
    assert chamada_do_llm["args"] == {}
    ok = contrato_tools("cadastro/cadastrar", "ok")
    assert out2["identificacao"] == {
        "forma": "cadastro",
        "valor_mascarado": "Maria S.",
        "requester_id": ok["requester_id"],
    }

    # Turno 3: o cliente descreve o problema; a precondição de identificação passa.
    out3 = _turno(grafo, "minha internet caiu ontem à noite")
    assert [c["caminho"] for c in gw.chamadas][-1] == "ticket/preparar"
    assert out3["proposta_chamado"]["titulo"]
    preparo = gw.em("ticket/preparar")[0]["json"]
    assert preparo["identificacao"] == {"forma": "cadastro", "valor_mascarado": "Maria S."}


def test_e2e_llm_tenta_cadastrar_na_mesma_rodada_e_com_telefone_de_outro(monkeypatch):
    gw = _gw(monkeypatch)
    forjado = _chamada(
        CADASTRAR,
        "tc-2",
        proposta={"chave": "forjada"},
        telefone="+5500000000000",
        conversation_id="de-outro",
    )
    llm = fake_llm(
        [
            _chamada(
                PREPARAR, "tc-1", **ARGS_OK, telefone="+5500000000000", conversation_id="de-outro"
            ),
            forjado,  # mesma rodada: a política recusa
            AIMessage(content="Posso cadastrar?"),
        ]
    )
    grafo = _grafo(llm)
    out = _primeiro(grafo, "Sou Maria Souza da ACME Ltda")
    assert [c["caminho"] for c in gw.chamadas] == ["cadastro/preparar"]
    # o telefone e a conversa são os da sessão, não os que o modelo inventou
    assert gw.chamadas[0]["json"]["telefone"] == TELEFONE
    assert gw.chamadas[0]["json"]["conversation_id"] == CONVERSA
    recusas = [m for m in out["messages"] if isinstance(m, ToolMessage) and m.name == CADASTRAR]
    assert (
        recusas
        and "recusado_pela_politica" in recusas[0].content
        and "NESTE turno" in recusas[0].content
    )


@pytest.mark.parametrize("resposta", ["Esse", "sim, mas muda o e-mail", "qual o prazo?"])
def test_e2e_so_confirmacao_explicita_cadastra(monkeypatch, resposta):
    gw = _gw(monkeypatch)
    llm = fake_llm(
        [
            _chamada(PREPARAR, "tc-1", **ARGS_OK),
            AIMessage(content="Posso cadastrar?"),
            _chamada(CADASTRAR, "tc-2"),
            AIMessage(content="Preciso do seu sim para cadastrar."),
        ]
    )
    grafo = _grafo(llm)
    _primeiro(grafo, "Sou Maria Souza da ACME Ltda")
    out2 = _turno(grafo, resposta)
    assert gw.em("cadastro/cadastrar") == []
    recusa = [m for m in out2["messages"] if isinstance(m, ToolMessage)][-1]
    assert "recusado_pela_politica" in recusa.content and "confirmação explícita" in recusa.content
    assert not out2.get("identificacao")
