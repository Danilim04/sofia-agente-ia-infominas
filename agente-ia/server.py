"""Servidor HTTP do cérebro (Contrato A) — integração Chatwoot via backend Go.

O backend Go (transporte Chatwoot + gate de identidade) chama `POST /chat`
**somente quando o usuário já está identificado**, enviando a mensagem e o
perfil mínimo (`identidade`) resolvido. Este serviço roda o grafo LangGraph
(Épico 7), isolando conversas por `conversation_id` (= `thread_id` do
`MemorySaver`), e devolve a resposta + ações opcionais para o Chatwoot.

Rodar:
    uvicorn server:app --host 0.0.0.0 --port 8001

Contrato A — request:
    {"conversation_id": "...", "canal": "whatsapp", "mensagem": "...",
     "identidade": {...}, "telefone": "...", "session_token": "..."}
Contrato A — resposta:
    {"reply": "...", "acoes": [], "fontes": [...]}

Obs.: `session_token` é aceito e ignorado nesta fase. Ele só passa a importar
no plano deferido (tools MCP), onde o Python o repassa ao Go para escopar as
consultas de dados ao usuário autorizado.
"""

from __future__ import annotations

import contextlib
import logging
import os
import threading
from contextlib import asynccontextmanager
from typing import Any, Optional

from fastapi import FastAPI
from langchain_core.messages import AIMessage, HumanMessage
from pydantic import BaseModel, Field

from src.agent.graph import build_graph
from src.config import get_settings
from src.identity.login_extractor import extrair_login
from src.observability.tracing import get_tracing_handler


def _setup_logging() -> None:
    """Configura o logging-raiz para o cérebro.

    Sob uvicorn, sem isto os loggers de `src.*` (guardrails, tools, nós) ficam
    em WARNING e seus `logger.info(...)` somem. Nível controlado por `LOG_LEVEL`
    (DEBUG/INFO/WARNING/ERROR; default INFO). Use `LOG_LEVEL=DEBUG` para ver a
    query do RAG, os tool_calls do agente e o uso de tokens.
    """
    level_name = os.getenv("LOG_LEVEL", "INFO").upper()
    level = getattr(logging, level_name, logging.INFO)
    logging.basicConfig(
        level=level,
        format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
        force=True,
    )
    # Garante o nível mesmo que uvicorn reconfigure só os loggers dele.
    logging.getLogger("src").setLevel(level)


_setup_logging()

logger = logging.getLogger(__name__)


class ChatRequest(BaseModel):
    conversation_id: str
    mensagem: str
    canal: str = "whatsapp"
    identidade: Optional[dict[str, Any]] = None
    telefone: Optional[str] = None
    session_token: Optional[str] = None


class ChatResponse(BaseModel):
    reply: str
    acoes: list[dict[str, Any]] = Field(default_factory=list)
    fontes: list[str] = Field(default_factory=list)


class ExtractLoginRequest(BaseModel):
    mensagem: str


class ExtractLoginResponse(BaseModel):
    login: Optional[str] = None


def _mascarar(valor: str) -> str:
    """Mascara um identificador deixando visíveis só os últimos 3 caracteres.

    Usado para não enviar PII (login/CPF/e-mail) em claro ao tracing externo.
    """
    v = (valor or "").strip()
    if len(v) <= 3:
        return "*" * len(v)
    return "*" * (len(v) - 3) + v[-3:]


def _extrair_reply(valores: dict) -> str:
    """Último texto do agente. Tolera `content` str ou lista de blocos (Anthropic)."""
    for m in reversed(valores.get("messages") or []):
        if isinstance(m, AIMessage):
            conteudo = m.content
            if isinstance(conteudo, str):
                return conteudo
            if isinstance(conteudo, list):
                return "".join(
                    b.get("text", "") for b in conteudo if isinstance(b, dict)
                )
    return ""


async def processar_chat(graph: Any, req: ChatRequest) -> ChatResponse:
    """Roda o grafo para uma mensagem do Contrato A e devolve a resposta.

    Função pura (recebe o grafo por injeção) — testável sem servidor/rede.
    A `identidade` entra no estado e é injetada no system prompt como DADO
    (ver `nodes._build_system_message`).
    """
    login = (req.identidade or {}).get("login") if req.identidade else None
    # PII mascarada nos logs (F5/LLM06/LGPD): telefone e login nunca em claro;
    # o conteúdo da mensagem só aparece em DEBUG.
    logger.info(
        "chat_request conversation_id=%s canal=%s telefone=%s login=%s identificado=%s len_mensagem=%d",
        req.conversation_id,
        req.canal,
        _mascarar(req.telefone or ""),
        _mascarar(login or ""),
        bool(req.identidade and req.identidade.get("encontrado")),
        len(req.mensagem or ""),
    )
    logger.debug(
        "chat_request_mensagem conversation_id=%s mensagem=%r",
        req.conversation_id,
        req.mensagem,
    )

    config: dict[str, Any] = {"configurable": {"thread_id": req.conversation_id}}
    # Tracing opcional (Langfuse): agrupa o turno pela conversa e registra
    # custo/latência/tool_calls. Off quando as chaves não estão configuradas.
    handler = get_tracing_handler()
    if handler is not None:
        metadata: dict[str, Any] = {
            "langfuse_session_id": req.conversation_id,
            "langfuse_tags": [req.canal],
        }
        if login:
            metadata["langfuse_user_id"] = _mascarar(login)
        config["callbacks"] = [handler]
        config["metadata"] = metadata
        config["run_name"] = "chat"

    inputs: dict[str, Any] = {
        "identidade": req.identidade,
        "messages": [HumanMessage(content=req.mensagem)],
    }
    if req.telefone:
        inputs["telefone"] = req.telefone

    valores = await graph.ainvoke(inputs, config=config)
    resposta = ChatResponse(
        reply=_extrair_reply(valores),
        fontes=list(valores.get("fontes_usadas") or []),
    )
    logger.info(
        "chat_response conversation_id=%s len_reply=%d fontes=%s",
        req.conversation_id,
        len(resposta.reply),
        resposta.fontes,
    )
    logger.debug(
        "chat_response_full conversation_id=%s reply=%r",
        req.conversation_id,
        resposta.reply,
    )
    return resposta


