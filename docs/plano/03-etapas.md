# 03 — Plano em etapas (ondas paralelizáveis)

Regras de leitura:

- **Onda** = conjunto de etapas que podem rodar **ao mesmo tempo**, cada uma
  numa sessão/worktree própria, **sem tocar nos mesmos arquivos** ("zona").
  Entre ondas a dependência é explícita: a onda N+1 só começa quando as
  etapas de que depende estão **mergeadas na `main`**.
- Cada etapa tem um **ID** (`E<onda>.<n>`) usado no nome da branch
  (`onda2/e2.3-tools-python`), no commit (`feat(tools): ... [E2.3]`) e no PR.
- **Critério de pronto** é verificável por comando ou por diff; se o agente
  não consegue provar, a etapa não está pronta.
- Estimativa: **P** (≤ meia sessão), **M** (uma sessão), **G** (uma sessão
  longa ou duas; dividir se passar disso).
- Subagentes e skills referem-se a `.claude/agents/<nome>.md` e
  `.claude/skills/<nome>/SKILL.md`. Todo agente carrega sempre `CLAUDE.md`
  (automático) e a skill `ambiente-testes` quando precisa rodar algo.
- Pendências (`P1`…`P11`) estão em `00-visao-geral.md`. Etapa que depende de
  pendência aberta implementa a **hipótese de trabalho** de forma
  configurável e deixa `// TODO(P<n>)` no código.

Ordem de execução por sessão: ler `CLAUDE.md` → ler esta etapa → ler as
skills listadas → ler os arquivos da zona → implementar → rodar
`scripts/verificar.sh` → abrir PR com o template → pedir revisão
(`revisor`).

---

## Onda 0 — Fundação do repositório (sequencial, uma sessão: `refatorador`)

Sem esta onda os hooks de `.claude/settings.json` não têm ferramentas para
rodar. É a única onda que pode tocar em arquivos de todo o repositório.

| ID | Etapa | Est. | Skills |
|---|---|---|---|
| E0.1 | **Tooling Python**: criar `agente-ia/pyproject.toml` com `[tool.ruff]` (line-length 100, `select = ["E","F","I","B","UP","S","N","PL"]`, excluir `evals/bruto`), `[tool.mypy]` (`python_version = "3.12"`, `strict = true` para `src/classificacao`, `src/tools`, `src/identity`; `ignore_missing_imports`), `[tool.pytest.ini_options]` (markers `rede`, `jev`, `llm_real`, `e2e`; `addopts = "-m 'not rede and not jev and not llm_real and not e2e'"`), `[tool.importlinter]` (contratos da seção 04). `requirements-dev.txt`: ruff, mypy, import-linter, pytest-cov, types-PyYAML. `scripts/verificar.sh` com modos `--rapido` (ruff + mypy + pytest unit) e `--completo` (+ go, contrato, e2e com mock). Funciona em Git Bash/Linux. | M | refatorar-modulo, ambiente-testes |
| E0.2 | **Tooling Go**: `backend/.golangci.yml` (errcheck, govet, staticcheck, unused, gosec, revive, depguard com lista de pacotes proibidos: `bot-azapfy/*`, `go.mongodb.org/*`), `gofmt` no verificar, `go test -race -cover`. Instruções de instalação do golangci-lint em `ambiente-testes`. | P | ambiente-testes |
| E0.3 | **CI**: `.github/workflows/ci.yml` (jobs: `lint-python`, `lint-go`, `arquitetura`, `test-python`, `test-go` (com Postgres service para `PG_TEST_URL`), `contratos`, `evals-pr`); `evals-noturnos.yml` (cron 03:00 BRT: `jev`, `llm_real`, juiz) com secrets `TYPESAFE_API_KEY`, `OPENROUTER_API_KEY`; `deploy.yml` passa a `workflow_dispatch` apenas (P10) e `configure-chatwoot.yml` é removido; branch protection documentada em `04`. | M | ambiente-testes |
| E0.4 | **Testes de arquitetura v0**: `agente-ia/tests/test_arquitetura.py` (roda `lint-imports`; varre `src/` e `backend/` por termos do domínio antigo `azapfy|zapin|chatwoot|mongo|sac_|azapers|zprs|zpf-` **menos** os caminhos listados em `docs/plano/legado-permitido.txt` — a lista nasce cobrindo tudo e **cada onda a encolhe**); `backend/internal/arquitetura_test.go` (via `go list -deps`: `engine` não importa `toolsapi`; `infodesk` não importa `engine`/`store`; ninguém importa `chatwoot|mongo|sac|identity` após E1.1). | M | refatorar-modulo |
| E0.5 | **Legado para `docs/zapin/`**: mover `arquitetura-bloco-a-*.md`, `laudo-*.md`, `plano-migracao-pgvector.md`, `contrato-b-*`, `memory.md`, `task.md`, `agente-ia/plano_de_execucao.md` e o `CLAUDE.md` antigo (já em `docs/zapin/CLAUDE-legado.md`); README raiz reescrito (3 parágrafos: o que é, como rodar, onde está o plano). | P | refatorar-modulo |
| E0.6 | **Renomear módulo Go** `bot-azapfy` → `bot-infominas` (`go.mod` + todos os imports), `name:` do compose → `sofia-bot`, imagens `sofia-bot:{brain,gateway}-<tag>`. `go build ./... && go test ./...` verdes. | P | refatorar-modulo |

