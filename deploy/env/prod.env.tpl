# ==============================================================================
# .env de PRODUÇÃO — renderizado pela pipeline (render-env.sh/envsubst) e
# gravado por inteiro em /opt/azapfy-bot/.env a cada deploy. NÃO EDITAR na VPS:
# qualquer mudança manual é sobrescrita no próximo deploy. Linha
# adicionada/removida AQUI reflete no servidor no próximo deploy.
#
# (Diferente do omni-route não há "bloco gerenciado": esta stack não gera
# segredos no servidor — TODO o .env vem de GitHub Secrets/vars, então o
# arquivo inteiro é gerenciado. O estado local são os volumes do compose —
# pgvector-data (Postgres: RAG + estado do gateway) e brain-data.)
# ==============================================================================

# --- Imagens (chegam via docker load — ver deploy-remote.sh) ------------------
IMAGE_REPO=azapfy-bot
IMAGE_TAG=${IMAGE_TAG}
COMPOSE_FILE=docker-compose.yml:docker-compose.prod.yml

# --- Cérebro: OpenRouter ------------------------------------------------------
OPENROUTER_API_KEY=${OPENROUTER_API_KEY}
OPENROUTER_BASE_URL=https://openrouter.ai/api/v1
# Agente no Gemini 3.5 Flash (decisão pós-Bloco A; caching implícito do
# Gemini — o cache_control explícito só liga em modelos anthropic/).
OPENROUTER_MODEL=google/gemini-3.5-flash
OPENROUTER_CLASSIFIER_MODEL=google/gemini-2.5-flash-lite
APP_REFERER=https://azapfy.com.br
APP_TITLE=Azapfy Suporte IA

# --- Cérebro: tuning ----------------------------------------------------------
LLM_TEMPERATURE=0.2
LLM_TIMEOUT=45
RAG_TOP_K=3
RAG_CHUNK_SIZE=800
RAG_CHUNK_OVERLAP=120
AGENT_MAX_ITERACOES=5
# Embeddings do índice pgvector pelo /embeddings do OpenRouter (mesma chave do
# LLM; multilíngue; ~US$0,02 por 1M tokens — a base inteira custa < 1 centavo).
# Trocar provider/modelo aqui = o deploy reconstrói o índice sozinho
# (--reconstruir-se-modelo-mudou). Rollback offline: EMBEDDINGS_PROVIDER=local
# + EMBEDDINGS_MODEL=sentence-transformers/all-MiniLM-L6-v2 (ou VECTOR_BACKEND=chroma).
EMBEDDINGS_PROVIDER=openrouter
EMBEDDINGS_MODEL=openai/text-embedding-3-small
# CHECKPOINT_DB_PATH vem do docker-compose.yml (volume brain-data:/data)

# --- Postgres (pgvector): RAG do cérebro + estado do gateway ------------------
# Serviço `pgvector` do compose. A URL é fiação do compose (PGVECTOR_URL no
# brain, PG_URL no gateway); aqui só a senha (GitHub Secret, obrigatória).
PGVECTOR_PASSWORD=${PGVECTOR_PASSWORD}
# Backend de LEITURA do RAG (virada feita em 2026-09-04): o agente lê o índice
# do pgvector, populado pela ingestão do deploy + cron diário (AzapDocs).
# Rollback = chroma (índice baked na imagem, só os docs/*.md do repo) + deploy.
VECTOR_BACKEND=pgvector

# --- Cérebro: fonte da base de conhecimento (AzapDocs, Contrato B) ------------
# Rotina diária: cron da VM (ansible, /usr/local/bin/azapfy-sync-docs) roda
# `docker compose run --rm brain python -m src.rag.sync --fonte api`.
# A chave (azk_...) vem do GitHub Secret DOCS_API_KEY; vazia = sync abortado.
DOCS_API_BASE_URL=https://intranet.azapfy.com.br/api/v1/integrations/docs
DOCS_API_KEY=${DOCS_API_KEY}
DOCS_API_TIMEOUT=30
# Webhook opcional de alerta do sync (vazio = só journalctl -t azapfy-sync).
SYNC_ALERTA_WEBHOOK=${SYNC_ALERTA_WEBHOOK}

