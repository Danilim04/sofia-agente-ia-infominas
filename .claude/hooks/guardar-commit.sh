#!/usr/bin/env bash
# Hook PreToolUse (Bash): se o comando É um `git commit` (no início ou após
# ; && || |), roda a esteira rápida (scripts/verificar.sh --rapido) ANTES e
# bloqueia o commit se falhar (exit 2). Espelha o .githooks/pre-commit usado
# por humanos. Burlar só com PULAR_VERIFICACAO=1 no ambiente (o CI barra igual).
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"
entrada="$(cat)"
cmd="$(campo_json "$entrada" tool_input.command)"
# Só comando git commit de verdade — não um "git commit" citado dentro de echo/grep.
printf '%s' "$cmd" | grep -Eq '(^|[;&|]|\$\()[[:space:]]*git[[:space:]]+(-C[[:space:]]+[^[:space:]]+[[:space:]]+)?commit([[:space:]]|$)' || exit 0
if [ "${PULAR_VERIFICACAO:-}" = "1" ]; then
  echo "[guardar-commit] PULAR_VERIFICACAO=1 — esteira não executada (o CI vai rodar)" >&2
  exit 0
fi
if [ ! -f "$RAIZ/scripts/verificar.sh" ]; then
  echo "[guardar-commit] scripts/verificar.sh não existe — commit bloqueado até a esteira existir" >&2
  exit 2
fi
saida="$(bash "$RAIZ/scripts/verificar.sh" --rapido 2>&1)"
status=$?
if [ $status -ne 0 ]; then
  {
    echo "[guardar-commit] esteira rápida FALHOU — commit bloqueado. Corrija e tente de novo."
    echo "$saida" | grep -E 'FALHA|error|Error|FAILED|failed' | head -30
    echo "(saída completa: rode scripts/verificar.sh --rapido)"
  } >&2
  exit 2
fi
echo "[guardar-commit] esteira rápida ok"
exit 0
