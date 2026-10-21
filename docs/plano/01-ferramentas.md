# 01 — Catálogo de ferramentas (tools)

Convenções herdadas do Zapin e mantidas:

- **Tool do agente** = função Python `@tool` em `agente-ia/src/tools/infodesk_tools.py`
  cuja docstring é a descrição enviada ao LLM. Ela **nunca fala com o
  Infodesk**: chama o gateway Go em `POST /tools/infodesk/<área>/<ação>` com o
  header `X-Tools-Token`. O gateway resolve identidade/escopo, aplica
  idempotência e chama a API do Infodesk.
- **Argumentos de sessão** (`telefone`, `conversation_id`, `classificacao`,
  `proposta`, `identificacao`) são `InjectedToolArg` + entrada em `POLITICAS`
  (`src/agent/tool_policy.py`): fora do schema do modelo, preenchidos do estado.
- **Convenção de resposta do gateway**: erro de negócio é HTTP **200** com
  `{"status": false, "erro": "<texto pt-BR para o LLM explicar>", "motivo":
  "<codigo>"}`; sucesso é `{"status": true, ...}`. HTTP 401/400/405 só para
  erro de protocolo (token, JSON, método). O LLM nunca vê mensagem crua do
  Infodesk.
- **Efeito colateral** (criar ticket, transferir) é sempre **duas fases**
  (preparar → confirmação explícita no turno seguinte → executar com a
  proposta injetada) ou **decisão de código** (transferência por mídia/falha).
- Exemplos JSON de cada endpoint vivem em `contratos/tools-infodesk/*.json` e
  são carregados nos testes dos **dois** lados (contrato Go ↔ Python).

Legenda de idempotência: **I-natural** (GET), **I-local** (gateway garante
por chave própria), **I-nenhuma** (chamada só de leitura de cache).

> **Revisão 2026-10-20 (API Infodesk v1 de 2026-10-19).** Este catálogo foi
> realinhado com `docs/infodesk/` (leia o README, seção "O que mudou", e o
> `07`). Em resumo: ticket nasce **da conversa** (`POST
> /conversations/:id/ticket`), transferência para humano é **só `handoff`**
> (ADR 0007), identificação começa por **`GET /contacts/lookup?phone=`**
> (ADR 0004, revisão), um `POST` de ticket sem resposta é **reconciliado por
> listagem** (ADR 0005, revisão), o reconciliador usa **`GET /events?after=`**
> e o `status` do ticket é um **objeto por mesa**. Os **códigos de `motivo`**
> e as chaves dos JSON em `contratos/` **não mudaram** nesta revisão; o que
> muda é o comportamento do gateway por trás deles. Campo novo em contrato =
> PR próprio (ADR 0006). Decisões de negócio continuam pendentes e estão
> referenciadas pelo item do `07` (T6, T10, T11, T12, C6, D1, D6, N1, N3).

---

## A. Tools expostas ao LLM (Python → gateway → Infodesk)

### A1. `informar_identificacao`

| Item | Valor |
|---|---|
| Propósito | Registrar o dado cadastral que o cliente informou (CPF **ou** nº de contrato) para identificar quem abre o ticket. É o **fallback** da identificação: a primeira tentativa é o `GET /contacts/lookup?phone=` que o **gateway** faz sozinho no início do episódio (ADR 0004, revisão 2026-10-20). Se o lookup resolveu e `IDENT_EXIGIR_CPF=false`, o cérebro já recebe `contato` preenchido no Contrato A e o LLM **não** pede CPF |
| Args do LLM | `cpf: str = ""`, `contrato: str = ""` (pelo menos um) |
| Injetados | `telefone`, `conversation_id` |
| Validação (política, Python) | CPF: 11 dígitos + dígitos verificadores válidos (`src/identity/cpf.py`); contrato: regex configurável `IDENT_CONTRATO_RE` (default `^\d{4,12}$`). Inválido → recusa `{"erro": "CPF inválido; peça para o cliente conferir"}` sem chamar o gateway |
| Gateway | `POST /tools/infodesk/contato/identificar` `{telefone, conversation_id, cpf?, contrato?}` → grava em `contatos` (telefone → dado, `expires_at = now + IDENT_TTL` 24 h, `origem = cpf\|contrato`). Se o lookup do telefone já tinha resolvido `requester_id`/`cliente_id`, eles são mantidos e devolvidos; o CPF **não** é confrontado com o cadastro (a API não expõe CPF do solicitante — `GET /clients?document=` busca **empresa** por CNPJ/CPF, não pessoa; hipótese: não usar na v1) |
| Infodesk | nenhum nesta tool. O `lookup` acontece **antes**, no gateway (seção B, `Lookup`). `POST /requesters` (cadastrar quem o lookup não achou) fica **fora da v1** (`07`, N3) |
| Retorno | `{status: true, identificado: true, forma: "cpf"\|"contrato", cliente_id: null\|str, nome_cadastro: null\|str}` (`cliente_id`/`nome_cadastro` vêm do lookup quando houve; senão `null`) |
| Erros | `campo_invalido`, `nao_identificado` (nenhum campo) |
| Idempotência | I-local (upsert por telefone) |
| Log | CPF mascarado (`***.***.789-**`) em INFO; nunca em claro |
| Dependências | tabela `contatos` (store); `IDENT_TTL`; `IDENT_EXIGIR_CPF` (default **`false`**: telefone conhecido dispensa CPF — hipótese de plano, `07` N3; `true` restaura o comportamento "sempre pede") |

