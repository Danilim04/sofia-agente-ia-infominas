# scripts/

| Script | Para quê |
|---|---|
| `verificar.sh` | Fonte única da esteira (`--rapido` no commit, `--completo` no merge). Ver skill `ambiente-testes`. |
| `start-all.sh` / `stop-all.sh` | Stack local de dev: mock-infodesk + cérebro + gateway (+ Chainlit). Abaixo. |
| `dev.env.example` | Variáveis de dev (sem segredos) lidas pelo `start-all.sh`. |
| `dev/taxonomia.dev.yaml` | Taxonomia **só para dev**, com as mesas do mock — não é o mapeamento real (P1). |
| `instalar-hooks-git.sh`, `reset-gate.sh` | Hooks do git e reinício do gate do harness. |

Guia completo, com o desenho e o Chainlit: `docs/plano/ambiente-dev.md`.

## `start-all.sh`

Sobe, nesta ordem, **mock-infodesk** (`:9000`), **cérebro** (`agente-ia/`,
`:8001`) e **gateway** (`backend/`, `:8080`), espera cada porta responder,
confere que a taxonomia carregou sem problemas e imprime onde abrir cada
coisa. Chave, segredo do webhook e `TOOLS_API_TOKEN` são forçados para casar
entre os três; `TAXONOMIA_PATH` aponta para `dev/taxonomia.dev.yaml`.

```bash
cp scripts/dev.env.example scripts/dev.env   # opcional, fora do git
scripts/start-all.sh                         # Ctrl+C derruba os três
scripts/start-all.sh --chainlit              # + Chainlit em :8000
MOCK_PORT=9100 scripts/start-all.sh          # o shell vence o dev.env
```

Segredos reais (`OPENROUTER_API_KEY`...) ficam em `agente-ia/.env` ou no
ambiente, nunca no `dev.env.example`. Logs em `scripts/logs/`; o gateway
compilado, em `scripts/.bin/` (ambos ignorados pelo git).

Pré-requisitos: `go` e `curl` no PATH; `agente-ia/venv` com as dependências
(senão usa o Python do PATH); no Windows, Git Bash. Para o RAG, o índice
ingerido (`python -m src.rag.ingest`).

## `stop-all.sh`

Fallback que mata processos órfãos pelas portas (caso algo escape do Ctrl+C);
`--chainlit` inclui a porta do Chainlit:

```bash
scripts/stop-all.sh
```
