# task.md — Tasks (feitas e pendentes)

> Rastreamento de tarefas do projeto de suporte técnico Azapfy.
> Contexto de estrutura/convenções: ver `memory.md`. Roadmap original:
> `agente-ia/plano_de_execucao.md`.

---

## ✅ Feitas

### agente-ia (cérebro Python) — em homologação de cliente, ~114 testes
- [x] Grafo LangGraph completo (`entry → input_guardrail → agent ⇄ tools → output_guardrail / safe_response`) + roteamento condicional.
- [x] Teto de iterações do loop agent⇄tools (`AGENT_MAX_ITERACOES=5`).
- [x] Guardrail de **input**: heurística regex → classificador LLM (fail-open, com contexto curto da conversa).
- [x] Guardrail de **output**: wrapper `<documento_externo>` + escape `<>&` (LLM01 indireta).
- [x] **RAG** completo: ingest dos 4 `docs/*.md` → ChromaDB (já populado) + retriever + tool `consultar_base_conhecimento` (com fontes/seção).
- [x] **4 CRM mock tools**: `buscar_cliente_por_telefone`, `verificar_chamados_abertos`, `rastrear_nota_fiscal`, `abrir_novo_chamado` (confirmação obrigatória — LLM08).
- [x] `server.py` — FastAPI Contrato A (`POST /chat`, `/health`, `/extract-login`).
- [x] `app.py` — Chainlit dev (streaming, `/trocar-telefone`, identidade mock).
- [x] Otimizações de custo: poda de histórico, prompt caching model-aware (`anthropic/`).
- [x] Cenários E2E 1–7 cobertos em `tests/test_e2e_scenarios.py`.
- [x] **Persona "Zapin"** no `SYSTEM_PROMPT_AGENTE` + `RESPOSTA_OFF_TOPIC` (alinhada ao gate Go). *(Bloco A: tom migrado de mineiro → humano/cordial em PT-BR neutro.)*
- [x] **Extrator de login** (`src/identity/login_extractor.py`, `SYSTEM_PROMPT_EXTRATOR_LOGIN`) exposto em `POST /extract-login` — fallback do gate Go (fail-soft, LLM barato, saída estruturada). Testes em `tests/test_login_extractor.py`.
- [x] **Observabilidade (logs)**: `_setup_logging()` + `LOG_LEVEL` no `.env`; logs estruturados em server/nodes/rag_tool (`chat_request`, `agent_tool_calls`, `tool_exec`, `rag_query`…).

### backend (gateway Go) — fundação + 1ª tool (identidade)
- [x] Webhook Chatwoot: auth HMAC/token, dedup, worker pool.
- [x] **Gate de identidade** (FSM): telefone → cache base própria (SQLite) → pede login → Mongo `BuscarPorLogin` + projeção (só `ativo`) → confirma e-mail/nome → cacheia com TTL.
- [x] Roteamento p/ humano via labels do Chatwoot.
- [x] **Resolução de login**: determinístico (`loginCandidatos`: msg crua, minúsculas, dígitos só p/ CPF/CNPJ formatado puro) + fallback IA via `LoginExtractor`/`POST /extract-login` (fail-soft).
- [x] Mensagens do gate + saudação no tom **"Zapin"** (humano/cordial, PT-BR neutro desde o Bloco A); nome na const `nomeAssistente`.
- [x] `mongo.Repo` (conexão real) + projeção; `brain.Client` (Contrato A + `ExtrairLogin`); `engine` (orquestra, com logs Debug); `store` SQLite.
- [x] Testes: `internal/identity` (inclui CPF formatado, fallback IA, IA indisponível via `fakeExtractor`) + `internal/mongo` (com fakes, sem rede).

### Dev / testes E2E
- [x] **`mock-chatwoot/`** — front que finge ser o Chatwoot (stdlib Python, sem
      pip). Emite webhook `message_created` para o Go e mocka a REST API que o Go
      chama para responder (`.../messages`, `.../labels`). Permite simular um
      telefone e testar todo o fluxo (gate → cérebro → resposta) e as tools, sem
      mexer no Go. Basta apontar `CHATWOOT_BASE_URL` do Go para o mock. Pré-req do
      caminho de login: Mongo acessível (ver `mock-chatwoot/README.md`).

---

## 🔲 Pendentes

### P0 — Fase 2: tools de dados reais (MCP) escopadas por identidade
> **Chamados (SAC) já são reais** (abrir + listar). Falta: rastreio de NF (ainda
> mock), `session_token` (aceito e ignorado em `server.py`) e `acoes` no
> `ChatResponse` (sempre vazio).

- [x] **Chamados reais (SAC)** — Go dono das chamadas: `internal/sac` (criar/editar-prioridade/buscarrelator/config c/ cache + link) + `internal/toolsapi` (`/tools/sac/{tipos,criar,listar}`, token `X-Tools-Token`, identidade resolvida pelo telefone→perfil, relator nunca vem do LLM). Python: `src/tools/sac_tools.py` (`consultar_tipos_de_chamado`, `abrir_chamado_suporte`, `listar_chamados_abertos`; `telefone` via `InjectedToolArg`). Prioridade via conta de serviço (`SAC_SERVICE_COD`, best-effort). Email projetado no `Perfil` (=`cod_relator`).
- [x] **Python** — `buscar_cliente_por_telefone` **removido** do `get_default_tools` (identidade vem pelo Contrato A). `crm_mocks` mantém só `rastrear_nota_fiscal` no default.
- [ ] **Confirmar mapeamento SAC p/ cliente EXTERNO**: `SAC_GRUPO_EMP=AZAPERS` (desk), `incidente.empresa=AZAPFY`, `incidente.cliente`=grupo do usuário — validar com chamado real fora do AZAPERS. Provisionar `SAC_SERVICE_COD` (área SAC/super) e confirmar `SAC_BASE_URL`/token.
- [ ] **Go/Python** — rastreio de NF e pesquisa de notas reais (ainda mock) escopados pela identidade.
- [ ] **Go** — emitir `session_token` no gate ao identificar e propagá-lo no Contrato A (`brain.ChatRequest`). Hoje o gate não gera token.
- [ ] **Python** — cabear `acoes` no `ChatResponse` (ex.: "abriu chamado", "rotear humano") para o Go agir no Chatwoot.

### P1 — Robustez / segurança / qualidade
- [ ] **Output guardrail real**: `output_guardrail_node` é no-op → PII-masking + cap de tamanho (LLM06).
- [ ] **Testes Go faltantes**: `engine`, `webhook`, `brain`, `store`, `chatwoot`, `config`.
- [ ] **Rate limiting / abuse** no edge (webhook Go) — mensagens/min por contato.

### P2 — Integração E2E e deploy
- [ ] **Smoke E2E real**: fluxo completo WhatsApp→Chatwoot→Go→Python com Mongo + Chatwoot reais. Critério: 6/7 cenários sem regressão (cenário 6, indirect injection, é o-chave).
- [ ] **Docker/compose**: subir Go + Python (+ Mongo de dev) juntos.
- [ ] **Tracing distribuído** (opcional): LangSmith/Langfuse no grafo (logging estruturado via `LOG_LEVEL` já existe; falta o tracing).

---

## Lembretes operacionais
- Mudar `rag_chunk_size`/`rag_chunk_overlap` → **re-ingestão** (`python -m src.rag.ingest`). Mudar `rag_top_k` não exige.
- Trocar modelo/parâmetro: alterar `config.py` + `.env.example` + `.env`.
- Validar: `pytest tests/ -v` (Python) · `go build ./... && go vet ./... && go test ./...` (Go).
