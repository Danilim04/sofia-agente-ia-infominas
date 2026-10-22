#!/usr/bin/env bash
# ==============================================================================
# Valida a stack da Sofia no host onde o compose roda (rode na raiz do repo, ou
# em qualquer pasta com os arquivos do compose e o .env).
#
#   deploy/healthcheck.sh [URL_PUBLICA]
#
# Portões, na ordem em que uma mensagem real percorre a stack:
#   1. pgvector aceitando conexão (estado do gateway + índice do RAG)
#   2. índice do RAG POPULADO (rag_chunks > 0); o cérebro tem de ler do pgvector
#      (VECTOR_BACKEND) — índice vazio = bot que não sabe nada, e silêncio não passa
#   3. cérebro /health (grafo compilado, taxonomia carregada)
#   4. gateway /health (processo vivo, Postgres no boot)
#   5. (opcional) URL pública: POST <URL_PUBLICA>/webhook sem assinatura, pelo
#      caminho que o Infodesk vai usar (DNS + HTTPS + proxy); o gateway deve
#      responder (400/401/403/405…), nunca 404/5xx/timeout — sem isso a stack
#      "sobe" mas o Infodesk não entrega webhook. Só /webhook precisa estar
#      exposto. Aceita PUBLIC_BASE_URL EXPORTADA no shell no lugar do argumento
#      (o valor do .env não é lido aqui).
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
if [ "${backend:-}" != "pgvector" ]; then
  echo "ERRO: o cérebro lê o RAG de '${backend:-?}', não do pgvector — a imagem não embute índice, o RAG ficaria vazio." >&2
  exit 1
fi
tenta "índice do RAG populado (rag_chunks > 0; se falhar: docker compose run --rm brain python -m src.rag.sync)" \
  bash -c 'n=$(docker compose exec -T pgvector psql -h 127.0.0.1 -U rag -d rag -Atc "select count(*) from rag_chunks" 2>/dev/null); [ "${n:-0}" -gt 0 ] 2>/dev/null'

tenta "cérebro /health (dentro do container)" \
  docker compose exec -T brain python -c \
  "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8001/health', timeout=5)"

tenta "gateway /health (dentro do container)" \
  docker compose exec -T gateway wget -qO- http://127.0.0.1:8080/health

# POST sem assinatura: o gateway responde (400/401/403/405…); DNS, TLS e proxy ok.
webhook_alcancavel() {
  local code
  code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 8 -X POST -d '{}' "${PUBLICA%/}/webhook" || true)"
  case "$code" in 200|202|204|400|401|403|405|422) return 0 ;; *) return 1 ;; esac
}
if [ -n "$PUBLICA" ]; then
  tenta "gateway alcançável por POST ${PUBLICA%/}/webhook (caminho do Infodesk)" webhook_alcancavel
else
  echo "– URL pública não conferida (passe-a como argumento ou defina PUBLIC_BASE_URL)"
fi

echo "✔ Stack saudável."
