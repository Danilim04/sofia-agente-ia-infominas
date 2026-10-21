"""E2E das tools do Infodesk (E2.3): grafo real + LLM scriptado + `FakeJEV` + gateway falso.

Prova o fluxo em duas fases ponta a ponta — preparar (turno 1), "sim" (turno 2),
abrir — e que o `abrir_chamado` roda SEM argumentos do modelo, só com o que o
código injetou do estado. "Esse", uma frase que não é só confirmação, a mesma
rodada do preparo ou a ausência de preparo NUNCA chegam ao `ticket/abrir` do
gateway. Tudo sem rede: o gateway é `tests/fakes.py::FakeGateway` com os JSON de
`contratos/tools-infodesk/`.
"""

from __future__ import annotations

import hashlib

import pytest
from langchain_core.messages import AIMessage, HumanMessage, ToolMessage
from langchain_core.utils.function_calling import convert_to_openai_tool

from src.agent.graph import build_graph, get_default_tools
from tests.fakes import (
    FakeJEV,
    contrato_tools,
    fake_llm,
    instalar_gateway_fake,
    resp_jev,
    sem_rag,
    servico_fake,
)

CONVERSA = "conv-e2e-infodesk"
TELEFONE = "+5531999990001"
ARGS_DE_SESSAO = {"telefone", "conversation_id", "classificacao", "proposta", "identificacao"}
CONTATO_LOOKUP = {"forma": "telefone", "valor_mascarado": "Maria S.", "cliente_id": "c-1"}
TITULO = "Internet sem conexão desde ontem à noite"
DESCRICAO = "Cliente relata internet totalmente sem conexão. Já reiniciou o roteador."


def _chamada(nome: str, id_: str, **args) -> AIMessage:
    return AIMessage(content="", tool_calls=[{"id": id_, "name": nome, "args": args}])


def _preparar(id_: str = "tc-prep") -> AIMessage:
    return _chamada("preparar_abertura_chamado", id_, titulo=TITULO, descricao=DESCRICAO)


def _gateway(monkeypatch, **extra):
    respostas = {
        "ticket/preparar": contrato_tools("ticket/preparar", "ok"),
        "ticket/abrir": contrato_tools("ticket/abrir", "ok"),
        "contato/identificar": contrato_tools("contato/identificar", "ok"),
        **extra,
    }
    return instalar_gateway_fake(monkeypatch, respostas)


def _grafo(llm):
    servico = servico_fake(FakeJEV(padrao=resp_jev()))
    return build_graph(
        llm=llm, tools=get_default_tools(), buscar_chunks=sem_rag, classificador=servico
    )


def _turno(grafo, texto: str, **estado):
    entrada = {"messages": [HumanMessage(content=texto)], **estado}
    return grafo.invoke(entrada, config={"configurable": {"thread_id": CONVERSA}})


def _primeiro_turno(grafo, texto: str = "minha internet caiu ontem, pode abrir um chamado?"):
    return _turno(
        grafo,
        texto,
        telefone=TELEFONE,
        conversation_id=CONVERSA,
        nome_contato="Maria",
        contato=CONTATO_LOOKUP,
    )


def _tool_messages(out) -> list[ToolMessage]:
    return [m for m in out["messages"] if isinstance(m, ToolMessage)]


# ---------------------------------------------------------------------------
# O fluxo feliz: preparar → "sim" → abrir
# ---------------------------------------------------------------------------


