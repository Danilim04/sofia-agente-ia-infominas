"""CPF/contrato fora do histórico do grafo (E3.7; regra 5 do CLAUDE.md).

Um turno real de `informar_identificacao` (grafo + LLM scriptado + gateway
falso) com `MemorySaver`: nada em claro no checkpoint (nenhuma versão, nenhuma
escrita pendente), nos args gravados na mensagem nem nos logs — mas o LLM do
turno LÊ o CPF e a tool o recebe completo. Sem rede.
"""

from __future__ import annotations

import logging

import pytest
from langchain_core.messages import AIMessage, HumanMessage
from langgraph.checkpoint.memory import MemorySaver

from src.agent import pii
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

CONVERSA = "conv-pii"
TELEFONE = "+5531999990001"
CPF_DIGITOS = "12345678909"
CPF_PONTUADO = "123.456.789-09"
CPF_MASCARADO = "***.***.789-**"
PROIBIDOS = (CPF_DIGITOS, CPF_PONTUADO, "123.456.789", "456.789-09")


@pytest.fixture(autouse=True)
def _cofre_limpo():
    pii.limpar_cofre()
    yield
    pii.limpar_cofre()


def _chamada(nome: str, id_: str, **args) -> AIMessage:
    return AIMessage(content="", tool_calls=[{"id": id_, "name": nome, "args": args}])


def _grafo(llm, saver):
    servico = servico_fake(FakeJEV(padrao=resp_jev()))
    return build_graph(
        llm=llm,
        tools=get_default_tools(),
        checkpointer=saver,
        buscar_chunks=sem_rag,
        classificador=servico,
    )


def _turno(grafo, texto: str):
    return grafo.invoke(
        {
            "messages": [HumanMessage(content=texto)],
            "telefone": TELEFONE,
            "conversation_id": CONVERSA,
            "contato": None,
        },
        config={"configurable": {"thread_id": CONVERSA}},
    )


def _tudo_como_texto(objeto, saida: list[str] | None = None) -> str:
    """Achata qualquer estrutura (bytes do checkpoint incluídos) numa string."""
    saida = [] if saida is None else saida
    if isinstance(objeto, (bytes, bytearray)):
        saida.append(bytes(objeto).decode("latin-1"))
    elif isinstance(objeto, dict):
        for k, v in objeto.items():
            _tudo_como_texto(k, saida)
            _tudo_como_texto(v, saida)
    elif isinstance(objeto, (list, tuple, set)):
        for item in objeto:
            _tudo_como_texto(item, saida)
    else:
        saida.append(str(objeto))
    return "\n".join(saida)


def _conteudo_do_saver(saver: MemorySaver) -> str:
    """Tudo o que o checkpointer guardou: checkpoints, blobs de canal e writes."""
    return _tudo_como_texto([saver.storage, saver.blobs, saver.writes])