**Pronto da onda 0**: `scripts/verificar.sh --rapido` e `--completo` rodam
localmente e no CI (verde, com a allowlist de legado cheia); hooks de
`.claude/settings.json` executam sem erro de "comando não encontrado".

---

## Onda 1 — Tirar o domínio antigo, entrar o Infodesk (paralelo: 5 sessões)

| ID | Etapa | Zona (só estes caminhos) | Depende | Est. | Subagente | Skills |
|---|---|---|---|---|---|---|
| E1.1 | **Go sem Chatwoot/Mongo/SAC/gate**: apagar `internal/{chatwoot,mongo,sac,identity}`; `engine` passa a depender de uma interface `Transporte` (`EnviarMensagem`, `TransferirDepartamento`, `LerConversa`) e de um `inbound` neutro (`{ConversationID string, Telefone, Nome, Texto, MsgID, TemMidia bool, CriadoEm}`); remover gate (sem login): a engine encaminha direto ao cérebro; `store`: `conversation_id` vira `string`, apagar `gate_state`/`identities`, manter `processed_events` e `falhas` (prefixo `SOF-`); `config`: remover vars Chatwoot/Mongo/SAC, adicionar `INFODESK_*` (lista em `01`, seção B) e `DEPARTAMENTO_HUMANO_PADRAO`; `alerta`: renomear `zapin_*` → `sofia_*`; testes: fakes `fakeTransporte`, manter cobertura de coalescer/disjuntor/transferência/whatsapp. `main.go` compila com `infodesk` ainda ausente usando um `transporteNulo` (log). | `backend/**` exceto `backend/internal/infodesk/**` | E0.* | G | refatorador | refatorar-modulo, infodesk-api |
| E1.2 | **Cliente Go do Infodesk** `internal/infodesk`: `Client` (Options: BaseURL, APIKey, HTTP, Now, cache TTL), métodos da seção B do `01` (`EnviarMensagem`, `TransferirDepartamento`, `Atribuir`, `Encerrar`, `Mesas`, `Departamentos`, `Atendentes`, `CriarTicket`, `Ticket`, `ResponderTicket`, `Conversas`, `Conversa`), tipos (`Ticket{Number string; Bruto}` tolerante a número/string), `*HTTPError{Status, Mensagem}` com `Transitorio()` (429/5xx/rede), higienização de corpo de erro (PII), `VerificarAssinatura(secret, raw, header)` (`sha256=` + tempo constante), tipos do webhook (`Evento`, `MensagemRecebida`). Testes com `httptest` cobrindo cada rota, 401/403/404/429, Retry-After, assinatura válida/inválida. Nenhum retry em `CriarTicket`. | `backend/internal/infodesk/**` | E0.6 | M | implementador-go | infodesk-api, nova-tool |
| E1.3 | **Python sem SAC/Mongo/AzapDocs, persona Sofia**: apagar `src/tools/{sac_tools,crm_mocks,identidade_mock}.py`, `src/identity/login_extractor.py`, `FonteAPIDocumentacao` e `sync --fonte api`, endpoint `/extract-login`, testes correspondentes; `get_default_tools()` vazio temporariamente (teste `test_default_tools` ajustado); `prompts.py`: `SYSTEM_PROMPT_AGENTE` reescrito para a **Sofia** (seções: persona, formato WhatsApp, com quem fala, identidade imutável, sobre a Infominas (placeholder P6), base de conhecimento, anti-injection, ferramentas, fluxo de chamado em 2 fases com identificação, fora de escopo), `RESPOSTA_*` no tom novo; `_periodo_do_dia` mantido; `falhas.py` prefixo `SOF-`; `config.py`: remover `sac_*`, `docs_api_*`, adicionar `infodesk_tools_base_url/token/timeout`, `jev_*`, `ident_*`; `state.py`: adicionar `classificacao`, `identificacao` (persistem), remover `proposta_chamado` da docstring do Mongo; `app.py` (Chainlit) pede telefone + nome; `.env.example` dos dois níveis atualizado. `pytest` verde. | `agente-ia/**` exceto `agente-ia/evals/**`, `agente-ia/config/**`, `agente-ia/src/classificacao/**` | E0.* | G | refatorador | refatorar-modulo |
| E1.4 | **`mock-infodesk/`** (stdlib Python, sem pip, como o `mock-chatwoot`): servidor em `:9000` que (a) expõe a REST do Infodesk usada por nós (`/integration/v1/{desks,users,departments,tickets,tickets/:n,tickets/:n/reply,conversations,conversations/:id,conversations/:id/{messages,assign,department,finish}}`) com estado em memória e erros realistas (401 sem chave, 403 por escopo configurável, 404, 429 opcional, 4xx de validação sem `deskId`); (b) emite webhooks assinados (`X-Infominas-Signature`) para `GATEWAY_WEBHOOK_URL` quando o "cliente" escreve na UI; (c) UI mínima (telefone, nome, enviar texto, enviar "áudio" = body vazio, ver bolhas, ver tickets abertos, ver departamento atual). `README.md` com como rodar e variáveis. Teste `mock_infodesk_test.py` (stdlib `unittest`). | `mock-infodesk/**`, `scripts/start-all.sh`, `scripts/stop-all.sh` | E0.* | M | implementador-python | infodesk-api, ambiente-testes |
| E1.5 | **Scaffolding de evals**: `agente-ia/evals/` com `README.md`, formatos (seção "Dataset" do `02`), `anonimizar.py` (CLI: entrada CSV/JSON bruto em `evals/bruto/` [gitignored] → JSONL anonimizado + relatório do que foi substituído; regex CPF/CNPJ/telefone/e-mail/CEP/placa, lista de nomes próprios via `config/nomes.txt` opcional), `classificacao/sintetico.jsonl` v0 (≥ 15 casos por categoria do placeholder, com pt-BR coloquial e erros de digitação), `conversa/TEMPLATE.yaml`, `relatorio.py` (métricas da seção "Dataset"), runners `python -m evals.classificacao --fonte sintetico|historico [--jev|--llm]` e `python -m evals.conversa` (esqueleto que importa `build_graph`; marca `xfail` até E2.2/E2.3), `config/taxonomia.yaml` placeholder (formato do `02`). | `agente-ia/evals/**`, `agente-ia/config/**`, `.gitignore` | E0.1 | M | escritor-evals | evals-agente, jev-classificacao |
| E1.6 | **Contratos** `contratos/`: `contrato-a/{chat-request,chat-response}.json`, `tools-infodesk/<endpoint>/{request,ok,erro-<motivo>}.json` exatamente como a seção A do `01`; `README.md` explicando que os testes Go (`toolsapi`) e Python (`infodesk_tools`) carregam esses arquivos; ADR `docs/decisoes/0006-contratos-versionados.md`. | `contratos/**`, `docs/decisoes/0006-*.md` | — | P | implementador-tool | nova-tool |

