#!/usr/bin/env bash
# ==============================================================================
# Renderiza o .env de produção para stdout. Roda no RUNNER da pipeline; os
# valores chegam pelo ambiente (GitHub Secrets/vars).
#
#   IMAGE_TAG=v1.2.3 OPENROUTER_API_KEY=… ./render-env.sh > prod.env
#
# Falha se alguma var exigida estiver vazia — fail-fast: um secret esquecido
# aborta o deploy em vez de zerar silenciosamente uma chave no servidor.
# ==============================================================================
set -euo pipefail
cd "$(dirname "$0")"

# INFODESK_API_KEY é o Bearer da API de integração (obrigatória: sem ela o
# gateway não sobe). PGVECTOR_PASSWORD é exigida: sem ela o Postgres não sobe
# e o gateway (que guarda seu estado lá) não inicia.
required=(IMAGE_TAG OPENROUTER_API_KEY INFODESK_API_KEY WEBHOOK_TOKEN TOOLS_API_TOKEN PGVECTOR_PASSWORD)

faltando=()
for var in "${required[@]}"; do
  [ -n "${!var:-}" ] || faltando+=("$var")
done
if [ "${#faltando[@]}" -gt 0 ]; then
  echo "ERRO: variáveis exigidas vazias/ausentes: ${faltando[*]}" >&2
  exit 1
fi

# DEPARTAMENTO_HUMANO_PADRAO (GitHub *variable*): departamento do Infodesk que
# recebe as conversas transferidas pelo bot. Opcional no render — vazio, o
# gateway avisa no boot e as transferências só avisam o cliente e registram.
export DEPARTAMENTO_HUMANO_PADRAO="${DEPARTAMENTO_HUMANO_PADRAO:-}"
export INFODESK_ORGANIZATION_ID="${INFODESK_ORGANIZATION_ID:-}"
export INFODESK_WEBHOOK_SECRET="${INFODESK_WEBHOOK_SECRET:-}"
export PUBLIC_BASE_URL="${PUBLIC_BASE_URL:-}"
export ALERTA_WEBHOOK_URL="${ALERTA_WEBHOOK_URL:-}"

# Lista EXPLÍCITA de substituições — o envsubst não toca em mais nada.
# Vars OPCIONAIS (fora da lista required) podem vir vazias: a chave entra
# vazia e o gateway trata como não configurada.
vars='$IMAGE_TAG $OPENROUTER_API_KEY $INFODESK_API_KEY $INFODESK_WEBHOOK_SECRET $INFODESK_ORGANIZATION_ID $DEPARTAMENTO_HUMANO_PADRAO $PUBLIC_BASE_URL $WEBHOOK_TOKEN $TOOLS_API_TOKEN $PGVECTOR_PASSWORD $DOCS_API_KEY $SYNC_ALERTA_WEBHOOK $ALERTA_WEBHOOK_URL $LANGFUSE_PUBLIC_KEY $LANGFUSE_SECRET_KEY $LANGFUSE_HOST'
envsubst "$vars" < prod.env.tpl
