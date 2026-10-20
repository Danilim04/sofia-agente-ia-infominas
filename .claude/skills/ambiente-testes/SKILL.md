---
name: ambiente-testes
description: Como preparar e usar o ambiente local de testes deste repositório — venv Python, Go, ferramentas de lint, scripts/verificar.sh, mock-infodesk, stack completa com start-all.sh e docker compose, variáveis de ambiente e o que fazer no Windows. Carregue antes de rodar testes, subir serviços ou quando um comando de verificação falhar por ferramenta ausente.
---

# Ambiente de testes

Fonte única da esteira: `scripts/verificar.sh` (modos `--rapido`,
`--completo`, `--so-python`, `--so-go`). Tudo abaixo existe para esse script
e o CI rodarem verdes.

## Preparar uma vez

```bash
# Python (agente-ia/)
cd agente-ia
python -m venv venv && source venv/bin/activate      # Windows Git Bash: source venv/Scripts/activate
pip install -r requirements.txt -r requirements-dev.txt   # ruff, mypy, import-linter, pytest-cov
cp .env.example .env                                   # OPENROUTER_API_KEY; TYPESAFE_API_KEY só para -m jev

# Go (backend/)
cd backend && go build ./... && go test ./...
# golangci-lint: https://golangci-lint.run/docs/welcome/install/ (binário; versão pinada em .golangci.yml)

# hooks do git para humanos (opcional; o Claude Code já usa .claude/settings.json)
scripts/instalar-hooks-git.sh
```

Sem venv, `verificar.sh` usa o `python` do PATH. **Windows**: o alias
`python` da Microsoft Store pode falhar ("Python was not found"); use o
interpretador real (ex.: `C:\Users\<user>\AppData\Local\Python\bin\python.exe`)
para criar o venv — depois o script acha `venv/Scripts/python.exe` sozinho.
Hooks e scripts exigem **bash** (Git Bash).

## Rodar

| Objetivo | Comando |
|---|---|
| Tudo rápido (o que o commit exige) | `scripts/verificar.sh --rapido` |
| Tudo (o que o merge exige) | `scripts/verificar.sh --completo` |
| Um teste Python | `cd agente-ia && pytest tests/test_x.py::test_y -v` |
| Um pacote Go | `cd backend && go test ./internal/engine -run TestNome -v` |
| Store no Postgres real | `PG_TEST_URL=postgresql://rag:rag@localhost:5432/rag go test ./internal/store -v` |
| Evals de PR | `cd agente-ia && pytest -m evals_pr` |
| Evals de classificação com JEV | `cd agente-ia && TYPESAFE_API_KEY=... python -m evals.classificacao --fonte sintetico --jev` |
| Marcadores pytest | `rede`, `jev`, `llm_real`, `e2e`, `evals_pr` (default exclui rede/jev/llm_real/e2e) |

## Stack local (sem Infodesk real)

```bash
scripts/start-all.sh          # cérebro :8001 + gateway :8080 + mock-infodesk :9000 (UI em http://localhost:9000)
scripts/stop-all.sh
```

O `mock-infodesk/` (stdlib, sem pip) finge ser o Infodesk: recebe nossas
chamadas REST e emite webhooks assinados ao gateway. Para usar LLM/JEV falsos
na stack: `LLM_FAKE=1 JEV_FAKE=1` (respostas scriptadas de
`tests/fakes.py`). Com docker: `docker compose up --build` (exige
`PGVECTOR_PASSWORD` no `.env`).

## Variáveis que os testes precisam

| Variável | Para quê | Default dos testes |
|---|---|---|
| `OPENROUTER_API_KEY` | só `-m llm_real` | `test-openrouter-key` (conftest) |
| `TYPESAFE_API_KEY` | só `-m jev` | ausente → testes `jev` pulados |
| `PG_TEST_URL` | `store_test.go` no Postgres | ausente → só SQLite |
| `TOOLS_API_TOKEN` | contrato cérebro ↔ gateway | `dev-token` nos fakes |
| `INFODESK_*` | gateway real | mock usa `http://localhost:9000`, chave `ik_test_mock` |

## Quando algo falha

- "não instalado — pulando": em dev é aviso; instale antes de abrir PR (o CI é estrito).
- Teste de arquitetura reclamando de termo legado: limpe o arquivo ou confira
  se a etapa permite; **não** adicione linha em `legado-permitido.txt`.
- `go test` lento: `-run` + pacote; `-race` só no `--completo`.
- Hook bloqueou o commit: leia o resumo, corrija, commit de novo. `PULAR_VERIFICACAO=1` só para WIP em branch própria.