### A2. `preparar_abertura_chamado`

| Item | Valor |
|---|---|
| Propósito | Dry-run: valida e devolve a proposta canônica do ticket. Não cria nada |
| Args do LLM | `titulo: str` (1 linha, ≤120), `descricao: str` (≤4000, relato do cliente em 3ª pessoa + fatos coletados: desde quando, já reiniciou, endereço se citado) |
| Injetados | `telefone`, `conversation_id`, `classificacao` (do estado: `{categoria, prioridade, confianca, origem: jev\|llm\|esclarecimento}`), `identificacao` (`{cpf\|contrato}`) |
| Precondição (`exigir`) | identificação presente no estado — por lookup (`contato.forma == "telefone"`) **ou** por CPF/contrato (senão recusa `"peça o CPF ou o número do contrato antes"`); classificação com `categoria != None` (senão recusa `"faça uma pergunta de esclarecimento antes"`); não há protocolo citado na última mensagem (regra ZB do Zapin) |
| Gateway | `POST /tools/infodesk/ticket/preparar` `{telefone, conversation_id, titulo, descricao, categoria, prioridade, identificacao}` → sanitiza (controle, tamanho), resolve `categoria → {deskId, catalogItemId?, departmentId, priorityId}` pela tabela (`taxonomia.yaml` carregada no boot e validada contra `/desks`, `/desks/:id` e `/departments`; o `priorityId` é o UUID do **nome** de prioridade **na mesa de destino**, resolvido no boot — `02`), **dedup**: (1) local — `tickets` tem ticket `aberto`/`pendente` da mesma conversa OU do mesmo telefone + mesma categoria nas últimas `TICKET_DEDUP_JANELA` (24 h); (2) no Infodesk — `GET /tickets?phone=<E.164>&status=open` (I-natural; ou `lookup.openTickets` já em cache do episódio) quando `DEDUP_CONSULTA_INFODESK=true` (default **true**; hipótese, `07` T5). Qualquer um → `motivo: "chamado_aberto"` com `chamados_abertos: [{numero, titulo, categoria, aberto_em}]` (os do Infodesk vêm com `categoria: null`) |
| Infodesk | cache de `/desks`, `/desks/:id`, `/departments`; `GET /tickets?phone=&status=open` (dedup, falha → só dedup local + log, nunca bloqueia) |
| Retorno | `{status: true, proposta: {titulo, descricao, categoria, mesa: {id, nome}, departamento: {id, nome}, prioridade: {nome, id\|null}, identificacao_mascarada, rodape}}` — `rodape` é o texto fixo anexado à descrição (`[Aberto pela Sofia · conversa <id> · CPF ***.***.789-**]`; com identificação por lookup, `· solicitante <nome_cadastro>`). Como o ticket aberto pela conversa já carrega `conversationId` e a transcrição, o rodapé é **configurável** (`TICKET_RODAPE=true` default; `07` T8) |
| Erros | `campo_invalido`, `categoria_sem_mapa` (categoria não está na tabela → alerta ao responsável + sugere transferir), `chamado_aberto`, `nao_identificado`, `referencia_indisponivel` (cache de mesas vazio) |
| Idempotência | I-nenhuma (puro) |
| Efeito no estado | `tools_node` carimba `proposta_chamado = {proposta, criada_em, turno}` (validade 15 min, como no Zapin) |

### A3. `abrir_chamado`