**Pronto da onda 1**: `grep -ri "chatwoot\|mongo\|azapfy" backend/ agente-ia/src` retorna só o que está em `legado-permitido.txt` (que encolheu para ≤ 10 linhas); `go test ./...` e `pytest` verdes; `mock-infodesk` sobe e recebe/emite; `python -m evals.classificacao --fonte sintetico --llm` roda (resultado não importa ainda).

---

## Onda 2 — Núcleo: transporte Infodesk, classificação, tools Python (paralelo: 4 sessões)

| ID | Etapa | Zona | Depende | Est. | Subagente | Skills |
|---|---|---|---|---|---|---|
| E2.1 | **Gateway recebe e responde pelo Infodesk**: `internal/webhook` reescrito para o formato Infodesk (token `?token=` opcional + HMAC obrigatório quando `INFODESK_WEBHOOK_SECRET` definido; `200` imediato; dedup por `id` e `data.message.id`; `organizationId`; só `sender == CLIENT`; `conversation.finished` → `encerrarEpisodio`; `conversation.started` → só log); `engine` ligado ao `infodesk.Client` (satisfaz `Transporte`), caminho único de transferência via `TransferirDepartamento(DEPARTAMENTO_HUMANO_PADRAO)` + estado `conversas.estado = transferida` (ignora webhooks dela até `finished`), mídia (`TemMidia` = body vazio) → transfere com aviso, pedido explícito (regex pt-BR) → transfere, `acoes` do `ChatResponse` executadas (`transferir`), `classificacao` gravada; `store`: tabelas `conversas`, `contatos`, `tickets` nos **dois backends** + `store_test.go`; `brain.ChatRequest` com `nome_contato`, `contato`, `estado_conversa`; `main.go` wiring completo. Testes: `engineCompleta` com `fakeInfodesk`, cenários: rajada vira um turno, mídia transfere, conversa transferida fica muda, `finished` limpa, falha definitiva do cérebro transfere e alerta, HMAC inválido = 401. | `backend/internal/{webhook,engine,store,config,brain,alerta}/**`, `backend/cmd/**` | E1.1, E1.2, E1.6 | G | implementador-go | infodesk-api, nova-tool, ambiente-testes |
| E2.2 | **Classificação JEV no cérebro**: `src/classificacao/{jev.py,perguntas.py,taxonomia.py,roteador.py,fallback_llm.py,tipos.py}` conforme `02` (SDK `typesafe-sdk` com `AsyncTypeSafeClient`, timeout 2 s, `RetryPolicy(max_retries=2)`, modelo pinado, fail-soft; `Classificacao` dataclass; `estado_para_jev()` que monta a transcrição mascarando CPF); nó `classificar` no grafo entre `input_guardrail` e `retrieve` (`retrieve` só se intenção ∈ {duvida_informacao, outro}); guardrail de input usa `malicioso`/`fora_de_escopo` do JEV em vez do classificador LLM (regex mantida); `state.classificacao` pegajosa; system prompt recebe bloco "Classificação" e "Identificação"; `ChatResponse.classificacao` e `acoes` (`transferir` por frustração/confiança/pedido); `POST /classificar`; `tools_node` ganha gancho `efeito_pos_tool(nome, resultado, state)` importado de `src/tools/efeitos.py` (default: no-op) para E2.3 preencher. Testes com `FakeJEV` (respostas scriptadas por pergunta) cobrindo cada linha da tabela de limiares, fail-soft, pegajosidade, retrieve condicional. | `agente-ia/src/{classificacao,agent,security}/**` exceto `src/agent/tool_policy.py`; `agente-ia/server.py`; `agente-ia/tests/{test_classificacao*,test_graph*,test_guardrails*,test_server*}.py` | E1.3, E1.5 | G | implementador-python | jev-classificacao, evals-agente |
| E2.3 | **Tools Python do Infodesk**: `src/tools/infodesk_tools.py` com as 6 tools da seção A do `01` (docstrings = instruções ao LLM, `_post` igual ao padrão do Zapin com `X-Tools-Token`, chave de idempotência calculada no `abrir`), `src/tools/efeitos.py` (carimba proposta no `preparar`, consome no `abrir`, grava `identificacao` no `informar_identificacao`), `src/identity/cpf.py` (validação DV + máscara) e `contrato.py`, `src/agent/tool_policy.py` reescrito (`POLITICAS` das 6 tools, resolvers `classificacao_da_sessao`, `identificacao_da_sessao`, validadores `cpf`, `contrato`, `numero`, `motivo`; precondições `exigir_identificado`, `exigir_categoria`, `exigir_proposta_confirmada` com `CONFIRMACOES_EXPLICITAS` da Sofia), `get_default_tools()` = as 6. Testes: política (cada recusa), tools com `httpx` monkeypatchado devolvendo os JSON de `contratos/`, e2e de 2 turnos (preparar → "sim" → abrir) com LLM scriptado. | `agente-ia/src/tools/**`, `agente-ia/src/identity/**`, `agente-ia/src/agent/tool_policy.py`, `agente-ia/tests/{test_tool_policy,test_infodesk_tools,test_cpf,test_e2e_*}.py` | E1.3, E1.6, E2.2 (gancho `efeitos`) | G | implementador-tool | nova-tool, infodesk-api |
| E2.4 | **Evals de classificação v1**: runner usa `src.classificacao.classificar()` real contra o JEV (`-m jev`, precisa `TYPESAFE_API_KEY`) e contra o fallback LLM; relatório de calibração; comparação de janelas de contexto (1/3/6/10 msgs); dataset sintético ampliado para ≥ 150; `evals/README.md` com como interpretar. Critério: relatório gerado em `evals/relatorios/<data>.md` (gitignored, exemplo commitado). | `agente-ia/evals/**` | E2.2 | M | escritor-evals | evals-agente, jev-classificacao |