def test_dois_turnos_preparar_sim_abrir_sem_argumentos_do_llm(monkeypatch):
    gw = _gateway(monkeypatch)
    llm = fake_llm(
        [
            _preparar(),
            AIMessage(content="Vou registrar: sem conexão desde ontem. Posso abrir?"),
            _chamada("abrir_chamado", "tc-abrir"),  # args == {}: o modelo não escolhe nada
            AIMessage(content="Pronto, Maria! Abri o chamado 48213."),
        ]
    )
    grafo = _grafo(llm)

    # Turno 1: só o preparo executa; nada é aberto; a proposta é carimbada.
    out1 = _primeiro_turno(grafo)
    assert [c["caminho"] for c in gw.chamadas] == ["ticket/preparar"]
    preparo = gw.chamadas[0]["json"]
    assert preparo["telefone"] == TELEFONE and preparo["conversation_id"] == CONVERSA
    assert preparo["categoria"] == "sem_conexao" and preparo["prioridade"] == "alta"
    assert preparo["identificacao"] == {"forma": "telefone", "valor_mascarado": "Maria S."}
    proposta = out1["proposta_chamado"]
    assert proposta["turno"] == 1 and proposta["criada_em"]
    assert proposta["titulo"] == contrato_tools("ticket/preparar", "ok")["proposta"]["titulo"]
    assert "Posso abrir" in out1["messages"][-1].content

    # Turno 2: "sim" → abre, com o que o código injetou.
    out2 = _turno(grafo, "sim")
    [abertura] = gw.em("ticket/abrir")
    assert [c["caminho"] for c in gw.chamadas] == ["ticket/preparar", "ticket/abrir"]
    chamada_do_llm = next(
        tc
        for m in out2["messages"]
        if isinstance(m, AIMessage)
        for tc in m.tool_calls
        if tc["name"] == "abrir_chamado"
    )
    assert chamada_do_llm["args"] == {}
    corpo = abertura["json"]
    assert corpo["telefone"] == TELEFONE and corpo["conversation_id"] == CONVERSA
    assert corpo["proposta"] == {k: v for k, v in proposta.items() if k != "turno"}
    bruto = CONVERSA + proposta["titulo"] + proposta["categoria"] + proposta["criada_em"]
    assert corpo["chave_idempotencia"] == hashlib.sha256(bruto.encode()).hexdigest()
    assert out2["proposta_chamado"] is None  # consumida
    [resultado] = _tool_messages(out2)[-1:]
    assert '"numero": "48213"' in resultado.content
    assert out2["messages"][-1].content == "Pronto, Maria! Abri o chamado 48213."


def test_abrir_ignora_os_argumentos_que_o_modelo_inventar(monkeypatch):
    gw = _gateway(monkeypatch)
    forjado = _chamada(
        "abrir_chamado",
        "tc-abrir",
        proposta={"titulo": "Cobrança indevida", "categoria": "financeiro"},
        telefone="+5500000000000",
        conversation_id="conversa-de-outro",
    )
    llm = fake_llm(
        [_preparar(), AIMessage(content="Posso abrir?"), forjado, AIMessage(content="ok")]
    )
    grafo = _grafo(llm)
    out1 = _primeiro_turno(grafo)
    _turno(grafo, "pode abrir")
    [abertura] = gw.em("ticket/abrir")
    corpo = abertura["json"]
    assert corpo["telefone"] == TELEFONE and corpo["conversation_id"] == CONVERSA
    assert corpo["proposta"]["categoria"] == "sem_conexao"
    assert corpo["proposta"]["titulo"] == out1["proposta_chamado"]["titulo"]


def test_o_schema_enviado_ao_modelo_nao_tem_arg_de_sessao(monkeypatch):
    _gateway(monkeypatch)
    llm = fake_llm([AIMessage(content="Oi, Maria!")])
    _primeiro_turno(_grafo(llm), "oi")
    tools_ligadas = llm.bind_tools.call_args.args[0]
    assert {t.name for t in tools_ligadas} == {t.name for t in get_default_tools()}
    for tool in tools_ligadas:
        schema = convert_to_openai_tool(tool)["function"]["parameters"]
        assert not set(schema.get("properties", {})) & ARGS_DE_SESSAO, tool.name


