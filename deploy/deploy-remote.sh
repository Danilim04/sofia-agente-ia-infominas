#!/usr/bin/env bash
# ==============================================================================
# Deploy remoto — FONTE ÚNICA do deploy (pipeline e break-glass).
# Roda no RUNNER (ou na máquina de quem depura), nunca no servidor.
#
#   deploy-remote.sh --host <ip> --version <tag> --env-file <env-renderizado> \
#     --key <chave-ssh> [--user deploy] [--build-on-host | --skip-build]
#
# Onde as imagens nascem (3 modos, mesma saída: sofia-bot:{brain,gateway}-<v>
# no docker da VPS):
#   · (default)        build LOCAL aqui + `docker save | ssh docker load`
#   · --skip-build     imagens já existem aqui (o CI buildou) → só save|load
#   · --build-on-host  fontes rastreadas pelo git vão por rsync para
#                      /opt/azapfy-bot/src e o build roda NA VM
#                      (deploy/build-on-host.sh). É o modo da VM atual: ela é
#                      ARM64 e o runner do GitHub é x86_64 — build nativo em vez
#                      de emulação QEMU (torch + ingestão emulados = lento/frágil).
#
# Sem registry de imagens em nenhum modo (não há credencial de Docker Hub/GHCR
# no projeto; para migrar p/ registry no futuro, troque o passo 3 por push+pull).
#
# O servidor recebe SÓ o necessário pra rodar a stack (allowlist + rsync
# --delete): compose files, healthcheck, scripts de env/build (e as fontes, no
# modo --build-on-host). O .env chega por stdin (nunca por argv) e é reescrito
# POR INTEIRO a cada deploy — estado local são os volumes do compose
# (pgvector-data: Postgres com o índice do RAG e o estado do gateway;
# brain-data: checkpoints) e os arquivos de estado .chatwoot-token /
# .zapin-credentials, que o rsync nunca toca.
#
# A base de conhecimento é ingerida A CADA deploy (passo 5): AzapDocs →
# pgvector, rodando na imagem nova do brain ANTES de o brain subir — o agente
# nasce lendo um índice populado. Entre deploys, o cron diário da VM
# (azapfy-sync-docs) mantém o índice atualizado.
#
# SSH_EXTRA_OPTS (env, opcional): opções extras para o ssh/rsync, ex.
# "-o UserKnownHostsFile=/caminho" quando a VM foi reprovisionada e o
# known_hosts local ainda guarda a chave antiga do IP.
# ==============================================================================
set -euo pipefail
cd "$(dirname "$0")/.."

HOST='' VERSION='' ENV_FILE='' KEY='' SSH_USER='deploy' BUILD=local
while [ $# -gt 0 ]; do
  case "$1" in
    --host)          HOST="$2"; shift 2 ;;
    --version)       VERSION="$2"; shift 2 ;;
    --env-file)      ENV_FILE="$2"; shift 2 ;;
    --key)           KEY="$2"; shift 2 ;;
    --user)          SSH_USER="$2"; shift 2 ;;
    --skip-build)    BUILD=skip; shift ;;
    --build-on-host) BUILD=host; shift ;;
    *) echo "ERRO: parâmetro desconhecido: $1" >&2; exit 1 ;;
  esac
done
[ -n "$HOST" ] && [ -n "$VERSION" ] && [ -n "$ENV_FILE" ] && [ -n "$KEY" ] ||
  { echo "Uso: deploy-remote.sh --host <ip> --version <tag> --env-file <arq> --key <chave> [--build-on-host|--skip-build]" >&2; exit 1; }
[ -f "$ENV_FILE" ] || { echo "ERRO: env-file não existe: $ENV_FILE" >&2; exit 1; }

SSH_OPTS=(-i "$KEY" -o StrictHostKeyChecking=accept-new -o ConnectTimeout=15)
if [ -n "${SSH_EXTRA_OPTS:-}" ]; then
  read -r -a _extra <<< "$SSH_EXTRA_OPTS"
  SSH_OPTS+=("${_extra[@]}")
