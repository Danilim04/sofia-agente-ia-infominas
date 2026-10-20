#!/usr/bin/env bash
# ==============================================================================
# scripts/verificar.sh — esteira local de qualidade (camadas 1 e 2 do
# docs/plano/04-esteira-qualidade.md). É o MESMO script que o hook de commit
# (.claude/hooks/guardar-commit.sh), o pre-commit do git (.githooks) e o CI
# chamam — uma fonte de verdade.
#
#   scripts/verificar.sh                # = --rapido
#   scripts/verificar.sh --rapido       # lint + tipos + arquitetura + unit (alvo < 90 s)
#   scripts/verificar.sh --completo     # + golangci-lint, -race, contratos, e2e, evals de PR
#   scripts/verificar.sh --so-python    # só o lado agente-ia/
#   scripts/verificar.sh --so-go        # só o lado backend/
#
# Ferramenta ausente: em dev é AVISO (pula); com CI=true ou VERIFICAR_ESTRITO=1
# é FALHA. Assim o hook funciona antes da Onda 0 terminar, e o CI nunca passa
# por falta de ferramenta.
# ==============================================================================
set -u

RAIZ="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MODO="${1:---rapido}"
FALHAS=0
PULADOS=0

vermelho() { printf '\033[31m%s\033[0m\n' "$*"; }
verde()    { printf '\033[32m%s\033[0m\n' "$*"; }
amarelo()  { printf '\033[33m%s\033[0m\n' "$*"; }
tem()      { command -v "$1" >/dev/null 2>&1; }
estrito()  { [ "${CI:-}" = "true" ] || [ "${VERIFICAR_ESTRITO:-}" = "1" ]; }

# Python do projeto: venv se existir, senão o do PATH (no Windows o alias da
# Store pode falhar — ver docs/plano/06-harness.md, "Windows").
PY=""
for cand in "$RAIZ/agente-ia/venv/bin/python" "$RAIZ/agente-ia/venv/Scripts/python.exe" "$RAIZ/agente-ia/.venv/bin/python" "$RAIZ/agente-ia/.venv/Scripts/python.exe"; do
  [ -x "$cand" ] && PY="$cand" && break
done
[ -z "$PY" ] && tem python && PY=python
[ -z "$PY" ] && tem python3 && PY=python3

# etapa <nome> <dir> <comando...>: roda e contabiliza.
etapa() {
  local nome="$1" dir="$2"; shift 2
  printf '\n== %s ==\n' "$nome"
  if (cd "$dir" && "$@"); then verde "ok: $nome"; else vermelho "FALHA: $nome"; FALHAS=$((FALHAS+1)); fi
}

# pular <nome> <como-instalar>
pular() {
  if estrito; then vermelho "FALHA: $1 não instalado ($2)"; FALHAS=$((FALHAS+1)); else amarelo "aviso: $1 não instalado — pulando ($2)"; PULADOS=$((PULADOS+1)); fi
}

py_mod() { "$PY" -c "import importlib.util,sys; sys.exit(0 if importlib.util.find_spec('$1') else 1)" 2>/dev/null; }

# ------------------------------------------------------------------ Python --
python_rapido() {
  local A="$RAIZ/agente-ia"
  [ -d "$A/src" ] || { amarelo "agente-ia/src ausente — pulando Python"; return; }
  if [ -z "$PY" ]; then pular "python" "instale Python 3.12+"; return; fi

  if py_mod ruff; then
    etapa "ruff format --check" "$A" "$PY" -m ruff format --check src tests
    etapa "ruff check" "$A" "$PY" -m ruff check src tests
  else pular "ruff" "pip install -r agente-ia/requirements-dev.txt"; fi

  if py_mod mypy; then etapa "mypy" "$A" "$PY" -m mypy src
  else pular "mypy" "pip install -r agente-ia/requirements-dev.txt"; fi

  if py_mod importlinter && grep -q 'tool.importlinter' "$A/pyproject.toml" 2>/dev/null; then
    # o pacote não tem __main__: usa o console script ao lado do python do venv
    LI="$(dirname "$PY")/lint-imports"; [ -x "$LI" ] || [ -x "$LI.exe" ] || LI=lint-imports
    etapa "import-linter" "$A" "$LI"
  else pular "import-linter" "pip install import-linter (+ [tool.importlinter] no pyproject)"; fi

  if py_mod pytest; then etapa "pytest (unit + arquitetura)" "$A" "$PY" -m pytest -q
  else pular "pytest" "pip install -r agente-ia/requirements.txt"; fi
}

python_completo() {
  local A="$RAIZ/agente-ia"
  [ -d "$A/evals" ] || return
  if py_mod pytest; then etapa "evals de PR (determinísticos)" "$A" "$PY" -m pytest -q -m evals_pr; fi
}

# ---------------------------------------------------------------------- Go --
go_rapido() {
  local B="$RAIZ/backend"
  [ -f "$B/go.mod" ] || { amarelo "backend/go.mod ausente — pulando Go"; return; }
  if ! tem go; then pular "go" "https://go.dev/dl"; return; fi
  etapa "gofmt" "$B" bash -c 'f=$(gofmt -l . ); if [ -n "$f" ]; then echo "$f"; exit 1; fi'
  etapa "go vet" "$B" go vet ./...
  etapa "go test" "$B" go test ./...
}

go_completo() {
  local B="$RAIZ/backend"
  [ -f "$B/go.mod" ] || return
  tem go || return
  if tem golangci-lint; then etapa "golangci-lint" "$B" golangci-lint run ./...
  else pular "golangci-lint" "https://golangci-lint.run/docs/welcome/install/"; fi
  # -race exige cgo (gcc). Sem gcc (Windows sem toolchain C) roda só com -cover
  # e avisa; no CI (Linux) é estrito. Detector de corrida fica garantido pelo CI.
  if [ "$(go env CGO_ENABLED)" = "1" ] && tem gcc; then
    etapa "go test -race -cover" "$B" go test -race -cover ./...
  elif estrito; then
    etapa "go test -race -cover" "$B" go test -race -cover ./...
  else
    amarelo "aviso: -race indisponível (CGO_ENABLED=0 ou sem gcc) — rodando go test -cover sem detector de corrida"
    etapa "go test -cover (sem -race)" "$B" go test -cover ./...
  fi
  if grep -rq 'go:build e2e' "$B/internal" 2>/dev/null; then
    etapa "e2e (tag e2e)" "$B" go test -tags e2e ./internal/e2e/...
  fi
}

# -------------------------------------------------------------------- main --
echo "verificar.sh modo=$MODO raiz=$RAIZ"
case "$MODO" in
  --rapido)    python_rapido; go_rapido ;;
  --completo)  python_rapido; go_rapido; python_completo; go_completo ;;
  --so-python) python_rapido; [ "${2:-}" = "--completo" ] && python_completo ;;
  --so-go)     go_rapido;     [ "${2:-}" = "--completo" ] && go_completo ;;
  *) echo "uso: $0 [--rapido|--completo|--so-python|--so-go]"; exit 64 ;;
esac

echo
if [ "$FALHAS" -gt 0 ]; then vermelho "verificar.sh: $FALHAS falha(s), $PULADOS pulado(s)"; exit 1; fi
verde "verificar.sh: tudo ok ($PULADOS pulado(s))"