**Pronto da onda 2**: `scripts/start-all.sh` sobe cérebro + gateway +
mock-infodesk e uma conversa pela UI do mock chega ao LLM e volta como bolhas
(tools ainda recusam no gateway porque `/tools/infodesk/*` não existe — a
resposta de recusa é tratada graciosamente); `pytest` e `go test` verdes;
`legado-permitido.txt` ≤ 5 linhas.

---

## Onda 3 — Tools no gateway, RAG, harness (paralelo: 4 sessões)

| ID | Etapa | Zona | Depende | Est. | Subagente | Skills |
|---|---|---|---|---|---|---|
| E3.1 | **`/tools/infodesk/*` no gateway**: `internal/toolsapi` reescrito (6 handlers da seção A do `01`, `auth` por `X-Tools-Token`, `decode` 64 KiB, convenção `{status,erro,motivo}`); `internal/taxonomia` (carrega `config/taxonomia.yaml`, valida contra `infodesk.Mesas()/Departamentos()` no boot, `Resolver(categoria) → {deskID, departmentID, priorityID}`); idempotência de `abrir` pela tabela `tickets` (insert-before-POST, unique, `pendente`→`aberto`/`falha`), dedup de `preparar` (`chamado_aberto`), posse em `consultar`, `listar` local, `identificar` (hash+sal, mascarado, TTL), `transferir` pelo caminho único da engine. Testes: `httptest` + `fakeInfodesk` + SQLite; **testes de contrato** carregando `contratos/tools-infodesk/**`; caso "POST /tickets sem resposta → segundo abrir não duplica". | `backend/internal/{toolsapi,taxonomia}/**`, `backend/cmd/bot/main.go` (só o wiring do toolsapi) | E2.1, E1.6 | G | implementador-tool | nova-tool, infodesk-api |
| E3.2 | **RAG com fonte da Infominas**: `src/rag/fonte.py` ganha `FonteAPIInfominas` (**stub** configurável até P6: interface igual à `FonteDocumentacao`, erro claro "DOCS_API_* não configurado"); `FonteDocsLocais` aponta para `agente-ia/docs/base/*.md` (3 docs placeholder sobre serviços genéricos de provedor, marcados `placeholder`); `COLLECTION_NAME = "infominas_kb"`; `retrieve` já condicional (E2.2); `sync`/`ingest`/`consultar` renomeados nos logs; remover `verificar.py` se Chroma for descontinuado (decisão: **manter Chroma só para dev**). | `agente-ia/src/rag/**`, `agente-ia/docs/base/**`, `agente-ia/tests/test_{rag,fonte,sync,consultar}*.py` | E1.3 | M | implementador-python | refatorar-modulo |
| E3.3 | **Reconciliador** `internal/reconciliador`: loop a cada `RECONCILIA_INTERVALO`, `Conversas(tab=entrada|atendimento)` → para cada conversa com `ultima_msg_em` local anterior à última do Infodesk, `Conversa(id)` e injeta mensagens `CLIENT` não vistas na engine (mesmo dedup). **Bloqueado por P3/C1**: implementar sobre `Bruto map[string]any` com extração configurável dos campos (`RECONCILIA_CAMPO_*`) e teste com fixture hipotética; desligado por default (`RECONCILIA_ATIVO=false`). | `backend/internal/reconciliador/**` | E2.1 | M | implementador-go | infodesk-api |
| E3.4 | **Harness de dev**: `app.py` (Chainlit) simula o Contrato A completo (telefone, nome, contato identificado opcional, mostra `classificacao` e `acoes` de cada turno); `scripts/start-all.sh` sobe mock-infodesk + gateway + cérebro com `.env` de dev; `docs/plano/ambiente-dev.md` curto. | `agente-ia/app.py`, `agente-ia/tests/test_app.py`, `scripts/**` | E2.2, E1.4 | P | implementador-python | ambiente-testes |
| E3.5 | **Evals de conversa**: ≥ 30 cenários `evals/conversa/*.yaml` (reclamação simples; dúvida com RAG; status com número; status sem posse; pedido de atendente; áudio; cliente irritado; baixa confiança → esclarecimento → categoria; categoria pegajosa; proposta expirada; "esse" não confirma; CPF inválido; já tem chamado aberto; prompt injection direta; injeção via RAG; fora de escopo; horário) com LLM scriptado + `FakeJEV`; runner integra ao `pytest -m evals_pr`; relatório. | `agente-ia/evals/**` | E2.2, E2.3 | M | escritor-evals | evals-agente |

