# 07 — Pontos ambíguos e perguntas abertas à Infominas

Lista consolidada do que a documentação **não responde** e que impacta a
implementação. Cada item tem: o que falta, por que importa, e a **hipótese de
trabalho** que o plano adota enquanto não há resposta (sempre a mais
conservadora). Agentes implementando **não** devem "resolver" um item daqui
assumindo um valor — devem codificar a hipótese de forma configurável e deixar
o item referenciado no código (`// ver docs/infodesk/07 item X`).

Estado: `ABERTO` = sem resposta; `RESPONDIDO (data)` = o doc da área foi
atualizado; a linha fica aqui com a resposta resumida para quem implementou
sobre a hipótese antiga saber o que mudar.

**Revisão de 2026-10-19:** o PDF v1 respondeu a maior parte dos itens
originais. Itens novos recebem os prefixos `N` (novos conceitos) e números
seguintes nas seções existentes.

**Revisão de 2026-10-21 (fonte: código do Infodesk).** O código-fonte do
Infominas Desk (`apps/api/src/integrations/**`, `main` de 2026-10-20, que é o
que o deploy publica) foi lido e respondeu os itens técnicos que o PDF deixava
em aberto. Esses itens estão marcados `RESPONDIDO (2026-10-21, código)` e o
detalhe, com o arquivo e a função de origem, está em
[10-regras-do-codigo.md](10-regras-do-codigo.md). **Uma resposta vinda do
código descreve o comportamento atual, não um compromisso de contrato**: se
o Infodesk mudar, só o 10 avisa. O que continua `ABERTO` é decisão de negócio
ou configuração da organização, que o código não responde.

## A — Acesso e ambiente

| # | Pergunta | Impacto | Hipótese de trabalho / resposta | Estado |
|---|---|---|---|---|
| A1 | Existe **ambiente de homologação/sandbox** (base URL + chave de teste) ou só produção? | Sem sandbox, todos os testes de integração batem em produção | Testes de integração só contra `mock-infodesk/`; smoke em produção com número de teste da Infominas. O PDF v1 continua citando só produção | ABERTO |
| A2 | Quem configura webhook/segredo na chave? | Runbook de deploy | **Resposta:** administrador do Desk em *Configurações › Integrações*; chave e `whsec_` mostrados uma vez; *Editar*, *Rotacionar segredo*, *Revogar*, *Enviar teste*, *Entregas* na mesma tela (01). Chaves diferentes por ambiente dependem de A1 | RESPONDIDO (2026-10-19) |
| A3 | Os endpoints de referência exigem escopo? | Falta de escopo | **Resposta:** valem para qualquer chave válida (01) | RESPONDIDO (2026-10-19) |
| A4 | Limites do **rate limit** e `Retry-After` | Dimensionar polling e retries | **Resposta (código):** `ThrottlerGuard` global do NestJS, janela de 60 s, limite `THROTTLE_LIMIT` (default **300**) por IP, para toda a API (não só a integração). Manter backoff exponencial com teto 30 s (10, §1) | RESPONDIDO (2026-10-21, código) |
| A5 | IP de origem fixo dos webhooks (allowlist)? | Infra do endpoint | Só HMAC + timestamp + HTTPS público. O worker de entrega roda nos workers da API (VPS `infodesk.infominas.net`); IP fixo depende da infra, não do código | ABERTO |
| A6 | **Base URL**: a capa do PDF v1 traz `https://infodesk.infominas.net/v1/integration/v1` (`v1` duplicado); o guia anterior trazia `https://infodesk.infominas.net/api/integration/v1`; o PDF manda copiar da tela de Integrações | Toda chamada | **Resposta (código):** o controller é `@Controller("integration/v1")` e a API é servida sob `/v1` (`PUBLIC_API_URL` default `https://infodesk.infominas.net/v1`), logo a base é mesmo **`https://infodesk.infominas.net/v1/integration/v1`**; `/api/...` cai no 404 do front. `INFODESK_BASE_URL` continua configurável e `GET /me` valida no boot (10, §1) | RESPONDIDO (2026-10-21, código) |

## T — Tickets