# ---------------------------------------------------------------------------
# O que NÃO abre chamado
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "resposta_do_cliente",
    [
        "Esse",
        "esse mesmo",
        "48213",
        "não",
        "sim, mas muda a prioridade",
        "pode abrir? qual o prazo?",
    ],
)
def test_so_confirmacao_explicita_abre_esse_e_o_resto_nao(monkeypatch, resposta_do_cliente):
    gw = _gateway(monkeypatch)
    llm = fake_llm(
        [
            _preparar(),
            AIMessage(content="Posso abrir?"),
            _chamada("abrir_chamado", "tc-abrir"),
            AIMessage(content="Não consegui abrir ainda; confirma pra mim?"),
        ]
    )
    grafo = _grafo(llm)
    out1 = _primeiro_turno(grafo)
    out2 = _turno(grafo, resposta_do_cliente)

    assert gw.em("ticket/abrir") == []  # o gateway nunca foi chamado
    recusa = _tool_messages(out2)[-1]
    assert "recusado_pela_politica" in recusa.content and "confirmação explícita" in recusa.content
    assert out2["proposta_chamado"] == out1["proposta_chamado"]  # segue pendente, não consumida


def test_ausencia_de_confirmacao_na_mesma_rodada_do_preparo_nao_abre(monkeypatch):
    """O modelo encadeia preparar → abrir no MESMO turno, sem o cliente ver o resumo."""
    gw = _gateway(monkeypatch)
    llm = fake_llm(
        [
            _preparar(),
            _chamada("abrir_chamado", "tc-abrir"),
            AIMessage(content="Preparei o resumo. Posso abrir?"),
        ]
    )
    out = _primeiro_turno(_grafo(llm))
    assert [c["caminho"] for c in gw.chamadas] == ["ticket/preparar"]
    assert "NESTE turno" in _tool_messages(out)[-1].content
    assert out["proposta_chamado"]["turno"] == 1


def test_abrir_sem_nunca_ter_preparado_nao_chega_ao_gateway(monkeypatch):
    gw = _gateway(monkeypatch)
    llm = fake_llm(
        [_chamada("abrir_chamado", "tc-abrir"), AIMessage(content="Vou preparar o resumo antes.")]
    )
    out = _primeiro_turno(_grafo(llm), "sim")
    assert gw.chamadas == []
    assert "preparar_abertura_chamado" in _tool_messages(out)[-1].content


def test_conversa_que_seguiu_sem_confirmar_exige_novo_preparo(monkeypatch):
    gw = _gateway(monkeypatch)
    llm = fake_llm(
        [
            _preparar(),
            AIMessage(content="Posso abrir?"),
            AIMessage(content="O prazo quem define é a equipe."),  # turno 2: fala de outra coisa
            _chamada("abrir_chamado", "tc-abrir"),  # turno 3: "sim" tardio
            AIMessage(content="Vou preparar de novo."),
        ]
    )
    grafo = _grafo(llm)
    _primeiro_turno(grafo)
    _turno(grafo, "e qual o prazo?")
    out3 = _turno(grafo, "sim")
    assert gw.em("ticket/abrir") == []
    assert "SEGUINTE" in _tool_messages(out3)[-1].content


# ---------------------------------------------------------------------------
# Identificação: CPF inválido não chega ao gateway; válido grava só o mascarado
# ---------------------------------------------------------------------------


