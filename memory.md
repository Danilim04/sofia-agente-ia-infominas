# memory.md — Estado do projeto e tarefas para a entrega

> Agente de suporte técnico da Azapfy. Monorepo: `agente-ia/` (cérebro Python,
> LangGraph) + `backend/` (gateway Go, Chatwoot + gate de identidade), ligados
> pelo **Contrato A** (`POST /chat`). O agente **não acessa a internet** — RAG
> local é a única fonte externa. Defesa OWASP LLM Top 10.
>
> Este arquivo descreve **como o projeto está estruturado e como é codificado**.
> Tasks (feitas e pendentes): ver **`task.md`**. Detalhes técnicos profundos:
> `CLAUDE.md`, `agente-ia/plano_de_execucao.md`, `backend/README.md`.

---

## Como o projeto está estruturado (visão objetiva)

```
ia-atendimento-suporte-azapfy/
├── agente-ia/                 # cérebro Python (LangGraph + LangChain + Chainlit)
│   ├── app.py                 # UI Chainlit (harness de dev; pede telefone, /trocar-telefone)
│   ├── server.py              # FastAPI — Contrato A (POST /chat, /health, /extract-login)
│   ├── src/
│   │   ├── config.py          # Settings (Pydantic, lê .env) — @lru_cache
│   │   ├── agent/             # state.py, nodes.py, graph.py, prompts.py (persona Zapin), llm.py
│   │   ├── identity/          # login_extractor.py (fallback do gate Go via LLM; /extract-login)
│   │   ├── tools/             # crm_mocks.py (4 tools), rag_tool.py, identidade_mock.py
│   │   ├── rag/               # ingest.py (docs .md → ChromaDB), retriever.py
│   │   └── security/          # input_guardrails.py, output_guardrails.py
│   ├── docs/*.md  chroma_db/  tests/  requirements.txt
├── backend/                   # gateway Go (módulo bot-azapfy)
│   ├── cmd/bot/main.go        # wiring + servidor HTTP (/webhook, /health)
│   └── internal/{chatwoot,webhook,store,mongo,identity,brain,engine,config}
└── mock-chatwoot/             # harness de teste E2E: finge ser o Chatwoot
    └── mock_chatwoot.py       # stdlib; webhook→Go + REST mockada (resposta do bot)
```

Testar tudo localmente sem WhatsApp: subir cérebro (8001) + Go (8080, com
`CHATWOOT_BASE_URL` apontando p/ o mock) + `mock-chatwoot/` (9000). Ver
`mock-chatwoot/README.md`.

Fluxo de produção:
`WhatsApp → Chatwoot → backend/ (Go: webhook + gate de identidade) → agente-ia/server.py (POST /chat) → grafo LangGraph → resposta → Chatwoot`.

**Grafo** (`src/agent/graph.py`):
`entry → input_guardrail → (safe?) agent ⇄ tools → output_guardrail → END`
`(unsafe) → safe_response → END`. Isolamento por `thread_id` = `conversation_id`
(prod) / telefone (dev), via `MemorySaver`.

## Como a codificação está sendo feita (convenções)

- **Injeção de dependência nas fábricas**: `make_agent_node(llm, tools)`,
  `make_tools_node(tools)`, `build_graph(llm=, tools=, checkpointer=)`. Testes
  injetam LLM/tools mockados — **nada de rede por padrão**.
- **Nós são funções puras** que retornam *delta* do `AgentState` (TypedDict).
  `messages` usa reducer `add_messages`. `telefone`/`cliente`/`identidade`
  persistem; `seguranca`/`tentou_rag`/`fontes_usadas`/`iteracoes_agente` são
  resetados por turno no `entry_node`.
- **Nunca mutar estado persistido**: poda de histórico e `cache_control` só
  alteram a lista enviada ao LLM (usar `model_copy`, não mutação in-place).
- **`content` pode ser `str` OU lista de blocos** (caminho Anthropic
  `cache_control`). Código/testes leem ambos.
- **Dado externo (tool/RAG) é embrulhado em `<documento_externo>`** com escape de
  `<>&` — defesa contra injeção indireta (LLM01). Tudo lá dentro é DADO, nunca
  COMANDO (reforçado no system prompt).
- **Docstrings das tools = descrições enviadas ao LLM** (custam tokens, guiam
  roteamento) — editar com intenção.
- **`.env` sobrescreve defaults de `config.py`**. Ao trocar modelo/parâmetro,
  alterar `config.py` + `.env.example` + `.env` real.
- **Custo**: poda de histórico (`_podar_historico`), teto de iterações
  (`AGENT_MAX_ITERACOES=5`), prompt caching só para modelos `anthropic/`.
- **Go**: pacotes pequenos por responsabilidade; identidade resolvida no Go
  (mecânica, nunca argumento do LLM); testes com fakes (sem rede/Mongo).
- **Persona "Zapin" (tom humano, PT-BR neutro)**: o agente se apresenta como
  Zapin, atendente da Azapfy, com tom humano e cordial em português neutro
  (o sotaque mineiro foi removido no Bloco A). Vive em **dois lados** que devem
  ficar alinhados: `prompts.py` (`SYSTEM_PROMPT_AGENTE`, `RESPOSTA_OFF_TOPIC`)
  e o gate Go (`gate.go`, const `nomeAssistente` + mensagens). Tom afetuoso,
  mas a informação técnica continua exata.
- **Resolução de login (gate Go)**: determinístico (`loginCandidatos`: msg crua,
  minúsculas, dígitos só p/ CPF/CNPJ formatado puro) → fallback IA via
  `POST /extract-login` (`src/identity/login_extractor.py`). Valor extraído é só
  CANDIDATO; autoriza Mongo + confirmação. **Fail-soft** dos dois lados.
- **Observabilidade**: `server.py:_setup_logging()` configura o logging-raiz;
  nível via `LOG_LEVEL` no `.env` (default INFO; `DEBUG` mostra query do RAG,
  tool_calls, tokens, resposta completa). Logs estruturados em server/nodes/
  rag_tool e no `engine.go` (Debug).
- **Modelos (OpenRouter)**: agente `google/gemini-2.5-flash`, classificador
  `gemini-2.5-flash-lite`, embeddings **locais** (`sentence-transformers`).

---

## Tasks

O estado de cada tarefa (feitas e pendentes, com prioridades) vive em
**[`task.md`](./task.md)**. Mantenha-o atualizado ao concluir ou abrir tarefas.