| # | Pergunta | Impacto | Hipótese de trabalho / resposta | Estado |
|---|---|---|---|---|
| T1 | Schema da resposta de `POST /tickets` e `GET /tickets/:number`; tipo de `number` | Tipar o cliente Go | **Resposta:** objeto completo em 03; `number` é **inteiro**; `status` é objeto por mesa com `isOpen/isClose/isCanceled`; HTTP de sucesso ainda não citado (aceitar 200/201) | RESPONDIDO (2026-10-19) |
| T2 | Valores de `channel` | 4xx | **Resposta:** `WHATSAPP`, `WEB`, `EMAIL`, `CHAT`, `AGENT` | RESPONDIDO (2026-10-19) |
| T3 | Como obter `priorityId` | Prioridade determinística no ticket | **Resposta:** `GET /desks/:id.priorities` (por mesa). Taxonomia mapeia nome → UUID resolvido na mesa de destino (02) | RESPONDIDO (2026-10-19) |
| T4 | O que são `clientId`/`requesterId` e como obtê-los | Identificação | **Resposta:** cliente = empresa; solicitante = pessoa do cliente. `GET /contacts/lookup?phone=`, `/clients`, `/requesters`, `POST /requesters` (04). Se omitidos, o chamado fica sem vínculo (só `contactPhone`); "se a organização exige, vira 400" (T10) | RESPONDIDO (2026-10-19) |
| T5 | Listar tickets por contato/telefone (dedup, "já tenho chamado?") | Dedup | **Resposta:** `GET /tickets?phone=&status=open`, `lookup.openTickets`, `/requesters/:id.openTickets` (03/04). Dedup local continua (ADR 0005), agora com reconciliação por listagem | RESPONDIDO (2026-10-19) |
| T6 | Rota de fechamento; o agente fecha? | Encerramento | **Resposta parcial:** `POST /tickets/:number/close` existe e "respeita checklist/regras da mesa". **Se a Sofia deve fechar** continua regra de negócio pendente → v1 só comenta (`/notes`) e deixa ao humano | RESPONDIDO parcial (2026-10-19) |
| T7 | `/reply` chega na mesma conversa de WhatsApp do bot? | Mensagem duplicada | **Resposta (código):** sim — `TicketsService.reply` publica a mensagem (`sender: AGENT`) **na conversa vinculada ao ticket** (cria uma se não houver) e faz fan-out ao canal (WhatsApp/e-mail); gera `message.sent` **e** `ticket.replied`. Como a conversa é a mesma do bot, a Sofia **não usa `/reply`**: fala por `/conversations/:id/messages` (10, §4) | RESPONDIDO (2026-10-21, código) |
| T8 | Campo de origem/externalId para carimbar o bot | Auditoria | **Resposta:** `conversationId` no objeto `ticket`; `channel: WHATSAPP`; abrir via `POST /conversations/:id/ticket` já vincula. Rodapé na descrição vira opcional | RESPONDIDO (2026-10-19) |
| T9 | Limite de tamanho de `title`/`description`/`text` | 4xx | **Resposta (código):** `title` ≤ **200**, `description` ≤ **20 000** (`CreateTicketDto` e `IntegrationConversationTicketDto`); `text` da mensagem **sem limite no DTO** — o teto de 900 por bolha continua sendo regra nossa (WhatsApp). Os limites internos da Sofia (120/4000) ficam abaixo dos do Desk e podem permanecer (10, §3) | RESPONDIDO (2026-10-21, código) |
| T10 | **Quais campos a organização Infominas exige** em `POST /tickets` (cliente? solicitante? serviço/`catalogItemId`? prioridade?) e por mesa | `400 VALIDATION_ERROR` em produção | **Resposta parcial (código):** é um **toggle da organização**, `requireTicketFields` ("Exigir campos na abertura do chamado", *Configurações*). Ligado → `400 "Campos obrigatórios do chamado: cliente, solicitante, serviço do catálogo, prioridade"` (prioridade pode ser `impact`+`urgency`). Desligado → só `deskId` e `title`. Além disso cada mesa pode ter **campos personalizados** com `required: true` (`Desk.customFields`), que **não aparecem em `GET /desks/:id`** e `/conversations/:id/ticket` não aceita `customValues` — mesa assim recusa o ticket da API. **Falta:** o valor do toggle na org Infominas e se alguma mesa de destino tem campo personalizado obrigatório (10, §3) | RESPONDIDO parcial (2026-10-21, código) |
| T11 | `POST /conversations/:id/ticket`: o chamado herda `clientId`/`requesterId`/`contactPhone` da conversa? Aceita `channel`? Devolve o objeto `ticket`? | Escolher entre `/tickets` e `/conversations/:id/ticket` | **Resposta (código):** herda **`clientId` e `requesterId`** da conversa; `channel` é derivado do canal da conversa (não aceito no corpo); `contactPhone` **não** é copiado (o ticket fica ligado à conversa, que tem o telefone); `description` do corpo vai **acima** da transcrição (`period` default `session`), separada por `---`; devolve o objeto `ticket` público (201). Vincular o solicitante na conversa **antes** continua certo (10, §3) | RESPONDIDO (2026-10-21, código) |
| T12 | `POST /conversations/:id/ticket` quando a conversa **já tem** `ticketNumber`: cria outro, erro `CONFLICT`, ou substitui? | Dedup | **Resposta (código):** **cria outro ticket e substitui o vínculo** da conversa, sem erro. Só `/link-ticket` dá `409 CONFLICT` (quando o ticket já está vinculado a **outra** conversa). O dedup continua inteiramente nosso (ADR 0005): checar `conversation.ticketNumber` e `GET /tickets?phone=&status=open` antes de abrir (10, §3) | RESPONDIDO (2026-10-21, código) |
| T13 | Formato de `messages` em `GET /tickets/:number` e corpo de `POST /alerts` | Tipagem | **Resposta (código):** `messages` = últimas 200 mensagens **públicas** (`internal: false`) do ticket, em ordem cronológica, com `{ id, sender, senderName, body, createdAt, conversationId }`. `/alerts` não usado pela Sofia (10, §3) | RESPONDIDO (2026-10-21, código) |