fi
SSH=(ssh "${SSH_OPTS[@]}" "${SSH_USER}@${HOST}")
DEST=/opt/azapfy-bot
REPO=sofia-bot
BRAIN_IMG="$REPO:brain-$VERSION"
GATEWAY_IMG="$REPO:gateway-$VERSION"

case "$BUILD" in
  local)
    echo "→ [1/8] Build LOCAL das imagens ($VERSION)…"
    docker build -t "$BRAIN_IMG" agente-ia/
    docker build -t "$GATEWAY_IMG" backend/ ;;
  skip)
    echo "→ [1/8] Build pulado (--skip-build) — usando imagens locais existentes."
    docker image inspect "$BRAIN_IMG" "$GATEWAY_IMG" > /dev/null ;;
  host)
    echo "→ [1/8] Build NA VM (--build-on-host): fontes seguem no passo 2, build no passo 3." ;;
esac

echo "→ [2/8] Allowlist + rsync da stack…"
staging="$(mktemp -d)"
trap 'rm -rf "$staging"' EXIT
mkdir -p "$staging/deploy/env"
cp docker-compose.yml docker-compose.prod.yml "$staging/"
cp deploy/healthcheck.sh deploy/build-on-host.sh "$staging/deploy/"
mkdir -p "$staging/deploy/chatwoot" && cp deploy/chatwoot/setup-bot.sh "$staging/deploy/chatwoot/"
cp deploy/env/apply-server-state.sh "$staging/deploy/env/"
if [ "$BUILD" = host ]; then
  # Só o que o git rastreia (nada de venv/, chroma_db/, .env, __pycache__):
  # o mesmo conteúdo que um checkout do CI teria, com as modificações locais.
  mkdir -p "$staging/src"
  git ls-files -z agente-ia backend | rsync -a --from0 --files-from=- ./ "$staging/src/"
fi
# --exclude protege o ESTADO da VM: .env (reescrito no passo 4) e os arquivos
# gravados pela pipeline configure-chatwoot (.chatwoot-token, .zapin-credentials).
rsync -az --delete --exclude='.env' --exclude='.chatwoot-token' --exclude='.zapin-credentials' \
  -e "ssh ${SSH_OPTS[*]}" \
  "$staging/" "${SSH_USER}@${HOST}:${DEST}/"
"${SSH[@]}" "chmod +x $DEST/deploy/healthcheck.sh $DEST/deploy/build-on-host.sh $DEST/deploy/env/apply-server-state.sh"

if [ "$BUILD" = host ]; then
  echo "→ [3/8] Build nativo na VM (deploy/build-on-host.sh $VERSION)…"
  "${SSH[@]}" "$DEST/deploy/build-on-host.sh '$VERSION'"
else
  echo "→ [3/8] Transferindo imagens (docker save | ssh docker load)…"
  docker save "$BRAIN_IMG" "$GATEWAY_IMG" | gzip | "${SSH[@]}" 'gunzip | docker load'
fi

echo "→ [4/8] .env (por stdin, chmod 600) + estado do servidor…"
"${SSH[@]}" "umask 077 && cat > $DEST/.env" < "$ENV_FILE"
# CHATWOOT_API_TOKEN vem do estado da VM (.chatwoot-token) quando existir —
# ver deploy/env/apply-server-state.sh.
"${SSH[@]}" "$DEST/deploy/env/apply-server-state.sh"

