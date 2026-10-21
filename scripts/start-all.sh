#!/usr/bin/env bash
#
# start-all.sh — sobe os 3 serviços do monorepo já conectados entre si, com a
# taxonomia de dev apontando para as mesas do mock:
#
#   mock-infodesk (:9000) ──webhook assinado──►  gateway Go (:8080) ──POST /chat──►  cérebro (:8001)
#          ▲                                          │
#          └────── POST /api/integration/v1/... ◄─────┘  (gateway "fala com o Infodesk" = no mock)
#
# Configuração: scripts/dev.env (cópia local de scripts/dev.env.example, fora do
# git) ou, na falta dele, o próprio dev.env.example. O ambiente do shell vence
# o arquivo. Chaves reais (OPENROUTER_API_KEY etc.) ficam em agente-ia/.env ou
# no ambiente — nunca em dev.env.example.
#
# A "cola" mock ↔ gateway ↔ cérebro é forçada aqui para sempre casar:
#   INFODESK_API_KEY        = MOCK_API_KEY         INFODESK_BASE_URL   = http://localhost:$MOCK_PORT/api
#   INFODESK_WEBHOOK_SECRET = MOCK_WEBHOOK_SECRET  GATEWAY_WEBHOOK_URL = http://localhost:$BACKEND_PORT/webhook
#   TOOLS_API_TOKEN (gateway) = INFODESK_TOOLS_TOKEN (cérebro)
#   TAXONOMIA_PATH (cérebro e gateway) = scripts/dev/taxonomia.dev.yaml (via dev.env)
#
# Uso:
#   scripts/start-all.sh                 # sobe os três, espera cada porta, segue logando até Ctrl+C
#   scripts/start-all.sh --chainlit      # + Chainlit (agente-ia/app.py) em :$CHAINLIT_PORT
#   MOCK_PORT=9100 scripts/start-all.sh  # o shell vence o dev.env
#
# Ctrl+C derruba os três. Funciona em Linux e no Git Bash do Windows (sem
# setsid, cada serviço é morto pelo pid e as sobras, por porta, via
# scripts/stop-all.sh).

set -euo pipefail

START_CHAINLIT="${START_CHAINLIT:-0}"
for arg in "$@"; do
  case "$arg" in
    --chainlit) START_CHAINLIT=1 ;;
    -h|--help) sed -n '2,/^set -euo/p' "${BASH_SOURCE[0]}" | sed '$d' | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "argumento desconhecido: $arg (use --chainlit ou --help)" >&2; exit 2 ;;
  esac
done

# --------------------------------------------------------------------------
# Caminhos
# --------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
LOG_DIR="$SCRIPT_DIR/logs"
BIN_DIR="$SCRIPT_DIR/.bin"
mkdir -p "$LOG_DIR" "$BIN_DIR"

IS_WIN=0
case "$(uname -s 2>/dev/null)" in MINGW*|MSYS*|CYGWIN*) IS_WIN=1 ;; esac
EXE=""; (( IS_WIN )) && EXE=".exe"

