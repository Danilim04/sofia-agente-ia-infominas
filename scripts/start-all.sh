#!/usr/bin/env bash
#
# start-all.sh — sobe os 3 serviços do monorepo já conectados entre si:
#
#   cérebro (agente-ia, FastAPI :8001)
#        ▲  POST /chat
#        │
#   backend (Go, :8080) ──── responde no ──►  mock-chatwoot (:9000)
#        ▲  webhook                                │
#        └──────────── message_created ◄───────────┘
#
# A "cola" entre mock e backend é o WEBHOOK_TOKEN: este script lê o token do
# backend/.env e o repassa ao mock, garantindo que os dois sempre casem.
# Também força BRAIN_BASE_URL / CHATWOOT_BASE_URL / GO_WEBHOOK_URL para as
# portas usadas aqui, então os três processos já sobem falando uns com os outros.
#
# Uso:
#   scripts/start-all.sh            # sobe tudo e segue logando até Ctrl+C
#   BRAIN_PORT=8001 BACKEND_PORT=8080 MOCK_PORT=9000 scripts/start-all.sh
#   WEBHOOK_TOKEN=outro-token scripts/start-all.sh   # sobrescreve o token
#
# Ctrl+C derruba os três (cada um roda no seu próprio process group).

set -euo pipefail

# --------------------------------------------------------------------------
# Caminhos
# --------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
LOG_DIR="$SCRIPT_DIR/logs"
mkdir -p "$LOG_DIR"

# --------------------------------------------------------------------------
# Portas (podem ser sobrescritas via env)
# --------------------------------------------------------------------------
BRAIN_PORT="${BRAIN_PORT:-8001}"
BACKEND_PORT="${BACKEND_PORT:-8080}"
MOCK_PORT="${MOCK_PORT:-9000}"

# --------------------------------------------------------------------------
# Helpers de cor / log
# --------------------------------------------------------------------------
if [[ -t 1 ]]; then
  C_BLUE=$'\033[34m'; C_GREEN=$'\033[32m'; C_RED=$'\033[31m'
  C_YEL=$'\033[33m'; C_DIM=$'\033[2m'; C_RST=$'\033[0m'
else
  C_BLUE=""; C_GREEN=""; C_RED=""; C_YEL=""; C_DIM=""; C_RST=""
fi
info()  { printf '%s\n' "${C_BLUE}▶${C_RST} $*"; }
ok()    { printf '%s\n' "${C_GREEN}✓${C_RST} $*"; }
warn()  { printf '%s\n' "${C_YEL}!${C_RST} $*"; }
err()   { printf '%s\n' "${C_RED}✗${C_RST} $*" >&2; }

# Lê KEY=VALUE de um arquivo .env (última ocorrência), sem aspas/espaços.
read_env() {
  local file="$1" key="$2"
  [[ -f "$file" ]] || return 0
  grep -E "^[[:space:]]*${key}=" "$file" 2>/dev/null | tail -n1 \
    | sed -E "s/^[[:space:]]*${key}=//; s/^[\"']//; s/[\"'][[:space:]]*$//; s/[[:space:]]*$//"
}

# --------------------------------------------------------------------------
# Token compartilhado mock <-> backend
# --------------------------------------------------------------------------
WEBHOOK_TOKEN="${WEBHOOK_TOKEN:-$(read_env "$ROOT_DIR/backend/.env" WEBHOOK_TOKEN)}"
WEBHOOK_TOKEN="${WEBHOOK_TOKEN:-dev-token}"

# --------------------------------------------------------------------------
# Pré-checagens
# --------------------------------------------------------------------------
need() { command -v "$1" >/dev/null 2>&1 || { err "comando '$1' não encontrado no PATH"; exit 1; }; }
need go
need python3
need curl

[[ -f "$ROOT_DIR/agente-ia/venv/bin/activate" ]] \
  || { err "venv do agente não existe: agente-ia/venv (rode: cd agente-ia && python -m venv venv && pip install -r requirements.txt)"; exit 1; }
[[ -f "$ROOT_DIR/agente-ia/.env" ]] \
  || warn "agente-ia/.env não encontrado — o cérebro pode falhar sem OPENROUTER_API_KEY"
if [[ ! -e "$ROOT_DIR/agente-ia/chroma_db/chroma.sqlite3" ]]; then
  warn "chroma_db vazio — rode 'cd agente-ia && source venv/bin/activate && python -m src.rag.ingest' antes (RAG não responderá)"
fi
[[ -f "$ROOT_DIR/backend/.env" ]] \
  || warn "backend/.env não encontrado — o backend Go pode falhar (CHATWOOT_BASE_URL/MONGO_URI obrigatórios)"

# --------------------------------------------------------------------------
# Gerência de processos: cada serviço roda no seu próprio process group
# (setsid) para que o Ctrl+C derrube também os filhos (ex.: o binário que o
# 'go run' compila e executa).
# --------------------------------------------------------------------------
declare -a PIDS=() NAMES=()

