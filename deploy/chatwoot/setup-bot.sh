#!/bin/sh
# ==============================================================================
# Provisiona o bot na instância omni-route da VM — IDEMPOTENTE. Roda DENTRO de
# um container alpine na rede do Chatwoot (workflow configure-chatwoot.yml):
#
#   docker run --rm -i --network omniroute_default \
#     -v /opt/omni-route/.env:/omni.env:ro -v /opt/azapfy-bot:/state \
#     -e HOST_UID=$(id -u) -e HOST_GID=$(id -g) \
#     alpine:3.22 sh -s < deploy/chatwoot/setup-bot.sh
#
# REGRA DE OURO (sincronia com o omni-route): o que o omni-route gerencia é
# criado PELA API DELE; o que é caminho oficial do produto via Chatwoot usa a
# MESMA API HTTP que o front do omni usa. Nada de rails console.
#
#   1. Usuário "Zapin (bot)"  → POST /api/users/workspace (omni; platform token,
#      política de senha, compensação de órfão). Senha vira estado da VM.
#   2. Token do Chatwoot      → POST /api/auth/workspace/login do PRÓPRIO bot
#      (omni devolve o api_access_token) → /state/.chatwoot-token.
#   3. Etiquetas fila-bot/humano → API do Chatwoot (mesmo caminho do front
#      do omni, Configurações → Etiquetas; o omni não tem endpoint de label).
#   4. Webhook do bot         → API do Chatwoot, idempotente por URL-base;
#      NUNCA toca o webhook do omni (/api/automations/chatwoot/events).
#   5. Automation rule        → REMOVIDA se existir: o omni-route não gerencia
#      automation rules (config fantasma). Quem põe conversa nova na fila-bot
#      é o GATEWAY, no conversation_created (padrão triage-bot).
#   6. Verificação            → GET /api/auth/workspace/me do bot NA API DO
#      OMNI prova que ele autentica pelo caminho do produto.
#
# Segredos (senha admin do omni, senha e token do bot) nunca saem da VM.
# ==============================================================================
set -eu
apk add --no-cache --quiet curl jq

OMNI_ENV=/omni.env
STATE=/state
BOT_ENV="$STATE/.env"
API=http://api:3333
CW=http://chatwoot-rails:3000
BOT_EMAIL=zapin-bot@azapfy.com.br
BOT_NAME='Zapin (bot)'

envval() { grep "^$2=" "$1" 2>/dev/null | head -1 | cut -d= -f2-; }
falha() { echo "ERRO: $1" >&2; exit 1; }

ADMIN_EMAIL="$(envval $OMNI_ENV SEED_ADMIN_EMAIL)"
ADMIN_PASS="$(envval $OMNI_ENV SEED_ADMIN_PASSWORD)"
WEBHOOK_TOKEN="$(envval "$BOT_ENV" WEBHOOK_TOKEN)"
LABEL_BOT="$(envval "$BOT_ENV" LABEL_BOT)"; LABEL_BOT="${LABEL_BOT:-fila-bot}"
LABEL_HUMANO="$(envval "$BOT_ENV" LABEL_HUMANO)"; LABEL_HUMANO="${LABEL_HUMANO:-fila-humano}"
[ -n "$ADMIN_EMAIL" ] && [ -n "$ADMIN_PASS" ] || falha "SEED_ADMIN_EMAIL/PASSWORD ausentes em /omni.env"
[ -n "$WEBHOOK_TOKEN" ] || falha "WEBHOOK_TOKEN ausente em $BOT_ENV — rode o Deploy antes"

# --- 1. Login do admin no plano PRODUTO do omni (senha validada no Chatwoot) --
ADMIN_JSON="$(jq -n --arg e "$ADMIN_EMAIL" --arg p "$ADMIN_PASS" '{email:$e,password:$p}' \
  | curl -sf -X POST "$API/api/auth/workspace/login" -H 'Content-Type: application/json' -d @-)" \
  || falha "login do admin na API do omni falhou"
ADMIN_JWT="$(echo "$ADMIN_JSON" | jq -r .token)"
ADMIN_CW_TOKEN="$(echo "$ADMIN_JSON" | jq -r .chatwoot.accessToken)"
ACC="$(echo "$ADMIN_JSON" | jq -r .chatwoot.accountId)"
[ -n "$ADMIN_JWT" ] && [ "$ADMIN_JWT" != null ] || falha "JWT do admin vazio"
echo "→ login admin no omni ok (conta $ACC)"

