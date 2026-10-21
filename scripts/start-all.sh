#!/usr/bin/env bash
#
# start-all.sh — sobe os 3 serviços do monorepo já conectados entre si:
#
#   mock-infodesk (:9000) ──webhook assinado──►  gateway Go (:8080) ──POST /chat──►  cérebro (:8001)
#          ▲                                          │
#          └────── POST /api/integration/v1/... ◄─────┘  (gateway "fala com o Infodesk" = no mock)
#
# A "cola" mock ↔ gateway são três valores forçados aqui para sempre casarem:
#   INFODESK_API_KEY        = MOCK_API_KEY         (default ik_test_mock)
#   INFODESK_WEBHOOK_SECRET = MOCK_WEBHOOK_SECRET  (default segredo-mock)
#   INFODESK_BASE_URL       = http://localhost:$MOCK_PORT/api
#   GATEWAY_WEBHOOK_URL     = http://localhost:$BACKEND_PORT/webhook
#
# ATENÇÃO (Onda 1): o gateway só passa a falar com o mock a partir da etapa
# E2.1 (webhook do Infodesk + cliente ligados). Até lá o passo 3 tende a
# FALHAR (o gateway ainda exige variáveis do domínio antigo); o script avisa e
# segue com mock + cérebro no ar — a UI do mock mostra as entregas falhando.
# A partir de E2.1, exporte GATEWAY_OBRIGATORIO=1 para abortar se ele não subir.
#
# Uso:
#   scripts/start-all.sh            # sobe tudo e segue logando até Ctrl+C
#   BRAIN_PORT=8001 BACKEND_PORT=8080 MOCK_PORT=9000 scripts/start-all.sh
#   MOCK_API_KEY=outra MOCK_WEBHOOK_SECRET=outro scripts/start-all.sh
#   MOCK_429_A_CADA=10 MOCK_SEM_REENTREGA=1 scripts/start-all.sh   # 429 periódico; sem reentrega automática de webhook
#
# Ctrl+C derruba os três. Windows: rode no Git Bash (sem setsid, cada serviço
# é morto pelo pid; sobras → scripts/stop-all.sh).

set -euo pipefail

# --------------------------------------------------------------------------
# Caminhos
# --------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
LOG_DIR="$SCRIPT_DIR/logs"
mkdir -p "$LOG_DIR"

# --------------------------------------------------------------------------
# Portas e segredos de dev (podem ser sobrescritos via env)
# --------------------------------------------------------------------------
BRAIN_PORT="${BRAIN_PORT:-8001}"
BACKEND_PORT="${BACKEND_PORT:-8080}"
MOCK_PORT="${MOCK_PORT:-9000}"
MOCK_API_KEY="${MOCK_API_KEY:-ik_test_mock}"
MOCK_WEBHOOK_SECRET="${MOCK_WEBHOOK_SECRET:-segredo-mock}"
MOCK_ORG_ID="${MOCK_ORG_ID:-44444444-4444-4444-8444-444444444401}"
TOOLS_API_TOKEN="${TOOLS_API_TOKEN:-dev-token}"

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

# --------------------------------------------------------------------------
# Python: venv do agente se existir (Linux/macOS ou Windows), senão o do PATH.
# No Windows o alias `python` da Store pode falhar — ver skill ambiente-testes.
# --------------------------------------------------------------------------
PY=""
for cand in "$ROOT_DIR/agente-ia/venv/bin/python" "$ROOT_DIR/agente-ia/venv/Scripts/python.exe" \
            "$ROOT_DIR/agente-ia/.venv/bin/python" "$ROOT_DIR/agente-ia/.venv/Scripts/python.exe"; do
  [[ -x "$cand" ]] && PY="$cand" && break
done
PY_VENV="$PY"
if [[ -z "$PY" ]]; then
  command -v python3 >/dev/null 2>&1 && PY=python3
  [[ -z "$PY" ]] && command -v python >/dev/null 2>&1 && PY=python
