"""Servidor HTTP do cérebro da Sofia (Contrato A) — chamado pelo gateway Go.

O gateway Go recebe o webhook do Infodesk, enfileira e coalesce as mensagens
da conversa e chama `POST /chat` com a mensagem e o contato (telefone, nome
no WhatsApp e, se houver, a identificação em cache). Este serviço roda o
grafo LangGraph, isolando conversas por `conversation_id` (= `thread_id` do
checkpointer), e devolve a resposta + ações + classificação.

Rodar:
    uvicorn server:app --host 0.0.0.0 --port 8001

Contrato A — request (`docs/plano/01-ferramentas.md`):
    {"conversation_id": "...", "canal": "whatsapp", "mensagem": "...",
     "telefone": "+55...", "nome_contato": "Maria",
     "contato": {"forma": "cpf", "valor_mascarado": "***.***.789-**",
                 "cliente_id": null, "nome_cadastro": null} | null,
     "estado_conversa": "bot"}
Contrato A — resposta:
    {"reply": "...", "acoes": [], "classificacao": null, "fontes": [...],
     "erro": null}

`POST /encerrar-episodio` `{"conversation_id": "..."}`: o gateway avisa que a
conversa foi ENCERRADA no Infodesk (`conversation.finished`); a proposta de
chamado pendente, a classificação e a identificação da conversa são limpas.
O histórico de mensagens fica.

Campos só AUMENTAM neste contrato (compatibilidade): `classificacao` e
`acoes` são devolvidos vazios até E2.2 preenchê-los.
"""

from __future__ import annotations

import asyncio
import contextlib
import logging
import os
import threading
from contextlib import asynccontextmanager
from typing import Any

from fastapi import FastAPI
from fastapi.responses import JSONResponse
from langchain_core.messages import AIMessage, HumanMessage
from pydantic import BaseModel, Field

from src.agent.falhas import (
    CAUSA_PRAZO_TURNO,
    CLASSE_TRANSITORIA,
    classificar_falha,
    gerar_id_ocorrencia,
)
from src.agent.graph import build_graph
from src.config import get_settings
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
    """Turno do cliente (Contrato A). Campos além de `conversation_id` e
    `mensagem` são opcionais — o contrato só cresce."""

    conversation_id: str
    mensagem: str
    canal: str = "whatsapp"
    telefone: str | None = None
    #: Nome do contato no WhatsApp (`data.contact.name` do webhook; pode ser apelido).
    nome_contato: str | None = None
    #: Identificação em cache no gateway: {forma, valor_mascarado, cliente_id,
    #: nome_cadastro}; `None` se o cliente nunca se identificou.
    contato: dict[str, Any] | None = None
    #: "bot" | "transferida" — defesa: o gateway não deveria mandar transferida.
    estado_conversa: str = "bot"


class ErroChat(BaseModel):
    """Falha do turno, classificada para o gateway decidir (ver `src/agent/falhas.py`).

    `classe`: "transitoria" (vale repetir) | "definitiva" (transferir na hora).
    `id_ocorrencia`: o MESMO id vai no log do cérebro, no log do gateway e na
    tabela `falhas` do gateway.
    """

    classe: str
    id_ocorrencia: str
    causa: str


class ChatResponse(BaseModel):
    reply: str = ""
    #: Ações para o gateway executar (ex.: {"tipo": "transferir", ...}) — E2.2.
    acoes: list[dict[str, Any]] = Field(default_factory=list)
    #: Classificação fixada para a conversa (auditoria/log do gateway) — E2.2.
    classificacao: dict[str, Any] | None = None
    fontes: list[str] = Field(default_factory=list)
    erro: ErroChat | None = None


class EncerrarEpisodioRequest(BaseModel):
    """Corpo de `POST /encerrar-episodio`: a conversa foi ENCERRADA no Infodesk."""

    conversation_id: str


#: Campos do estado zerados no fim do episódio (o histórico fica).
CAMPOS_DO_EPISODIO = ("proposta_chamado", "classificacao", "identificacao")


