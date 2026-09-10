# scripts/

Scripts de conveniência para rodar o stack local inteiro.

## `start-all.sh`

Sobe os **três** serviços já conectados entre si, na ordem certa:

1. **cérebro** (`agente-ia/`, FastAPI) em `:8001` — sobe primeiro porque o backend chama `POST /chat`.
2. **backend** (`backend/`, Go) em `:8080` — sobe com `BRAIN_BASE_URL` apontando para o cérebro e `CHATWOOT_BASE_URL` para o mock.
3. **mock-chatwoot** (`mock-chatwoot/`) em `:9000` — sobe com `GO_WEBHOOK_URL` apontando para o backend.

A **cola mock ↔ backend é o `WEBHOOK_TOKEN`**: o script lê o token do
`backend/.env` e o repassa ao mock, então os dois sempre casam (é o "token do
mock para o backend"). Cada serviço espera o anterior ficar pronto
(`/health` / porta aberta) antes de subir.

```bash
scripts/start-all.sh
# abra http://localhost:9000 e converse
# Ctrl+C derruba os três
```

Portas e token são configuráveis por env:

```bash
BRAIN_PORT=8001 BACKEND_PORT=8080 MOCK_PORT=9000 scripts/start-all.sh
WEBHOOK_TOKEN=outro-token scripts/start-all.sh
```

Logs de cada serviço ficam em `scripts/logs/{cerebro,backend,mock}.log`.

### Pré-requisitos

- `agente-ia/venv` criado e dependências instaladas, `agente-ia/.env` com `OPENROUTER_API_KEY`.
- ChromaDB ingerido: `cd agente-ia && source venv/bin/activate && python -m src.rag.ingest`.
- `backend/.env` preenchido (precisa de `MONGO_URI` acessível para o gate de login).
- `go` e `python3` no PATH.

## `stop-all.sh`

Fallback que mata processos órfãos pelas portas (caso algo escape do Ctrl+C):

```bash
scripts/stop-all.sh
```