fi
# O mock é stdlib: qualquer Python 3.12+ serve (MOCK_PYTHON sobrescreve).
MOCK_PYTHON="${MOCK_PYTHON:-$PY}"

# --------------------------------------------------------------------------
# Pré-checagens
# --------------------------------------------------------------------------
need() { command -v "$1" >/dev/null 2>&1 || { err "comando '$1' não encontrado no PATH"; exit 1; }; }
need go
need curl
[[ -n "$MOCK_PYTHON" ]] || { err "nenhum python encontrado (venv em agente-ia/ ou python3 no PATH)"; exit 1; }
[[ -n "$PY_VENV" ]] \
  || warn "venv do agente não existe (agente-ia/venv) — o cérebro vai usar o python do PATH e pode faltar dependência"
[[ -f "$ROOT_DIR/agente-ia/.env" ]] \
  || warn "agente-ia/.env não encontrado — o cérebro pode falhar sem OPENROUTER_API_KEY (ou use LLM_FAKE=1 JEV_FAKE=1)"
[[ -f "$ROOT_DIR/backend/.env" ]] \
  || warn "backend/.env não encontrado — as variáveis do Infodesk são forçadas aqui; outras podem faltar"

# --------------------------------------------------------------------------
# Gerência de processos. Com setsid (Linux/macOS) cada serviço roda no seu
# próprio process group e o Ctrl+C derruba também os filhos (ex.: o binário
# que o 'go run' compila). Sem setsid (Git Bash) mata-se pelo pid.
# --------------------------------------------------------------------------
declare -a PIDS=() NAMES=()
HAVE_SETSID=0
command -v setsid >/dev/null 2>&1 && HAVE_SETSID=1

cleanup() {
  trap '' INT TERM
  echo
  info "Parando serviços..."
  for pid in "${PIDS[@]}"; do
    if (( HAVE_SETSID )); then kill -TERM "-${pid}" 2>/dev/null || kill -TERM "${pid}" 2>/dev/null || true
    else kill -TERM "${pid}" 2>/dev/null || true; fi
  done
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    local alive=0
    for pid in "${PIDS[@]}"; do kill -0 "$pid" 2>/dev/null && alive=1; done
    [[ $alive -eq 0 ]] && break
    sleep 0.3
  done
  for pid in "${PIDS[@]}"; do
    if (( HAVE_SETSID )); then kill -KILL "-${pid}" 2>/dev/null || kill -KILL "${pid}" 2>/dev/null || true
    else kill -KILL "${pid}" 2>/dev/null || true; fi
  done
  ok "Tudo parado. (sobrou algo nas portas? scripts/stop-all.sh)"
}
trap cleanup INT TERM EXIT

# start_service <nome> <comando-shell>
start_service() {
  local name="$1" cmd="$2"
  local log="$LOG_DIR/${name}.log"
  : > "$log"
  if (( HAVE_SETSID )); then setsid bash -c "$cmd" >"$log" 2>&1 &
  else bash -c "$cmd" >"$log" 2>&1 & fi
  local pid=$!
  PIDS+=("$pid"); NAMES+=("$name")
  info "${name} iniciado (pid ${pid}) → ${C_DIM}${log}${C_RST}"
}

# port_open <porta>  -> 0 se aceitando conexão em 127.0.0.1
port_open() { (exec 3<>"/dev/tcp/127.0.0.1/$1") >/dev/null 2>&1 || curl -sS -o /dev/null "http://127.0.0.1:$1/" >/dev/null 2>&1; }

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
info "Subindo a stack da Sofia (mock-infodesk + cérebro + gateway)"
printf '  %s\n' "${C_DIM}mock     : http://localhost:${MOCK_PORT}  (UI)  ·  INFODESK_BASE_URL=http://localhost:${MOCK_PORT}/api${C_RST}"
printf '  %s\n' "${C_DIM}cérebro  : http://localhost:${BRAIN_PORT}${C_RST}"
printf '  %s\n' "${C_DIM}gateway  : http://localhost:${BACKEND_PORT}  (webhook em /webhook)${C_RST}"
printf '  %s\n' "${C_DIM}chave    : ${MOCK_API_KEY}  ·  segredo do webhook: ${MOCK_WEBHOOK_SECRET}${C_RST}"
echo

