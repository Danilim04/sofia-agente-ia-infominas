# ==============================================================================
# Cérebro Python da Sofia (Contrato A, POST /chat) — dependências + código +
# base de conhecimento placeholder. NÃO há ingestão em build-time: em produção o
# índice do RAG vive no Postgres/pgvector e é populado por
# `python -m src.rag.sync` (ver deploy/README.md); a imagem só precisa saber
# CONSULTAR. O Chroma é só backend de dev (VECTOR_BACKEND=chroma) — nesse caso
# rode `python -m src.rag.ingest` antes, fora da imagem.
#
# Taxonomia: a imagem leva uma cópia do placeholder em /app/config, mas o
# docker-compose monta a pasta config/ compartilhada em /config (somente leitura)
# e aponta TAXONOMIA_PATH para lá — a mesma que o gateway lê.
#
# Imagem: sofia-bot:brain-<tag> (compose; IMAGE_REPO/IMAGE_TAG).
# ==============================================================================
FROM python:3.12-slim

WORKDIR /app
ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    PIP_NO_CACHE_DIR=1 \
    HF_HOME=/app/.hf \
    CHROMA_PERSIST_DIR=/app/chroma_db \
    DOCS_DIR=/app/docs/base

# torch CPU-only ANTES do requirements: o wheel default do PyPI arrasta CUDA
# (~3 GB de libs nvidia) que não servem pra nada num host sem GPU.
# requirements.txt traz typesafe-sdk (JEV) e o resto do cérebro.
COPY requirements.txt .
RUN pip install --index-url https://download.pytorch.org/whl/cpu torch && \
    pip install -r requirements.txt

COPY src/ src/
COPY config/ config/
# Só a base de conhecimento (docs/base); o resto de agente-ia/docs é legado.
COPY docs/base/ docs/base/
COPY server.py .

# Guarda contra um .dockerignore que esvazie a base; /app/.hf nasce com o owner
# certo para o volume hf-cache (cache do modelo de embeddings).
RUN ls docs/base/*.md > /dev/null && mkdir -p /app/.hf
RUN useradd --uid 10001 --create-home app && chown -R app:app /app
# /data recebe o checkpointer SQLite (volume brain-data no compose). Criar o
# path JÁ com o owner certo faz o volume nomeado nascer gravável pelo user app
# — sem isto o server cai no fallback MemorySaver (histórico não persiste).
RUN mkdir -p /data && chown app:app /data
USER app

EXPOSE 8001
CMD ["python", "-m", "uvicorn", "server:app", "--host", "0.0.0.0", "--port", "8001"]
