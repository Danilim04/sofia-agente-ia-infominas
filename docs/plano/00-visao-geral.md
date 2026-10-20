# 00 — Visão geral: Sofia, agente de atendimento da Infominas

> Plano escrito em 2026-10-19. Fonte das regras de negócio: respostas do
> Daniel (arquiteto) na sessão de planejamento + `docs/infodesk/`. O que não
> está nessas duas fontes está em **Pendências** (fim deste arquivo) e NÃO
> deve ser assumido por quem implementa.

## O produto em uma frase

**Sofia** é uma atendente virtual no WhatsApp da Infominas que recebe a
mensagem do cliente final, conversa de forma humana, **classifica** o
problema (JEV, determinístico, com confiança), **roteia** a conversa para o
departamento certo, **abre o ticket** na mesa certa do Infodesk, responde
dúvidas com uma base de conhecimento e **transfere para um atendente humano**
quando o pedido, a mídia, a confiança ou o tom exigem.

## Decisões já tomadas (respostas da sessão)

| # | Decisão | Fonte |
|---|---|---|
| D1 | Manter a arquitetura **gateway Go + cérebro Python** do Zapin; o Infodesk substitui Chatwoot/Evolution; Mongo e SAC saem | resposta 18 |
| D2 | **JEV** (TypeSafe, serviço remoto, rótulo + confiança) classifica categoria, fila e prioridade; o LLM só conversa e cobre os casos de baixa confiança | respostas 1–4 |
| D3 | Categorias virão de **export do Infodesk** (pendente); mapeamento categoria → mesa → departamento → prioridade é **tabela de configuração** validada no boot | respostas 5–7 |
| D4 | Chamado já aberto: **perguntar o número e não duplicar** | resposta 8 |
| D5 | Identificação: o Infodesk tem cadastro de clientes vinculado ao telefone, mas **a API documentada não expõe busca de cliente** (verificado: nenhuma rota em `docs/infodesk/`). Enquanto não houver rota, o bot **pede CPF ou nº de contrato antes de abrir** o ticket; cliente não identificado não abre ticket | respostas 9–10 |
| D6 | Escopo: triagem + ticket + status por número **+ FAQ com RAG**; os documentos vêm de uma **API/portal da Infominas** (detalhes pendentes) | respostas 11, 20 |
| D7 | Persona **Sofia**, pt-BR, informal ("você"), no máximo 1 emoji, saudação por período do dia | resposta 12 |
| D8 | Transferir para humano em: pedido explícito, mídia/áudio, baixa confiança (JEV + LLM), cliente irritado, fora do horário humano (horários pendentes) | resposta 13 |
| D9 | Canal: **só WhatsApp, um número** | resposta 14 |
| D10 | Sem sandbox do Infodesk: testes contra **mock local**; smoke em produção com número de teste | resposta 15 |
| D11 | Produção na **infra da Infominas**; LLM via **OpenRouter** aceito | resposta 16 |
| D12 | Há **histórico de conversas/tickets sem anonimização**: o dataset de evals exige etapa de anonimização antes de versionar | resposta 17 |
| D13 | **GitHub Actions + revisão humana + `main` protegida** | resposta 19 |

## Arquitetura alvo

```mermaid
flowchart LR
    subgraph Infominas
        WA[Cliente no WhatsApp]
        ID[Infodesk<br/>conversas · tickets · mesas · departamentos]
    end
    subgraph "Stack Sofia (infra Infominas)"
        GW[gateway Go<br/>backend/]
        BR[cérebro Python<br/>agente-ia/]
        PG[(Postgres<br/>estado do gateway + pgvector RAG)]
    end
    JEV[JEV · TypeSafe<br/>api.typesafe.ai]
    LLM[OpenRouter<br/>LLM de conversa]
    DOCS[API/portal de docs<br/>da Infominas]

    WA -->|mensagem| ID
    ID -->|webhook message.received · conversation.assigned · ticket.*<br/>HMAC sha256 de timestamp.corpo| GW
    GW -->|POST /chat · Contrato A| BR
    BR -->|POST /v1/systemone| JEV
    BR -->|chat completions| LLM
    BR -->|POST /tools/infodesk/*| GW
    GW -->|GET /contacts/lookup · POST /conversations/:id/messages<br/>POST /conversations/:id/ticket · POST /conversations/:id/handoff<br/>GET /events?after=| ID
    GW --- PG
    BR --- PG
    DOCS -->|sync diário| PG
```

**Responsabilidades (sem mudar o princípio do Zapin: o LLM nunca é base confiável):**

