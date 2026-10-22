"""PII do contato fora do checkpoint em DISCO (E4.5b, I1/I3).

Fluxo real de cadastro (preparar → "sim" → cadastrar) por `processar_chat`, com o
checkpointer SQLite de produção em arquivo temporário e o estado completo: nada de
telefone, nome do WhatsApp, nome do cadastro, nome/e-mail digitados pelo cliente no
disco — e, mesmo assim, as tools falam com o gateway com os valores reais e o JEV
nunca recebe o nome. Sem rede.
"""

from __future__ import annotations

import asyncio

import pytest
from langchain_core.messages import AIMessage

from server import ChatRequest, processar_chat
from src.agent.graph import build_graph, get_default_tools
from src.agent.pii import limpar_cofre
from tests.fakes import (
    FakeJEV,
    contrato_tools,
    fake_llm,
    instalar_gateway_fake,
    resp_jev,
    sem_rag,
    servico_fake,
)

CONVERSA = "conv-pii-cadastro"
TELEFONE = "+5531999990001"
NOME_WHATSAPP = "Zeca Bonfim"
ARGS = {"nome": "Maria Souza", "empresa": "ACME Ltda", "email": "maria@acme.com.br"}
FALA = "Oi! Sou Maria Souza, da ACME Ltda, meu e-mail é maria@acme.com.br"
PROIBIDOS_NO_DISCO = ("5531999990001", "999990001", "Zeca", "Bonfim", "maria@acme.com.br")


def _chamada(tool: str, id_: str, **args) -> AIMessage:
    return AIMessage(content="", tool_calls=[{"id": id_, "name": tool, "args": args}])


@pytest.fixture(autouse=True)
def _cofre_limpo():
    limpar_cofre()
    yield
    limpar_cofre()


def test_cadastro_completo_nao_deixa_pii_do_contato_no_sqlite(monkeypatch, tmp_path):
    pytest.importorskip("langgraph.checkpoint.sqlite.aio")
    from langgraph.checkpoint.sqlite.aio import AsyncSqliteSaver

    gw = instalar_gateway_fake(
        monkeypatch,
        {
            "cadastro/preparar": contrato_tools("cadastro/preparar", "ok"),
            "cadastro/cadastrar": contrato_tools("cadastro/cadastrar", "ok"),
        },
    )
    llm = fake_llm(
        [
            _chamada("preparar_cadastro_solicitante", "tc-1", **ARGS),
            AIMessage(content="Posso cadastrar você com esses dados?"),
            _chamada("cadastrar_solicitante", "tc-2"),
            AIMessage(content="Cadastro feito! Me conta o que está acontecendo."),
        ]
    )
    jev = FakeJEV(padrao=resp_jev())
    servico = servico_fake(jev)
    banco = tmp_path / "checkpoints.db"

    def pedido(texto: str) -> ChatRequest:
        return ChatRequest(
            conversation_id=CONVERSA,
            mensagem=texto,
            telefone=TELEFONE,
            nome_contato=NOME_WHATSAPP,
            contato=None,
        )

    async def rodar():
        async with AsyncSqliteSaver.from_conn_string(str(banco)) as saver:
            grafo = build_graph(
                llm=llm,
                tools=get_default_tools(),
                checkpointer=saver,
                buscar_chunks=sem_rag,
                classificador=servico,
            )
            r1 = await processar_chat(grafo, pedido(FALA))
            r2 = await processar_chat(grafo, pedido("sim"))
            estado = (await grafo.aget_state({"configurable": {"thread_id": CONVERSA}})).values
            return r1, r2, estado

    r1, r2, estado = asyncio.run(rodar())

    # As tools falaram com o gateway com os valores REAIS (vêm do config, em memória).
    preparo = gw.em("cadastro/preparar")[0]["json"]
    assert preparo["telefone"] == TELEFONE
    assert (preparo["nome"], preparo["email"]) == (ARGS["nome"], ARGS["email"])
    assert gw.em("cadastro/cadastrar")[0]["json"]["telefone"] == TELEFONE
    assert r1.erro is None and r2.erro is None

    # O estado persistido não carrega telefone nem nomes.
    assert not estado.get("telefone") and not estado.get("nome_contato")
    assert "nome_cadastro" not in (estado.get("contato") or {})

    # Nada em disco: banco e arquivos vizinhos (WAL), em UTF-8 e em UTF-16 do msgpack/json.
    bruto = b"".join(p.read_bytes() for p in tmp_path.iterdir() if p.is_file())
    for proibido in PROIBIDOS_NO_DISCO:
        assert proibido.encode() not in bruto, f"{proibido!r} gravado em disco"

    # O nome digitado (args da tool) e o texto do cliente saem do estado corrente.
    for m in estado["messages"]:
        texto = str(m.content) + str(getattr(m, "tool_calls", ""))
        assert "Maria Souza" not in texto and "maria@acme.com.br" not in texto

    # I3: o JEV (e portanto o fallback LLM e o Langfuse) não recebe o nome — nem o do
    # WhatsApp nem o que o cliente digitou — a partir do 2º turno.
    for estado_jev, _ in jev.chamadas:  # nome do WhatsApp e e-mail: sempre mascarados
        for dado in ("Zeca", "Bonfim", "maria@acme.com.br"):
            assert dado not in estado_jev
    # (o 1º turno ainda não conhece o nome digitado: ele só aparece na chamada ao preparar)
    assert len(jev.chamadas) >= 2
    for estado_jev, _ in jev.chamadas[1:]:
        for nome in ("Zeca", "Bonfim", "Souza", "maria@acme.com.br"):
            assert nome not in estado_jev, f"{nome!r} chegou ao JEV: {estado_jev!r}"