cleanup() {
  trap '' INT TERM
  echo
  info "Parando serviços..."
  for i in "${!PIDS[@]}"; do
    local pid="${PIDS[$i]}"
    kill -TERM "-${pid}" 2>/dev/null || kill -TERM "${pid}" 2>/dev/null || true
  done
  # dá um tempo para saída limpa, depois força
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    local alive=0
    for pid in "${PIDS[@]}"; do kill -0 "$pid" 2>/dev/null && alive=1; done
    [[ $alive -eq 0 ]] && break
    sleep 0.3
  done
  for pid in "${PIDS[@]}"; do
    kill -KILL "-${pid}" 2>/dev/null || kill -KILL "${pid}" 2>/dev/null || true
  done
  ok "Tudo parado."
}
trap cleanup INT TERM EXIT

# start_service <nome> <comando-shell>
start_service() {
  local name="$1" cmd="$2"
  local log="$LOG_DIR/${name}.log"
  : > "$log"
  setsid bash -c "$cmd" >"$log" 2>&1 &
  local pid=$!
  PIDS+=("$pid"); NAMES+=("$name")
  info "${name} iniciado (pid ${pid}) → ${C_DIM}${log}${C_RST}"
}

# port_open <porta>  -> 0 se aceitando conexão em 127.0.0.1
port_open() { (exec 3<>"/dev/tcp/127.0.0.1/$1") >/dev/null 2>&1; }

# wait_ready <nome> <pid> <timeout_s> <check-cmd...>
wait_ready() {
  local name="$1" pid="$2" timeout="$3"; shift 3
  local i=0
  while (( i < timeout )); do
    if ! kill -0 "$pid" 2>/dev/null; then
      err "${name} morreu durante a inicialização. Fim do log:"
      tail -n 25 "$LOG_DIR/${name}.log" >&2
      return 1
    fi
    if "$@" >/dev/null 2>&1; then
      ok "${name} pronto."
      return 0
    fi
    sleep 1; (( i++ ))
  done
  err "timeout (${timeout}s) esperando ${name}. Fim do log:"
  tail -n 25 "$LOG_DIR/${name}.log" >&2
  return 1
}

# --------------------------------------------------------------------------
# Banner
# --------------------------------------------------------------------------
echo
info "Subindo o stack Azapfy (mock + cérebro + backend)"
printf '  %s\n' "${C_DIM}cérebro  : http://localhost:${BRAIN_PORT}${C_RST}"
printf '  %s\n' "${C_DIM}backend  : http://localhost:${BACKEND_PORT}${C_RST}"
printf '  %s\n' "${C_DIM}mock     : http://localhost:${MOCK_PORT}${C_RST}"
printf '  %s\n' "${C_DIM}token    : ${WEBHOOK_TOKEN} (mock ↔ backend)${C_RST}"
echo

# --------------------------------------------------------------------------
# 1) Cérebro (agente-ia) — precisa subir primeiro: o backend chama /chat
# --------------------------------------------------------------------------
start_service "cerebro" "
  cd '$ROOT_DIR/agente-ia'
  source venv/bin/activate
  exec uvicorn server:app --host 0.0.0.0 --port $BRAIN_PORT
"
wait_ready "cerebro" "${PIDS[-1]}" 60 curl -fsS "http://localhost:${BRAIN_PORT}/health" \
  || { err "cérebro não respondeu em /health — abortando."; exit 1; }

# --------------------------------------------------------------------------
# 2) Backend Go — conectado ao cérebro (BRAIN_BASE_URL) e ao mock
#    (CHATWOOT_BASE_URL). Token forçado para casar com o mock.
# --------------------------------------------------------------------------
start_service "backend" "
  cd '$ROOT_DIR/backend'
  export PORT='$BACKEND_PORT'
  export WEBHOOK_TOKEN='$WEBHOOK_TOKEN'
  export BRAIN_BASE_URL='http://localhost:$BRAIN_PORT'
  export CHATWOOT_BASE_URL='http://localhost:$MOCK_PORT'
  exec go run ./cmd/bot
"
wait_ready "backend" "${PIDS[-1]}" 120 port_open "$BACKEND_PORT" \
  || { err "backend não subiu (provável Mongo inacessível — veja o log acima). Abortando."; exit 1; }

# --------------------------------------------------------------------------
# 3) Mock Chatwoot — aponta o webhook para o backend e usa o MESMO token.
# --------------------------------------------------------------------------
start_service "mock" "
  cd '$ROOT_DIR/mock-chatwoot'
  export WEBHOOK_TOKEN='$WEBHOOK_TOKEN'
  export GO_WEBHOOK_URL='http://localhost:$BACKEND_PORT/webhook'
  exec python3 mock_chatwoot.py --port $MOCK_PORT
"
wait_ready "mock" "${PIDS[-1]}" 30 port_open "$MOCK_PORT" \
  || { err "mock não subiu. Abortando."; exit 1; }

# --------------------------------------------------------------------------
# Pronto
# --------------------------------------------------------------------------
echo
ok "Stack no ar! Abra o front do mock e converse:"
printf '   %s\n' "${C_GREEN}http://localhost:${MOCK_PORT}${C_RST}"
echo
info "Logs ao vivo: tail -f ${LOG_DIR}/{cerebro,backend,mock}.log"
info "Ctrl+C para derrubar os três."
echo

# Segue vivo, espelhando os logs até Ctrl+C.
tail -n 0 -F "$LOG_DIR/cerebro.log" "$LOG_DIR/backend.log" "$LOG_DIR/mock.log" &
PIDS+=("$!"); NAMES+=("tail")
wait