| Componente | Dono de | Nunca faz |
|---|---|---|
| **gateway Go** (`backend/`) | receber webhook (HMAC, dedup, idade, `organizationId`), fila FIFO + coalescência por conversa, estado da conversa (bot/transferida/encerrada), cache de contato identificado, **cliente HTTP do Infodesk**, **idempotência e registro de tickets**, transferência para humano via handoff (inclusive por mídia/falha/pedido explícito/humano assumiu), lookup do telefone, bolhas WhatsApp, alertas, reconciliação pelo feed `GET /events` | decidir conteúdo de resposta; classificar |
| **cérebro Python** (`agente-ia/`) | grafo da conversa, **classificação (JEV → fallback LLM)**, guardrails de entrada/saída, RAG, tools que o modelo pode chamar (com política de injeção/validação), persona Sofia | falar com o Infodesk diretamente (sempre via `/tools/infodesk/*` do gateway); escolher mesa/prioridade por texto livre |
| **JEV** | rótulo + `probabilities` + `confidence` para intenção, categoria, prioridade, frustração, segurança | gerar texto |
| **LLM** | conversa humanizada, resumo/descrição do ticket (proposta), perguntas de esclarecimento, classificação **só** quando o JEV está abaixo do limiar | autorização, escolha de fila, abertura sem confirmação |

## Fluxo ponta a ponta de uma reclamação

```mermaid
sequenceDiagram
    autonumber
    participant C as Cliente (WhatsApp)
    participant I as Infodesk
    participant G as gateway Go
    participant B as cérebro Python
    participant J as JEV
    participant L as LLM

    C->>I: "minha internet caiu de novo"
    I->>G: webhook message.received (HMAC de "timestamp.corpo", handledBy=EXTERNAL_BOT)
    G-->>I: 200 em <100 ms
    G->>G: assinatura + janela 5 min, dedup (id do evento + message.id), organizationId, seq, fila por conversa, debounce 8 s
    G->>G: handledBy != EXTERNAL_BOT? conversa transferida/encerrada? mídia? pediu atendente? → handoff e para
    G->>I: GET /contacts/lookup?phone= (1º turno do episódio)
    I-->>G: requesters[] (telefone conhecido → identificado sem CPF)
    G->>B: POST /chat {conversation_id, mensagem, telefone, nome, contato{forma: telefone|cpf|contrato}?}
    B->>B: input_guardrail (regex) 
    B->>J: systemone(state=últimas msgs, questions={intencao, categoria, prioridade, frustracao, seguranca})
    J-->>B: {intencao: reclamacao 0.93, categoria: sem_conexao 0.88, prioridade: alta 0.71, frustracao: 0.4, ...}
    B->>B: roteador: confiança ≥ limiar → classificação fixa no estado; senão fallback LLM; senão pergunta de esclarecimento
    B->>B: retrieve (RAG) só se intenção = dúvida
    B->>L: agent (persona Sofia + classificação + contexto) 
    L-->>B: pergunta de identificação (CPF/contrato) só se o lookup não identificou (ou IDENT_EXIGIR_CPF=true)
    B-->>G: reply
    G->>I: POST /conversations/:id/messages (bolhas)
    I->>C: "Boa tarde! Sou a Sofia... pode me passar o CPF ou nº do contrato?"
    C->>I: "123.456.789-09"
    I->>G: webhook
    G->>B: POST /chat
    B->>L: agent → tool informar_identificacao(cpf)
    B->>B: política valida dígito verificador; tool → G /tools/infodesk/contato/identificar (cache 24 h)
    B->>L: agent → tool preparar_abertura_chamado(resumo, descricao)
    B->>G: /tools/infodesk/ticket/preparar (categoria/prioridade INJETADAS do estado; mesa/depto pela tabela; dedup local)
    G-->>B: proposta {titulo, descricao, mesa, departamento, prioridade}
    L-->>B: "Vou registrar: 'Sem conexão desde hoje cedo' na equipe de Suporte. Confirma?"
    C->>I: "sim"
    B->>B: política exige confirmação explícita no turno seguinte + proposta válida (15 min)
    B->>G: /tools/infodesk/ticket/abrir (proposta injetada)
    G->>G: grava ticket 'pendente' (idempotência) 
    G->>I: POST /conversations/:id/ticket {deskId, title, description, priorityId, catalogItemId?, period: session, finish: false}
    I-->>G: {number: 4821, status: {name: "Aberto", isOpen: true}, conversationId, ...}
    Note over G,I: sem resposta? GET /tickets?phone=&status=open casa conversationId antes de qualquer nova tentativa (ADR 0005)
    G->>I: POST /conversations/:id/handoff {departmentId, note gerada por código} (se TRANSFERIR_APOS_TICKET)
    G-->>B: {status: true, numero: "4821"}
    L-->>B: "Pronto! Seu chamado é o nº 4821..."
    G->>I: POST /messages {text}
    Note over G,I: handoff tira o bot; atendente assume (conversation.assigned); conversation.finished → gateway limpa estado
```