| Item | Valor |
|---|---|
| Propósito | Cria o ticket **exatamente** como preparado e confirmado |
| Args do LLM | **nenhum** |
| Injetados | `proposta` (do estado), `telefone`, `conversation_id` |
| Precondição | `_exigir_proposta_confirmada` do Zapin: proposta existe, dentro de 15 min, turno = turno da proposta + 1, última mensagem humana é confirmação explícita (`CONFIRMACOES_EXPLICITAS`, ajustada à persona: inclui "sim sofia", "pode abrir", "confirmo") |
| Gateway | `POST /tools/infodesk/ticket/abrir` `{telefone, conversation_id, proposta, chave_idempotencia}` onde `chave_idempotencia = sha256(conversation_id + proposta.titulo + proposta.categoria + proposta.criada_em)` calculada no **Python** a partir da proposta carimbada. Gateway: (1) `INSERT tickets(chave, conversation_id, telefone, categoria, status='pendente')` — conflito na chave → devolve o ticket já existente (`numero` se houver, ou `motivo: "em_andamento"` se ainda pendente); (1b) se `conversas.ticket_number` local já existe para esta conversa → **não abre outro**: `POST /conversations/:id/link-ticket` não se aplica (já vinculado) e o gateway devolve `chamado_aberto`-equivalente via `preparar` (defesa; `07` T12); (2) se a identificação veio do lookup (`requester_id`), `POST /conversations/:id/requester {id}` best-effort (falha = log; `07` T11); (3) **`POST /conversations/:id/ticket`** com `{deskId, title, description (+ rodape se `TICKET_RODAPE`), priorityId, catalogItemId?, period: "session", finish: TICKET_FINISH_CONVERSA}` — `finish` default **`false`** (decisão de negócio pendente, `07` C6); `requesterId`/`clientId`/`contactPhone` **não** vão no corpo: presume-se herdados da conversa (`07` T11; se a Infominas disser que não herda, enviar como em `POST /tickets`); (4) `UPDATE tickets SET numero (int), status='aberto', raw_resposta`; (5) se `TRANSFERIR_APOS_TICKET=true` (default **true**, revisar com P11/C6): **handoff** `POST /conversations/:id/handoff {departmentId, note}` (best-effort; falha vira alerta, não desfaz o ticket) e marca conversa `transferida` (ADR 0007) |
| Infodesk | `POST /integration/v1/conversations/:id/requester` (opcional); `POST /integration/v1/conversations/:id/ticket`; `POST /integration/v1/conversations/:id/handoff` (opcional). `POST /tickets` direto **só** como fallback configurável (`TICKET_VIA_CONVERSA=false`) caso a rota pela conversa se mostre inviável em homologação |
| Retorno | `{status: true, numero: "<number como texto>", mesa, departamento, prioridade, transferido: bool}` — o Infodesk devolve `number` **inteiro**; o gateway converte para texto no contrato (o LLM só repete) |
| Erros | `sem_proposta`, `em_andamento` (retry concorrente), `infodesk_indisponivel` (5xx/timeout/rede **sem** ter recebido `number` → ticket fica `pendente` e o gateway **reconcilia por listagem** antes de responder: `GET /tickets?phone=<E.164>&status=open` e casa por `conversationId == conversation_id` (preferido) ou `title == proposta.titulo` + `createdAt` ≥ `proposta.criada_em`; achou → `aberto` + `numero` e devolve **sucesso** normal; não achou ou o GET também falhou → responde `infodesk_indisponivel`, alerta `ticket_pendente`, mensagem ao cliente "não consegui registrar agora, um atendente vai assumir" + handoff; o reconciliador (seção B) repete a busca pelos `pendente` até `TICKET_PENDENTE_TTL` (10 min) e então marca `falha`), `escopo_insuficiente` (403 `FORBIDDEN` → alerta de configuração). `400 VALIDATION_ERROR` (campo exigido pela organização, `07` T10) e `400 INVALID_REFERENCE` (taxonomia desatualizada) viram `infodesk_indisponivel` para o LLM + alerta de configuração com `errors` no log (sem PII) |
| Idempotência | **I-local** por `chave_idempotencia` (unique index). Retry de rede após `POST` sem resposta **nunca** repete o POST: só `GET /tickets?phone=` (ADR 0005, revisão 2026-10-20). Um segundo `abrir` com a mesma chave devolve o resultado da reconciliação, não um ticket novo |
| Log | `ticket_aberto numero=… conversa=… categoria=… mesa=… reconciliado=bool` (sem CPF/telefone) |

### A4. `consultar_status_chamado`