@pytest.mark.parametrize("cpf_digitado", [CPF_DIGITOS, CPF_PONTUADO])
def test_turno_de_identificacao_nao_deixa_cpf_no_checkpoint_nem_nos_logs(
    monkeypatch, caplog, cpf_digitado
):
    gw = instalar_gateway_fake(
        monkeypatch, {"contato/identificar": contrato_tools("contato/identificar", "ok")}
    )
    llm = fake_llm(
        [
            _chamada("informar_identificacao", "tc-1", cpf=cpf_digitado),
            AIMessage(content="Anotado, Maria! Posso seguir com o seu chamado."),
        ]
    )
    saver = MemorySaver()
    grafo = _grafo(llm, saver)

    caplog.set_level(logging.DEBUG)
    caplog.set_level(logging.DEBUG, logger="src.agent.nodes")
    out = _turno(grafo, f"meu cpf é {cpf_digitado}")

    # 1. A tool recebeu o valor necessário (em claro, só em memória).
    [identificacao] = gw.em("contato/identificar")
    assert identificacao["json"]["cpf"] == CPF_DIGITOS
    assert identificacao["json"]["telefone"] == TELEFONE

    # 2. O LLM do turno LEU o CPF que o cliente digitou (senão não teria como repassar).
    primeira_chamada = llm.invoke.call_args_list[0].args[0]
    humana = next(m for m in primeira_chamada if isinstance(m, HumanMessage))
    assert cpf_digitado in humana.content

    # 3. Nada em claro no checkpoint: versões, blobs de canal e writes pendentes.
    guardado = _conteudo_do_saver(saver)
    for proibido in PROIBIDOS:
        assert proibido not in guardado, f"{proibido!r} vazou no checkpoint"
    assert CPF_MASCARADO in guardado  # a forma mascarada é o que fica

    # 4. Nem no estado devolvido: texto da mensagem do cliente e args gravados.
    for m in out["messages"]:
        texto = str(m.content) + str(getattr(m, "tool_calls", "")) + str(m.additional_kwargs)
        for proibido in PROIBIDOS:
            assert proibido not in texto
    chamada_gravada = next(
        tc for m in out["messages"] if isinstance(m, AIMessage) for tc in m.tool_calls
    )
    assert chamada_gravada["args"] == {"cpf": CPF_MASCARADO}
    assert out["identificacao"] == {"forma": "cpf", "valor_mascarado": CPF_MASCARADO}

    # 5. Nem nos logs (DEBUG incluído: `tool_exec_args`).
    for registro in caplog.records:
        mensagem = registro.getMessage()
        for proibido in PROIBIDOS:
            assert proibido not in mensagem, f"{proibido!r} vazou no log: {mensagem}"
    assert any("tool_exec_args" in r.getMessage() for r in caplog.records)

    # 6. Fim do turno: o cofre em memória foi esvaziado.
    assert pii.tamanho_do_cofre() == 0


def test_cpf_dito_no_turno_seguinte_tambem_nao_fica_no_historico(monkeypatch):
    """O segundo turno relê o checkpoint: o histórico que o LLM recebe já está mascarado."""
    instalar_gateway_fake(
        monkeypatch, {"contato/identificar": contrato_tools("contato/identificar", "ok")}
    )
    llm = fake_llm(
        [
            _chamada("informar_identificacao", "tc-1", cpf=CPF_PONTUADO),
            AIMessage(content="Anotado!"),
            AIMessage(content="Claro, como posso ajudar?"),
        ]
    )
    saver = MemorySaver()
    grafo = _grafo(llm, saver)
    _turno(grafo, f"meu cpf é {CPF_PONTUADO}")
    _turno(grafo, "obrigado")

    ultima_chamada = llm.invoke.call_args_list[-1].args[0]
    historico = " ".join(str(m.content) + str(getattr(m, "tool_calls", "")) for m in ultima_chamada)
    assert CPF_PONTUADO not in historico and CPF_DIGITOS not in historico
    assert CPF_MASCARADO in historico


def test_contrato_nos_args_da_tool_tambem_e_mascarado(monkeypatch):
    gw = instalar_gateway_fake(
        monkeypatch, {"contato/identificar": contrato_tools("contato/identificar", "ok")}
    )
    llm = fake_llm(
        [
            _chamada("informar_identificacao", "tc-1", contrato="000123456"),
            AIMessage(content="Anotado!"),
        ]
    )
    saver = MemorySaver()
    out = _turno(_grafo(llm, saver), "segue o meu contrato: 000-123-456")

    [identificacao] = gw.em("contato/identificar")
    assert identificacao["json"]["contrato"] == "000123456"
    assert "000123456" not in _conteudo_do_saver(saver)
    chamada = next(tc for m in out["messages"] if isinstance(m, AIMessage) for tc in m.tool_calls)
    assert chamada["args"] == {"contrato": "******456"}


# ---------------------------------------------------------------------------
# Unidade: a máscara e o cofre
# ---------------------------------------------------------------------------


def test_mascara_de_texto_cobre_pontuado_e_so_digitos_e_deixa_o_resto():
    assert pii.mascarar_cpfs_no_texto("cpf 123.456.789-09 e 98765432100") == (
        "cpf ***.***.789-** e ***.***.321-**"
    )
    # protocolo curto, telefone com DDI+DDD (13 dígitos) e número maior não são CPF
    texto = "chamado 48213, tel +5531999990001, serial 123456789012"
    assert pii.mascarar_cpfs_no_texto(texto) == texto


