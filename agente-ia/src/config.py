import re
from functools import lru_cache
from pathlib import Path

from pydantic import Field
from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(
        env_file=".env",
        env_file_encoding="utf-8",
        extra="ignore",
        case_sensitive=False,
    )

    openrouter_api_key: str = Field(..., alias="OPENROUTER_API_KEY")
    openrouter_base_url: str = Field("https://openrouter.ai/api/v1", alias="OPENROUTER_BASE_URL")
    openrouter_model: str = Field("google/gemini-3.5-flash", alias="OPENROUTER_MODEL")
    openrouter_classifier_model: str = Field(
        "google/gemini-2.5-flash-lite", alias="OPENROUTER_CLASSIFIER_MODEL"
    )
    app_referer: str = Field("http://localhost:8000", alias="APP_REFERER")
    app_title: str = Field("Sofia — Infominas", alias="APP_TITLE")

    chroma_persist_dir: Path = Field(Path("./chroma_db"), alias="CHROMA_PERSIST_DIR")
    docs_dir: Path = Field(Path("./docs"), alias="DOCS_DIR")
    # Embeddings do índice pgvector (sync + retrieval do agente):
    #   provider "local"      → sentence-transformers em CPU; EMBEDDINGS_MODEL é
    #                           o nome HuggingFace (default all-MiniLM-L6-v2).
    #   provider "openrouter" → POST /embeddings do OpenRouter com a MESMA chave
    #                           do LLM; EMBEDDINGS_MODEL é o id no OpenRouter
    #                           (produção: openai/text-embedding-3-small).
    # Trocar provider/modelo exige reconstruir o índice (sync --full ou
    # --reconstruir-se-modelo-mudou) — espaços vetoriais não se misturam.
    # O Chroma de dev NÃO usa isto: ele é sempre local, com a constante
    # CHROMA_EMBEDDINGS_MODEL de src/rag/ingest.py.
    embeddings_provider: str = Field("local", alias="EMBEDDINGS_PROVIDER")
    embeddings_model: str = Field(
        "sentence-transformers/all-MiniLM-L6-v2", alias="EMBEDDINGS_MODEL"
    )

    # Backend de leitura do RAG: "chroma" (só dev, default) ou "pgvector"
    # (produção). O sync grava no pgvector independente deste valor; este
    # valor só decide de onde o agente LÊ.
    vector_backend: str = Field("chroma", alias="VECTOR_BACKEND")
    pgvector_url: str = Field("", alias="PGVECTOR_URL")

    # Trava anti-remoção em massa do sync (fração da base viva por ciclo).
    sync_limiar_remocao: float = Field(0.2, alias="SYNC_LIMIAR_REMOCAO")

    llm_temperature: float = Field(0.2, alias="LLM_TEMPERATURE")
    # Timeout por chamada de LLM. Com 1 retry do cliente e 2 chamadas por turno
    # (classificador + agente) precisa caber no prazo do turno abaixo.
    llm_timeout: float = Field(20.0, alias="LLM_TIMEOUT")
    # Prazo TOTAL do turno no cérebro (segundos): `processar_chat` interrompe o
    # grafo e devolve falha transitória ao gateway. Regra de ordem (E3.7), checada
    # no boot por `validar_prazos`:
    #   BRAIN_TIMEOUT (90s) > TURNO_TETO (80s) > BRAIN_TURNO_PRAZO (70s)
    #                                          > INFODESK_TOOLS_TIMEOUT (30s) + JEV
    # Quem desiste primeiro é o cérebro, com classe e ID — nunca o gateway no
    # escuro. O turno paga JEV, duas chamadas de LLM e a tool mais lenta
    # (`abrir_chamado`, que espera até 35s). `validar_prazos` garante o piso da tool
    # mais lenta + JEV; as chamadas de LLM (LLM_TIMEOUT, 1 retry cada) não entram na
    # conta — no pior caso extremo o próprio prazo do turno as interrompe.
    brain_turno_prazo: float = Field(70.0, alias="BRAIN_TURNO_PRAZO")
    # Prazos do GATEWAY, lidos só para conferir a ordem acima quando o ambiente
    # (.env/compose compartilhado) os traz; ausentes, o cérebro confere só o que
    # conhece. Formato Go: "90s", "1m30s", "1500ms" (unidade obrigatória, como no Go).
    brain_timeout_gateway: str = Field("", alias="BRAIN_TIMEOUT")
    turno_teto_gateway: str = Field("", alias="TURNO_TETO")
    # Teto de tokens de SAÍDA por chamada. Sem ele, o OpenRouter reserva o máximo
    # do modelo (65.536 tokens) a cada chamada e recusa com 402 quando o saldo
    # não cobre a reserva — mesmo que a resposta real caiba em poucos tokens.
    # Agente: respostas WhatsApp-native (~400 chars) + tool calls cabem em 1.024.
    # Classificador de segurança: JSON curto, 256 basta.
    llm_max_tokens: int = Field(1024, alias="LLM_MAX_TOKENS")
    llm_max_tokens_classificador: int = Field(256, alias="LLM_MAX_TOKENS_CLASSIFICADOR")
    rag_top_k: int = Field(3, alias="RAG_TOP_K")
    rag_chunk_size: int = Field(800, alias="RAG_CHUNK_SIZE")
    rag_chunk_overlap: int = Field(120, alias="RAG_CHUNK_OVERLAP")

    # Teto de iterações do loop agent⇄tools por turno (defesa contra loops
    # caros: cada iteração é uma chamada cheia ao LLM). O essencial da redução
    # vem da política de tools no system prompt; este é só o limite de guarda.
    agent_max_iteracoes: int = Field(5, alias="AGENT_MAX_ITERACOES")

    # Tools do Infodesk (identificar, preparar/abrir/consultar chamado,
    # transferir) moram no gateway Go em /tools/infodesk/*; o cérebro as chama
    # por HTTP. `infodesk_tools_token` casa com TOOLS_API_TOKEN do Go
    # (header X-Tools-Token). Usadas pelas tools de E2.3.
    infodesk_tools_base_url: str = Field("http://localhost:8080", alias="INFODESK_TOOLS_BASE_URL")
    infodesk_tools_token: str = Field("", alias="INFODESK_TOOLS_TOKEN")
    infodesk_tools_timeout: float = Field(30.0, alias="INFODESK_TOOLS_TIMEOUT")

    # Checkpointer persistente do grafo (histórico das conversas por
    # conversation_id). Sem o pacote `langgraph-checkpoint-sqlite` (ou em erro),
    # o server cai para MemorySaver com warning — dev/testes seguem sem setup.
    checkpoint_db_path: Path = Field(Path("./data/checkpoints.db"), alias="CHECKPOINT_DB_PATH")

    # --- Observabilidade: Langfuse (tracing) — OPCIONAL ---
    # Sem as duas chaves, o tracing fica DESLIGADO e o agente roda normal
    # (fail-safe: dev/testes sem rede e o período até as chaves serem preenchidas).
    # Host: https://cloud.langfuse.com (EU) ou https://us.cloud.langfuse.com (US).
    langfuse_public_key: str = Field("", alias="LANGFUSE_PUBLIC_KEY")
    langfuse_secret_key: str = Field("", alias="LANGFUSE_SECRET_KEY")
    langfuse_host: str = Field("https://cloud.langfuse.com", alias="LANGFUSE_HOST")

    @property
    def langfuse_enabled(self) -> bool:
        """Tracing só liga quando as duas chaves estão presentes."""
        return bool(self.langfuse_public_key and self.langfuse_secret_key)