# ---------------------------------------------------------------------------
# App FastAPI — grafo compilado uma vez por processo (isolamento por thread_id).
# ---------------------------------------------------------------------------

_GRAPH: Any = None


def _get_graph():
    """Fallback lazy (testes chamam sem lifespan): grafo com MemorySaver."""
    global _GRAPH
    if _GRAPH is None:
        _GRAPH = build_graph()
    return _GRAPH


def _warmup_rag_em_background() -> None:
    """Aquece embeddings + ChromaDB fora do caminho da 1ª mensagem (B1).

    O primeiro load do sentence-transformers custa vários segundos; sem o
    warm-up esse custo cai no primeiro cliente do dia. Fail-safe: erro só loga.
    """

    def _aquecer() -> None:
        try:
            from src.tools.rag_tool import buscar_chunks

            buscar_chunks("warmup")
            logger.info("rag_warmup_ok")
        except Exception as exc:  # noqa: BLE001 — warm-up nunca derruba o server
            logger.warning("rag_warmup_falhou erro=%s", exc)

    threading.Thread(target=_aquecer, name="rag-warmup", daemon=True).start()


@asynccontextmanager
async def _lifespan(app: FastAPI):
    """Sobe o grafo com checkpointer SQLite persistente + warm-up do RAG.

    Sem o pacote `langgraph-checkpoint-sqlite` (ou em erro de setup), cai para
    o MemorySaver default com warning — dev/testes rodam sem nenhum setup, mas
    em produção o histórico das conversas passa a sobreviver a restart.
    """
    global _GRAPH
    saver_cm = None
    saver = None
    try:
        from langgraph.checkpoint.sqlite.aio import AsyncSqliteSaver

        caminho = get_settings().checkpoint_db_path
        caminho.parent.mkdir(parents=True, exist_ok=True)
        saver_cm = AsyncSqliteSaver.from_conn_string(str(caminho))
        saver = await saver_cm.__aenter__()
        # Exercita a conexão JÁ NO STARTUP: incompatibilidade de versão do
        # aiosqlite só estoura no primeiro uso, e sem isto o fail-soft abaixo
        # nunca dispara — o server subiria "saudável" com todo /chat em 500.
        await saver.setup()
        logger.info("checkpointer_sqlite path=%s", caminho)
    except Exception as exc:  # noqa: BLE001 — fail-soft para MemorySaver
        if saver_cm is not None:
            with contextlib.suppress(Exception):
                await saver_cm.__aexit__(None, None, None)
        saver_cm = None
        saver = None
        logger.warning(
            "checkpointer_sqlite_indisponivel erro=%s — usando MemorySaver "
            "(histórico NÃO sobrevive a restart)",
            exc,
        )
    _GRAPH = build_graph(checkpointer=saver) if saver is not None else build_graph()
    _warmup_rag_em_background()
    try:
        yield
    finally:
        if saver_cm is not None:
            await saver_cm.__aexit__(None, None, None)


app = FastAPI(title="Azapfy Suporte IA — cérebro", lifespan=_lifespan)


@app.get("/health")
async def health() -> dict[str, str]:
    return {"status": "ok"}


@app.post("/chat", response_model=ChatResponse)
async def chat(req: ChatRequest) -> ChatResponse:
    return await processar_chat(_get_graph(), req)


@app.post("/extract-login", response_model=ExtractLoginResponse)
async def extract_login(req: ExtractLoginRequest) -> ExtractLoginResponse:
    """Extrai o login embutido numa frase (fallback do gate Go).

    O valor é só um candidato — quem autoriza é o gate (Mongo + confirmação).
    """
    # A mensagem contém o próprio identificador (PII) — em claro só em DEBUG.
    logger.info("extract_login_request len_mensagem=%d", len(req.mensagem or ""))
    logger.debug("extract_login_request_mensagem mensagem=%r", req.mensagem)
    resultado = extrair_login(req.mensagem)
    logger.info(
        "extract_login_response login=%s", _mascarar(resultado.get("login") or "")
    )
    return ExtractLoginResponse(login=resultado.get("login"))