# nativo <caminho>: caminho que um processo nativo (Go/Python no Windows)
# entende — `C:/Users/...` no Git Bash, o próprio caminho no Linux.
nativo() {
  if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf '%s\n' "$1"; fi
}

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
# Configuração de dev: carrega CHAVE=valor sem `source` (não executa nada do
# arquivo; tolera CRLF). Só exporta o que NÃO está no ambiente e não é vazio:
# o shell vence o arquivo, e um valor vazio nunca sobrescreve o agente-ia/.env.
# --------------------------------------------------------------------------
carregar_env() {
  local arquivo="$1" linha chave valor
  while IFS= read -r linha || [[ -n "$linha" ]]; do
    linha="${linha%$'\r'}"
    [[ "$linha" =~ ^[[:space:]]*(#|$) ]] && continue
    [[ "$linha" =~ ^[[:space:]]*([A-Za-z_][A-Za-z0-9_]*)=(.*)$ ]] || continue
    chave="${BASH_REMATCH[1]}"; valor="${BASH_REMATCH[2]}"
    valor="${valor#"${valor%%[![:space:]]*}"}"; valor="${valor%"${valor##*[![:space:]]}"}"
    if [[ "$valor" =~ ^\"(.*)\"$ || "$valor" =~ ^\'(.*)\'$ ]]; then valor="${BASH_REMATCH[1]}"; fi
    [[ -z "$valor" ]] && continue
    [[ -n "${!chave+x}" ]] && continue
    export "$chave=$valor"
  done < "$arquivo"
}

ENV_ARQUIVO="$SCRIPT_DIR/dev.env"
if [[ -f "$ENV_ARQUIVO" ]]; then
  carregar_env "$ENV_ARQUIVO"
  CONFIG_USADA="scripts/dev.env"
else
  ENV_ARQUIVO="$SCRIPT_DIR/dev.env.example"
  carregar_env "$ENV_ARQUIVO"
  CONFIG_USADA="scripts/dev.env.example (copie para scripts/dev.env para ajustar)"
fi

# --------------------------------------------------------------------------
# Portas e segredos de dev (do dev.env; estes são só o último recurso)
# --------------------------------------------------------------------------
BRAIN_PORT="${BRAIN_PORT:-8001}"
BACKEND_PORT="${BACKEND_PORT:-8080}"
MOCK_PORT="${MOCK_PORT:-9000}"
CHAINLIT_PORT="${CHAINLIT_PORT:-8000}"
MOCK_API_KEY="${MOCK_API_KEY:-ik_test_mock}"
MOCK_WEBHOOK_SECRET="${MOCK_WEBHOOK_SECRET:-segredo-mock}"
MOCK_ORG_ID="${MOCK_ORG_ID:-44444444-4444-4444-8444-444444444401}"
TOOLS_API_TOKEN="${TOOLS_API_TOKEN:-dev-token}"

# Taxonomia: relativo à raiz do repo → absoluto nativo (cérebro roda em
# agente-ia/ e o gateway em backend/, então um caminho relativo não serviria).
if [[ -n "${TAXONOMIA_PATH:-}" ]]; then
  [[ "$TAXONOMIA_PATH" = /* || "$TAXONOMIA_PATH" =~ ^[A-Za-z]:[/\\] ]] || TAXONOMIA_PATH="$ROOT_DIR/$TAXONOMIA_PATH"
  [[ -f "$TAXONOMIA_PATH" ]] || { err "TAXONOMIA_PATH não existe: $TAXONOMIA_PATH"; exit 1; }
  TAXONOMIA_PATH="$(nativo "$TAXONOMIA_PATH")"
  export TAXONOMIA_PATH
fi

# --------------------------------------------------------------------------
# Python: venv do agente se existir (Linux/macOS ou Windows), senão o do PATH.
# No Windows o alias `python` da Store pode existir e falhar ("Python was not
# found"): por isso cada candidato é testado de verdade antes de ser usado.
# --------------------------------------------------------------------------
roda() { "$1" -c 'import sys' >/dev/null 2>&1; }
PY=""
for cand in "$ROOT_DIR/agente-ia/venv/bin/python" "$ROOT_DIR/agente-ia/venv/Scripts/python.exe" \
            "$ROOT_DIR/agente-ia/.venv/bin/python" "$ROOT_DIR/agente-ia/.venv/Scripts/python.exe"; do
  [[ -x "$cand" ]] && roda "$cand" && PY="$cand" && break
done
PY_VENV="$PY"
if [[ -z "$PY" ]]; then
  for cand in python3 python "${LOCALAPPDATA:+$(cygpath -u "$LOCALAPPDATA" 2>/dev/null)/Python/bin/python.exe}"; do
    [[ -n "$cand" ]] && command -v "$cand" >/dev/null 2>&1 && roda "$cand" && PY="$cand" && break
  done
fi
# O mock é stdlib: qualquer Python 3.12+ serve (MOCK_PYTHON sobrescreve).
MOCK_PYTHON="${MOCK_PYTHON:-$PY}"

# --------------------------------------------------------------------------
# Pré-checagens
# --------------------------------------------------------------------------
need() { command -v "$1" >/dev/null 2>&1 || { err "comando '$1' não encontrado no PATH"; exit 1; }; }
need go
need curl
[[ -n "$MOCK_PYTHON" ]] || { err "nenhum python que funcione (venv em agente-ia/ ou python3 no PATH; no Windows o alias da Store não conta)"; exit 1; }
[[ -n "$PY_VENV" ]] \
  || warn "venv do agente não existe (agente-ia/venv) — o cérebro vai usar o python do PATH e pode faltar dependência"
if [[ ! -f "$ROOT_DIR/agente-ia/.env" && -z "${OPENROUTER_API_KEY:-}" ]]; then
  warn "sem agente-ia/.env nem OPENROUTER_API_KEY no ambiente — o cérebro pode não subir; a chave real fica fora do git"
fi

# port_open <porta>  -> 0 se aceitando conexão em 127.0.0.1
port_open() { (exec 3<>"/dev/tcp/127.0.0.1/$1") >/dev/null 2>&1 || curl -sS -o /dev/null "http://127.0.0.1:$1/" >/dev/null 2>&1; }

ocupadas=""
for p in "$MOCK_PORT" "$BACKEND_PORT" "$BRAIN_PORT"; do port_open "$p" && ocupadas="$ocupadas $p"; done
(( START_CHAINLIT )) && port_open "$CHAINLIT_PORT" && ocupadas="$ocupadas $CHAINLIT_PORT"
if [[ -n "$ocupadas" ]]; then
  err "porta(s) já em uso:${ocupadas}. Sobrou uma execução anterior? Rode scripts/stop-all.sh e tente de novo."
  exit 1
fi

# --------------------------------------------------------------------------
# Gerência de processos. Com setsid (Linux/macOS) cada serviço roda no seu
# próprio process group e o Ctrl+C derruba também os filhos. Sem setsid (Git
# Bash) mata-se pelo pid e o que restar nas portas, pelo stop-all.sh (o
# launcher do venv e o `go run` deixam filhos fora do alcance do pid).
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
  if (( ! HAVE_SETSID )); then
    BRAIN_PORT="$BRAIN_PORT" BACKEND_PORT="$BACKEND_PORT" MOCK_PORT="$MOCK_PORT" CHAINLIT_PORT="$CHAINLIT_PORT" START_CHAINLIT="$START_CHAINLIT" \
      bash "$SCRIPT_DIR/stop-all.sh" >/dev/null 2>&1 || true
  fi
  ok "Tudo parado."
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
    sleep 1; (( i++ )) || true
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
printf '  %s\n' "${C_DIM}config   : ${CONFIG_USADA}${C_RST}"
printf '  %s\n' "${C_DIM}taxonomia: ${TAXONOMIA_PATH:-<padrão do gateway: placeholder versionado>}${C_RST}"
echo

# --------------------------------------------------------------------------
# 1) Mock Infodesk — primeiro: o gateway carrega /desks e /departments no boot
#    (docs/infodesk/02). Webhooks apontados para o gateway.
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
# 2) Cérebro (agente-ia) — antes do gateway: o gateway chama POST /chat.
#    As tools do cérebro falam com o gateway (INFODESK_TOOLS_*), nunca com o
#    Infodesk. TAXONOMIA_PATH já está no ambiente (herdado).
# --------------------------------------------------------------------------
start_service "cerebro" "
  cd '$ROOT_DIR/agente-ia'
  export INFODESK_TOOLS_BASE_URL='http://localhost:$BACKEND_PORT'
  export INFODESK_TOOLS_TOKEN='$TOOLS_API_TOKEN'
  exec '$PY' -m uvicorn server:app --host 0.0.0.0 --port $BRAIN_PORT
"
wait_ready "cerebro" "${PIDS[-1]}" 90 curl -fsS "http://localhost:${BRAIN_PORT}/health" \
  || { err "cérebro não respondeu em /health — abortando."; exit 1; }

# --------------------------------------------------------------------------
# 3) Gateway Go — conectado ao cérebro (BRAIN_BASE_URL) e ao mock (INFODESK_*).
#    Compila para scripts/.bin e executa o binário: o pid do serviço passa a
#    ser o do próprio gateway (com `go run` o Ctrl+C deixava o binário vivo).
# --------------------------------------------------------------------------
info "compilando o gateway..."
( cd "$ROOT_DIR/backend" && go build -o "$(nativo "$BIN_DIR")/gateway$EXE" ./cmd/bot ) \
  || { err "go build do gateway falhou. Abortando."; exit 1; }
start_service "gateway" "
  cd '$ROOT_DIR/backend'
  export PORT='$BACKEND_PORT'
  export BRAIN_BASE_URL='http://localhost:$BRAIN_PORT'
  export TOOLS_API_TOKEN='$TOOLS_API_TOKEN'
  export INFODESK_BASE_URL='http://localhost:$MOCK_PORT/api'
  export INFODESK_API_KEY='$MOCK_API_KEY'
  export INFODESK_WEBHOOK_SECRET='$MOCK_WEBHOOK_SECRET'
  export INFODESK_ORGANIZATION_ID='$MOCK_ORG_ID'
  exec '$BIN_DIR/gateway$EXE'
"
wait_ready "gateway" "${PIDS[-1]}" 60 curl -fsS "http://localhost:${BACKEND_PORT}/health" \
  || { err "gateway não subiu (veja o log acima). Abortando."; exit 1; }

# A taxonomia é resolvida contra as mesas do mock no boot do gateway: confira
# o resultado em vez de supor (categoria_sem_mapa nasce aqui).
TAX_LINHA="$(grep -m1 'taxonomia carregada' "$LOG_DIR/gateway.log" 2>/dev/null || true)"
if [[ -n "$TAX_LINHA" ]]; then
  ok "gateway: $(printf '%s' "$TAX_LINHA" | sed -E 's/^.*"msg":"([^"]*)"(.*)\}.*$/\1\2/; s/[,"]/ /g')"
else
  warn "gateway: não achei 'taxonomia carregada' no log — confira scripts/logs/gateway.log"
fi
if grep -qi 'taxonomia diverge\|sem mapa' "$LOG_DIR/gateway.log" 2>/dev/null; then
  warn "a taxonomia diverge das mesas do mock (categoria_sem_mapa à vista). Linhas:"
  grep -i 'taxonomia diverge\|sem mapa' "$LOG_DIR/gateway.log" | head -n 5 >&2
fi

# --------------------------------------------------------------------------
# 4) Chainlit (opcional): o cliente simulado do Contrato A, com o mesmo
#    ambiente do cérebro (INFODESK_TOOLS_* → gateway).
# --------------------------------------------------------------------------
if (( START_CHAINLIT )); then
  start_service "chainlit" "
    cd '$ROOT_DIR/agente-ia'
    export INFODESK_TOOLS_BASE_URL='http://localhost:$BACKEND_PORT'
    export INFODESK_TOOLS_TOKEN='$TOOLS_API_TOKEN'
    exec '$PY' -m chainlit run app.py --headless --port $CHAINLIT_PORT
  "
  wait_ready "chainlit" "${PIDS[-1]}" 90 curl -fsS "http://localhost:${CHAINLIT_PORT}/" \
    || { err "Chainlit não subiu — abortando."; exit 1; }
fi

# --------------------------------------------------------------------------
# Pronto
# --------------------------------------------------------------------------
echo
ok "Stack no ar! Onde abrir:"
printf '   %-22s %s\n' "mock-infodesk (UI)" "${C_GREEN}http://localhost:${MOCK_PORT}${C_RST}   ← escreva como o cliente e veja as respostas e os chamados"
printf '   %-22s %s\n' "cérebro (health/docs)" "http://localhost:${BRAIN_PORT}/health · http://localhost:${BRAIN_PORT}/docs"
printf '   %-22s %s\n' "gateway (health)" "http://localhost:${BACKEND_PORT}/health   (webhook: /webhook)"
if (( START_CHAINLIT )); then
  printf '   %-22s %s\n' "Chainlit (Contrato A)" "${C_GREEN}http://localhost:${CHAINLIT_PORT}${C_RST}"
else
  printf '   %-22s %s\n' "Chainlit (Contrato A)" "scripts/start-all.sh --chainlit  (ou: cd agente-ia && chainlit run app.py -w)"
fi
echo
info "Logs: tail -f ${LOG_DIR}/{mock,cerebro,gateway}.log"
info "Ctrl+C derruba tudo (sobrou algo nas portas? scripts/stop-all.sh)."
echo

# Segue vivo, espelhando os logs até Ctrl+C.
tail -n 0 -F "$LOG_DIR/mock.log" "$LOG_DIR/cerebro.log" "$LOG_DIR/gateway.log" &
PIDS+=("$!"); NAMES+=("tail")
wait
