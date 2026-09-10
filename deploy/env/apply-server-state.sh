#!/usr/bin/env bash
# ==============================================================================
# Funde o ESTADO do servidor no .env — roda NA VPS, em /opt/azapfy-bot
# (sincronizado pelo deploy-remote.sh; a pipeline do Chatwoot também o chama).
#
# Estado hoje: só o CHATWOOT_API_TOKEN. Ele é gerado DENTRO do Chatwoot da
# própria VM (usuário "Zapin (bot)", pipeline configure-chatwoot) e gravado em
# .chatwoot-token — nunca passa pelo GitHub (o runner não tem como escrever
# secrets, e não precisa: quem gera e quem consome estão na mesma máquina).
# O .env renderizado pelo CI chega com a chave vazia; AQUI ela ganha o valor.
# Fonte da verdade: .chatwoot-token (re-rode a pipeline do Chatwoot p/ rotacionar).
# ==============================================================================
set -euo pipefail
cd "$(dirname "$0")/../.."

[ -f .env ] || { echo "ERRO: .env não existe — deploy nunca rodou?" >&2; exit 1; }

if [ ! -s .chatwoot-token ]; then
  echo "AVISO: .chatwoot-token ausente — o gateway fica sem CHATWOOT_API_TOKEN"
  echo "       até rodar a pipeline 'Configurar Chatwoot (caixa do bot)'."
  exit 0
fi

token="$(tr -d '[:space:]' < .chatwoot-token)"
case "$token" in
  *[!A-Za-z0-9_-]*) echo "ERRO: .chatwoot-token com caracteres inesperados." >&2; exit 1 ;;
esac

if grep -q '^CHATWOOT_API_TOKEN=' .env; then
  sed -i "s|^CHATWOOT_API_TOKEN=.*|CHATWOOT_API_TOKEN=${token}|" .env
else
  printf 'CHATWOOT_API_TOKEN=%s\n' "$token" >> .env
fi
echo "CHATWOOT_API_TOKEN aplicado do estado do servidor (.chatwoot-token)."
