"""Entry point Chainlit — harness de dev da Sofia (sem gateway, sem Infodesk).

Como rodar:

    chainlit run app.py -w

Pré-requisitos:
- `.env` com `OPENROUTER_API_KEY` real.
- `python -m src.rag.ingest` já executado (ChromaDB populado), se quiser RAG.

Fluxo:
1. `on_chat_start` pede o **telefone** e o **nome** do cliente — simula o que
   o gateway Go extrai do webhook do Infodesk (`data.contact`) e monta
   localmente o contato no formato do Contrato A (`montar_contato_dev`). Sem
   identificação em cache: o agente pede CPF/contrato na conversa, como em
   produção.
2. Cada mensagem é processada pelo grafo, com streaming de tokens do LLM
   via `astream_events(version="v2")`.
3. Comando `/trocar-telefone` reabre o pedido para simular outro cliente na
   mesma sessão.
4. As fontes consultadas (arquivo e seção do RAG) são exibidas após cada
   resposta — auditabilidade (LLM09). Em produção o cliente nunca as vê.
"""

from __future__ import annotations

import logging
import re
from typing import Any

import chainlit as cl
from langchain_core.messages import AIMessage, HumanMessage

from src.agent.graph import build_graph
from src.agent.prompts import NOME_ASSISTENTE

logger = logging.getLogger(__name__)


COMANDO_TROCAR_TELEFONE = "/trocar-telefone"


# ---------------------------------------------------------------------------
# Singleton do grafo — compilado uma vez por processo. O isolamento entre
# conversas é feito pelo `thread_id` (= telefone) no `MemorySaver`.
# ---------------------------------------------------------------------------

_GRAPH: Any = None


def _get_graph():
    global _GRAPH
    if _GRAPH is None:
        _GRAPH = build_graph()
    return _GRAPH


# ---------------------------------------------------------------------------
# Helpers puros (testáveis sem chainlit)
# ---------------------------------------------------------------------------


def normalizar_telefone(telefone: Any) -> str:
    """Só dígitos (aceita máscara `(31) 99999-0001`); vazio se não houver."""
    return re.sub(r"\D", "", str(telefone or ""))


def montar_contato_dev(telefone: Any, nome: Any) -> dict[str, Any]:
    """O que o gateway mandaria no Contrato A para este cliente (sem cache de
    identificação — `contato` é `None` e o agente pede CPF/contrato).

    Devolve `{"telefone", "nome_contato", "contato"}` prontos para o estado.
    """
    return {
        "telefone": normalizar_telefone(telefone),
        "nome_contato": (str(nome or "").strip() or None),
        "contato": None,
    }


def saudacao_dev(nome_contato: Any) -> str:
    """Mensagem de abertura do harness (NÃO é a saudação da Sofia — essa vem do
    grafo). Só confirma quem está simulado na sessão."""
    nome = str(nome_contato or "").strip() or "cliente"
    return (
        f"Sessão de dev: você é **{nome}**, cliente final falando com a "
        f"{NOME_ASSISTENTE} pelo WhatsApp. Sem identificação em cache — ela vai "
        f"pedir CPF ou contrato quando precisar. Use `{COMANDO_TROCAR_TELEFONE}` "
        "para simular outro cliente."
    )


def extrair_texto_resposta_ask(res: Any) -> str:
    """Lê o texto digitado em resposta a `cl.AskUserMessage`.

    Aceita `dict` (formato recente do Chainlit) ou objeto com `.output`/`.content`.
    """
    if res is None:
        return ""
    if isinstance(res, dict):
        valor = res.get("output") or res.get("content") or ""
    else:
        valor = getattr(res, "output", None) or getattr(res, "content", None) or ""
    return (valor or "").strip()


def formatar_fontes(fontes: Any) -> str | None:
    """Monta o markdown de citação. URLs viram links, restantes ficam em backticks."""
    if not fontes:
        return None
    rotulos: list[str] = []
    for f in fontes:
        if isinstance(f, str) and f.lower().startswith(("http://", "https://")):
            rotulos.append(f"🌐 [{f}]({f})")
        else:
            rotulos.append(f"📄 `{f}`")
    return "**Fontes consultadas:** " + " · ".join(rotulos)