| Item | Valor |
|---|---|
| Propósito | Responder "como está meu chamado nº X?" |
| Args do LLM | `numero: str` (dígitos) |
| Injetados | `telefone`, `conversation_id`, `identificacao` |
| Validação | `numero` só dígitos (1–10) |
| Gateway | `POST /tools/infodesk/ticket/consultar` `{telefone, conversation_id, numero, identificacao}` → `GET /tickets/:number` e então **posse** (por código, nesta ordem): (a) `tickets` local tem `numero` com o mesmo telefone; (b) `ticket.conversationId` pertence a uma conversa deste telefone (`conversas`); (c) `ticket.contactPhone` ou `ticket.requester.phone` igual ao telefone da sessão (ambos normalizados E.164, tolerando o 9º dígito); (d) `ticket.requester.id == identificacao.requester_id` (lookup); (e) senão → `motivo: "posse_nao_verificada"` (o LLM diz que não consegue confirmar por aqui e oferece atendente) e o gateway **não** devolve nenhum campo do ticket |
| Infodesk | `GET /integration/v1/tickets/:number` (`number` inteiro na rota; `numero` do LLM é texto só-dígitos convertido por código) |
| Retorno | `{status: true, numero, status_ticket: "<status.name do Infodesk>", titulo, aberto_em, atualizado_em, mesa}` — `status` é **objeto por mesa** (`{id, name, isOpen, isClose, isCanceled}`); `status_ticket` leva `status.name` **literal** (nunca inventar nome de status); `isClosed`/`isCanceled` atualizam `tickets.status` local e, se a mesa não tiver nome legível, o prompt orienta o LLM a dizer "em andamento"/"encerrado" com base no que o gateway informar em `status_ticket`. Campos a mais (`etapa`, `prazo`) só por PR de contrato (ADR 0006) |
| Erros | `nao_encontrado` (404 `NOT_FOUND`), `posse_nao_verificada`, `infodesk_indisponivel` |
| Idempotência | I-natural |

### A5. `listar_meus_chamados`

| Item | Valor |
|---|---|
| Propósito | "Quais chamados eu tenho?" — chamados **abertos** do telefone no Infodesk (qualquer origem) + os que a Sofia abriu nos últimos 90 dias (tabela local), sem duplicar por `numero` |
| Args do LLM | nenhum |
| Injetados | `telefone`, `conversation_id` |
| Gateway | `POST /tools/infodesk/ticket/listar` → `GET /tickets?phone=<E.164>&status=open&limit=20` (ou `lookup.openTickets` do episódio, se em cache) ∪ `SELECT` em `tickets` por telefone, últimos 90 dias (traz os já encerrados que a Sofia abriu, com `status_ticket` do último evento `ticket.*` recebido). Se o GET falha, devolve só a parte local com `observacao` dizendo isso; se as duas falham → `chamados_indisponiveis` |
| Infodesk | `GET /integration/v1/tickets?phone=&status=open` (I-natural). Posse é dada pelo filtro `phone`; nada de outro telefone entra |
| Retorno | `{status: true, total, chamados: [{numero, titulo, categoria, status_ticket, aberto_em}], observacao: "<texto fixo do gateway: 'abertos no Infodesk + abertos por aqui' ou 'só os abertos por aqui (Infodesk indisponível)'>"}` — `categoria: null` para chamados que não foram abertos pela Sofia |
| Erros | `chamados_indisponiveis` (herdado do contrato E1.6; confirmar em E3.1) |
| Idempotência | I-natural |

### A6. `transferir_para_atendente`

| Item | Valor |
|---|---|
| Propósito | O LLM pede transferência quando o cliente exige humano ou o assunto foge do que a Sofia pode resolver. (As transferências por mídia, falha, frustração alta e baixa confiança são **decisão de código**, não desta tool) |
| Args do LLM | `motivo: Literal["pedido_cliente", "fora_do_escopo", "nao_resolvido"]` |
| Injetados | `telefone`, `conversation_id`, `classificacao` (para escolher o departamento) |
| Precondição | conversa ainda com o bot (estado local: `estado == bot` **e** `handled_by == EXTERNAL_BOT`) |
| Gateway | `POST /tools/infodesk/conversa/transferir` `{conversation_id, motivo, categoria?}` → departamento = `taxonomia[categoria].departamento` ou `DEPARTAMENTO_HUMANO_PADRAO` (`07` D6); envia a mensagem de aviso ao cliente; **`POST /conversations/:id/handoff {departmentId, note}`** (ADR 0007) — `note` é montada **por código** a partir de `motivo` + classificação + identificação mascarada + nº do ticket se houver (`"Sofia → humano · motivo: pedido do cliente · categoria: sem_conexao (jev 0.88) · solicitante: Maria S. (ACME) · sem chamado"`), nunca texto livre do LLM; marca `conversas.estado = transferida`; registra `falhas`/ocorrência com ID `SOF-AAAAMMDD-HHMM-NN`; vigia de alertas |
| Infodesk | `POST /integration/v1/conversations/:id/handoff`. **Não** usa `/department`, `/assign` nem `/bot {active:false}` (ADR 0007; `07` C4, C10, N4). `userId` do handoff só se P11 mapear fila → pessoa |
| Retorno | `{status: true, departamento: {nome}, mensagem_ao_cliente: "<texto fixo do gateway>"}` — o **gateway** envia a mensagem de aviso **antes** do handoff (o handoff não avisa o cliente; `07` C10); o LLM só confirma em uma frase |
| Erros | `ja_transferida` (inclui o caso "humano já assumiu": `handled_by != EXTERNAL_BOT`), `infodesk_indisponivel` (→ alerta; o gateway marca transferida localmente mesmo assim para o bot calar e o reconciliador/vigia `BOT_TIMEOUT_HANDOFF` repete o handoff — `07` N1) |
| Idempotência | I-local (estado da conversa) |

