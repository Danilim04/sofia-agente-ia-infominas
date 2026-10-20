#!/usr/bin/env bash
# Hook PostToolUse (Edit|Write|MultiEdit): formata o arquivo recém-editado.
# Python → ruff format + ruff check --fix ; Go → gofmt -w + go vet do pacote.
# NUNCA bloqueia (exit 0): feedback rápido; quem barra é o guardar-commit.sh e o CI.
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"
entrada="$(cat)"
arquivo="$(campo_json "$entrada" tool_input.file_path)"
[ -z "$arquivo" ] && exit 0
arquivo="${arquivo//\\//}"
[ -f "$arquivo" ] || exit 0

case "$arquivo" in
  *.py)
    PY=""
    for cand in "$RAIZ/agente-ia/venv/bin/python" "$RAIZ/agente-ia/venv/Scripts/python.exe" "$RAIZ/agente-ia/.venv/bin/python" "$RAIZ/agente-ia/.venv/Scripts/python.exe"; do
      [ -x "$cand" ] && PY="$cand" && break
    done
    [ -z "$PY" ] && command -v python >/dev/null 2>&1 && PY=python
    if [ -n "$PY" ] && "$PY" -c "import ruff" 2>/dev/null; then
      (cd "$RAIZ/agente-ia" && "$PY" -m ruff format --quiet "$arquivo" && "$PY" -m ruff check --fix --quiet "$arquivo") \
        || echo "[formatar] ruff apontou problemas em $(relativo "$arquivo") — veja com: ruff check $(relativo "$arquivo")"
    else
      echo "[formatar] ruff não instalado (pip install -r agente-ia/requirements-dev.txt) — arquivo não formatado"
    fi
    ;;
  *.go)
    if command -v gofmt >/dev/null 2>&1; then
      gofmt -w "$arquivo"
      pacote="$(dirname "$arquivo")"
      (cd "$pacote" && go vet . 2>&1 | head -20) || echo "[formatar] go vet apontou problemas em $(relativo "$pacote")"
    else
      echo "[formatar] gofmt não encontrado — instale Go"
    fi
    ;;
esac
exit 0
