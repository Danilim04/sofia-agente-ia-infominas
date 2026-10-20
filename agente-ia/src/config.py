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
    # (classificador + agente) precisa caber no prazo do turno abaixo — 45s
    # estourava o BRAIN_TIMEOUT do gateway (60s) e o cliente recebia erro
    # enquanto o cérebro ainda trabalhava (e até abria chamado depois).
    llm_timeout: float = Field(20.0, alias="LLM_TIMEOUT")
    # Prazo TOTAL do turno no cérebro (segundos): `processar_chat` interrompe o
    # grafo e devolve falha transitória ao gateway. Tem de ser MENOR que o
    # BRAIN_TIMEOUT do gateway (60s) e que o teto do turno dele (TURNO_TETO,
    # 45s desde a mensagem do cliente): quem desiste primeiro é o cérebro, com
    # classe e ID — nunca o gateway no escuro.
    brain_turno_prazo: float = Field(40.0, alias="BRAIN_TURNO_PRAZO")
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
