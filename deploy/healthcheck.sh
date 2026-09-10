#!/usr/bin/env bash
# ==============================================================================
# Valida o deploy NA VPS (sincronizado e executado via SSH pelo
# deploy-remote.sh; pode ser rodado à mão em /opt/azapfy-bot para diagnosticar).
#
# Cinco portões, na ordem em que uma mensagem real percorre a stack:
#   0. pgvector aceitando conexão (estado do gateway + índice do RAG) e o
#      índice do RAG POPULADO (rag_chunks > 0) — com VECTOR_BACKEND=pgvector,
#      índice vazio = bot que não sabe nada; silêncio não passa
#   1. gateway /health respondendo (processo vivo, Mongo e Postgres no boot)
#   2. brain   /health respondendo (grafo compilado, Chroma aberto)
#   3. gateway alcançável PELA REDE DO CHATWOOT no alias azapfy-bot — é o
#      caminho do webhook; sem isso o deploy "sobe" mas o bot nunca recebe
#      mensagem nenhuma. Silêncio não passa.
# ==============================================================================
set -euo pipefail
cd "$(dirname "$0")/.."

CHATWOOT_NET=omniroute_default
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
tenta "índice do RAG populado (rag_chunks > 0 no pgvector)" \
  bash -c 'n=$(docker compose exec -T pgvector psql -h 127.0.0.1 -U rag -d rag -Atc "select count(*) from rag_chunks" 2>/dev/null); [ "${n:-0}" -gt 0 ] 2>/dev/null'

tenta "gateway /health (dentro do container)" \
  docker compose exec -T gateway wget -qO- http://127.0.0.1:8080/health

tenta "brain /health (dentro do container)" \
  docker compose exec -T brain python -c \
  "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8001/health', timeout=5)"

# O caminho do webhook: um container efêmero na rede do Chatwoot resolve o
# alias `azapfy-bot` e bate no /health — prova DNS + rede + porta, exatamente
# o que o chatwoot-rails fará ao entregar um message_created.
tenta "gateway alcançável como http://azapfy-bot:8080 na rede $CHATWOOT_NET" \
  docker run --rm --network "$CHATWOOT_NET" alpine:3.22 \
  wget -qO- -T 5 http://azapfy-bot:8080/health

echo "✔ Stack saudável — caminho do webhook validado."