### A7. `consultar_base_conhecimento` (não é tool: nó `retrieve`)

Mantido do Zapin como etapa fixa do grafo (retrieval-first), **condicionada à
intenção** `duvida_informacao` ou `outro` (para reclamação pura o RAG é
pulado, economizando latência). Fonte dos docs: P6. Sem cobertura → o prompt
manda oferecer chamado/atendente, nunca inventar.

---

## B. Operações internas do gateway (não expostas ao LLM)

| Operação | Pacote Go | Infodesk | Observações |
|---|---|---|---|
| Receber webhook | `internal/webhook` | `message.received`, `conversation.started`, `conversation.assigned`, `conversation.handoff`, `conversation.finished`, `ticket.created\|updated\|closed\|reopened\|canceled`, `ping` (eventos a assinar na chave; `message.sent`/`message.status` opcionais) | **`HMAC-SHA256(secret, "<X-Infominas-Timestamp>.<raw body>")`** em hex com prefixo `sha256=`, `subtle.ConstantTimeCompare`; **recusa `\|now − timestamp\| > 5 min`** (`WEBHOOK_JANELA`, relógio injetável `Now`) → `401`; `200` em ms (processa em fila); dedup pelo **`id` do evento** (`evt_…`, estável entre reentregas; 7 dias) e por `data.message.id`; `X-Infominas-Delivery` só no log; valida `organizationId` contra `GET /me`; guarda o maior `seq` visto (`eventos_cursor`); **só vira turno do cliente** quando `event == message.received` ∧ `message.sender == CLIENT` ∧ `conversation.handledBy == EXTERNAL_BOT` ∧ `conversation.channel == WHATSAPP` ∧ `contact.groupJid == null` (`07` N5); qualquer evento atualiza `conversas.handled_by` (reentrega até 6 h: não rebaixar estado com evento mais velho que `conversas.updated_at` — `07` W9); `conversation.assigned` ou `handledBy != EXTERNAL_BOT` → `transferida` (humano assumiu sem handoff nosso); `conversation.finished` → `encerrarEpisodio` (idempotente); `ticket.*` → `tickets.status` local; `message.sent` com `senderName == INFODESK_NOME_CHAVE` = eco, ignorar (`07` W8); `ping` → `200` sem parse |
| Validar chave no boot | `internal/infodesk` `Me(ctx) (Me, error)` | `GET /me` | `Me{OrganizationID, KeyID, Name, Scopes, HandlesConversations}`; boot falha se `HandlesConversations == false` ou se faltar escopo da lista mínima (`tickets:read`, `tickets:write`, `conversations:read`, `conversations:write`, `contacts:read`, `events:read`); `OrganizationID` vira o valor esperado nos webhooks; `Name` alimenta `INFODESK_NOME_CHAVE` se não configurado |
| Identificar telefone | `internal/infodesk` `Lookup(ctx, phone) (Lookup, error)` | `GET /contacts/lookup?phone=` | `Lookup{Requesters []Solicitante, ClientIDs []string, OpenTickets []TicketResumo, Conversation map; Bruto}`; chamado pelo gateway no **primeiro turno do episódio** (cache em `contatos` com `origem = lookup`, TTL `IDENT_TTL`); 1 solicitante → identificado (`requester_id`, `cliente_id`, `nome_cadastro`); 0 → fallback CPF/contrato (A1); >1 → `IDENT_LOOKUP_AMBIGUO` (default: tratar como não identificado e pedir CPF; hipótese); falha/`403` → log + fallback, nunca bloqueia (ADR 0004, revisão). `clientContacts` ignorado (`07` N2) |
| Enviar mensagem | `internal/infodesk` `EnviarMensagem(ctx, convID, texto)` | `POST /conversations/:id/messages` `{ "text": … }` | **campo `text`** (o guia antigo dizia `body`); `DividirBolhas` + `FormatWhatsApp` (negrito `*x*`; confirmar C7) + `QuebrarMensagem(900)` (`07` T9/C7); pausa `BOLHA_PAUSA` entre bolhas; retry 2× só em 5xx/rede/`429` com dedup própria (não reenviar bolha já aceita); `409 WHATSAPP_WINDOW_CLOSED` = definitivo → encerra episódio local (sem template na v1); a resposta `201 {conversationId, messages[]}` fica como `Bruto` |
| Nota interna | `internal/infodesk` `NotaInterna(ctx, convID, texto)` | `POST /conversations/:id/messages` `{ "text", "internal": true }` | opcional (`RESUMO_NOTA_INTERNA=false` default): resumo da triagem só para a equipe; alternativa ao rodapé do ticket |
| **Handoff** (transferir para humano) | `internal/infodesk` `Handoff(ctx, convID, Handoff{DepartmentID, UserID *string, Note})` | `POST /conversations/:id/handoff` | **caminho único** de transferência para humano (ADR 0007): bot sai, conversa volta à fila do departamento (ou distribuição automática), `note` vira nota interna, atendentes notificados; `UserID` só com P11. `Note` sempre gerada por código (sem PII em claro: nome do cadastro sim, CPF mascarado). Evento `conversation.handoff` confirma |
| Mudar fila sem soltar o bot | `internal/infodesk` `TransferirDepartamento(ctx, convID, deptID)` | `POST /conversations/:id/department` | mantido no cliente; **não é transferência para humano** e **não é chamado na v1** (`07` C4); `null` remove — não usado |
| Atribuir atendente | `internal/infodesk` `Atribuir(ctx, convID, userID)` | `POST /conversations/:id/assign` | implementado no cliente, **não** chamado na v1 (ADR 0007; `07` C10) |
| Assumir/soltar sem aviso | — | `POST /conversations/:id/bot` | **não implementar** (`07` N4): soltar sem notificar deixa o cliente sem ninguém |
| Vincular solicitante | `internal/infodesk` `VincularSolicitante(ctx, convID, requesterID)` | `POST /conversations/:id/requester {id}` | best-effort antes de abrir o ticket quando o lookup identificou (fluxo de referência do `08`); `null` desfaz — não usado |
| Encerrar conversa | `internal/infodesk` `Encerrar(ctx, convID)` | `POST /conversations/:id/finish` | implementado, **não chamado** na v1 (decisão de negócio `07` C6; `finish` do ticket também `false`) |
| Dados de referência | `internal/infodesk` `Mesas()`, `Mesa(id)`, `Catalogo(deskID)`, `Departamentos()`, `Atendentes()` + cache TTL 10 min | `GET /desks`, `/desks/:id`, `/catalog?deskId=`, `/departments`, `/users` | carregados no boot; `Mesa(id)` traz `priorities`, `statuses`, `stages`, `catalogItems` → `taxonomia.yaml` validada contra eles e `priorityId`/`catalogItemId` **resolvidos no boot por mesa** (`02`); nome inexistente → boot falha em `STRICT_TAXONOMIA=true`, senão alerta; aceitar array puro **ou** `{data}` (`07` D3); `INVALID_REFERENCE` em qualquer chamada → `InvalidarCache()` + recarga |
| Abrir ticket **pela conversa** | `internal/infodesk` `CriarTicketDaConversa(ctx, convID, NovoTicketConversa) (Ticket, error)` | `POST /conversations/:id/ticket` | `NovoTicketConversa{DeskID, Title, Description, PriorityID *string, CatalogItemID *string, Period: "session", Finish: false}`; `Ticket{Number int; Status Status{ID, Name, IsOpen, IsClose, IsCanceled}; IsClosed bool; ConversationID, ContactPhone string; Requester *Solicitante; Desk, Priority Referencia; Bruto map[string]any}` — **`Number` é inteiro** (`07` T1); aceitar 200/201; **nenhum retry** |
| Abrir ticket direto | `internal/infodesk` `CriarTicket(ctx, NovoTicket) (Ticket, error)` | `POST /tickets` | mantido como fallback (`TICKET_VIA_CONVERSA=false`): `NovoTicket{DeskID, Title, Description, Channel: "WHATSAPP", ContactPhone, PriorityID, CatalogItemID, ClientID, RequesterID *string}`; **nenhum retry** |
| Listar tickets | `internal/infodesk` `ListarTickets(ctx, FiltroTickets{Phone, Status, ConversationID?, Limit, Cursor}) (Pagina[Ticket], error)` | `GET /tickets?phone=&status=open&limit=&cursor=` | usado por A2 (dedup), A5 (listar) e pela **reconciliação de `pendente`** (A3 / ADR 0005); paginação `{data, nextCursor}`; filtro por `conversationId` é feito **localmente** sobre a página (a rota não tem esse filtro) |
| Consultar ticket | `internal/infodesk` `Ticket(ctx, number int)` | `GET /tickets/:number` | devolve `Ticket` tipado + `Bruto` (`messages` fica bruto, `07` T13) |
| Vincular chamado existente | `internal/infodesk` `VincularTicket(ctx, convID, number)` + `NotaTicket(ctx, number, texto)` | `POST /conversations/:id/link-ticket`, `POST /tickets/:number/notes` | quando o cliente fala de chamado já aberto (`lookup.openTickets`); uso pelo cérebro depende de tool nova (fora da v1 — candidata A8, item novo do catálogo quando T12 for respondido) |
| Responder ticket | `internal/infodesk` `ResponderTicket(ctx, number, body)` | `POST /tickets/:number/reply` | implementado, **não usado** na v1 (T7) |
| Feed de eventos | `internal/infodesk` `Eventos(ctx, after int64, limit int) (FeedEventos, error)` | `GET /events?after=<seq>&limit=500` | `FeedEventos{Data []Evento, NextAfter int64, HasMore bool}` — **mesmo envelope** do webhook, mesmo handler; escopo `events:read` |
| Ler conversa | `internal/infodesk` `Conversa(ctx, id)` | `GET /conversations/:id` | checagem "bot ainda conduz?" quando um evento reentregue é mais velho que o estado local (`07` W9); `handledBy`/`botActive` tipados, resto `Bruto` |
| Listar conversas | `internal/infodesk` `Conversas(ctx, FiltroConversas)` | `GET /conversations?botActive=true&channel=WHATSAPP&…` | só para o **vigia** `BOT_TIMEOUT_HANDOFF` (`07` N1) e diagnóstico; **não** é mais o motor do reconciliador |
| **Reconciliador** | `internal/reconciliador` | `GET /events?after=<seq>` (+ `ListarTickets` para `pendente`) | **nasce ligado** (`RECONCILIA_ATIVO=true`): no boot e a cada `RECONCILIA_INTERVALO` (60 s) lê o feed a partir do `seq` persistido em `eventos_cursor`, entrega cada envelope ao **mesmo handler do webhook** (mesmo dedup por `id`), avança o cursor só após processar; pagina enquanto `hasMore`; também reconcilia `tickets.status == pendente` por listagem (A3). Primeira execução sem cursor: `after = maior seq visto pelo webhook` ou, sem nenhum, `now − RECONCILIA_RETROATIVO` (1 h) via `createdAt` (ignora mais velhos) |
| Vigia de conversa sem resposta | `internal/reconciliador` (`vigia.go`) | `GET /conversations?botActive=true` + `Handoff` | conversa com `handled_by == EXTERNAL_BOT` e última mensagem `CLIENT` sem resposta nossa há > `BOT_TIMEOUT_HANDOFF` (5 min) → handoff com nota de erro + alerta (`07` N1; hipótese até a Infominas dizer se o Desk tem timeout próprio) |
| Transferência por código | `internal/engine/transferencia.go` | `POST /conversations/:id/handoff` | **mesmo caminho** da A6 (`Handoff`), origens: `cliente` (pedido explícito detectado no Go: regex `atendente|humano|pessoa de verdade`), `anexo` (`message.type != TEXT` ou `attachment != null`; lembrar das **duas mensagens** por mídia no WhatsApp — a legenda/rótulo `"[imagem]"` é texto), `cerebro` (falha definitiva/esgotou retries), `disjuntor`, `frustracao` (vinda do cérebro em `acoes`), `confianca` (idem), `horario` (fora do horário humano — P7), `vigia` (N1), `ticket_pendente` (A3). `note` por código em todas |
| Alertas | `internal/alerta` | — | mesmos tipos do Zapin, renomeados (`sofia_alerta`, prefixo `[Sofia]`), + `taxonomia_invalida`, `ticket_pendente` (POST sem resposta e listagem não achou), `handoff_falhou`, `humano_assumiu_sem_handoff` (métrica), `webhook_timestamp_fora_da_janela` (possível replay ou relógio errado), `chave_sem_escopo` (`GET /me` no boot) |

