#!/usr/bin/env bash
# ==============================================================================
# Valida a stack da Sofia no host onde o compose roda (rode na raiz do repo, ou
# em qualquer pasta com os arquivos do compose e o .env).
#
#   deploy/healthcheck.sh [URL_PUBLICA]
#
# Portões, na ordem em que uma mensagem real percorre a stack:
#   1. pgvector aceitando conexão (estado do gateway + índice do RAG)
#   2. índice do RAG POPULADO (rag_chunks > 0) — só com VECTOR_BACKEND=pgvector;
#      índice vazio = bot que não sabe nada, e silêncio não passa
#   3. cérebro /health (grafo compilado, taxonomia carregada)
#   4. gateway /health (processo vivo, Postgres no boot)
#   5. (opcional) URL pública: GET <URL_PUBLICA>/health pelo caminho que o
#      Infodesk vai usar (DNS + HTTPS + proxy) — sem isso a stack "sobe" mas o
#      Infodesk nunca consegue entregar um webhook. Aceita PUBLIC_BASE_URL do
#      ambiente no lugar do argumento.
# ==============================================================================
set -euo pipefail
cd "$(dirname "$0")/.."

PUBLICA="${1:-${PUBLIC_BASE_URL:-}}"
RETRIES="${RETRIES:-12}"
DELAY="${DELAY:-5}"

tenta() { # tenta <descrição> <comando…>
  local desc="$1"; shift
  for i in $(seq 1 "$RETRIES"); do
    if "$@" > /dev/null 2>&1; then
      echo "✔ $desc"
      return 0
    fi
    echo "  tentativa $i/$RETRIES — $desc…"
    sleep "$DELAY"
  done
  echo "ERRO: $desc não respondeu." >&2
  return 1
}

tenta "pgvector aceitando conexões (dentro do container)" \
  docker compose exec -T pgvector pg_isready -h 127.0.0.1 -U rag -d rag

# psql via 127.0.0.1 DENTRO do container: o pg_hba do initdb libera loopback
# sem senha (a senha só é exigida na rede docker) — nada de segredo aqui.
backend="$(docker compose exec -T brain printenv VECTOR_BACKEND 2>/dev/null | tr -d '\r' || true)"
if [ "${backend:-chroma}" = "pgvector" ]; then
  tenta "índice do RAG populado (rag_chunks > 0 no pgvector)" \
    bash -c 'n=$(docker compose exec -T pgvector psql -h 127.0.0.1 -U rag -d rag -Atc "select count(*) from rag_chunks" 2>/dev/null); [ "${n:-0}" -gt 0 ] 2>/dev/null'
else
  echo "– índice do RAG não conferido (VECTOR_BACKEND=${backend:-chroma}, não é pgvector)"
fi

tenta "cérebro /health (dentro do container)" \
  docker compose exec -T brain python -c \
  "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8001/health', timeout=5)"

tenta "gateway /health (dentro do container)" \
  docker compose exec -T gateway wget -qO- http://127.0.0.1:8080/health

if [ -n "$PUBLICA" ]; then
  tenta "gateway alcançável por ${PUBLICA%/}/health (caminho do webhook)" \
    curl -fsS --max-time 8 "${PUBLICA%/}/health"
else
  echo "– URL pública não conferida (passe-a como argumento ou defina PUBLIC_BASE_URL)"
fi

echo "✔ Stack saudável."