## C — Conversas

| # | Pergunta | Impacto | Hipótese de trabalho / resposta | Estado |
|---|---|---|---|---|
| C1 | Schema de `GET /conversations`, paginação, `tab` | Reconciliador | **Resposta:** objeto `conversation` (04), filtros completos, paginação por cursor; `tab`: `entrada` = sem dono, `abertas`, `encerrados`, `todas`. O reconciliador deve usar **`GET /events?after=`** (05) em vez de varrer conversas | RESPONDIDO (2026-10-19) |
| C2 | Schema de mensagens; valores de `sender` | Contexto | **Resposta:** objeto `message` (04); `sender` ∈ `CLIENT`, `AGENT`, `SYSTEM`; `recentMessages` (50) em `GET /conversations/:id`; `GET /conversations/:id/messages?after=` | RESPONDIDO (2026-10-19) |
| C3 | **Como saber que um humano assumiu** | Bot responde por cima do humano | **Resposta:** `conversation.handledBy`/`botActive` em todo evento de mensagem + evento `conversation.assigned`; "um atendente pode assumir a qualquer momento (isso tira o bot)". Gateway cala quando `handledBy != EXTERNAL_BOT` (08) | RESPONDIDO (2026-10-19) |
| C4 | Transferir para humano = `/department` ou `/assign`? Existe fila? | Tool de roteamento | **Resposta:** `POST /conversations/:id/handoff { departmentId?, userId?, note }` — volta para a fila (ou `userId`, ou distribuição automática), `note` vira nota interna, atendentes notificados (08). `/department` muda fila sem soltar o bot | RESPONDIDO (2026-10-19) |
| C5 | `/finish` dispara `conversation.finished` para nós? Nova mensagem reabre ou cria outra? | Loop; ciclo de vida | **Resposta (código):** o nosso `/finish` grava `conversation.finished` **sem avisar o cliente** (`notify = null`). Nova mensagem do cliente numa conversa `FINISHED` **reabre a mesma conversa** (mesmo `id`): `status` volta a `IN_PROGRESS`, `sessionStartedAt` é renovado e sai `conversation.started` antes do `message.received`; se a org tem chave com "Conduz o atendimento", a conversa **volta ao bot externo** nessa reabertura. Estado local por `conversation.id` + `sessionStartedAt` está certo (10, §4) | RESPONDIDO (2026-10-21, código) |
| C6 | O bot encerra a conversa depois de abrir o ticket? | Regra de negócio | **Fato (código):** `finish: true` envia ao cliente a mensagem configurada no Desk (`ON_FINISH_OPEN_TICKET`, com nº e título), **pede avaliação** (se a config de chat ativar) e grava `finished`; `finish: false` só vincula. **Decisão de negócio pendente**; v1: `finish: false` + handoff (`TICKET_FINISH_CONVERSA`) | ABERTO (decisão de negócio) |
| C7 | Limite de `text`; markdown/negrito/quebra de linha | Formatação | 900 chars por bolha, bolhas separadas; `*negrito*` do WhatsApp — confirmar se o Desk reescreve (não verificado no código) | ABERTO |
| C8 | Autor das mensagens do bot no painel | UX do atendente | **Resposta:** o **nome da chave** (`senderName`) — pedir chave chamada "Sofia". Código confirma: `addMessage(..., { keepBot: true, senderName: ctx.name })` | RESPONDIDO (2026-10-19) |
| C9 | Formato de `message.attachment` (além de `url`: `mimeType`? `filename`? `size`? `id` para `/attachments/:id`?) | Mídia | **Resposta (código):** `attachment = { id, filename, mime, size, url }`, com `url = <PUBLIC_API_URL>/integration/v1/attachments/<id>` (exige a chave). Não baixar anexos na v1 continua (10, §5) | RESPONDIDO (2026-10-21, código) |
| C10 | `/assign` "avisa o cliente conforme a config de chat" — qual mensagem? E o `handoff` avisa? | UX | **Resposta (código):** o **`handoff` não envia nada ao cliente** (só nota interna + notificação dos atendentes); `/assign` segue a config de chat do Desk. A Sofia avisa o cliente **antes** do handoff com a mensagem dela, como planejado (10, §4) | RESPONDIDO (2026-10-21, código) |