**Pronto da onda 3**: pela UI do mock-infodesk, um cliente abre um ticket de
ponta a ponta (identificação → proposta → "sim" → ticket aparece no mock →
conversa transferida de departamento); `python -m evals.conversa` ≥ 30
cenários verdes; `go test ./... -race` verde com Postgres no CI.

---

## Onda 4 — Integração, operação e revisão (paralelo: 4 sessões + revisão)

| ID | Etapa | Zona | Depende | Est. | Subagente | Skills |
|---|---|---|---|---|---|---|
| E4.1 | **E2E automatizado**: `backend/internal/e2e/` (build tag `e2e`) sobe mock-infodesk + gateway + cérebro falso (ou real com `OPENROUTER_API_KEY`) e roda 5 cenários (ticket, transferência por áudio, pedido de atendente, dedup de webhook repetido, HMAC inválido); `scripts/verificar.sh --completo` inclui. | `backend/internal/e2e/**`, `scripts/verificar.sh` | E3.1, E3.4 | M | implementador-go | ambiente-testes |
| E4.2 | **Compose e deploy genérico**: `docker-compose.yml` (pgvector, brain, gateway; volume `config/`), `.env.example` final (tabela de variáveis com obrigatórias), Dockerfiles revisados (sem AzapDocs; `typesafe-sdk`), `deploy/README.md` para infra da Infominas (P10: requisitos — Docker, domínio HTTPS para `/webhook`, secrets), `deploy.yml` só `workflow_dispatch`; remover `infra/terraform` e ansible específicos (manter `infra/README.md` dizendo o que falta). | `docker-compose*.yml`, `.env.example`, `agente-ia/Dockerfile`, `backend/Dockerfile`, `deploy/**`, `infra/**`, `.github/workflows/deploy.yml` | E3.1 | M | implementador-go | ambiente-testes |
| E4.3 | **Observabilidade**: logs estruturados com nomes novos (`chat_request`, `classificacao`, `tool_exec`, `ticket_aberto`, `transferencia`, `sofia_alerta`), Langfuse com tags `intencao`/`categoria`/`origem`, métrica de `jev_indisponivel`, `GET /health` do gateway informa cache de referência carregado e taxonomia válida; `docs/plano/operacao.md` (o que olhar quando algo quebra). | `agente-ia/src/observability/**`, `agente-ia/server.py` (logs), `backend/internal/alerta/**`, `backend/cmd/bot/main.go` (health), `docs/plano/operacao.md` | E3.1 | P | implementador-python | ambiente-testes |
| E4.4 | **Evals noturnos**: `evals-noturnos.yml` roda `-m jev` (classificação real) e `evals/conversa/juiz` (LLM real + juiz com rubrica); publica relatório como artefato e comenta no último PR mergeado se regressão > 2 pts. | `.github/workflows/evals-noturnos.yml`, `agente-ia/evals/juiz/**` | E2.4, E3.5 | P | escritor-evals | evals-agente |
| E4.5 | **Revisão de segurança**: rodar `/security-review` e checklist OWASP LLM Top 10 (`revisar-pr`, seção segurança) sobre `main`; verificar: nenhum argumento de escopo no schema do LLM, CPF nunca em log/prompt em claro, HMAC em tempo constante, idempotência provada por teste, mensagens de erro do Infodesk não chegam ao cliente, `output_guardrail` barra "abri o chamado" sem tool. Resultado em `docs/decisoes/0007-revisao-seguranca-v1.md` com achados e PRs de correção. | leitura; correções em PRs separados | onda 3 | M | revisor | revisar-pr |
| E4.6 | **Limpeza final**: `legado-permitido.txt` vazio (teste de arquitetura sem exceções), `docs/zapin/` só com os docs de referência, `README.md` final, `CLAUDE.md` revisado contra o estado real. | `docs/**`, `README.md`, `CLAUDE.md`, `docs/plano/legado-permitido.txt` | todas | P | refatorador | refatorar-modulo |

