"""A máscara de PII não pode estragar a proposta de chamado (E4.5b, B1).

`mesa.nome`, `departamento.nome` e `prioridade.nome` são dados do gateway, não PII: o
`abrir` injeta a proposta do estado e o gateway lê dela a prioridade. Também cobre o
cadastro em vários turnos: o modelo precisa reler o que o cliente já deu.
"""

from __future__ import annotations

import asyncio

import pytest
from langchain_core.messages import AIMessage, HumanMessage

from server import ChatRequest, processar_chat
from src.agent.graph import build_graph, get_default_tools
from src.agent.pii import limpar_cofre, mascarar_args, mascarar_estrutura_de_texto
from tests.fakes import (
    FakeJEV,
    contrato_tools,
    fake_llm,
    instalar_gateway_fake,
    resp_jev,
    sem_rag,
    servico_fake,
)

CONVERSA = "conv-pii-proposta"
TELEFONE = "+5531999990001"
CONTATO = {"forma": "telefone", "valor_mascarado": "Maria S.", "cliente_id": "c-1"}


@pytest.fixture(autouse=True)
def _cofre_limpo():
    limpar_cofre()
    yield
    limpar_cofre()


def _chamada(tool: str, id_: str, **args) -> AIMessage:
    return AIMessage(content="", tool_calls=[{"id": id_, "name": tool, "args": args}])


def test_nome_so_e_pii_no_topo_dos_args_nao_dentro_de_estruturas():
    proposta = contrato_tools("ticket/preparar", "ok")["proposta"]
    assert mascarar_estrutura_de_texto(proposta) == proposta  # intacta
    assert mascarar_args({"nome": "Maria Souza", "empresa": "ACME", "email": "m@a.com"}) == {
        "nome": "<NOME>",
        "empresa": "<EMPRESA>",
        "email": "<EMAIL>",
    }
    # dentro de um arg (ex.: dicionário aninhado) o "nome" não é tocado
    assert mascarar_args({"x": {"nome": "Suporte"}}) == {"x": {"nome": "Suporte"}}


def test_a_prioridade_da_proposta_chega_ao_abrir_do_gateway(monkeypatch):
    preparo = contrato_tools("ticket/preparar", "ok")
    gw = instalar_gateway_fake(
        monkeypatch,
        {
            "ticket/preparar": preparo,
            "ticket/abrir": contrato_tools("ticket/abrir", "ok"),
        },
    )
    llm = fake_llm(
        [
            _chamada(
                "preparar_abertura_chamado",
                "tc-1",
                titulo="Internet sem conexão",
                descricao="Cliente sem conexão desde ontem.",
            ),
            AIMessage(content="Vou registrar assim. Posso abrir?"),
            _chamada("abrir_chamado", "tc-2"),
            AIMessage(content="Pronto, aberto."),
        ]
    )
    grafo = build_graph(
        llm=llm,
        tools=get_default_tools(),
        buscar_chunks=sem_rag,
        classificador=servico_fake(FakeJEV(padrao=resp_jev())),
    )

    def pedido(texto: str) -> ChatRequest:
        return ChatRequest(
            conversation_id=CONVERSA, mensagem=texto, telefone=TELEFONE, contato=CONTATO
        )

    r1 = asyncio.run(processar_chat(grafo, pedido("minha internet caiu ontem, abre um chamado")))
    estado = grafo.get_state({"configurable": {"thread_id": CONVERSA}}).values
    proposta = estado["proposta_chamado"]
    esperada = preparo["proposta"]
    assert proposta["prioridade"]["nome"] == esperada["prioridade"]["nome"]
    assert proposta["mesa"] == esperada["mesa"]
    assert proposta["departamento"] == esperada["departamento"]
    assert "<NOME>" not in str(proposta)

    r2 = asyncio.run(processar_chat(grafo, pedido("sim")))
    assert r1.erro is None and r2.erro is None
    enviada = gw.em("ticket/abrir")[0]["json"]["proposta"]
    assert enviada["prioridade"]["nome"] == esperada["prioridade"]["nome"]
    assert enviada["mesa"]["nome"] == esperada["mesa"]["nome"]


def test_cadastro_em_varios_turnos_o_modelo_ainda_le_o_que_o_cliente_ja_deu(monkeypatch, tmp_path):
    """Um dado por mensagem (como manda o prompt) e nome do WhatsApp coincidente: o
    checkpoint guarda placeholders, mas o histórico que o modelo recebe (cofre em
    memória, TTL) segue legível — senão o preparo seria recusado por `<NOME> Souza`."""
    pytest.importorskip("langgraph.checkpoint.sqlite.aio")
    from langgraph.checkpoint.sqlite.aio import AsyncSqliteSaver

    instalar_gateway_fake(
        monkeypatch, {"cadastro/preparar": contrato_tools("cadastro/preparar", "ok")}
    )
    llm = fake_llm(
        [
            AIMessage(content="Qual o seu nome completo?"),
            AIMessage(content="E a empresa?"),
            AIMessage(content="E o seu e-mail?"),
            _chamada(
                "preparar_cadastro_solicitante",
                "tc-1",
                nome="Maria Souza",
                empresa="ACME Ltda",
                email="maria@acme.com.br",
            ),
            AIMessage(content="Posso cadastrar?"),
        ]
    )
    servico = servico_fake(FakeJEV(padrao=resp_jev()))

    async def rodar():
        async with AsyncSqliteSaver.from_conn_string(str(tmp_path / "c.db")) as saver:
            grafo = build_graph(
                llm=llm,
                tools=get_default_tools(),
                checkpointer=saver,
                buscar_chunks=sem_rag,
                classificador=servico,
            )
            for fala in ("quero me cadastrar", "Maria Souza", "ACME Ltda", "maria@acme.com.br"):
                r = await processar_chat(
                    grafo,
                    ChatRequest(
                        conversation_id=CONVERSA,
                        mensagem=fala,
                        telefone=TELEFONE,
                        nome_contato="Maria",
                    ),
                )
                assert r.erro is None

    asyncio.run(rodar())

    # na chamada do preparo o histórico tem os dados LEGÍVEIS dos turnos anteriores
    historico = " ".join(
        str(m.content) for m in llm.invoke.call_args_list[3].args[0] if isinstance(m, HumanMessage)
    )
    assert "Maria Souza" in historico and "maria@acme.com.br" in historico
    assert "<NOME>" not in historico and "<EMAIL>" not in historico
    # e o preparo foi aceito pela política (nada recusado)
    resultados = [
        m
        for m in llm.invoke.call_args_list[4].args[0]
        if getattr(m, "name", "") == "preparar_cadastro_solicitante"
    ]
    assert resultados and "recusado_pela_politica" not in str(resultados[0].content)
    # no disco, só placeholders
    bruto = b"".join(p.read_bytes() for p in tmp_path.iterdir() if p.is_file())
    for proibido in (b"maria@acme.com.br", b"999990001"):
        assert proibido not in bruto