### Contrato A (gateway → cérebro) — mudanças

```jsonc
// POST /chat
{
  "conversation_id": "uuid-infodesk",
  "canal": "whatsapp",
  "mensagem": "texto coalescido da rajada",
  "telefone": "+5531990000000",
  "nome_contato": "Maria",              // data.contact.name do webhook (pode ser apelido)
  "contato": {                           // null se nunca identificado
    "forma": "cpf",                      // "telefone" (lookup do Infodesk) | "cpf" | "contrato"
    "valor_mascarado": "***.***.789-**", // para "telefone": o nome do cadastro mascarado ("Maria S.")
    "cliente_id": null, "nome_cadastro": null   // preenchidos quando o lookup resolveu
  },
  "estado_conversa": "bot"              // bot | transferida (nunca chega transferida; defesa)
}
// `forma: "telefone"` é o único acréscimo da revisão 2026-10-20 (valor novo, chave igual):
// o requester_id fica só no gateway (tabela `contatos`); o cérebro não precisa dele.
// resposta
{
  "reply": "texto com bolhas separadas por ---",
  "acoes": [ {"tipo": "transferir", "origem": "frustracao"|"confianca"|"pedido_cliente", "motivo": "..."} ],
  "classificacao": {"intencao": "...", "categoria": "...", "prioridade": "...", "confianca": 0.91, "origem": "jev"},
  "fontes": [],
  "erro": null
}
```

