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

Campos só AUMENTAM neste contrato (compatibilidade). `classificacao` e `acoes`
saem do estado do grafo (nó `classificar`, E2.2):
    "classificacao": {"intencao", "categoria", "prioridade", "confianca", "origem"}
    "acoes": [{"tipo": "transferir", "origem": "frustracao"|"confianca"|"pedido_cliente",
               "motivo": "..."}]

`POST /classificar` (só evals/ops; header `X-Tools-Token` = `INFODESK_TOOLS_TOKEN`):
roda o JEV/roteador SEM o grafo e SEM estado de conversa —
    {"conversation_id", "mensagem", "historico": [{"autor": "cliente"|"sofia", "texto"}]}
 → {"classificacao": {...5 chaves...}, "frustracao": {"score", "confianca"},
    "seguranca": {"malicioso": bool, "fora_de_escopo": bool}, "esclarecimento": null|{...}}
"""

from __future__ import annotations

import asyncio
import contextlib
import hmac
import logging
import os
import threading
from contextlib import asynccontextmanager
from typing import Any, Literal

from fastapi import Depends, FastAPI, Header, HTTPException
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
from src.agent.llm import get_classifier_llm
from src.agent.nodes import PREFIXO_SESSAO_NO_CONFIG
from src.classificacao import Mensagem, ServicoClassificacao, criar_servico_padrao
from src.config import get_settings
from src.observability import eventos
from src.observability.metricas import instalar_contador
from src.observability.pii import (
    instalar_mascara_pii,
    mascarar_telefone,
    mascarar_texto,
    registrar_nomes,
)
from src.observability.tracing import TracoDoTurno, get_tracing_handler


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
    # PII (CPF, telefone, nome) mascarada em TODA mensagem de log, em qualquer nível
    # (E4.3): rede de segurança para o que um módulo esquecer de mascarar.
    instalar_mascara_pii()
    # O uvicorn tem handlers próprios (propagate=False): o traceback "Exception in
    # ASGI application" sairia sem máscara. `uvicorn.access` fica de fora (o
    # formatter dele lê `record.args`).
    for nome in ("uvicorn", "uvicorn.error"):
        instalar_mascara_pii(logging.getLogger(nome).handlers)
    # Métrica `jev_indisponivel` (contador + log `sofia_metrica`).
    instalar_contador()


_setup_logging()

logger = logging.getLogger(__name__)


#: Limites do Contrato A (E4.5b, M2): um pedido fora deles é 422 antes de chegar ao grafo.
MENSAGEM_MAX_CHARS = 16_000
NOME_CONTATO_MAX_CHARS = 120

#: Header que o gateway manda em /chat e /encerrar-episodio (E4.5b, I7): o MESMO
#: segredo compartilhado das tools (`TOOLS_API_TOKEN` no Go = `INFODESK_TOOLS_TOKEN` aqui).
HEADER_TOKEN_DO_GATEWAY = "X-Brain-Token"  # noqa: S105 - nome do header, não um segredo


def exigir_token_do_gateway(
    x_brain_token: str | None = Header(default=None, alias=HEADER_TOKEN_DO_GATEWAY),
) -> None:
    """Dependência de /chat e /encerrar-episodio: só o gateway chama.

    Com o segredo configurado (`INFODESK_TOOLS_TOKEN`) o header é obrigatório e
    comparado em tempo constante — fail-closed: ausente ou errado é 401, sem dizer
    qual. Sem segredo configurado (dev local, testes) o cérebro fica aberto, como
    antes, e avisa no log uma vez por processo.
    """
    esperado = get_settings().infodesk_tools_token
    if not esperado:
        _avisar_sem_token_uma_vez()
        return
    recebido = x_brain_token or ""
    if not hmac.compare_digest(recebido.encode(), esperado.encode()):
        logger.warning("brain_token_recusado presente=%s", bool(x_brain_token))
        raise HTTPException(status_code=401, detail="não autorizado")


_AVISOU_SEM_TOKEN: list[bool] = []


def _avisar_sem_token_uma_vez() -> None:
    if not _AVISOU_SEM_TOKEN:
        _AVISOU_SEM_TOKEN.append(True)
        logger.warning(
            "brain_sem_token INFODESK_TOOLS_TOKEN vazio — /chat e /encerrar-episodio ficam "
            "abertos (aceitável só em dev local)"
        )


class ChatRequest(BaseModel):
    """Turno do cliente (Contrato A). Campos além de `conversation_id` e
    `mensagem` são opcionais — o contrato só cresce."""

    conversation_id: str = Field(max_length=200)
    #: Teto folgado: o gateway junta a rajada do cliente num turno só (E4.5b, M2).
    mensagem: str = Field(max_length=MENSAGEM_MAX_CHARS)
    canal: str = Field("whatsapp", max_length=40)
    telefone: str | None = Field(None, max_length=40)
    #: Nome do contato no WhatsApp (`data.contact.name` do webhook; pode ser apelido).
    nome_contato: str | None = Field(None, max_length=NOME_CONTATO_MAX_CHARS)
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
    #: Ações para o gateway executar: `{"tipo": "transferir", "origem":
    #: "frustracao"|"confianca"|"pedido_cliente", "motivo": "..."}` (E2.2).
    acoes: list[dict[str, Any]] = Field(default_factory=list)
    #: Classificação fixada para a conversa (auditoria/log do gateway): as 5
    #: chaves `intencao, categoria, prioridade, confianca, origem` (E2.2).
    classificacao: dict[str, Any] | None = None
    fontes: list[str] = Field(default_factory=list)
    erro: ErroChat | None = None


class EncerrarEpisodioRequest(BaseModel):
    """Corpo de `POST /encerrar-episodio`: a conversa foi ENCERRADA no Infodesk."""

    conversation_id: str


#: Campos do estado zerados no fim do episódio (o histórico fica).
#: `identificacao` entra por decisão desta etapa (ver PR da E1.3): o cache de
#: identificação por telefone vive no gateway e volta em `contato` no próximo
#: turno; aqui só o registro mascarado da conversa é descartado.
CAMPOS_DO_EPISODIO = ("proposta_chamado", "classificacao", "identificacao", "esclarecimentos")


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
    return mascarar_telefone(valor)


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
    # PII fora do log (LGPD, E4.3): telefone mascarado; a mensagem do cliente NÃO
    # entra em log em claro em nível nenhum — só o tamanho (INFO) e a versão
    # mascarada (DEBUG); o nome do contato nunca entra.
    registrar_nomes(
        req.conversation_id, (req.nome_contato, (req.contato or {}).get("nome_cadastro"))
    )
    eventos.registrar(
        logger,
        eventos.CHAT_REQUEST,
        conversation_id=req.conversation_id,
        canal=req.canal,
        telefone=_mascarar(req.telefone or ""),
        identificado=bool(req.contato),
        estado_conversa=req.estado_conversa,
        len_mensagem=len(req.mensagem or ""),
    )
    if logger.isEnabledFor(logging.DEBUG):
        logger.debug(
            "chat_request_mensagem conversation_id=%s mensagem_mascarada=%s",
            req.conversation_id,
            mascarar_texto(req.mensagem, (req.nome_contato or "",)),
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
    # Tracing opcional (Langfuse): o trace raiz (`TracoDoTurno`) agrupa o turno
    # pela conversa, leva o telefone mascarado e, depois do grafo, as tags
    # intencao/categoria/origem; o handler aninha custo/latência/tool_calls.
    # Off quando as chaves não estão configuradas.
    handler = get_tracing_handler()
    if handler is not None:
        config["callbacks"] = [handler]
        config["run_name"] = "chat"

    # PII do contato FORA do estado persistido (E4.5b, I1): telefone, nome do WhatsApp e
    # nome do cadastro vão por `config` (o gateway os reenvia a cada turno); o estado
    # recebe `None` nos dois primeiros, o que também limpa checkpoints antigos.
    contato_no_estado = (
        {k: v for k, v in req.contato.items() if k != "nome_cadastro"} if req.contato else None
    )
    nome_cadastro = (req.contato or {}).get("nome_cadastro")
    sessao = {
        "telefone": req.telefone,
        "nome_contato": req.nome_contato,
        "nome_cadastro": nome_cadastro if isinstance(nome_cadastro, str) else None,
    }
    config["configurable"].update({PREFIXO_SESSAO_NO_CONFIG + k: v for k, v in sessao.items() if v})
    inputs: dict[str, Any] = {
        "nome_contato": None,
        "telefone": None,
        "contato": contato_no_estado,
        # Vai ao estado para as tools repassarem ao gateway (correlação do log
        # do gateway com a conversa) — nunca é argumento do modelo.
        "conversation_id": req.conversation_id,
        "messages": [HumanMessage(content=req.mensagem)],
    }

    try:
        with TracoDoTurno(
            conversation_id=req.conversation_id, canal=req.canal, telefone=req.telefone
        ) as traco:
            # Prazo do turno MENOR que o BRAIN_TIMEOUT/TURNO_TETO do gateway: o
            # cérebro desiste primeiro e devolve falha transitória classificada.
            valores = await asyncio.wait_for(
                graph.ainvoke(inputs, config=config),
                timeout=get_settings().brain_turno_prazo,
            )
            traco.marcar_classificacao(valores.get("classificacao"))
    except TimeoutError as exc:
        return _falha_do_turno(req, exc, classe=CLASSE_TRANSITORIA, causa=CAUSA_PRAZO_TURNO)
    except Exception as exc:
        return _falha_do_turno(req, exc)
    resposta = ChatResponse(
        reply=_extrair_reply(valores),
        acoes=[dict(a) for a in valores.get("acoes") or []],
        classificacao=valores.get("classificacao") or None,
        fontes=list(valores.get("fontes_usadas") or []),
    )
    logger.info(
        "chat_response conversation_id=%s len_reply=%d fontes=%s acoes=%s",
        req.conversation_id,
        len(resposta.reply),
        resposta.fontes,
        [a.get("origem") for a in resposta.acoes],
    )
    for acao in resposta.acoes:
        if acao.get("tipo") == "transferir":
            eventos.registrar(
                logger,
                eventos.TRANSFERENCIA,
                conversation_id=req.conversation_id,
                origem=acao.get("origem"),
            )
    if logger.isEnabledFor(logging.DEBUG):
        logger.debug(
            "chat_response_full conversation_id=%s reply_mascarada=%s",
            req.conversation_id,
            mascarar_texto(resposta.reply, (req.nome_contato or "",)),
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
_SERVICO: ServicoClassificacao | None = None


def _get_servico() -> ServicoClassificacao:
    """Serviço de classificação (JEV + fallback LLM), único por processo.

    Carrega a taxonomia (avisa no log enquanto `placeholder: true` — P1) e só
    liga o JEV real se houver `TYPESAFE_API_KEY` (P8).
    """
    global _SERVICO
    if _SERVICO is None:
        _SERVICO = criar_servico_padrao(get_classifier_llm)
    return _SERVICO


def _get_graph():
    """Fallback lazy (testes chamam sem lifespan): grafo com MemorySaver."""
    global _GRAPH
    if _GRAPH is None:
        _GRAPH = build_graph(classificador=_get_servico())
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
    servico = _get_servico()
    _GRAPH = (
        build_graph(checkpointer=saver, classificador=servico)
        if saver is not None
        else build_graph(classificador=servico)
    )
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


@app.post("/chat", response_model=ChatResponse, dependencies=[Depends(exigir_token_do_gateway)])
async def chat(req: ChatRequest):
    """Contrato A. Falha do turno sai com status HTTP por classe + corpo com
    `erro` (classe, id_ocorrencia, causa): 503 = transitória (o gateway pode
    repetir), 500 = definitiva (o gateway transfere ao atendente na hora)."""
    resposta = await processar_chat(_get_graph(), req)
    if resposta.erro is not None:
        status = 503 if resposta.erro.classe == CLASSE_TRANSITORIA else 500
        return JSONResponse(status_code=status, content=resposta.model_dump())
    return resposta


@app.post(
    "/encerrar-episodio",
    response_model=EncerrarEpisodioResponse,
    dependencies=[Depends(exigir_token_do_gateway)],
)
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


# ---------------------------------------------------------------------------
# POST /classificar — só evals/ops: JEV/roteador sem o grafo
# ---------------------------------------------------------------------------


class ItemHistorico(BaseModel):
    autor: Literal["cliente", "sofia"]
    texto: str


class ClassificarRequest(BaseModel):
    conversation_id: str
    mensagem: str
    historico: list[ItemHistorico] = Field(default_factory=list)


class ClassificarResponse(BaseModel):
    classificacao: dict[str, Any]
    frustracao: dict[str, float]
    seguranca: dict[str, bool]
    #: `None` quando não precisa esclarecer; senão `{"necessario": true, "candidatas": [...]}`.
    esclarecimento: dict[str, Any] | None = None


async def classificar_mensagem(
    servico: ServicoClassificacao, req: ClassificarRequest
) -> ClassificarResponse:
    """Classifica UMA mensagem (sem estado de conversa: sem pegajosidade nem esclarecimentos).

    Função pura (serviço por injeção) — testável sem servidor/rede. O histórico
    entra como contexto; PII é mascarada pelo `estado_para_jev` antes de sair.
    """
    mensagens = [Mensagem(i.autor, i.texto) for i in req.historico]
    mensagens.append(Mensagem("cliente", req.mensagem))
    c = await servico.classificar(mensagens, None)
    cfg = servico.config
    return ClassificarResponse(
        classificacao=c.para_estado(),
        frustracao={
            "score": c.frustracao,
            "confianca": round(float(c.confiancas.get("frustracao", 0.0)), 4),
        },
        seguranca={
            "malicioso": c.malicioso >= cfg.limiar_malicioso_bloqueio,
            "fora_de_escopo": c.fora_de_escopo >= cfg.limiar_fora_escopo,
        },
        esclarecimento=(
            {"necessario": True, "candidatas": list(c.candidatas)} if c.precisa_esclarecer else None
        ),
    )


def _token_valido(recebido: str | None) -> bool:
    esperado = get_settings().infodesk_tools_token
    return (
        bool(esperado)
        and recebido is not None
        and hmac.compare_digest(recebido.encode(), esperado.encode())
    )


@app.post("/classificar", response_model=ClassificarResponse)
async def classificar_endpoint(
    req: ClassificarRequest,
    x_tools_token: str | None = Header(default=None, alias="X-Tools-Token"),
):
    """Só para evals/ops. Fail-closed: sem `INFODESK_TOOLS_TOKEN` configurado, ou
    com token errado, responde 403 — nunca fica aberto por esquecimento."""
    if not _token_valido(x_tools_token):
        return JSONResponse(status_code=403, content={"erro": "token inválido ou não configurado"})
    try:
        return await classificar_mensagem(_get_servico(), req)
    except Exception as exc:  # nunca um 500 mudo
        logger.exception(
            "classificar_erro conversation_id=%s tipo=%s", req.conversation_id, type(exc).__name__
        )
        return JSONResponse(status_code=500, content={"erro": "falha ao classificar"})