# --- 2. Usuário do bot VIA OMNI-ROUTE ----------------------------------------
# 201 + generatedPassword = criado agora. 409 (ou 201 sem senha aplicada) =
# já existia — o caminho oficial do omni é "Redefinir senha", então: tenta a
# credencial de estado da VM; se não servir, reset pelo omni (platform token).
ws_login() { # ws_login <email> <senha> → JSON no stdout (vazio se falhar)
  jq -n --arg e "$1" --arg p "$2" '{email:$e,password:$p}' \
    | curl -sf -X POST "$API/api/auth/workspace/login" \
        -H 'Content-Type: application/json' -d @- || true
}

CREATE_RESP="$(jq -n --arg n "$BOT_NAME" --arg e "$BOT_EMAIL" \
    '{name:$n,email:$e,role:"administrator"}' \
  | curl -s -w '\n%{http_code}' -X POST "$API/api/users/workspace" \
      -H "Authorization: Bearer $ADMIN_JWT" -H 'Content-Type: application/json' -d @-)"
CREATE_CODE="$(echo "$CREATE_RESP" | tail -1)"
CREATE_JSON="$(echo "$CREATE_RESP" | sed '$d')"

BOT_PASS=''
if [ "$CREATE_CODE" = 201 ]; then
  BOT_PASS="$(echo "$CREATE_JSON" | jq -r '.generatedPassword // empty')"
  [ -n "$BOT_PASS" ] && echo "→ usuário do bot criado pelo omni-route"
elif [ "$CREATE_CODE" != 409 ]; then
  echo "$CREATE_JSON" | head -3 >&2
  falha "POST /api/users/workspace devolveu HTTP $CREATE_CODE"
fi

BOT_JSON=''
if [ -z "$BOT_PASS" ] && [ -s "$STATE/.zapin-credentials" ]; then
  BOT_PASS="$(envval "$STATE/.zapin-credentials" ZAPIN_PASSWORD)"
  BOT_JSON="$(ws_login "$BOT_EMAIL" "$BOT_PASS")"
  if [ -n "$BOT_JSON" ]; then
    echo "→ usuário já existia — credencial do estado da VM ainda vale"
  else
    BOT_PASS='' # senha de estado envelheceu — cai para o reset
  fi
fi
if [ -z "$BOT_PASS" ]; then
  BOT_ID="$(curl -sf "$CW/api/v1/accounts/$ACC/agents" -H "api_access_token: $ADMIN_CW_TOKEN" \
    | jq -r --arg e "$BOT_EMAIL" '.[] | select(.email==$e) | .id' | head -1)"
  [ -n "$BOT_ID" ] || falha "usuário $BOT_EMAIL não encontrado para reset"
  BOT_PASS="$(curl -sf -X POST "$API/api/users/workspace/$BOT_ID/reset-password" \
    -H "Authorization: Bearer $ADMIN_JWT" | jq -r '.generatedPassword // empty')"
  [ -n "$BOT_PASS" ] || falha "reset de senha não devolveu generatedPassword"
  echo "→ usuário já existia — senha resetada pelo omni-route (user $BOT_ID)"
fi
umask 077
printf 'ZAPIN_EMAIL=%s\nZAPIN_PASSWORD=%s\n' "$BOT_EMAIL" "$BOT_PASS" > "$STATE/.zapin-credentials"

# --- 3. Login do BOT no omni → token do Chatwoot (estado da VM) ---------------
[ -n "$BOT_JSON" ] || BOT_JSON="$(ws_login "$BOT_EMAIL" "$BOT_PASS")"
[ -n "$BOT_JSON" ] || falha "login do bot na API do omni falhou"
echo "$BOT_JSON" | jq -r .chatwoot.accessToken | tr -d '\n' > "$STATE/.chatwoot-token"
[ -s "$STATE/.chatwoot-token" ] || falha "token do bot veio vazio"
echo "→ token do bot gravado como estado da VM (.chatwoot-token)"