def test_cliente_sem_identificacao_nao_prepara_cpf_invalido_nao_vai_ao_gateway(monkeypatch):
    gw = _gateway(monkeypatch)
    llm = fake_llm(
        [
            _preparar("tc-1"),  # tenta preparar antes de se identificar
            _chamada("informar_identificacao", "tc-2", cpf="123.456.789-00"),  # DV errado
            AIMessage(content="Esse CPF não passou na conferência. Pode mandar de novo?"),
            _chamada("informar_identificacao", "tc-3", cpf="123.456.789-09"),
            _preparar("tc-4"),
            AIMessage(content="Posso abrir?"),
        ]
    )
    grafo = _grafo(llm)
    out1 = _turno(
        grafo,
        "minha internet caiu. meu cpf é 123.456.789-00",
        telefone=TELEFONE,
        conversation_id=CONVERSA,
        nome_contato="Maria",
        contato=None,
    )
    assert gw.chamadas == []  # nem o preparo nem o CPF inválido saíram do cérebro
    recusas = [m.content for m in _tool_messages(out1)]
    assert "informar_identificacao" in recusas[0]  # preparar → peça o CPF antes
    assert "CPF inválido" in recusas[1] and "12345678900" not in recusas[1]
    assert out1.get("identificacao") is None

    out2 = _turno(grafo, "123.456.789-09")
    assert [c["caminho"] for c in gw.chamadas] == ["contato/identificar", "ticket/preparar"]
    assert gw.chamadas[0]["json"]["cpf"] == "12345678909"  # em claro só rumo ao gateway
    assert out2["identificacao"] == {"forma": "cpf", "valor_mascarado": "***.***.789-**"}
    assert "12345678909" not in str(out2["identificacao"])
    # o preparo seguinte já carrega a identificação mascarada do estado
    assert gw.chamadas[1]["json"]["identificacao"] == out2["identificacao"]
    assert out2["proposta_chamado"] is not None


def test_cliente_nao_identificado_nao_ve_o_cpf_na_resposta_da_tool(monkeypatch):
    _gateway(monkeypatch)
    llm = fake_llm(
        [
            _chamada("informar_identificacao", "tc-1", cpf="12345678909"),
            AIMessage(content="Anotado, Maria!"),
        ]
    )
    out = _turno(
        _grafo(llm),
        "meu cpf é 12345678909",
        telefone=TELEFONE,
        conversation_id=CONVERSA,
        contato=None,
    )
    mensagem_da_tool = _tool_messages(out)[-1].content
    assert "12345678909" not in mensagem_da_tool
    assert out["identificacao"]["valor_mascarado"] == "***.***.789-**"


# ---------------------------------------------------------------------------
# Consulta e transferência pelo grafo
# ---------------------------------------------------------------------------


def test_consultar_status_usa_a_identificacao_e_o_telefone_da_sessao(monkeypatch):
    gw = _gateway(monkeypatch, **{"ticket/consultar": contrato_tools("ticket/consultar", "ok")})
    llm = fake_llm(
        [
            _chamada("consultar_status_chamado", "tc-1", numero="48213", telefone="+5500000000000"),
            AIMessage(content="O chamado 48213 está desconhecido por aqui."),
        ]
    )
    _primeiro_turno(_grafo(llm), "como está o chamado 48213?")
    [consulta] = gw.em("ticket/consultar")
    assert consulta["json"]["telefone"] == TELEFONE
    assert consulta["json"]["numero"] == "48213"
    assert consulta["json"]["identificacao"] == {"forma": "telefone", "valor_mascarado": "Maria S."}


def test_consultar_numero_invalido_nao_chega_ao_gateway(monkeypatch):
    gw = _gateway(monkeypatch)
    llm = fake_llm(
        [
            _chamada("consultar_status_chamado", "tc-1", numero="48213 OR 1=1"),
            AIMessage(content="Pode conferir o número pra mim?"),
        ]
    )
    out = _primeiro_turno(_grafo(llm), "como está o chamado 48213?")
    assert gw.chamadas == []
    assert "número de chamado inválido" in _tool_messages(out)[-1].content


def test_transferir_leva_a_categoria_da_classificacao_nao_a_do_modelo(monkeypatch):
    gw = _gateway(
        monkeypatch, **{"conversa/transferir": contrato_tools("conversa/transferir", "ok")}
    )
    llm = fake_llm(
        [
            _chamada(
                "transferir_para_atendente", "tc-1", motivo="pedido_cliente", categoria="financeiro"
            ),
            AIMessage(content="Vou te passar para um atendente."),
        ]
    )
    _primeiro_turno(_grafo(llm), "quero falar com uma pessoa")
    [transferencia] = gw.em("conversa/transferir")
    esperado = {**contrato_tools("conversa/transferir", "request"), "conversation_id": CONVERSA}
    assert transferencia["json"] == esperado