`acoes` passa a ser **usado** pelo gateway (no Zapin era sempre vazio): `transferir` é executado pelo caminho único de transferência. `classificacao` vai para o log/alerta e para a tabela `conversas` (auditoria).

Endpoints do cérebro: `POST /chat`, `POST /encerrar-episodio` (limpa `proposta_chamado`, `classificacao` e `identificacao` do estado da conversa e devolve `campos_limpos`; o cache de identificação por telefone vive no gateway e volta em `contato` no próximo `/chat`, e o estado do cérebro só guarda o registro mascarado), `GET /health`, `POST /classificar` (**só para evals/ops**: roda o JEV/roteador sem o grafo; protegido pelo mesmo `X-Tools-Token`).

---

## C. Tabelas novas do `store` (os dois backends)

| Tabela | Colunas | Uso |
|---|---|---|
| `processed_events` | `event_id` PK (o `id` `evt_…` do **evento**, não o `X-Infominas-Delivery`), `seq`, `created_at` | dedup por 7 dias (cobre as reentregas de até ~7,5 h e a manual); migração renomeia `delivery_id` → `event_id` |
| `eventos_cursor` | `nome` PK (`"infodesk"`), `ultimo_seq` BIGINT, `updated_at` | cursor do `GET /events?after=` (reconciliador); atualizado também pelo webhook (maior `seq` visto) |
| `conversas` | `conversation_id` PK, `telefone`, `estado` (`bot`/`transferida`/`encerrada`), **`handled_by`** (`EXTERNAL_BOT`/`INTERNAL_BOT`/`AGENT`/`QUEUE`, último visto), `ticket_number` NULL (int), `sessao_iniciada_em`, `ultima_msg_id`, `ultima_msg_em`, `ultima_resposta_em` (nossa; para o vigia N1), `classificacao_json`, `updated_at` | estado bot/humano (o `estado` é nosso; `handled_by` é o do Infodesk — divergência vira métrica `humano_assumiu_sem_handoff`); reconciliação; fim de episódio |
| `contatos` | `telefone` PK, `origem` (`lookup`/`cpf`/`contrato`), `forma`, `valor_mascarado`, `valor_hash` (sha256 + sal), **`requester_id`**, `cliente_id`, `nome_cadastro`, `expires_at` | identificação com TTL; o CPF em claro **não** é persistido (só hash + mascarado; o rodapé do ticket recebe o mascarado); `requester_id` alimenta `/conversations/:id/requester` e a posse de A4 |
| `tickets` | `chave_idempotencia` PK, `conversation_id`, `telefone`, `numero` NULL (**inteiro**), `categoria`, `mesa_id`, `departamento_id`, `prioridade`, `status` (`pendente`/`aberto`/`falha`), `status_infodesk` (`status.name` do último `ticket.*`), `encerrado` bool, `reconciliado` bool, `raw_resposta`, `created_at`, `updated_at` | idempotência, dedup, listagem local, reconciliação de `pendente` |
| `falhas` | como no Zapin, `ID` com prefixo `SOF-` | ocorrências/transferências |

