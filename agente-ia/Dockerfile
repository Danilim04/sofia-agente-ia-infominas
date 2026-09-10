# ==============================================================================
# Cérebro Python (Contrato A) — imagem auto-contida: dependências + docs +
# ChromaDB JÁ INGERIDO em build-time (o modelo de embeddings local também fica
# baked no HF_HOME). Nada é baixado em runtime; o container sobe pronto.
#
# O ingest roda com OPENROUTER_API_KEY dummy: ele só usa embeddings LOCAIS
# (sentence-transformers), mas o Settings exige a chave — o valor real chega
# em runtime via .env do compose e sobrescreve qualquer resquício.
# ==============================================================================
FROM python:3.12-slim

WORKDIR /app
ENV PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1 \
    HF_HOME=/app/.hf \
    CHROMA_PERSIST_DIR=/app/chroma_db \
    DOCS_DIR=/app/docs

# torch CPU-only ANTES do requirements: o wheel default do PyPI arrasta CUDA
# (~3 GB de libs nvidia) que não servem pra nada numa VPS sem GPU.
COPY requirements.txt .
RUN pip install --index-url https://download.pytorch.org/whl/cpu torch && \
    pip install -r requirements.txt

COPY src/ src/
COPY docs/ docs/
COPY server.py .

# docs Markdown → chunks → ChromaDB persistido, SEMPRE com o modelo local
# fixo (CHROMA_EMBEDDINGS_MODEL em src/rag/ingest.py; baixado para o HF_HOME
# da imagem). É o índice de rollback offline. Em produção o agente lê o
# pgvector, embutido pelo OpenRouter (EMBEDDINGS_PROVIDER=openrouter).
RUN OPENROUTER_API_KEY=dummy-build-ingest \
    python -m src.rag.ingest --docs-dir docs --persist-dir /app/chroma_db

RUN useradd --uid 10001 --create-home app && chown -R app:app /app
# /data recebe o checkpointer SQLite (volume brain-data no compose). Criar o
# path JÁ com o owner certo faz o volume nomeado nascer gravável pelo user app
# — sem isto o server cai no fallback MemorySaver (histórico não persiste).
RUN mkdir -p /data && chown app:app /data
USER app

EXPOSE 8001
CMD ["python", "-m", "uvicorn", "server:app", "--host", "0.0.0.0", "--port", "8001"]
