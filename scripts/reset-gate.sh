#!/usr/bin/env bash
#
# reset-gate.sh — reseta o estado do gate de identidade do bot Azapfy numa VM.
#
# Apaga o SQLite do gate (gateway.db + sidecars -wal/-shm) e reinicia o serviço
# do gateway via docker compose. Serve para limpar um estado GateFalha preso
# (achado F1) ou zerar gate/cache/dedup antes de uma bateria de teste.
#
# É DESTRUTIVO: o db do gate é apagado. Por padrão faz backup remoto antes e
# pede confirmação. Use --yes para automação e --dry-run para só inspecionar.
#
# Uso:
#   ./reset-gate.sh -i <IP> -k <CHAVE_SSH> [opções]
#
# Exemplo:
#   ./reset-gate.sh -i 62.238.63.240 -k ~/.ssh/azapfy_bot
#   ./reset-gate.sh -i 62.238.63.240 -k ~/.ssh/azapfy_bot --dry-run
#   ./reset-gate.sh -i 62.238.63.240 -k ~/.ssh/azapfy_bot -y   # sem perguntar

set -euo pipefail

# ---- defaults ----
IP=""
KEY=""
USER="root"
PORT="22"
APP_DIR="/opt/azapfy-bot"
DB_PATH="data/gateway.db"   # relativo ao APP_DIR (no host)
SERVICE="gateway"
BACKUP="1"
ASSUME_YES="0"
DRY_RUN="0"

prog="$(basename "$0")"

usage() {
  cat <<EOF
$prog — reseta o gate (apaga gateway.db + reinicia o serviço) numa VM do bot.

Obrigatórios:
  -i, --ip <IP>          IP da VM do bot (ex.: 62.238.63.240)
  -k, --key <ARQUIVO>    caminho da chave SSH privada

Opções:
  -u, --user <USUARIO>   usuário SSH            (default: $USER)
  -P, --port <PORTA>     porta SSH             (default: $PORT)
  -d, --app-dir <DIR>    diretório do compose  (default: $APP_DIR)
      --db-path <REL>    db do gate relativo ao app-dir (default: $DB_PATH)
  -s, --service <NOME>   serviço do gateway no compose  (default: $SERVICE)
      --no-backup        NÃO faz backup do db antes de apagar
  -y, --yes              não pede confirmação (para automação)
  -n, --dry-run          mostra o que faria, sem conectar/executar
  -h, --help             esta ajuda

Segurança: por padrão faz backup remoto (gateway.db.bak-<timestamp>) e pede
confirmação antes de apagar. Requer docker compose no host remoto.
EOF
}

die() { echo "erro: $*" >&2; exit 1; }

# ---- parse args ----
while [[ $# -gt 0 ]]; do
  case "$1" in
    -i|--ip)       IP="${2:-}"; shift 2 ;;
    -k|--key)      KEY="${2:-}"; shift 2 ;;
    -u|--user)     USER="${2:-}"; shift 2 ;;
    -P|--port)     PORT="${2:-}"; shift 2 ;;
    -d|--app-dir)  APP_DIR="${2:-}"; shift 2 ;;
    --db-path)     DB_PATH="${2:-}"; shift 2 ;;
    -s|--service)  SERVICE="${2:-}"; shift 2 ;;
    --no-backup)   BACKUP="0"; shift ;;
    -y|--yes)      ASSUME_YES="1"; shift ;;
    -n|--dry-run)  DRY_RUN="1"; shift ;;
    -h|--help)     usage; exit 0 ;;
    *)             die "argumento desconhecido: $1 (use --help)" ;;
  esac
done

# ---- validação ----
[[ -n "$IP" ]]  || { usage; echo; die "faltou --ip"; }
[[ -n "$KEY" ]] || { usage; echo; die "faltou --key"; }
[[ -f "$KEY" ]] || die "chave SSH não encontrada: $KEY"

# perms da chave (ssh recusa chave permissiva)
perms="$(stat -c '%a' "$KEY" 2>/dev/null || stat -f '%A' "$KEY" 2>/dev/null || echo '')"
if [[ -n "$perms" && "$perms" =~ [0-7][0-7][4-7]$ ]]; then
  echo "aviso: a chave $KEY tem permissões $perms (muito abertas). Ajuste com: chmod 600 $KEY" >&2
fi

SSH_TARGET="${USER}@${IP}"
SSH_OPTS=(-i "$KEY" -p "$PORT"
          -o ConnectTimeout=10
          -o BatchMode=yes
          -o StrictHostKeyChecking=accept-new)

# ---- script remoto (roda no host da VM) ----
# usa env vars APP_DIR / SVC / DB / BACKUP injetadas na chamada ssh.
read -r -d '' REMOTE_SCRIPT <<'REMOTE' || true
set -e
command -v docker >/dev/null 2>&1 || { echo "erro: docker não encontrado no host remoto" >&2; exit 3; }
cd "$APP_DIR" 2>/dev/null || { echo "erro: app-dir inexistente: $APP_DIR" >&2; exit 3; }

echo "== compose ps (antes) =="
docker compose ps "$SVC" || true

if [ ! -f "$DB" ]; then
  echo "aviso: '$DB' não existe em $(pwd) — nada a apagar (talvez já limpo, ou --db-path diferente)."
else
  if [ "$BACKUP" = "1" ]; then
    BK="${DB}.bak-$(date +%Y%m%d-%H%M%S)"
    cp -f "$DB" "$BK"
    echo "backup criado: $(pwd)/$BK"
  fi
fi

echo "== parando serviço '$SVC' =="
docker compose stop "$SVC"

rm -f "$DB" "${DB}-wal" "${DB}-shm"
echo "removido: $DB (+ -wal/-shm se existiam)"

echo "== subindo serviço '$SVC' =="
docker compose up -d "$SVC"
sleep 2

echo "== compose ps (depois) =="
docker compose ps "$SVC"

echo "== últimas linhas de log =="
docker compose logs "$SVC" --tail 15 2>&1 || true

echo "OK: gate resetado."
REMOTE

# prefixo de env para o bash remoto (quoting seguro)
REMOTE_ENV="APP_DIR=$(printf '%q' "$APP_DIR") SVC=$(printf '%q' "$SERVICE") DB=$(printf '%q' "$DB_PATH") BACKUP=$(printf '%q' "$BACKUP")"

echo "Alvo:      $SSH_TARGET (porta $PORT)"
echo "App dir:   $APP_DIR"
echo "DB do gate:$DB_PATH   serviço: $SERVICE   backup: $([[ "$BACKUP" == 1 ]] && echo sim || echo não)"
echo

if [[ "$DRY_RUN" == "1" ]]; then
  echo "[dry-run] comando que seria executado:"
  echo "  ssh ${SSH_OPTS[*]} $SSH_TARGET \"$REMOTE_ENV bash -s\" <<'EOF'"
  echo "$REMOTE_SCRIPT" | sed 's/^/  | /'
  echo "  EOF"
  echo
  echo "[dry-run] nada foi executado."
  exit 0
fi

# ---- confirmação (destrutivo) ----
if [[ "$ASSUME_YES" != "1" ]]; then
  echo "Isto vai APAGAR $DB_PATH e reiniciar '$SERVICE' em $SSH_TARGET."
  read -r -p "Confirmar? [digite 'sim']: " resp
  [[ "$resp" == "sim" ]] || { echo "cancelado."; exit 130; }
fi

# ---- executa ----
ssh "${SSH_OPTS[@]}" "$SSH_TARGET" "$REMOTE_ENV bash -s" <<< "$REMOTE_SCRIPT"