# ---------------------------------------------------------------------------
# Pedido de telefone + nome
# ---------------------------------------------------------------------------


async def _perguntar(texto: str) -> str:
    res = await cl.AskUserMessage(content=texto, timeout=300).send()
    return extrair_texto_resposta_ask(res)


async def _pedir_telefone() -> str:
    return await _perguntar(
        "Informe o **telefone** do cliente (simula o `data.contact.phone` do "
        "webhook do Infodesk). Ex.: `31999990001` ou `(31) 99999-0001`."
    )


async def _pedir_nome() -> str:
    return await _perguntar(
        "Informe o **nome** do cliente como aparece no WhatsApp (simula o "
        "`data.contact.name`). Ex.: `Maria`."
    )


async def _iniciar_sessao(telefone: str) -> None:
    nome = await _pedir_nome()
    dados = montar_contato_dev(telefone, nome)
    cl.user_session.set("telefone", dados["telefone"])
    cl.user_session.set("nome_contato", dados["nome_contato"])
    cl.user_session.set("contato", dados["contato"])
    await cl.Message(content=saudacao_dev(dados["nome_contato"])).send()


# ---------------------------------------------------------------------------
# Handlers Chainlit
# ---------------------------------------------------------------------------


@cl.on_chat_start
async def on_chat_start():
    telefone = normalizar_telefone(await _pedir_telefone())
    if not telefone:
        await cl.Message(
            content=(
                "Telefone não informado. Envie qualquer mensagem para tentar "
                f"novamente ou use `{COMANDO_TROCAR_TELEFONE} <numero>`."
            ),
        ).send()
        return
    await _iniciar_sessao(telefone)


@cl.on_message
async def on_message(message: cl.Message):
    texto = (message.content or "").strip()

    # /trocar-telefone [<numero>]
    if texto.lower().startswith(COMANDO_TROCAR_TELEFONE.lower()):
        resto = texto[len(COMANDO_TROCAR_TELEFONE) :].strip()
        novo = normalizar_telefone(resto or await _pedir_telefone())
        if not novo:
            await cl.Message(content="Nenhum telefone informado.").send()
            return
        await _iniciar_sessao(novo)
        return

    telefone = cl.user_session.get("telefone")
    if not telefone:
        await cl.Message(
            content=(
                "Sessão sem telefone identificado. "
                f"Use `{COMANDO_TROCAR_TELEFONE} <numero>` para iniciar."
            ),
        ).send()
        return

    config = {"configurable": {"thread_id": telefone}}
    inputs = {
        "telefone": telefone,
        "conversation_id": telefone,
        "nome_contato": cl.user_session.get("nome_contato"),
        "contato": cl.user_session.get("contato"),
        "messages": [HumanMessage(content=texto)],
    }

    msg = cl.Message(content="")
    streamed = False
    try:
        async for event in _get_graph().astream_events(inputs, config=config, version="v2"):
            if event.get("event") == "on_chat_model_stream":
                chunk = event.get("data", {}).get("chunk")
                token = getattr(chunk, "content", "") if chunk is not None else ""
                if token:
                    streamed = True
                    await msg.stream_token(token)
    except Exception as exc:
        logger.exception("falha_no_grafo")
        await cl.Message(content=f"Erro ao processar a mensagem: {exc}").send()
        return

    estado = await _get_graph().aget_state(config)
    valores = getattr(estado, "values", None) or {}

    # Caminho safe_response (ou LLM sem stream): pega o último AIMessage do estado
    if not streamed:
        ultima_ai: AIMessage | None = None
        for m in reversed(valores.get("messages") or []):
            if isinstance(m, AIMessage):
                ultima_ai = m
                break
        if ultima_ai is not None:
            msg.content = ultima_ai.content or msg.content

    await msg.send()

    fontes_md = formatar_fontes(valores.get("fontes_usadas") or [])
    if fontes_md:
        await cl.Message(content=fontes_md).send()