def test_proteger_e_idempotente_e_reidratar_devolve_o_original():
    original = HumanMessage(content=f"meu cpf é {CPF_PONTUADO}", id="h-1")
    protegida = pii.proteger_mensagem(original)
    assert protegida.content == f"meu cpf é {CPF_MASCARADO}"
    assert original.content.endswith(CPF_PONTUADO)  # o objeto de entrada não é mutado
    assert pii.proteger_mensagem(protegida) is protegida
    [de_volta] = pii.reidratar([protegida])
    assert de_volta.content == original.content
    pii.descartar([protegida])
    [sem_cofre] = pii.reidratar([protegida])
    assert sem_cofre.content == protegida.content


def test_proteger_mascara_o_espelho_cru_do_provedor():
    msg = AIMessage(
        content="",
        id="a-1",
        tool_calls=[{"id": "t-1", "name": "informar_identificacao", "args": {"cpf": CPF_DIGITOS}}],
        additional_kwargs={
            "tool_calls": [
                {
                    "id": "t-1",
                    "type": "function",
                    "function": {
                        "name": "informar_identificacao",
                        "arguments": f'{{"cpf": "{CPF_DIGITOS}"}}',
                    },
                }
            ]
        },
    )
    protegida = pii.proteger_mensagem(msg)
    assert CPF_DIGITOS not in str(protegida.tool_calls) + str(protegida.additional_kwargs)
    assert CPF_MASCARADO in str(protegida.additional_kwargs)


def test_cofre_expira_sozinho(monkeypatch):
    relogio = {"t": 1000.0}
    monkeypatch.setattr(pii.time, "monotonic", lambda: relogio["t"])
    protegida = pii.proteger_mensagem(HumanMessage(content=f"cpf {CPF_DIGITOS}", id="h-ttl"))
    assert pii.reidratar([protegida])[0].content == f"cpf {CPF_DIGITOS}"
    relogio["t"] += pii.COFRE_TTL_S + 1
    assert pii.reidratar([protegida])[0].content == protegida.content
    assert pii.tamanho_do_cofre() == 0


def test_checkpointer_sqlite_assincrono_nao_grava_cpf_em_disco(monkeypatch, tmp_path):
    """O caminho de produção: `AsyncSqliteSaver` + `ainvoke` + `aupdate_state`."""
    pytest.importorskip("langgraph.checkpoint.sqlite.aio")
    import asyncio

    from langgraph.checkpoint.sqlite.aio import AsyncSqliteSaver

    gw = instalar_gateway_fake(
        monkeypatch, {"contato/identificar": contrato_tools("contato/identificar", "ok")}
    )
    llm = fake_llm(
        [
            _chamada("informar_identificacao", "tc-1", cpf=CPF_PONTUADO),
            AIMessage(content="Anotado!"),
        ]
    )
    banco = tmp_path / "checkpoints.db"

    async def rodar():
        async with AsyncSqliteSaver.from_conn_string(str(banco)) as saver:
            grafo = _grafo(llm, saver)
            config = {"configurable": {"thread_id": CONVERSA}}
            await grafo.ainvoke(
                {
                    "messages": [HumanMessage(content=f"meu cpf é {CPF_PONTUADO}")],
                    "telefone": TELEFONE,
                    "conversation_id": CONVERSA,
                },
                config=config,
            )
            # fim do episódio (server.encerrar_episodio): atualização de estado pelo grafo
            await grafo.aupdate_state(config, {"identificacao": None}, as_node="output_guardrail")
            return (await grafo.aget_state(config)).values

    valores = asyncio.run(rodar())

    assert gw.em("contato/identificar")[0]["json"]["cpf"] == CPF_DIGITOS
    assert valores["identificacao"] is None
    # o WAL pode ter páginas ainda não consolidadas: varre o banco e os arquivos vizinhos
    bruto = b"".join(p.read_bytes() for p in tmp_path.iterdir() if p.is_file())
    for proibido in PROIBIDOS:
        assert proibido.encode() not in bruto, f"{proibido!r} gravado em disco"
    assert CPF_MASCARADO.encode() in bruto