class EncerrarEpisodioResponse(BaseModel):
    status: str = "ok"
    #: True quando havia uma proposta de chamado pendente e ela foi limpa.
    proposta_limpa: bool = False
    #: Quais campos do episódio tinham valor e foram zerados.
    campos_limpos: list[str] = Field(default_factory=list)


def _mascarar(valor: str) -> str:
    """Mascara um identificador deixando visíveis só os últimos 3 caracteres.

    Usado para não enviar PII (telefone/CPF) em claro ao log nem ao tracing externo.
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
                return "".join(b.get("text", "") for b in conteudo if isinstance(b, dict))
    return ""


async def processar_chat(graph: Any, req: ChatRequest) -> ChatResponse:
    """Roda o grafo para uma mensagem do Contrato A e devolve a resposta.

    Função pura (recebe o grafo por injeção) — testável sem servidor/rede.
    `nome_contato`/`contato` entram no estado e vão ao system prompt como DADO
    (ver `nodes._formatar_contato`).
    """
    # PII mascarada nos logs (LGPD): telefone nunca em claro; o nome do contato
    # e o conteúdo da mensagem só aparecem em DEBUG.
    logger.info(
        "chat_request conversation_id=%s canal=%s telefone=%s identificado=%s "
        "estado_conversa=%s len_mensagem=%d",
        req.conversation_id,
        req.canal,
        _mascarar(req.telefone or ""),
        bool(req.contato),
        req.estado_conversa,
        len(req.mensagem or ""),
    )
    logger.debug(
        "chat_request_mensagem conversation_id=%s nome_contato=%r mensagem=%r",
        req.conversation_id,
        req.nome_contato,
        req.mensagem,
    )
    if req.estado_conversa != "bot":
        # Defesa: o gateway não deveria mandar turno de conversa transferida;
        # se mandar, o cérebro responde normalmente e o gateway decide.
        logger.warning(
            "chat_request_estado_inesperado conversation_id=%s estado_conversa=%s",
            req.conversation_id,
            req.estado_conversa,
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
        if req.telefone:
            metadata["langfuse_user_id"] = _mascarar(req.telefone)
        config["callbacks"] = [handler]
        config["metadata"] = metadata
        config["run_name"] = "chat"

    inputs: dict[str, Any] = {
        "nome_contato": req.nome_contato,
        "contato": req.contato,
        # Vai ao estado para as tools repassarem ao gateway (correlação do log
        # do gateway com a conversa) — nunca é argumento do modelo.
        "conversation_id": req.conversation_id,
        "messages": [HumanMessage(content=req.mensagem)],
    }
    if req.telefone:
        inputs["telefone"] = req.telefone

    try:
        # Prazo do turno MENOR que o BRAIN_TIMEOUT/TURNO_TETO do gateway: o
        # cérebro desiste primeiro e devolve falha transitória classificada.
        valores = await asyncio.wait_for(
            graph.ainvoke(inputs, config=config),
            timeout=get_settings().brain_turno_prazo,
        )
    except TimeoutError as exc:
        return _falha_do_turno(req, exc, classe=CLASSE_TRANSITORIA, causa=CAUSA_PRAZO_TURNO)
    except Exception as exc:
        return _falha_do_turno(req, exc)
    resposta = ChatResponse(
        reply=_extrair_reply(valores),
        classificacao=valores.get("classificacao") or None,
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


async def encerrar_episodio(graph: Any, conversation_id: str) -> EncerrarEpisodioResponse:
    """Fim do episódio (conversa encerrada no Infodesk): zera os campos do
    episódio — proposta de chamado pendente, classificação e identificação.

    O mesmo `conversation_id` pode voltar a receber mensagens; uma proposta
    preparada num episódio anterior não pode ser "confirmada" por uma mensagem
    qualquer do episódio seguinte, e a classificação/identificação de ontem
    não valem para o problema de hoje (a identificação continua em cache no
    gateway, por telefone — o Contrato A a devolve em `contato`). O histórico
    de mensagens fica intacto; os campos são zerados como se o último nó do
    grafo (`output_guardrail`) os tivesse escrito — nada fica pendente para o
    próximo turno. Conversa sem nada a limpar (ou desconhecida) não ganha
    checkpoint.
    """
    config: dict[str, Any] = {"configurable": {"thread_id": conversation_id}}
    estado = await graph.aget_state(config)
    valores = getattr(estado, "values", None) or {}
    limpos = [campo for campo in CAMPOS_DO_EPISODIO if valores.get(campo)]
    if not limpos:
        logger.info("episodio_encerrado conversation_id=%s campos_limpos=[]", conversation_id)
        return EncerrarEpisodioResponse(proposta_limpa=False)
    await graph.aupdate_state(config, dict.fromkeys(CAMPOS_DO_EPISODIO), as_node="output_guardrail")
    logger.info("episodio_encerrado conversation_id=%s campos_limpos=%s", conversation_id, limpos)
    return EncerrarEpisodioResponse(
        proposta_limpa="proposta_chamado" in limpos, campos_limpos=limpos
    )


def _falha_do_turno(
    req: ChatRequest,
    exc: BaseException,
    *,
    classe: str | None = None,
    causa: str | None = None,
) -> ChatResponse:
    """Transforma a exceção do grafo em `ChatResponse.erro` (classe + ID).

    O gateway Go lê a classe para decidir (repetir × transferir) e o ID para
    o registro da falha; aqui o log `chat_erro` leva o mesmo ID e o
    `conversation_id`, sem o conteúdo da mensagem (PII). `classe`/`causa`
    explícitas pulam a classificação (ex.: prazo do turno estourado).
    """
    if classe is None or causa is None:
        classe, causa = classificar_falha(exc)
    id_ocorrencia = gerar_id_ocorrencia()
    logger.error(
        "chat_erro conversation_id=%s classe=%s id_ocorrencia=%s causa=%s tipo=%s",
        req.conversation_id,
        classe,
        id_ocorrencia,
        causa,
        type(exc).__name__,
        # Traceback só para falha definitiva (bug/configuração) — a
        # transitória é ruído de rede e já tem a causa resumida.
        exc_info=classe != CLASSE_TRANSITORIA,
    )
    return ChatResponse(
        reply="",
        erro=ErroChat(classe=classe, id_ocorrencia=id_ocorrencia, causa=causa),
    )


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
    """Aquece embeddings + índice fora do caminho da 1ª mensagem.

    O primeiro load do sentence-transformers custa vários segundos; sem o
    warm-up esse custo cai no primeiro cliente do dia. Fail-safe: erro só loga.
    """

    def _aquecer() -> None:
        try:
            from src.tools.rag_tool import buscar_chunks

            buscar_chunks("warmup")
            logger.info("rag_warmup_ok")
        except Exception as exc:
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
    except Exception as exc:
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


app = FastAPI(title="Sofia — Infominas (cérebro)", lifespan=_lifespan)


@app.get("/health")
async def health() -> dict[str, str]:
    return {"status": "ok"}


@app.post("/chat", response_model=ChatResponse)
async def chat(req: ChatRequest):
    """Contrato A. Falha do turno sai com status HTTP por classe + corpo com
    `erro` (classe, id_ocorrencia, causa): 503 = transitória (o gateway pode
    repetir), 500 = definitiva (o gateway transfere ao atendente na hora)."""
    resposta = await processar_chat(_get_graph(), req)
    if resposta.erro is not None:
        status = 503 if resposta.erro.classe == CLASSE_TRANSITORIA else 500
        return JSONResponse(status_code=status, content=resposta.model_dump())
    return resposta


@app.post("/encerrar-episodio", response_model=EncerrarEpisodioResponse)
async def encerrar_episodio_endpoint(req: EncerrarEpisodioRequest):
    """Chamado pelo gateway Go no `conversation.finished` do Infodesk. Falha
    sai como 500 com corpo — o gateway só loga: a validade de 15 min da
    proposta é a rede de segurança."""
    try:
        return await encerrar_episodio(_get_graph(), req.conversation_id)
    except Exception as exc:  # nunca um 500 mudo
        logger.exception(
            "episodio_erro conversation_id=%s tipo=%s",
            req.conversation_id,
            type(exc).__name__,
        )
        return JSONResponse(
            status_code=500,
            content={"status": "erro", "proposta_limpa": False, "campos_limpos": []},
        )