echo "→ [5/8] Postgres no ar + ingestão da base de conhecimento (AzapDocs → pgvector)…"
"${SSH[@]}" "cd $DEST && docker compose up -d --wait --wait-timeout 180 pgvector"
# Roda na imagem NOVA do brain (mesmo código e modelo de embeddings baked),
# como container efêmero. Falha PARCIAL (ex.: um documento vazio na fonte) não
# derruba o deploy: o índice é fail-soft por documento e o cron diário insiste.
# O que derruba é índice VAZIO — portão do healthcheck no passo 7.
# --reconstruir-se-modelo-mudou: trocar EMBEDDINGS_PROVIDER/MODEL no .env é
# uma decisão de deploy — o índice é refeito no novo espaço vetorial aqui
# mesmo (o cron diário continua estrito e abortaria).
# Sem DOCS_API_KEY no .env a fonte oficial (AzapDocs) é inalcançável: em vez de
# subir um bot que não sabe nada (índice vazio = healthcheck falha), o deploy
# cai para a fonte LOCAL — os docs/*.md do repo, que já estão na imagem em
# /app/docs. Os ids não colidem (local = nome do arquivo, API = id numérico):
# quando a chave chegar, o próximo deploy acrescenta a base oficial; para
# tirar os docs do repo do índice, ver deploy/README.md ("fonte local").
if "${SSH[@]}" "cd $DEST && grep -qE '^DOCS_API_KEY=.+' .env"; then
  FONTE=api
else
  FONTE=local
  echo "   AVISO: DOCS_API_KEY vazia — ingerindo a fonte LOCAL (docs/*.md da imagem) para o bot subir."
  echo "   Cadastre o secret DOCS_API_KEY e faça um novo deploy para indexar o AzapDocs."
fi
if "${SSH[@]}" "cd $DEST && docker compose run --rm -T brain python -m src.rag.sync --fonte $FONTE --reconstruir-se-modelo-mudou"; then
  echo "   ingestão ($FONTE) concluída sem falhas."
else
  echo "   AVISO: ingestão ($FONTE) terminou com falha/trava (exit != 0) — ver a saída acima."
  echo "   O índice já existente continua servindo; corrija a fonte e rode: sudo -u deploy azapfy-sync-docs"
fi

# Bootstrap do Chatwoot numa VM NOVA: o gateway recusa subir sem
# CHATWOOT_API_TOKEN, e esse token só existe depois que o bot foi provisionado
# no omni-route (pipeline configure-chatwoot → .chatwoot-token). Sem isto o
# primeiro deploy de toda VM nova morria no healthcheck. Se o estado não
# existe, roda o MESMO setup-bot.sh da pipeline do Chatwoot (idempotente) aqui;
# rotações posteriores continuam pela pipeline dedicada.
if ! "${SSH[@]}" "test -s $DEST/.chatwoot-token"; then
  echo "→ [5b/8] .chatwoot-token ausente — provisionando o bot no omni-route (setup-bot.sh)…"
  "${SSH[@]}" "docker run --rm -i --network omniroute_default \
      -v /opt/omni-route/.env:/omni.env:ro -v $DEST:/state \
      -e HOST_UID=\$(id -u) -e HOST_GID=\$(id -g) \
      alpine:3.22 sh -s" < deploy/chatwoot/setup-bot.sh
  "${SSH[@]}" "$DEST/deploy/env/apply-server-state.sh"
fi

echo "→ [6/8] Up da stack (sem build)…"
"${SSH[@]}" "cd $DEST && docker compose up -d --remove-orphans --wait --wait-timeout 300"

echo "→ [7/8] Healthcheck (Postgres, índice do RAG, brain, gateway, rede do Chatwoot)…"
"${SSH[@]}" "cd $DEST && ./deploy/healthcheck.sh"

echo "→ [8/8] Limpeza: mantém as 3 tags mais recentes de cada serviço…"
"${SSH[@]}" "for svc in brain gateway; do
  docker images '$REPO' --format '{{.Tag}} {{.CreatedAt}}' \
    | grep \"^\$svc-\" | sort -rk2 | tail -n +4 | awk '{print \$1}' \
    | xargs -r -I{} docker rmi '$REPO:{}' || true
done; docker image prune -f > /dev/null; docker builder prune -f --keep-storage 20GB > /dev/null 2>&1 || true"

echo "✔ Deploy $VERSION concluído."