# --- Observabilidade ----------------------------------------------------------
LOG_LEVEL=info

# --- Cérebro: Langfuse (tracing) — chaves via GitHub Secrets (opcional) --------
# Vazias = tracing desligado (o agente roda normal). Host default: cloud EU.
LANGFUSE_PUBLIC_KEY=${LANGFUSE_PUBLIC_KEY}
LANGFUSE_SECRET_KEY=${LANGFUSE_SECRET_KEY}
LANGFUSE_HOST=${LANGFUSE_HOST}

# --- Gateway: Chatwoot (por DENTRO da rede docker do servidor) ----------------
CHATWOOT_BASE_URL=http://chatwoot-rails:3000
CHATWOOT_ACCOUNT_ID=1
# Preenchido pelo ESTADO da VM (.chatwoot-token → apply-server-state.sh) por
# cima do que o CI renderizar; o secret é só bootstrap/override manual.
CHATWOOT_API_TOKEN=${CHATWOOT_API_TOKEN}
WEBHOOK_TOKEN=${WEBHOOK_TOKEN}
WEBHOOK_SECRET=
# Base interna que o Chatwoot alcança — vira a URL a cadastrar no webhook.
PUBLIC_BASE_URL=http://azapfy-bot:8080

# --- Gateway: etiquetas / gate ------------------------------------------------
LABEL_BOT=fila-bot
LABEL_HUMANO=fila-humano
# Caixa de entrada cujas conversas NOVAS o gateway etiqueta com fila-bot no
# conversation_created (5 = caixa "whatsapp-daniel-teste" da conta Omni Route,
# a caixa que o bot atende desde 2026-09-04 — a antiga 3 "whatsapp-infodesk"
# foi removida do Chatwoot; 0 = todas).
INBOX_ID=5
CONFIRM_FIELD=email
MAX_TENTATIVAS=3
IDENTITY_TTL=24h
# F1: conversa em "falha" volta a ser atendida depois deste TTL (rede de
# segurança — a conversa RESOLVIDA no Chatwoot zera o estado do gate na hora).
GATE_FALHA_TTL=1h

# --- Gateway: robustez de transporte (Bloco A) --------------------------------
# Coalescência de rajadas (F11): silêncio que fecha o lote / teto acumulado.
DEBOUNCE_JANELA=8s
DEBOUNCE_TETO=20s
# Descarta mensagens reentregues antigas (F10).
EVENTO_IDADE_MAX=10m
# Divide respostas longas em mensagens de até N chars no WhatsApp (F12).
REPLY_MAX_CHARS=900

# --- Gateway: Mongo da Azapfy -------------------------------------------------
MONGO_URI=${MONGO_URI}
MONGO_DB=azapfy3
MONGO_COLLECTION=users
MONGO_TIMEOUT=8s
BRAIN_TIMEOUT=60s

# --- SAC / chamados -----------------------------------------------------------
SAC_BASE_URL=https://api3.azapfy.com.br
SAC_PORTAL_URL=https://atendimento.azapfy.com.br
SAC_SERVICE_COD=${SAC_SERVICE_COD}
SAC_GRUPO_EMP=AZAPERS
SAC_EMPRESA=AZAPERS
SAC_TIMEZONE=America/Sao_Paulo
SAC_API_TOKEN=${SAC_API_TOKEN}
SAC_CONFIG_TTL=10m
# Segredo compartilhado cérebro↔gateway (X-Tools-Token) — mesmo valor nos dois:
TOOLS_API_TOKEN=${TOOLS_API_TOKEN}
SAC_TOOLS_TOKEN=${TOOLS_API_TOKEN}
SAC_TOOLS_TIMEOUT=30
