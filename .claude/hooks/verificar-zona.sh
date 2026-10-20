#!/usr/bin/env bash
# Hook PreToolUse (Edit|Write|MultiEdit): isolamento entre agentes paralelos.
# Se a sessão definiu ZONA_ARQUIVOS (prefixos relativos à raiz, separados por
# ':'), qualquer edição fora deles é BLOQUEADA (exit 2). Sem a variável, não
# faz nada — a convenção de zonas continua valendo (docs/plano/05-agentes.md),
# só não é imposta mecanicamente.
#
# Exemplo: ZONA_ARQUIVOS="backend/internal/infodesk/:contratos/" claude
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"
[ -z "${ZONA_ARQUIVOS:-}" ] && exit 0
entrada="$(cat)"
arquivo="$(campo_json "$entrada" tool_input.file_path)"
[ -z "$arquivo" ] && exit 0
rel="$(relativo "$arquivo")"
# Sempre permitido: anotações da própria etapa no plano e ADRs novos.
case "$rel" in
  docs/decisoes/*|docs/plano/legado-permitido.txt) exit 0 ;;
esac
IFS=':' read -r -a zonas <<<"$ZONA_ARQUIVOS"
for z in "${zonas[@]}"; do
  z="${z#./}"
  [ -z "$z" ] && continue
  case "$rel" in
    "$z"*) exit 0 ;;
  esac
done
echo "[zona] edição BLOQUEADA: '$rel' está fora da zona desta sessão (ZONA_ARQUIVOS=$ZONA_ARQUIVOS). Se a etapa realmente exige esse arquivo, pare e relate — não amplie a zona por conta própria." >&2
exit 2