# --- 4. Etiquetas (mesma API que o front do omni usa) -------------------------
# CONVERGE, não só cria: etiqueta existente ganha PATCH para o estado desejado.
# show_on_sidebar:true é obrigatório — sem ele a etiqueta existe na API mas
# fica INVISÍVEL na barra lateral/filtros do produto (foi o sintoma real:
# "as etiquetas não aparecem no omni-router").
LABELS_JSON="$(curl -sf "$CW/api/v1/accounts/$ACC/labels" -H "api_access_token: $ADMIN_CW_TOKEN")"
for l in "$LABEL_BOT" "$LABEL_HUMANO"; do
  case "$l" in
    "$LABEL_BOT") cor='#1F93FF'; desc='Fila do bot Zapin (em atendimento pela IA)' ;;
    *)            cor='#F97316'; desc='Fila humana (transbordo do bot Zapin)' ;;
  esac
  BODY="$(jq -n --arg t "$l" --arg c "$cor" --arg d "$desc" \
    '{title:$t,color:$c,show_on_sidebar:true,description:$d}')"
  LID="$(echo "$LABELS_JSON" | jq -r --arg t "$l" '.payload[] | select(.title==$t) | .id' | head -1)"
  if [ -n "$LID" ]; then
    echo "$BODY" | curl -sf -X PATCH "$CW/api/v1/accounts/$ACC/labels/$LID" \
      -H "api_access_token: $ADMIN_CW_TOKEN" -H 'Content-Type: application/json' -d @- > /dev/null \
      || falha "convergir etiqueta $l (id $LID)"
    echo "→ etiqueta $l convergida (id $LID, visível na sidebar)"
  else
    echo "$BODY" | curl -sf -X POST "$CW/api/v1/accounts/$ACC/labels" \
      -H "api_access_token: $ADMIN_CW_TOKEN" -H 'Content-Type: application/json' -d @- > /dev/null \
      || falha "criar etiqueta $l"
    echo "→ etiqueta $l criada"
  fi
done

# --- 5. Webhook do bot (idempotente por URL-base; o do omni fica intocado) ----
WANT_URL="http://azapfy-bot:8080/webhook?token=$WEBHOOK_TOKEN"
BODY="$(jq -n --arg u "$WANT_URL" \
  '{webhook:{url:$u,subscriptions:["message_created","conversation_updated","conversation_created"]}}')"
WH_ID="$(curl -sf "$CW/api/v1/accounts/$ACC/webhooks" -H "api_access_token: $ADMIN_CW_TOKEN" \
  | jq -r '(.payload.webhooks // .payload)[] | select(.url | startswith("http://azapfy-bot:")) | .id' | head -1)"
if [ -n "$WH_ID" ]; then
  echo "$BODY" | curl -sf -X PATCH "$CW/api/v1/accounts/$ACC/webhooks/$WH_ID" \
    -H "api_access_token: $ADMIN_CW_TOKEN" -H 'Content-Type: application/json' -d @- > /dev/null \
    || falha "atualizar webhook $WH_ID"
  echo "→ webhook do bot atualizado (id $WH_ID; assina conversation_created)"
else
  echo "$BODY" | curl -sf -X POST "$CW/api/v1/accounts/$ACC/webhooks" \
    -H "api_access_token: $ADMIN_CW_TOKEN" -H 'Content-Type: application/json' -d @- > /dev/null \
    || falha "criar webhook do bot"
  echo "→ webhook do bot criado"
fi

# --- 6. Remove automation rule fantasma (omni-route não gerencia rules) -------
curl -sf "$CW/api/v1/accounts/$ACC/automation_rules" -H "api_access_token: $ADMIN_CW_TOKEN" \
  | jq -r '.payload[] | select(.name=="Fila do bot (Zapin)") | .id' \
  | while read -r rid; do
      curl -sf -X DELETE "$CW/api/v1/accounts/$ACC/automation_rules/$rid" \
        -H "api_access_token: $ADMIN_CW_TOKEN" > /dev/null
      echo "→ automation rule fantasma removida (id $rid) — quem etiqueta é o gateway"
    done

# --- 7. VERIFICAÇÃO pela API do omni-route ------------------------------------
BOT_JWT="$(echo "$BOT_JSON" | jq -r .token)"
ME="$(curl -sf "$API/api/auth/workspace/me" -H "Authorization: Bearer $BOT_JWT")" \
  || falha "GET /api/auth/workspace/me do bot falhou"
echo "→ verificação omni-route (workspace/me do bot): $(echo "$ME" \
  | jq -c '{email: (.user.email // .email), role: (.user.role // .role), conta: (.chatwoot.accountId // null)}')"

# Arquivos de estado pertencem ao user deploy do host, não ao root do container.
[ -n "${HOST_UID:-}" ] && chown "${HOST_UID}:${HOST_GID:-$HOST_UID}" \
  "$STATE/.chatwoot-token" "$STATE/.zapin-credentials" || true

echo "SETUP_OK conta=$ACC labels=$LABEL_BOT,$LABEL_HUMANO bot=$BOT_EMAIL"