def test_o_prompt_do_turno_ainda_usa_o_nome_do_whatsapp(monkeypatch):
    """O nome sai do disco, não da conversa: o system prompt do turno o recebe via config."""
    instalar_gateway_fake(monkeypatch, {})
    llm = fake_llm([AIMessage(content="Oi, Zeca! Como posso ajudar?")])
    grafo = build_graph(
        llm=llm,
        tools=get_default_tools(),
        buscar_chunks=sem_rag,
        classificador=servico_fake(FakeJEV(padrao=resp_jev())),
    )
    req = ChatRequest(
        conversation_id="conv-nome-prompt",
        mensagem="oi",
        telefone=TELEFONE,
        nome_contato=NOME_WHATSAPP,
    )
    resp = asyncio.run(processar_chat(grafo, req))
    assert resp.erro is None
    system = llm.invoke.call_args_list[0].args[0][0]
    assert "Zeca Bonfim" in str(system.content)


@pytest.mark.xfail(
    strict=True,
    reason=(
        "o nome que o cliente digita em texto livre só é conhecido quando o modelo chama o "
        "preparar_cadastro: a versão corrente do checkpoint sai limpa, mas as versões do "
        "MESMO turno (SQLite guarda todas) ainda têm o texto original. Fecha com retenção "
        "(podar versões antigas do thread) — follow-up."
    ),
)
def test_nome_digitado_em_texto_livre_nao_fica_nas_versoes_antigas_do_checkpoint(
    monkeypatch, tmp_path
):
    pytest.importorskip("langgraph.checkpoint.sqlite.aio")
    from langgraph.checkpoint.sqlite.aio import AsyncSqliteSaver

    instalar_gateway_fake(
        monkeypatch, {"cadastro/preparar": contrato_tools("cadastro/preparar", "ok")}
    )
    llm = fake_llm(
        [
            _chamada("preparar_cadastro_solicitante", "tc-1", **ARGS),
            AIMessage(content="Posso cadastrar você com esses dados?"),
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
            await processar_chat(
                grafo,
                ChatRequest(conversation_id=CONVERSA, mensagem=FALA, telefone=TELEFONE),
            )

    asyncio.run(rodar())
    bruto = b"".join(p.read_bytes() for p in tmp_path.iterdir() if p.is_file())
    assert b"Maria Souza" not in bruto
