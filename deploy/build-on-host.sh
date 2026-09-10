#!/usr/bin/env bash
# ==============================================================================
# Build NATIVO das imagens do bot NA PRÓPRIA VM — usado quando a VM não tem a
# mesma arquitetura do runner (a VM atual é ARM64; o GitHub runner é x86_64;
# emular arm64 com QEMU para instalar torch + rodar a ingestão é lento e
# frágil). Roda NA VPS, em /opt/azapfy-bot, chamado por SSH pelo
# deploy-remote.sh --build-on-host, que antes sincroniza as fontes em ./src/.
#
#   deploy/build-on-host.sh <version>
#
# Resultado: azapfy-bot:brain-<version> e azapfy-bot:gateway-<version> no
# docker local da VM — o mesmo que o `docker load` do caminho por imagem.
# O cache de camadas fica na VM (o build seguinte só refaz o que mudou).
# ==============================================================================
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:-}"
[ -n "$VERSION" ] || { echo "Uso: build-on-host.sh <version>" >&2; exit 1; }
REPO=azapfy-bot

[ -d src/agente-ia ] && [ -d src/backend ] ||
  { echo "ERRO: fontes ausentes em $(pwd)/src — o deploy-remote.sh sincroniza antes." >&2; exit 1; }

echo "→ build brain   ($REPO:brain-$VERSION, $(uname -m))…"
docker build -t "$REPO:brain-$VERSION" src/agente-ia

echo "→ build gateway ($REPO:gateway-$VERSION, $(uname -m))…"
docker build -t "$REPO:gateway-$VERSION" src/backend

docker image inspect "$REPO:brain-$VERSION" "$REPO:gateway-$VERSION" \
  --format '✔ {{index .RepoTags 0}}  {{.Architecture}}  {{.Size}} bytes'