**Caminhos alternativos**

- **Dúvida (FAQ)**: intenção `duvida_informacao` → `retrieve` injeta trechos
  da base → resposta com o que a base tem; sem cobertura → oferece abrir
  chamado ou falar com atendente. Nenhuma citação fabricada (guardrail de
  saída do Zapin mantido).
- **Status de chamado**: intenção `status_chamado` → o modelo pede o número →
  tool `consultar_status_chamado(numero)` → `GET /tickets/:number`; o gateway
  só devolve se o ticket pertence ao telefone da sessão (posse por código:
  tabela local → `conversationId` → `contactPhone`/`requester.phone` →
  `requester_id` do lookup; `01` A4). `status` é objeto por mesa: a Sofia
  repete `status.name`, nunca inventa.
- **Transferência para humano** (sempre pelo gateway, caminho único como
  `transferencia.go`): pedido explícito (detecção determinística no Go, como
  `PedeAtendente`, + intenção `pedir_atendente` do JEV), mídia/áudio
  (`message.type != TEXT` ou `attachment`), frustração alta, baixa confiança
  após esclarecimento, falha do cérebro/JEV/disjuntor, fora do horário humano
  (pendente), conversa sem resposta nossa por 5 min (vigia). Ação: mensagem
  de aviso + **`POST /conversations/:id/handoff {departmentId, note}`**
  (ADR 0007; a `note` é montada por código) + marca local `transferida` → o
  gateway ignora webhooks dessa conversa até `conversation.finished`.
  **Humano que assume por conta própria** também é detectado:
  `conversation.assigned` / `handledBy != EXTERNAL_BOT` cala o bot.
- **Reconciliação**: no boot e a cada minuto, `GET /events?after=<seq>`
  entrega ao mesmo handler do webhook os eventos que não chegaram (entrega
  é *pelo menos uma vez*, com reentregas por até 6 h; dedup pelo `id` do
  evento). Nasce ligada (E3.3).

## O que muda em relação ao Zapin

| Área | Zapin (Azapfy) | Sofia (Infominas) |
|---|---|---|
| Transporte | Chatwoot + Evolution (webhooks `message_created`, labels, equipes, assignee) | **Infodesk** (`message.received` + 15 outros eventos, HMAC `X-Infominas-Signature` sobre `timestamp.corpo`, reentrega *pelo menos uma vez*, feed `GET /events`; `handledBy`/`botActive` dizem se é a vez do bot; o estado `bot/transferida/encerrada` continua no nosso Postgres) |
| Identidade | Gate FSM: login → Mongo → confirmação de e-mail | **Sem login.** `GET /contacts/lookup?phone=` no 1º turno (telefone conhecido → solicitante/cliente, sem CPF quando `IDENT_EXIGIR_CPF=false`); fallback: CPF/contrato pedido na conversa (validado por código) + cache 24 h (ADR 0004 revisado) |
| Chamados | SAC da Azapfy via `/tools/sac/*` | **Infodesk tickets** via `/tools/infodesk/*`, abertos **a partir da conversa** (`POST /conversations/:id/ticket`); idempotência **local** (a API não tem chave de idempotência) com reconciliação por `GET /tickets?phone=`; "meus chamados" = listagem do Infodesk por telefone ∪ local |
| Classificação | Classificador LLM só para segurança (suporte/off_topic/malicioso) | **JEV** para intenção, categoria, prioridade, frustração e segurança; LLM só abaixo do limiar |
| Roteamento | Nenhum (uma equipe) | Tabela categoria → mesa/catálogo/prioridade (ticket) + departamento (handoff); UUIDs resolvidos no boot por mesa; transferência para humano = `handoff` (ADR 0007) |
| RAG | AzapDocs (Contrato B) → pgvector | Mesmo pipeline; **fonte nova** (API/portal da Infominas, pendente) |
| Persona | Zapin, "Boníssimo dia" 🧡 | **Sofia**, pt-BR informal, 1 emoji, saudação por período |
| Grafo | `entry → input_guardrail → retrieve → agent ⇄ tools → output_guardrail` | `entry → input_guardrail → classificar → retrieve? → agent ⇄ tools → output_guardrail` (nó `classificar` novo; `retrieve` condicional à intenção) |
| Segurança | `tool_policy` injeta telefone/empresa | `tool_policy` injeta telefone, **classificação**, **proposta**, **identificação** — o modelo nunca escolhe mesa, prioridade ou CPF |
| Deploy | VM Azapfy ARM64 ao lado do omni-route | Infra da Infominas (compose genérico; runbook novo) |
| Testes/CI | só `go vet`/`go test` em CI | lint + arquitetura + testes + evals nos dois lados (ver `04-esteira-qualidade.md`) |
| Removido | `internal/chatwoot`, `internal/mongo`, `internal/identity` (gate de login), `internal/sac`, `mock-chatwoot/`, `src/tools/sac_tools.py`, `crm_mocks.py`, `identidade_mock.py`, `login_extractor.py`, `FonteAPIDocumentacao` do AzapDocs, workflow `configure-chatwoot.yml`, `infra/terraform` e ansible específicos da VM Azapfy | — |
| Mantido (refatorado) | `engine` (coalescer, disjuntor, transferência, bolhas), `store` (dois backends), `brain`, `alerta`, `webhook` (receptor), `toolsapi` (padrão), grafo/nós/política/guardrails/RAG/tracing/falhas do Python | — |

