# ==============================================================================
# Cérebro Python (Contrato A) — dependências + código + docs. NÃO há ingestão
# em build-time: em produção o índice do RAG vive no Postgres/pgvector e é
# populado pelo deploy (AzapDocs → pgvector, `src.rag.sync --fonte api`) e pelo
# cron diário; a imagem só precisa saber CONSULTAR. O Chroma continua
# disponível como backend legado de dev (VECTOR_BACKEND=chroma) — nesse caso
# rode `python -m src.rag.ingest` antes, fora da imagem.
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

RUN useradd --uid 10001 --create-home app && chown -R app:app /app
# /data recebe o checkpointer SQLite (volume brain-data no compose). Criar o
# path JÁ com o owner certo faz o volume nomeado nascer gravável pelo user app
# — sem isto o server cai no fallback MemorySaver (histórico não persiste).
RUN mkdir -p /data && chown app:app /data
USER app

EXPOSE 8001
CMD ["python", "-m", "uvicorn", "server:app", "--host", "0.0.0.0", "--port", "8001"]