# --------------------------------------------------------------------------
# 1) Mock Infodesk — primeiro: o gateway pode carregar /desks e /departments
#    no boot (docs/infodesk/02). Webhooks apontados para o gateway.
# --------------------------------------------------------------------------
start_service "mock" "
  cd '$ROOT_DIR/mock-infodesk'
  export MOCK_PORT='$MOCK_PORT'
  export MOCK_API_KEY='$MOCK_API_KEY'
  export MOCK_WEBHOOK_SECRET='$MOCK_WEBHOOK_SECRET'
  export MOCK_ORG_ID='$MOCK_ORG_ID'
  export GATEWAY_WEBHOOK_URL='http://localhost:$BACKEND_PORT/webhook'
  exec '$MOCK_PYTHON' -u mock_infodesk.py
"
wait_ready "mock" "${PIDS[-1]}" 30 curl -fsS "http://localhost:${MOCK_PORT}/mock/config" \
  || { err "mock-infodesk não subiu. Abortando."; exit 1; }

# --------------------------------------------------------------------------
# 2) Cérebro (agente-ia) — antes do gateway: o gateway chama POST /chat
# --------------------------------------------------------------------------
start_service "cerebro" "
  cd '$ROOT_DIR/agente-ia'
  export TOOLS_API_TOKEN='$TOOLS_API_TOKEN'
  exec '$PY' -m uvicorn server:app --host 0.0.0.0 --port $BRAIN_PORT
"
wait_ready "cerebro" "${PIDS[-1]}" 60 curl -fsS "http://localhost:${BRAIN_PORT}/health" \
  || { err "cérebro não respondeu em /health — abortando."; exit 1; }

# --------------------------------------------------------------------------
# 3) Gateway Go — conectado ao cérebro (BRAIN_BASE_URL) e ao mock (INFODESK_*).
# --------------------------------------------------------------------------
start_service "gateway" "
  cd '$ROOT_DIR/backend'
  export PORT='$BACKEND_PORT'
  export BRAIN_BASE_URL='http://localhost:$BRAIN_PORT'
  export TOOLS_API_TOKEN='$TOOLS_API_TOKEN'
  export INFODESK_BASE_URL='http://localhost:$MOCK_PORT/api'
  export INFODESK_API_KEY='$MOCK_API_KEY'
  export INFODESK_WEBHOOK_SECRET='$MOCK_WEBHOOK_SECRET'
  export INFODESK_ORGANIZATION_ID='$MOCK_ORG_ID'
  exec go run ./cmd/bot
"
if ! wait_ready "gateway" "${PIDS[-1]}" 120 port_open "$BACKEND_PORT"; then
  if [[ "${GATEWAY_OBRIGATORIO:-0}" == "1" ]]; then err "gateway não subiu (veja o log acima). Abortando."; exit 1; fi
  warn "gateway não subiu — esperado até E2.1. Mock e cérebro seguem no ar; os webhooks do mock vão falhar (visível na UI)."
fi

# --------------------------------------------------------------------------
# Pronto
# --------------------------------------------------------------------------
echo
ok "Stack no ar! Abra a UI do mock e escreva como o cliente:"
printf '   %s\n' "${C_GREEN}http://localhost:${MOCK_PORT}${C_RST}"
echo
info "Logs ao vivo: tail -f ${LOG_DIR}/{mock,cerebro,gateway}.log"
info "Ctrl+C para derrubar os três."
echo

# Segue vivo, espelhando os logs até Ctrl+C.
tail -n 0 -F "$LOG_DIR/mock.log" "$LOG_DIR/cerebro.log" "$LOG_DIR/gateway.log" &
PIDS+=("$!"); NAMES+=("tail")
wait
