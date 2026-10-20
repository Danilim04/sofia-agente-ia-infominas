#!/usr/bin/env bash
#
# stop-all.sh — derruba qualquer processo órfão dos 3 serviços, por porta.
# Normalmente o Ctrl+C no start-all.sh já limpa tudo; use isto se algo escapou.
#
# Uso: scripts/stop-all.sh   (respeita BRAIN_PORT/BACKEND_PORT/MOCK_PORT)
# Linux/macOS: fuser ou lsof. Windows (Git Bash): netstat + taskkill.

set -uo pipefail

BRAIN_PORT="${BRAIN_PORT:-8001}"
BACKEND_PORT="${BACKEND_PORT:-8080}"
MOCK_PORT="${MOCK_PORT:-9000}"

pids_na_porta() {
  local port="$1"
  if command -v fuser >/dev/null 2>&1; then
    fuser -n tcp "$port" 2>/dev/null | tr -s ' ' '\n' | grep -E '^[0-9]+$' || true
  elif command -v lsof >/dev/null 2>&1; then
    lsof -t -i "tcp:$port" -sTCP:LISTEN 2>/dev/null || true
  elif command -v netstat >/dev/null 2>&1 && command -v taskkill >/dev/null 2>&1; then
    # colunas do netstat do Windows: Proto  Local  Remoto  Estado  PID
    netstat -ano -p tcp 2>/dev/null | awk -v p=":$port" '$2 ~ p"$" && $4 == "LISTENING" {print $5}' | sort -u
  else
    echo "! nem fuser, lsof ou netstat+taskkill disponíveis — não consigo achar o pid da porta $port" >&2
  fi
}

matar() {
  local pid="$1"
  if command -v taskkill >/dev/null 2>&1 && ! command -v fuser >/dev/null 2>&1 && ! command -v lsof >/dev/null 2>&1; then
    taskkill //PID "$pid" //T //F >/dev/null 2>&1 || true
  else
    kill -TERM "$pid" 2>/dev/null || true
    sleep 1
    kill -KILL "$pid" 2>/dev/null || true
  fi
}

kill_port() {
  local port="$1" name="$2" pids
  pids="$(pids_na_porta "$port")"
  if [[ -z "$pids" ]]; then
    echo "· ${name} (:$port) — nada rodando"
    return 0
  fi
  echo "✗ matando ${name} (:$port) → pids: $(echo "$pids" | tr '\n' ' ')"
  for pid in $pids; do matar "$pid"; done
}

kill_port "$MOCK_PORT"    "mock"
kill_port "$BACKEND_PORT" "gateway"
kill_port "$BRAIN_PORT"   "cerebro"
echo "✓ feito."