---

## Onda 5 — Homologação (depende das pendências; sequencial com a Infominas)

| ID | Etapa | Pendência | Critério de pronto |
|---|---|---|---|
| E5.1 | Taxonomia real: preencher `config/taxonomia.yaml` com o export (P1) e UUIDs de prioridade (P4); remover `placeholder: true`; evals de categoria deixam de ser `xfail` | P1, P4 | boot do gateway valida 100 % das mesas/departamentos; `python -m evals.classificacao --fonte sintetico --jev` ≥ metas do `02` |
| E5.2 | Dataset histórico: `anonimizar.py` sobre o export (P9), rotulagem, calibração dos `JEV_LIMIAR_*`; ADR com os valores escolhidos e as curvas | P9, P8 | `historico.jsonl` ≥ 500 casos; ECE ≤ 0.08; limiares versionados |
| E5.3 | Identificação pelo Infodesk: se P2 trouxer rota, `contato/identificar` passa a consultar e o pedido de CPF vira opcional (`IDENT_EXIGIR_CPF=false`) | P2 | teste com mock da rota; fluxo sem CPF nos evals |
| E5.4 | Reconciliador ligado (`RECONCILIA_ATIVO=true`) com os campos reais (P3) | P3 | teste com fixture real; 1 h em produção sem duplicata |
| E5.5 | Horário humano e mensagem fora do horário (P7); departamento/mesa de triagem (P11) | P7, P11 | config + eval de conversa "fora do horário" |
| E5.6 | Smoke em produção (D10): número de teste da Infominas, roteiro de 8 cenários, checklist em `docs/plano/homologacao.md`; alertas chegando no canal do responsável | P10 | checklist assinado |

