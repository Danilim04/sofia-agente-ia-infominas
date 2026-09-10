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

# CHATWOOT_API_TOKEN é OPCIONAL aqui: a fonte primária é o estado da VM
# (.chatwoot-token, gravado pela pipeline configure-chatwoot e aplicado por
# apply-server-state.sh POR CIMA do valor renderizado). O secret, se existir,
# serve só de bootstrap/override manual.
# PGVECTOR_PASSWORD é exigida: sem ela o Postgres não sobe e o gateway (que
# guarda seu estado lá) não inicia.
required=(IMAGE_TAG OPENROUTER_API_KEY MONGO_URI WEBHOOK_TOKEN TOOLS_API_TOKEN PGVECTOR_PASSWORD)

faltando=()
for var in "${required[@]}"; do
  [ -n "${!var:-}" ] || faltando+=("$var")
done
if [ "${#faltando[@]}" -gt 0 ]; then
  echo "ERRO: variáveis exigidas vazias/ausentes: ${faltando[*]}" >&2
  exit 1
fi

# Lista EXPLÍCITA de substituições — o envsubst não toca em mais nada.
# Vars OPCIONAIS (fora da lista required, ex.: SAC_*) podem vir vazias: a
# chave entra vazia e o gateway trata como não configurada.
vars='$IMAGE_TAG $OPENROUTER_API_KEY $MONGO_URI $CHATWOOT_API_TOKEN $WEBHOOK_TOKEN $TOOLS_API_TOKEN $PGVECTOR_PASSWORD $DOCS_API_KEY $SYNC_ALERTA_WEBHOOK $SAC_API_TOKEN $SAC_SERVICE_COD $LANGFUSE_PUBLIC_KEY $LANGFUSE_SECRET_KEY $LANGFUSE_HOST'
envsubst "$vars" < prod.env.tpl
