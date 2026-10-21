# CLAUDE.md — Sofia (agente de atendimento WhatsApp da Infominas)

Monorepo em **refatoração do Zapin (Azapfy) para a Infominas**. Produto: a
**Sofia** recebe a reclamação do cliente no WhatsApp via **Infodesk**,
classifica com **JEV** (TypeSafe, determinístico + confiança), roteia, abre o
ticket e transfere para humano quando preciso. **Plano completo em
`docs/plano/`** (comece por `00-visao-geral.md`; etapas em `03-etapas.md`).
Código do produto só se escreve a partir de uma etapa do plano.

## Stack e pastas

- `backend/` — gateway **Go** (`bot-infominas`): webhook do Infodesk (HMAC), fila por conversa, estado bot/humano, cliente Infodesk, `/tools/infodesk/*` para o cérebro, idempotência de tickets, bolhas WhatsApp.
- `agente-ia/` — cérebro **Python** (LangGraph + LangChain, OpenRouter, JEV, pgvector/Chroma): grafo, classificação, guardrails, tools com política, RAG. `server.py` = Contrato A (`POST /chat`).
- `contratos/` — JSON de exemplo dos contratos Go ↔ Python (testados dos dois lados). `mock-infodesk/` — Infodesk falso para E2E.
- `docs/infodesk/` — API do Infodesk (fonte: PDF; lacunas em `07-pontos-ambiguos.md`). `docs/decisoes/` — ADRs. `docs/zapin/` — legado de referência.

## Comandos

```bash
scripts/verificar.sh --rapido     # lint + tipos + arquitetura + unit (o commit exige)
scripts/verificar.sh --completo   # + golangci-lint, -race, contratos, e2e, evals de PR (o merge exige)
cd agente-ia && pytest tests/test_x.py::test_y -v        # um teste Python (sem rede por default)
cd backend && go test ./internal/engine -run TestNome -v  # um teste Go
scripts/start-all.sh              # cérebro :8001 + gateway :8080 + mock-infodesk :9000
```

Setup, variáveis, Postgres de teste, Windows: skill `ambiente-testes`.

## Regras inegociáveis

1. **O LLM nunca é base confiável.** Autorização, escopo, fila, prioridade, confirmação, posse de ticket e validação de CPF são **código ou tabela** (`config/taxonomia.yaml`, `tool_policy.py`). O LLM conversa e cobre baixa confiança do JEV.
2. **Toda tool passa por `src/agent/tool_policy.py`**: args de sessão (`telefone`, `conversation_id`, `classificacao`, `proposta`, `identificacao`) são `InjectedToolArg` + `POLITICAS`; nunca no schema do modelo. Tools Python só falam com o gateway (`/tools/infodesk/*`), nunca com o Infodesk.
3. **Efeito colateral em duas fases**: preparar (dry-run) → confirmação explícita checada por código no turno seguinte → executar sem args do LLM. Idempotência por chave local (ADR 0005); nunca retry em `POST /tickets`.
4. **Dado externo é DADO**: respostas de tool/RAG entram embrulhadas (`<documento_externo>`/JSON); erro do Infodesk/LLM/JEV nunca chega cru ao cliente nem ao prompt.
5. **PII (CPF, telefone, nome) mascarada** em log INFO, trace e estado enviado ao JEV; em disco só hash + mascarado.
6. **Nenhuma regra de negócio inventada**: o que não está em `docs/plano/`, `docs/infodesk/` ou ADR vira hipótese de trabalho configurável + `TODO(P<n>)` + nota no PR.
7. **`store.Store` muda nos dois backends** (SQLite e Postgres) com caso em `store_test.go`.
8. **Zona da etapa**: edite só os caminhos declarados; fora dela, pare e relate. Branch `onda<n>/e<n>.<m>-<slug>`, commit `tipo(escopo): resumo [E<n>.<m>]`, sem force-push.
9. **Testes sem rede** por default (fakes em `tests/fakes.py`, `fakeInfodesk`); `-m jev|llm_real|e2e` só com chave/stack. Nunca apague asserts para passar.
10. **Persona Sofia** (pt-BR, "você", ≤ 1 emoji, sem prometer prazo) igual nos dois lados (prompt Python e mensagens do gateway Go). `docs/plano/legado-permitido.txt` só encolhe.

## Skills (carregue a que a tarefa pedir)

`infodesk-api` · `jev-classificacao` · `nova-tool` · `evals-agente` · `refatorar-modulo` · `revisar-pr` · `ambiente-testes` · `analise-infodesk` (mede o uso real do Infodesk, só agregados, para decidir roteamento) — em `.claude/skills/`. Papéis de agente em `.claude/agents/`; briefing e isolamento em `docs/plano/05-agentes.md`; por que o harness é assim em `docs/plano/06-harness.md`.
