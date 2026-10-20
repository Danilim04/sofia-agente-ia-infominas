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
IMAGE_REPO=sofia-bot
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
LLM_TIMEOUT=20
RAG_TOP_K=3
RAG_CHUNK_SIZE=800
RAG_CHUNK_OVERLAP=120
AGENT_MAX_ITERACOES=5
# Embeddings do índice pgvector pelo /embeddings do OpenRouter (mesma chave do
# LLM; multilíngue; ~US$0,02 por 1M tokens — a base inteira custa < 1 centavo).
# Trocar provider/modelo aqui = o deploy reconstrói o índice sozinho
# (--reconstruir-se-modelo-mudou). Rollback offline: EMBEDDINGS_PROVIDER=local
# + EMBEDDINGS_MODEL=sentence-transformers/all-MiniLM-L6-v2 (índice refeito no pgvector).
EMBEDDINGS_PROVIDER=openrouter
EMBEDDINGS_MODEL=openai/text-embedding-3-small
# CHECKPOINT_DB_PATH vem do docker-compose.yml (volume brain-data:/data)

# --- Postgres (pgvector): RAG do cérebro + estado do gateway ------------------
# Serviço `pgvector` do compose. A URL é fiação do compose (PGVECTOR_URL no
# brain, PG_URL no gateway); aqui só a senha (GitHub Secret, obrigatória).
PGVECTOR_PASSWORD=${PGVECTOR_PASSWORD}
# Backend de LEITURA do RAG (virada feita em 2026-09-04): o agente lê o índice
# do pgvector, populado pela ingestão do deploy + cron diário (AzapDocs).
# A imagem NÃO embute índice Chroma: pgvector é o único backend em produção.
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
# HMAC do webhook (X-Chatwoot-Signature sobre "{timestamp}.{corpo}"): secret
# OPCIONAL do GitHub. Vazio = sem verificação (comportamento atual: o Chatwoot
# em uso não assina). Só preencher quando o lado do Chatwoot passar a assinar
# com o MESMO segredo — senão todo webhook é recusado (401).
WEBHOOK_SECRET=${WEBHOOK_SECRET}
# Base interna que o Chatwoot alcança — vira a URL a cadastrar no webhook.
PUBLIC_BASE_URL=http://azapfy-bot:8080

# --- Gateway: etiquetas / gate ------------------------------------------------
LABEL_BOT=fila-bot
LABEL_HUMANO=fila-humano
# Caixa de entrada do bot: conversas NOVAS dela ganham fila-bot no
# conversation_created e SÓ mensagens dela são atendidas (ZD-2). O id é
# ESPECÍFICO da instância do Chatwoot (na VM antiga era 5,
# "whatsapp-daniel-teste"); vem da GitHub variable INBOX_ID — OBRIGATÓRIA e
# > 0: o render-env.sh aborta o deploy sem ela (0 = todas as caixas, só dev).
# A pipeline "Configurar Chatwoot" lista as caixas existentes ao final.
INBOX_ID=${INBOX_ID}
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
# Equipe (desk) do SAC que atende os chamados do bot. A EMPRESA do chamado
# (incidente.empresa) é sempre a do cliente, vinda do cadastro — não há valor fixo.
SAC_GRUPO_EMP=AZAPERS
SAC_TIMEZONE=America/Sao_Paulo
SAC_API_TOKEN=${SAC_API_TOKEN}
SAC_CONFIG_TTL=10m
# Segredo compartilhado cérebro↔gateway (X-Tools-Token) — mesmo valor nos dois:
TOOLS_API_TOKEN=${TOOLS_API_TOKEN}
SAC_TOOLS_TOKEN=${TOOLS_API_TOKEN}
SAC_TOOLS_TIMEOUT=30

# --- Cérebro: resiliência a falhas (lote ZC) ---------------------------------
# Teto de tokens de SAÍDA por chamada ao OpenRouter. Sem teto, o OpenRouter
# reserva 65.536 tokens por chamada e devolve 402 quando o saldo não cobre a
# reserva. Agente: 1024 (respostas curtas + tool calls); classificador e
# extrator de login (mesmo cliente): 256.
LLM_MAX_TOKENS=1024
LLM_MAX_TOKENS_CLASSIFICADOR=256
# Prazo TOTAL do turno no cérebro (s): menor que TURNO_TETO (45s) e que
# BRAIN_TIMEOUT (60s) — o cérebro desiste primeiro e devolve falha classificada.
BRAIN_TURNO_PRAZO=40
# Equipe do Chatwoot que recebe a conversa transferida pelo bot (ZC-3); id da
# instância (GitHub variable TEAM_HUMANO_ID; vazia = 0 = só troca a etiqueta).
TEAM_HUMANO_ID=${TEAM_HUMANO_ID}
# Teto do turno desde a mensagem do cliente (coalescência + cérebro + novas
# tentativas em falha transitória: 2 s e 5 s). Falha definitiva não repete.
TURNO_TETO=45s
# Alertas ao responsável (falha definitiva, >5% de turnos com falha em 15 min,
# disjuntor aberto, etiqueta reportar-zapin): sempre no log `zapin_alerta`;
# opcionalmente POST JSON {"text": ...} nesta URL (GitHub Secret; vazio = só log).
ALERTA_WEBHOOK_URL=${ALERTA_WEBHOOK_URL}
# URL pública do Chatwoot (link da conversa nos alertas; CHATWOOT_BASE_URL é
# interna à rede docker). GitHub variable; vazia = usa a interna.
CHATWOOT_PUBLIC_URL=${CHATWOOT_PUBLIC_URL}
# Etiqueta que o atendente aplica para reportar um problema do bot: o gateway
# avisa o responsável com ID da ocorrência, causa e link (vazio = desligado).
# A etiqueta/macro em si é criada à mão no Chatwoot.
LABEL_REPORTAR=reportar-zapin

# --- Gateway: quando o bot age (lote ZD) -------------------------------------
# Id do usuário do Chatwoot que o gateway usa (o "Zapin"). Conversa cujo
# responsável (assignee) é OUTRO usuário está com atendente humano: o bot não
# responde nem adota. Vazio = o gateway descobre no boot pelo próprio token
# (GET /api/v1/profile com o .chatwoot-token) — não precisa de variable;
# preencha só para forçar um id (se a descoberta falhar, qualquer responsável
# conta como humano).
BOT_USER_ID=