## W — Webhooks

| # | Pergunta | Impacto | Hipótese de trabalho / resposta | Estado |
|---|---|---|---|---|
| W1 | Payload de `conversation.started`/`finished` | Saudação, fim | **Resposta:** `{ conversation, contact, …extras }`; `finished.reason` (05) | RESPONDIDO (2026-10-19) |
| W2 | `message.received` dispara para mensagens do atendente ou da API? | Loop | **Resposta:** não — essas vêm em `message.sent` (`sender: AGENT`, `senderName` = nome da chave ou do atendente) | RESPONDIDO (2026-10-19) |
| W3 | Mídia no webhook | Transferência por anexo | **Resposta:** `message.type` ∈ `IMAGE/FILE/AUDIO/CONTACT` + `attachment.url`; duas mensagens por mídia no WhatsApp (05) | RESPONDIDO (2026-10-19) |
| W4 | Formato dos ids; estabilidade em reentrega | Dedup | **Resposta:** `id` do evento (`evt_…`) é estável entre reentregas; `X-Infominas-Delivery` muda. Dedup por `id` do evento, 7 dias | RESPONDIDO (2026-10-19) |
| W5 | `organizationId` fixo por chave? | Multi-tenant | **Resposta:** `GET /me.organizationId` (01) | RESPONDIDO (2026-10-19) |
| W6 | `webhookSecret` = chave de API? Rotação? | Runbook | **Resposta:** segredo separado `whsec_…`, mostrado uma vez, rotacionável sem trocar a chave (01) | RESPONDIDO (2026-10-19) |
| W7 | Campos exatos dos extras de `conversation.assigned` (`userId`? `previousUserId`?), `handoff` (`note` só?) e `data` do `ping` | Tipagem | **Resposta (código):** `conversation.assigned` **não tem extras** (o `assignedUserId` novo está no próprio `conversation`); `conversation.handoff` tem `note` (string ou `null`); `conversation.department_changed` tem extras de departamento; `ticket.replied` tem `reply: { body, messageId }`; `message.status` tem `status` e `error`; `ping` tem `data: { message }`, `seq: 0`, `id: evt_ping_<ts>`. Tipar só `conversation`/`contact`; extras como bruto (10, §5) | RESPONDIDO (2026-10-21, código) |
| W8 | Eventos gerados pelas **nossas próprias** chamadas chegam ao nosso webhook (`message.sent` da nossa mensagem, `finished` do nosso `/finish`, `ticket.created` do nosso ticket)? | Loop/dedup | **Resposta (código):** **sim, todos.** O outbox grava na transação de negócio sem distinguir a origem: nosso `/messages` → `message.sent` com `senderName` = nome da chave; nosso `/finish` → `conversation.finished`; nosso `/ticket` → `ticket.created`; nosso `/handoff` → `conversation.handoff`. Ignorar `message.sent` com `senderName` = nome da chave e `ticket.created` com `number` já conhecido (10, §5) | RESPONDIDO (2026-10-21, código) |
| W9 | Durante uma **reentrega** (até 6 h depois), o `conversation` do envelope reflete o estado **da época** ou o atual? | Decisão "bot ainda conduz?" | **Resposta (código):** o **da época** — o `payload` é gravado em `integration_events` no momento do evento e a reentrega reenvia o mesmo JSON. Logo `handledBy` de um evento reentregue é histórico: comparar `createdAt` com o estado local e, em dúvida, `GET /conversations/:id` (10, §5) | RESPONDIDO (2026-10-21, código) |