@lru_cache
def get_settings() -> Settings:
    return Settings()  # type: ignore[call-arg]


class ConfigPrazosInvalida(ValueError):
    """A ordem dos prazos do turno está quebrada (erro de configuração no boot)."""


#: Folga mínima do turno para o JEV quando o chamador não informa o `JEV_TIMEOUT`.
FOLGA_JEV_PADRAO_S = 2.0

_DURACAO_GO = re.compile(r"(\d+(?:\.\d+)?)(ms|s|m|h)")
_UNIDADE_S = {"ms": 0.001, "s": 1.0, "m": 60.0, "h": 3600.0}


def duracao_em_segundos(texto: str, variavel: str) -> float:
    """ "90s"/"1m30s"/"1500ms" → segundos; inválido → erro citando a variável.

    Mesma regra do gateway (`time.ParseDuration`): número sem unidade só vale "0".
    """
    bruto = texto.strip()
    if bruto == "0":
        return 0.0
    partes = _DURACAO_GO.findall(bruto)
    if not partes or "".join(n + u for n, u in partes) != bruto:
        raise ConfigPrazosInvalida(
            f"{variavel} inválido ({texto!r}): use duração como 90s ou 1m30s"
        )
    return sum(float(n) * _UNIDADE_S[u] for n, u in partes)


def validar_prazos(
    settings: Settings, *, folga_jev: float = FOLGA_JEV_PADRAO_S, espera_tool_minima: float = 0.0
) -> None:
    """Confere a ordem dos prazos do turno; violação levanta `ConfigPrazosInvalida`.

    - cérebro: `BRAIN_TURNO_PRAZO` > espera da tool + folga do JEV (o turno paga a
      tool e o JEV; sem folga, a tool consome o turno inteiro). A espera da tool é
      `INFODESK_TOOLS_TIMEOUT` ou, se maior, o piso próprio de `abrir_chamado`
      (`espera_tool_minima`);
    - com `TURNO_TETO` no ambiente: `TURNO_TETO` > `BRAIN_TURNO_PRAZO`;
    - com `BRAIN_TIMEOUT` e `TURNO_TETO`: `BRAIN_TIMEOUT` > `TURNO_TETO`.
    """
    prazo = settings.brain_turno_prazo
    espera_tool = max(settings.infodesk_tools_timeout, espera_tool_minima)
    piso = espera_tool + folga_jev
    if prazo <= piso:
        raise ConfigPrazosInvalida(
            f"BRAIN_TURNO_PRAZO ({prazo:g}s) precisa ser maior que INFODESK_TOOLS_TIMEOUT "
            f"({espera_tool:g}s, com o piso do abrir_chamado) + folga do JEV ({folga_jev:g}s)"
        )
    teto = (
        duracao_em_segundos(settings.turno_teto_gateway, "TURNO_TETO")
        if settings.turno_teto_gateway.strip()
        else None
    )
    brain = (
        duracao_em_segundos(settings.brain_timeout_gateway, "BRAIN_TIMEOUT")
        if settings.brain_timeout_gateway.strip()
        else None
    )
    if teto is not None and teto <= prazo:
        raise ConfigPrazosInvalida(
            f"TURNO_TETO ({teto:g}s) precisa ser maior que BRAIN_TURNO_PRAZO ({prazo:g}s): "
            "o cérebro tem de desistir antes do gateway"
        )
    if brain is not None and teto is not None and brain <= teto:
        raise ConfigPrazosInvalida(
            f"BRAIN_TIMEOUT ({brain:g}s) precisa ser maior que TURNO_TETO ({teto:g}s)"
        )
    if brain is not None and teto is None and brain <= prazo:
        raise ConfigPrazosInvalida(
            f"BRAIN_TIMEOUT ({brain:g}s) precisa ser maior que BRAIN_TURNO_PRAZO ({prazo:g}s)"
        )
