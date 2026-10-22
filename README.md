# Sofia — agente de atendimento WhatsApp da Infominas

**Sofia** é a atendente virtual que recebe a reclamação do cliente final da
Infominas pelo WhatsApp, via **Infodesk** (o sistema de atendimento e tickets
da empresa). Ela conversa em português, identifica o cliente (telefone, CPF),
**classifica** o problema com o **JEV** (TypeSafe: classificação determinística
com confiança), **roteia** para o departamento certo, **abre o ticket** na mesa
certa depois da confirmação explícita do cliente e **transfere para um atendente
humano** quando o pedido, a mídia, a confiança, o tom ou o horário exigem.

Princípio de projeto: **o LLM conversa, o código decide.** Autorização, escopo,
fila, prioridade, confirmação, posse de ticket e validação de CPF são código ou
tabela (`agente-ia/config/taxonomia.yaml`, `tool_policy.py`), nunca o modelo. As
regras inegociáveis estão no [`CLAUDE.md`](CLAUDE.md).

O código nasce da refatoração do bot Zapin; o que sobrou do legado está
marcado e listado em [`docs/plano/legado-permitido.txt`](docs/plano/legado-permitido.txt).

## Arquitetura

```
 cliente ──WhatsApp──► Infodesk ──webhook (HMAC)──► GATEWAY Go :8080 ──POST /chat──► CÉREBRO Python :8001
                          ▲                          fila por conversa,                LangGraph + OpenRouter
                          │                          estado bot/humano,                ├─ JEV: intenção, categoria,
                          │                          idempotência de ticket,           │   prioridade, frustração, segurança
                          │                          reconciliador (GET /events),      ├─ guardrails de entrada/saída
                          └──── API pública v1 ◄──── horário humano, bolhas            └─ tools com política + RAG (pgvector)
                                                          ▲                                   │
                                                          └────── /tools/infodesk/* ◄─────────┘
                                                Postgres/pgvector (schema do gateway + índice do RAG)
```

- O **gateway** é o único que fala com o Infodesk. O cérebro só chama o gateway
  (`/tools/infodesk/*`, token compartilhado) e é chamado por ele (Contrato A, `POST /chat`).
- Efeito colateral (abrir ticket, cadastrar solicitante, transferir) em duas
  fases: preparar → confirmação checada por código no turno seguinte → executar.
- CPF, telefone e nome saem mascarados de log, trace e estado enviado ao JEV.

## Rodar localmente

Pré-requisitos: Go (versão do `backend/go.mod`), Python 3.12 com `uv`, Docker só
para a stack de produção e o Postgres dos testes.

```bash
cd agente-ia && uv venv venv --python 3.12 && uv pip install -r requirements.txt -r requirements-dev.txt
cd ../backend && go build ./...
cd .. && scripts/verificar.sh --rapido     # lint, tipos, arquitetura e testes (o que o commit e o CI rodam)
scripts/start-all.sh                       # mock-infodesk :9000 + cérebro :8001 + gateway :8080
scripts/start-all.sh --chainlit            # + simulador do Contrato A em :8000
```

Abra <http://localhost:9000> e converse como o cliente (também faz o papel do
atendente). Sem `OPENROUTER_API_KEY` em `agente-ia/.env` a stack sobe, mas os
turnos que chamam o LLM falham. Variáveis, Postgres de teste e dicas para
Windows: [`.claude/skills/ambiente-testes/SKILL.md`](.claude/skills/ambiente-testes/SKILL.md)
e [`docs/plano/ambiente-dev.md`](docs/plano/ambiente-dev.md). A stack em
contêineres e o deploy em qualquer host com Docker: [`deploy/README.md`](deploy/README.md).

## Onde está cada coisa

| Pasta | O que tem |
|---|---|
| `backend/` | Gateway Go (`bot-infominas`): webhook, engine, store (SQLite/Postgres), cliente Infodesk, `toolsapi`, reconciliador, alertas, testes E2E e de arquitetura |
| `agente-ia/` | Cérebro Python: grafo LangGraph, classificação JEV, guardrails, tools e `tool_policy.py`, RAG, observabilidade, `evals/` (classificação, conversa, juiz LLM) |
| `contratos/` | JSON de exemplo dos contratos Go ↔ Python, testados dos dois lados |
| `mock-infodesk/` | Infodesk falso (REST, webhooks assinados e UI) para dev e E2E |
| `deploy/`, `infra/` | Guia de deploy genérico, `healthcheck.sh`, `checar-env.py`; `infra/` só diz o que falta decidir (P10) |
| `docs/plano/` | Plano, etapas, esteira de qualidade, operação, ambiente de dev |
| `docs/infodesk/` | API do Infodesk (fonte: PDF) e as lacunas em `07-pontos-ambiguos.md` |
| `docs/decisoes/` | ADRs (0001–0010) |
| `docs/zapin/` | Legado do Zapin, só como referência |
| `scripts/` | `verificar.sh`, `start-all.sh`, coletores e análises só-leitura do Infodesk |
| `.claude/` | Skills, agentes e hooks do harness de desenvolvimento |

## Documentação

- Plano: [visão geral](docs/plano/00-visao-geral.md) · [tools](docs/plano/01-ferramentas.md) ·
  [classificação](docs/plano/02-classificacao.md) · [etapas e estado](docs/plano/03-etapas.md) ·
  [esteira de qualidade](docs/plano/04-esteira-qualidade.md) · [agentes](docs/plano/05-agentes.md) ·
  [harness](docs/plano/06-harness.md) · [operação](docs/plano/operacao.md)
- Decisões em aberto e o que o dono do produto decidiu: [caderno de decisões](docs/plano/caderno-de-decisoes.md)
- Como validar em produção: [roteiro de homologação](docs/plano/homologacao.md)
- API do Infodesk: [`docs/infodesk/`](docs/infodesk/README.md) · ADRs: [`docs/decisoes/`](docs/decisoes/README.md)

Quem trabalha com o Claude Code começa pelo [`CLAUDE.md`](CLAUDE.md).