## D — Dados de referência e roteamento

| # | Pergunta | Impacto | Hipótese de trabalho / resposta | Estado |
|---|---|---|---|---|
| D1 | Relação **mesa × departamento**; para cada categoria, qual mesa (chamado) **e** qual departamento (handoff)? | Tabela de roteamento | **Fato (código):** mesa (`Desk`) e departamento de chat (`ServiceGroup`) **não têm vínculo entre si**; o que liga os dois é o **item de catálogo**, que carrega `defaultDeskId` (mesa padrão) **e** `serviceGroupId` (equipe) — mas a API só expõe `defaultDeskId`. A tabela `taxonomia.yaml` continua nossa; o script `scripts/descobrir-taxonomia.py` gera o rascunho a partir do catálogo (10, §2). **Falta:** a decisão da Infominas por categoria | ABERTO (regra de negócio) |
| D2 | Frequência de mudança; cache | TTL | TTL 10 min + reload no boot; `INVALID_REFERENCE` força reload. Código: nenhum evento avisa mudança de referência | ABERTO (baixo impacto) |
| D3 | `/desks`, `/users`, `/departments`, `/catalog` são paginados (`{ data, nextCursor }`) ou array puro? | Cliente Go | **Resposta (código):** **array puro** nos quatro (e em `/desks/:id.catalogItems`); só `/tickets`, `/conversations`, `/messages`, `/events` paginam. Aceitar as duas formas continua barato e seguro (10, §2) | RESPONDIDO (2026-10-21, código) |
| D4 | Valores de `users.type` | Filtrar bots internos | **Resposta (código):** `/users` já filtra `type ∈ {ADMIN, ATTENDANT}` e `active: true`; não há bot interno na lista. `departmentIds` vem de `ServiceGroupMember` (10, §2) | RESPONDIDO (2026-10-21, código) |
| D5 | Campos de `catalogItems`/`/catalog` e relação catálogo × mesa (os 15 catálogos de 09 não têm mesa visível) | `catalogItemId` | **Resposta (código):** item = `{ id, name, description, code, ticketType, defaultDeskId, area: { id, name } }`. Relação: a mesa aponta para **um** catálogo (`Desk.catalogId`, só quando `catalogEnabled`); `GET /catalog?deskId=` devolve os itens desse catálogo; `GET /catalog` sem `deskId` devolve **todos**. No `POST`, item de outro catálogo → `400 "não pertence ao catálogo desta mesa"`. Enviar `catalogItemId` só quando a taxonomia mapear **e** o item pertencer ao catálogo da mesa (10, §2) | RESPONDIDO (2026-10-21, código) |
| D6 | **Departamentos de chat** da Infominas (nomes) — a tela de Departamentos ainda não foi capturada | Destino do handoff | **Resposta parcial (código):** departamento de chat = **`ServiceGroup`** ("Grupos de serviço" do Desk), `GET /departments` devolve `{ id, name, description }` dos ativos; cada um tem `chatDistributionEnabled` (round-robin) e membros. **Falta:** a lista real (rodar o script com a chave) e qual é o humano padrão (P11) (10, §2) | RESPONDIDO parcial (2026-10-21, código) |

