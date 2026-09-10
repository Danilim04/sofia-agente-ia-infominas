"""Entry point Chainlit (Épico 8) — UI do agente de suporte Azapfy.

Como rodar:

    chainlit run app.py -w

Pré-requisitos:
- `.env` com `OPENROUTER_API_KEY` real.
- `python -m src.rag.ingest` já executado (ChromaDB populado).

Fluxo:
1. `on_chat_start` pede o **telefone** do usuário (simula a injeção do
   header de telefone que existiria na integração real). Resolve a
   **identidade** via mock de dev (`resolver_identidade_dev`, no formato do
   Contrato A que o gate Go entregará) e exibe saudação.
2. Cada mensagem é processada pelo grafo (Épico 7), com streaming de
   tokens do LLM via `astream_events(version="v2")`.
3. Comando `/trocar-telefone` reabre o pedido para simular outra
   identidade na mesma sessão.
4. As fontes consultadas (arquivo e seção das docs do RAG) são
   exibidas após cada resposta — auditabilidade (LLM09).
"""

from __future__ import annotations

import logging
from typing import Any, Optional

import chainlit as cl
from langchain_core.messages import AIMessage, HumanMessage

from src.agent.graph import build_graph
from src.tools.identidade_mock import resolver_identidade_dev


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


def saudacao_para_identidade(identidade: Any) -> str:
    """Mensagem de boas-vindas a partir do perfil mínimo (formato do Contrato A)."""
    if not isinstance(identidade, dict):
        return "Olá! Como posso ajudar com o suporte técnico Azapfy?"

    if identidade.get("encontrado"):
        nome = identidade.get("nome") or "usuário"
        empresas = identidade.get("empresas") or []
        nomes_emp = ", ".join(e.get("grupo_empresa", "—") for e in empresas) or "—"
        return (
            f"Olá, **{nome}**! Identifiquei seu acesso "
            f"(empresas: **{nomes_emp}**). "
            "Como posso ajudar com o suporte técnico Azapfy hoje?"
        )

    return (
        "Não consegui identificar seu acesso por esse telefone. Posso responder "
        "dúvidas gerais sobre o suporte Azapfy, mas consultas à sua conta "
        "precisam de identificação. Informe seu **login** ou use "
        f"`{COMANDO_TROCAR_TELEFONE}` para tentar outro número."
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
        valor = (
            getattr(res, "output", None)
            or getattr(res, "content", None)
            or ""
        )
    return (valor or "").strip()


def formatar_fontes(fontes: Any) -> Optional[str]:
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
# Pedido de telefone + identificação
# ---------------------------------------------------------------------------


async def _pedir_telefone() -> str:
    res = await cl.AskUserMessage(
        content=(
            "Informe o **telefone** do cliente para identificação "
            "(simula a injeção do header de telefone na integração real). "
            "Ex.: `11999990001` ou `(11) 99999-0001`."
        ),
        timeout=300,
    ).send()
    return extrair_texto_resposta_ask(res)


async def _identificar_e_saudar(telefone: str) -> dict:
    identidade = resolver_identidade_dev(telefone)
    cl.user_session.set("telefone", telefone)
    cl.user_session.set("identidade", identidade)
    await cl.Message(content=saudacao_para_identidade(identidade)).send()
    return identidade


# ---------------------------------------------------------------------------
# Handlers Chainlit
# ---------------------------------------------------------------------------


@cl.on_chat_start
async def on_chat_start():
    telefone = await _pedir_telefone()
    if not telefone:
        await cl.Message(
            content=(
                "Telefone não informado. Envie qualquer mensagem para tentar "
                f"novamente ou use `{COMANDO_TROCAR_TELEFONE} <numero>`."
            ),
        ).send()
        return
    await _identificar_e_saudar(telefone)


@cl.on_message
async def on_message(message: cl.Message):
    texto = (message.content or "").strip()

    # /trocar-telefone [<numero>]
    if texto.lower().startswith(COMANDO_TROCAR_TELEFONE.lower()):
        resto = texto[len(COMANDO_TROCAR_TELEFONE) :].strip()
        novo = resto or await _pedir_telefone()
        if not novo:
            await cl.Message(content="Nenhum telefone informado.").send()
            return
        await _identificar_e_saudar(novo)
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

    identidade = cl.user_session.get("identidade")
    config = {"configurable": {"thread_id": telefone}}
    inputs = {
        "telefone": telefone,
        "identidade": identidade,
        "messages": [HumanMessage(content=texto)],
    }

    msg = cl.Message(content="")
    streamed = False
    try:
        async for event in _get_graph().astream_events(
            inputs, config=config, version="v2"
        ):
            if event.get("event") == "on_chat_model_stream":
                chunk = event.get("data", {}).get("chunk")
                token = getattr(chunk, "content", "") if chunk is not None else ""
                if token:
                    streamed = True
                    await msg.stream_token(token)
    except Exception as exc:  # noqa: BLE001 — feedback ao usuário, log para debug
        logger.exception("falha_no_grafo")
        await cl.Message(content=f"Erro ao processar a mensagem: {exc}").send()
        return

    estado = await _get_graph().aget_state(config)
    valores = getattr(estado, "values", None) or {}

    # Caminho safe_response (ou LLM sem stream): pega o último AIMessage do estado
    if not streamed:
        ultima_ai: Optional[AIMessage] = None
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