## Estrutura de pastas alvo

```
sofia-agente-ia-infominas/
├── CLAUDE.md                     # enxuto; aponta para skills e docs
├── .claude/                      # harness (skills, agents, hooks, settings)
├── docs/
│   ├── infodesk/                 # API do Infodesk (fonte: PDF)
│   ├── plano/                    # este plano (00–06)
│   ├── decisoes/                 # ADRs curtos
│   └── zapin/                    # legado de referência (CLAUDE antigo, laudos)
├── backend/                      # gateway Go (módulo `bot-infominas`)
│   ├── cmd/bot/main.go
│   └── internal/{config,infodesk,webhook,engine,store,contato,toolsapi,brain,alerta,reconciliador}
├── agente-ia/                    # cérebro Python
│   ├── server.py  app.py  pyproject.toml
│   ├── config/taxonomia.yaml     # categorias → mesa/departamento/prioridade (preenchida pelo export)
│   ├── src/{agent,classificacao,tools,rag,security,identity,observability}
│   ├── tests/                    # unit + contrato + e2e com fakes
│   └── evals/                    # datasets anonimizados + runners (classificação e conversa)
├── mock-infodesk/                # harness E2E: finge ser o Infodesk (stdlib Python)
├── contratos/                    # JSON de exemplo do Contrato A e de /tools/infodesk/* (testados dos dois lados)
├── docker-compose.yml  .env.example  deploy/
└── .github/workflows/{ci.yml,evals-noturnos.yml,deploy.yml}
```

## Pendências / decisões em aberto (resumo; detalhe em cada doc)

| ID | Pendência | Bloqueia | Dono |
|---|---|---|---|
| P1 | Export das categorias do Infodesk + lista de mesas/departamentos + mapeamento | `config/taxonomia.yaml` real (etapas de classificação rodam com taxonomia **placeholder** marcada) | Daniel / Infominas |
| P2 | ~~Rota de busca de cliente por telefone~~ **Respondida (2026-10-19):** `GET /contacts/lookup?phone=` (`docs/infodesk/04`). Resta a decisão de negócio `07` N3 (valor de `IDENT_EXIGIR_CPF`; cadastrar via `POST /requesters`?) — E5.3 | — | Infominas (N3) |
| P3 | ~~Schemas de resposta~~ **Respondida (2026-10-19):** objetos `ticket`, `conversation`, `message`, envelope de evento em `docs/infodesk/03–05`. Restam T11/T12/T13, W7, C9 (campos secundários) no `07` | — | Infominas (itens do 07) |
| P4 | `priorityId`: **resolver no boot** por mesa via `GET /desks/:id.priorities` (`02`); pendente só confirmar os nomes das prioridades em cada mesa de destino | nada bloqueado (nome ausente → `priorityId` omitido + alerta) | Infominas (com P1) |
| P5 | ~~Como detectar que um humano assumiu~~ **Respondida (2026-10-19):** `conversation.handledBy`/`botActive` em todo evento + `conversation.assigned` (`docs/infodesk/08`); gateway cala quando `handledBy != EXTERNAL_BOT` (E2.1) | — | — |
| P6 | API/portal de documentos da Infominas para o RAG (URL, auth, formato) | fonte do RAG (pipeline fica pronto com fonte local) | Daniel |
| P7 | Horário de atendimento humano e mensagem fora do horário | regra de transferência "fora do horário" | Infominas |
| P8 | Acesso ao JEV (chave `TYPESAFE_API_KEY`, limites de rate, plano) e desempenho em pt-BR | thresholds reais; evals de classificação com JEV real | Daniel |
| P9 | Export do histórico de conversas/tickets + aprovação do processo de anonimização | dataset real de evals | Daniel / Infominas |
| P10 | Infra da Infominas: host, Docker disponível?, domínio público HTTPS para o webhook, quem opera | deploy | Infominas |
| P11 | Departamento humano padrão (destino de toda transferência) e se existe fila "triagem" para baixa confiança | config de transferência | Infominas |