---

## Mapa de dependências

```mermaid
flowchart TB
    O0[Onda 0: E0.1–E0.6] --> E11 & E12 & E13 & E14 & E15 & E16
    E11[E1.1 Go sem legado] --> E21
    E12[E1.2 cliente Infodesk] --> E21
    E16[E1.6 contratos] --> E21 & E23 & E31
    E13[E1.3 Python sem legado] --> E22 & E23 & E32
    E15[E1.5 evals scaffold] --> E22 & E24
    E14[E1.4 mock-infodesk] --> E34 & E41
    E21[E2.1 gateway Infodesk] --> E31 & E33
    E22[E2.2 classificação JEV] --> E23 & E24 & E34 & E35
    E23[E2.3 tools Python] --> E35
    E24[E2.4 evals classif.] --> E44
    E31[E3.1 toolsapi Go] --> E41 & E42 & E43 & E45
    E32[E3.2 RAG]
    E33[E3.3 reconciliador]
    E34[E3.4 harness dev] --> E41
    E35[E3.5 evals conversa] --> E44
    E41[E4.1 E2E] --> E46
    E42[E4.2 compose/deploy] --> E46
    E43[E4.3 observabilidade] --> E46
    E44[E4.4 evals noturnos] --> E46
    E45[E4.5 revisão segurança] --> E46
    E46[E4.6 limpeza] --> O5[Onda 5: homologação]
```

## Divisão de trabalho por subagente (resumo)

| Subagente | Etapas |
|---|---|
| `refatorador` | E0.1–E0.6, E1.1, E1.3, E4.6 |
| `implementador-go` | E1.2, E2.1, E3.3, E4.1, E4.2 |
| `implementador-python` | E1.4, E2.2, E3.2, E3.4, E4.3 |
| `implementador-tool` | E1.6, E2.3, E3.1 |
| `escritor-evals` | E1.5, E2.4, E3.5, E4.4 |
| `revisor` | revisão de **todo** PR + E4.5 |