Toda mudança em `store.Store` vale para SQLite e Postgres e ganha caso em
`store_test.go` (regra herdada).

---

## D. Matriz tool × política × efeito

| Tool | Injeta | Valida | Exige | Efeito colateral | Fase |
|---|---|---|---|---|---|
| `informar_identificacao` | telefone, conversation_id | cpf (DV), contrato (regex) | — (só é chamada se o lookup do gateway não resolveu ou `IDENT_EXIGIR_CPF=true`) | grava cache | única |
| `preparar_abertura_chamado` | telefone, conversation_id, classificacao, identificacao | titulo/descricao (tamanho) | identificado (lookup **ou** CPF/contrato); categoria definida; sem protocolo citado | nenhum (estado: proposta; 1 GET de dedup no Infodesk) | 1 de 2 |
| `abrir_chamado` | telefone, conversation_id, proposta | — | proposta válida + confirmação explícita no turno seguinte | **cria ticket pela conversa**; handoff opcional (`TRANSFERIR_APOS_TICKET`) | 2 de 2 |
| `consultar_status_chamado` | telefone, conversation_id, identificacao | numero | posse por código (tabela local, `conversationId`, telefone do ticket ou `requester_id`) | nenhum | única |
| `listar_meus_chamados` | telefone, conversation_id | — | — | nenhum (GET filtrado por `phone`) | única |
| `transferir_para_atendente` | telefone, conversation_id, classificacao | motivo (enum) | conversa com o bot (`handled_by == EXTERNAL_BOT`) | **handoff** (ADR 0007) | única (sem confirmação: o pedido do cliente é a confirmação) |
