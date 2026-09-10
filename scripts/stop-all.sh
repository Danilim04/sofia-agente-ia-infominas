#!/usr/bin/env bash
#
# stop-all.sh — derruba qualquer processo órfão dos 3 serviços, por porta.
# Normalmente o Ctrl+C no start-all.sh já limpa tudo; use isto se algo escapou.
#
# Uso: scripts/stop-all.sh   (respeita BRAIN_PORT/BACKEND_PORT/MOCK_PORT)

set -uo pipefail

BRAIN_PORT="${BRAIN_PORT:-8001}"
BACKEND_PORT="${BACKEND_PORT:-8080}"
MOCK_PORT="${MOCK_PORT:-9000}"

kill_port() {
  local port="$1" name="$2" pids=""
  if command -v fuser >/dev/null 2>&1; then
    pids="$(fuser -n tcp "$port" 2>/dev/null | tr -s ' ' '\n' | grep -E '^[0-9]+$' || true)"
  elif command -v lsof >/dev/null 2>&1; then
    pids="$(lsof -t -i "tcp:$port" 2>/dev/null || true)"
  else
    echo "! nem 'fuser' nem 'lsof' disponíveis — não consigo achar pid da porta $port" >&2
    return 0
  fi
  if [[ -z "$pids" ]]; then
    echo "· ${name} (:$port) — nada rodando"
    return 0
  fi
  echo "✗ matando ${name} (:$port) → pids: $(echo "$pids" | tr '\n' ' ')"
  # shellcheck disable=SC2086
  kill -TERM $pids 2>/dev/null || true
  sleep 1
  # shellcheck disable=SC2086
  kill -KILL $pids 2>/dev/null || true
}

kill_port "$MOCK_PORT"    "mock"
kill_port "$BACKEND_PORT" "backend"
kill_port "$BRAIN_PORT"   "cerebro"
echo "✓ feito."