## N — Novos conceitos (bot externo, identificação)

| # | Pergunta | Impacto | Hipótese de trabalho | Estado |
|---|---|---|---|---|
| N1 | Com **Conduz o atendimento** ligado, o que acontece com conversas que o bot **não responde** (ex.: gateway fora do ar por 1 h)? Ficam com selo Bot sem ninguém ver? Há timeout que devolve à fila? | Cliente sem resposta | **Resposta parcial (código):** o Desk **não tem timeout que devolva a conversa do bot externo à fila**; enquanto `botActive` sem fluxo interno, não há som/push/sino para os atendentes. O que existe é o encerramento por inatividade do chat (`finishOnInactivityWhatsapp` + `inactivityMinutesWhatsapp`, config de chat), que **finaliza** a conversa sem avisar. Logo o **vigia** local (`BOT_TIMEOUT_HANDOFF`) é obrigatório. **Falta:** valores da config de inatividade na org (10, §4) | RESPONDIDO parcial (2026-10-21, código) |
| N2 | Formato de `lookup.clientContacts` e diferença para `requesters` | Identificação | **Resposta (código):** `requesters` = pessoas cadastradas como **solicitante** (`Requester`, podem abrir chamado); `clientContacts` = **contatos do cadastro do cliente** (`ClientContact`: `{ id, name, email, phone, role, clientId, client }`), que não são solicitantes. Ignorar `clientContacts` na v1 continua; se nenhum `requester` casar, o telefone é "desconhecido" (10, §6) | RESPONDIDO (2026-10-21, código) |
| N3 | **Política de identificação** (ADR 0004): com `GET /contacts/lookup?phone=` disponível, telefone conhecido dispensa CPF? Telefone desconhecido → pedir CPF (atual) ou nome+empresa + `POST /requesters` (fluxo do PDF)? | ADR 0004, tools A1/A2 | Manter ADR 0004 (CPF/contrato validado por código) **acrescentando** o `lookup` como primeira tentativa (`IDENT_EXIGIR_CPF=false` quando o lookup resolve). Cadastro via `POST /requesters` **não** na v1 (cria dado no Infodesk a partir do que o cliente disse) | ABERTO (decisão de plano) |
| N4 | `POST /conversations/:id/bot { active: false }` vs `handoff`: o `/bot` solta sem notificar — a conversa fica em `QUEUE` sem ninguém avisado? | Uso indevido | **Resposta (código):** confirmado — `setExternalBot` só troca `botActive` "sem aviso a ninguém" (nem evento de integração, nem notificação). Nunca usar `/bot { active:false }`; só `handoff` (10, §4) | RESPONDIDO (2026-10-21, código) |
| N5 | Chat do agente desktop e portal geram `message.received` mas "não passam pelo bot" — chegam com `handledBy` ≠ `EXTERNAL_BOT`? | Filtro do gateway | **Resposta (código):** o bot externo só é ligado em `channel == WHATSAPP` **e** `waGroupId == null`, no início de sessão, quando a conversa está finalizada ou sem dono; os outros canais e grupos chegam com `handledBy` ∈ `QUEUE`/`AGENT`. O filtro `channel == WHATSAPP && handledBy == EXTERNAL_BOT && groupJid == null` está certo (10, §4) | RESPONDIDO (2026-10-21, código) |

## Como usar este arquivo

- Perguntas para a Infominas: enviar as linhas `ABERTO` de uma vez. Depois da
  revisão de 2026-10-21 restam **A1, A5, C6, C7, D1, D2, N3** e as partes
  "Falta" de **T10, D6, N1** — todas decisões de negócio ou configuração da
  organização; o questionário pronto está no 10, §8.
- Com a chave da API em mãos, rodar `scripts/descobrir-taxonomia.py`
  (10, §7): ele responde sozinho a lista real de mesas, prioridades, itens de
  catálogo e departamentos (D6, P1, P4).
- Ao receber resposta: atualizar o doc da área (01–06, 08), marcar
  `RESPONDIDO (AAAA-MM-DD)` aqui e abrir/ajustar a etapa correspondente em
  `docs/plano/03-etapas.md`.
