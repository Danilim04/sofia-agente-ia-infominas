# 10 — Regras do Infodesk lidas no código-fonte (2026-10-21)

Fonte: repositório **`infominas-desk`** (monorepo pnpm/turbo; API NestJS +
Prisma em `apps/api/`), branch `main` em 2026-10-20 (commit `1bd78f0`), que é
o que o workflow `deploy.yml` publica em `infodesk.infominas.net` após o CI.
A integração do chatbot (`feat/integracao-chatbot`, 19/10) está mergeada nela.

**Como ler este arquivo.** O PDF (01–06, 08) é o contrato; este doc é o que o
código **faz hoje**. Serve para fechar os itens técnicos do
[07](07-pontos-ambiguos.md) e para desenhar o gateway e as tools com menos
hipótese. Se o Infodesk mudar, o PDF pode não acompanhar — ao estranhar um
comportamento em produção, reler o arquivo citado em cada seção. Nada aqui é
regra de negócio da Infominas; o que depende de decisão deles está no §8.

Arquivos citados (todos sob `apps/api/src/`):

| Sigla | Arquivo |
|---|---|
| `ctrl` | `integrations/integration-api.controller.ts`, `integration-tickets.controller.ts`, `integration-conversations.controller.ts` |
| `api` | `integrations/integration-api.service.ts` |
| `tk` | `integrations/integration-tickets.service.ts` |
| `pay` | `integrations/integration-payloads.ts` |
| `ev` | `integrations/integration-events.service.ts` |
| `dl` | `integrations/integration-delivery.service.ts` |
| `guard` | `integrations/integration.guard.ts` |
| `dto` | `integrations/dto/integration.dto.ts`, `tickets/dto/create-ticket.dto.ts` |
| `TS` | `tickets/tickets.service.ts` |
| `CS` | `conversations/conversations.service.ts` |
| `schema` | `prisma/schema.prisma` |

## 1. Acesso

- **Base URL**: os três controllers são `@Controller("integration/v1")` e a
  API é servida sob `/v1` (`PUBLIC_API_URL` default
  `https://infodesk.infominas.net/v1`, `pay`). Base real:
  **`https://infodesk.infominas.net/v1/integration/v1`**. O nosso
  `INFODESK_BASE_URL` aceita a raiz com ou sem `/integration/v1`
  (`backend/internal/infodesk/client.go`, `PrefixoRotas`).
- **Chave**: `Authorization: Bearer <chave>` ou `X-Api-Key` (`guard`).
- **`GET /me`** devolve `{ organizationId, keyId, name, scopes,
  handlesConversations }` (`ctrl`). É o teste de chave do boot.
- **Rate limit**: `ThrottlerGuard` global, 60 s, `THROTTLE_LIMIT` (default
  300) por IP, para a API inteira (`app.module.ts`). Nada específico da
  integração; sem garantia de `Retry-After`.
- **Dados de referência** (`/desks`, `/desks/:id`, `/catalog`, `/users`,
  `/departments`) não exigem escopo (`ctrl`).
- **Ator** das ações feitas pela chave é `null`; a auditoria registra o nome
  da chave ("via integração <nome>"). Notas em ticket recebem o prefixo
  `[<nome da chave>]` (`tk.note`).

## 2. Dados de referência (o que alimenta a taxonomia)

| Rota | Retorno (código) | Observações |
|---|---|---|
| `GET /desks` | **array** de `{ id, name }`, só `active: true`, ordem por nome (`api.desks`) | nome é **único por organização** (`schema`, `@@unique([organizationId, name])`): comparar por nome exato é seguro |
| `GET /desks/:id` | `{ id, name, catalogEnabled, statuses[], stages[], priorities[], catalogItems[] }` (`tk.desk`) | `statuses`: `{ id, name, isOpen, isClose, isCanceled, isInAttendance }`; `stages`: `{ id, name, isFirst, isLast }`; `priorities`: `{ id, name }` em ordem de `position`; `catalogItems` só se `catalogEnabled` e a mesa tiver catálogo, senão `[]`. **Não expõe** `customFields` (ver §3) |
| `GET /catalog?deskId=` | **array** de itens do catálogo **da mesa** (`tk.catalog`); sem `deskId`, **todos** os itens da org | item = `{ id, name, description, code, ticketType, defaultDeskId, area: { id, name } }`, ordem por área e posição |
| `GET /departments` | **array** de `{ id, name, description }`, só ativos (`api.departments`) | é a tabela **`ServiceGroup`** ("Grupos de serviço"); tem `chatDistributionEnabled` (round-robin entre membros) e membros (`ServiceGroupMember`) |
| `GET /users` | **array** de `{ id, username, email, type, departmentIds[] }`, só `ADMIN`/`ATTENDANT` ativos (`api.users`) | `departmentIds` = grupos de serviço do usuário |

Fatos de modelo que importam para o roteamento (`schema`):

- **Prioridade, status e etapa são por mesa** (`DeskPriority`, `DeskStatus`,
  `DeskStage` têm `deskId`). Não há conjunto padrão de nomes no código: cada
  mesa cadastra os seus. Logo o bloco `prioridades` da taxonomia precisa ser
  conferido **em cada mesa de destino**; nome ausente numa mesa → `priorityId`
  omitido só ali (o ticket nasce com a prioridade padrão da mesa).
- **Catálogo**: a mesa aponta para **um** catálogo (`Desk.catalogId`, válido
  quando `catalogEnabled`). Um catálogo tem áreas (`CatalogArea`) e itens
  (`CatalogItem`). O item carrega **`defaultDeskId`** (mesa padrão),
  **`serviceGroupId`** (equipe, não exposto na API), `ticketType`
  (`INCIDENT`/`REQUEST`…), `defaultPriorityLevel` (P1..P5) e `teamName`. Ou
  seja, o **catálogo já é uma tabela de roteamento** do lado deles: o rascunho
  da taxonomia parte dele (§7).
- **Mesa × departamento**: nenhum vínculo direto. O que liga os dois é o item
  de catálogo (`defaultDeskId` + `serviceGroupId`). Como `serviceGroupId` não
  sai na API, o departamento do handoff continua sendo escolha nossa por
  categoria (07, D1).
- `Desk.allowPublicOpen`/`allowAgentOpen` controlam abertura pelo portal e
  pelo agente desktop; **não** afetam a API de integração.

## 3. Abrir chamado

Rotas: `POST /tickets` (corpo = `CreateTicketDto`) e
`POST /conversations/:id/ticket` (corpo = `IntegrationConversationTicketDto`:
`deskId`, `title?`, `description?`, `priorityId?`, `catalogItemId?`,
`period?`, `finish?`). As duas passam por `TicketsService.create` (`TS`), com
as **mesmas regras da tela**.

Validações, na ordem em que o código as aplica (`TS.create`):

1. **Campos obrigatórios da organização** — toggle `requireTicketFields`
   ("Exigir campos na abertura do chamado", tela *Configurações* do Desk).
   Ligado: exige `clientId`, `requesterId`, `catalogItemId` e (`priorityId`
   ou `impact`+`urgency`); falta → `400 "Campos obrigatórios do chamado: …"`.
   Desligado: só `deskId` e `title`. **O valor na org Infominas é pendência**
   (§8).
2. `clientId`/`requesterId` precisam existir (404 se não).
3. `catalogItemId` precisa existir **e**, se a mesa usa catálogo, pertencer ao
   catálogo da mesa (`400 "O item de catálogo não pertence ao catálogo desta
   mesa."`).
4. `statusId`/`stageId`/`priorityId`, se enviados, precisam pertencer à mesa
   (`400 "Prioridade não pertence à mesa."` etc.). Omitidos: status inicial e
   primeiro estágio da mesa; prioridade fica vazia (ou vem da matriz
   impacto×urgência, se enviada).
5. **Campos personalizados da mesa** (`Desk.customFields`, builder por mesa):
   os marcados `required` precisam vir em `customValues`, senão
   `400 "O campo "<label>" é obrigatório."`. **`GET /desks/:id` não devolve
   `customFields` e `/conversations/:id/ticket` não aceita `customValues`**:
   uma mesa de destino com campo personalizado obrigatório **recusa o ticket
   da Sofia** e só a tela de configuração da mesa mostra isso (§8).
6. Limites: `title` ≤ 200, `description` ≤ 20 000 (`dto`). A Sofia mantém os
   seus (120/4000), abaixo disso.

`POST /conversations/:id/ticket` (`tk.fromConversation` → `CS.convertToTicket`):

- Herda **`clientId` e `requesterId` da conversa** (por isso vincular o
  solicitante na conversa antes de abrir); `channel` vem do canal da conversa.
  `contactPhone` do ticket **não** é preenchido; o telefone está na conversa.
- `description` do corpo vai **acima** da transcrição (`period` default
  `session` = desde `sessionStartedAt`), separada por `\n\n---\n\n`.
- Grava `ticketId` na conversa e uma mensagem `SYSTEM` "🎫 Chamado #N aberto a
  partir desta conversa" (essa mensagem **não** vai ao WhatsApp: é interna da
  timeline).
- **Conversa que já tinha ticket: cria outro e substitui o vínculo, sem
  erro.** Só `/link-ticket` conflita (`409`) quando o ticket já está preso a
  **outra** conversa (`claimTicketConversation`). Dedup é inteiramente nosso
  (ADR 0005).
- `finish: true` → `CS.finish(id, "ON_FINISH_OPEN_TICKET", { TICKET_NUMBER,
  TICKET_TITLE })`: envia ao cliente a mensagem configurada no Desk **antes**
  de finalizar, pede avaliação (se a config de chat ativar) e grava
  `conversation.finished`.
- Resposta `201` com o objeto `ticket` público (`pay.ticketsWithNames`):
  `number` inteiro, `status`/`stage`/`priority` como `{ id, name, … }`,
  `conversationId`, `customValues`, datas de SLA. `GET /tickets/:number`
  acrescenta `messages`: até 200 mensagens **públicas** `{ id, sender,
  senderName, body, createdAt, conversationId }`.

`GET /tickets?phone=` casa o telefone pelo **solicitante**, pelo
`contactPhone` do ticket **ou** pela conversa vinculada (`tk.list`), em todas
as variantes BR (com/sem 9, com/sem `+55`). É a reconciliação do ADR 0005.

`POST /tickets/:number/close` passa por `TS.close`, que exige checklist
obrigatório concluído (`400` se houver item pendente). A Sofia não fecha na v1.

## 4. Conversa, bot externo e handoff

- **Quando o bot externo assume** (`CS`, ingestão de mensagem): só se a org
  tem chave ativa com `handlesConversations`; só em `channel == WHATSAPP` e
  `waGroupId == null`; no **início de sessão** (conversa nova, ou conversa
  `FINISHED` que recebe mensagem, ou primeira mensagem do cliente) **e** a
  conversa estava finalizada ou sem dono. Nesse momento `botActive = true`,
  `botFluxId = null`, e o chatbot interno não roda. Outros canais e grupos
  chegam com `handledBy` ∈ `QUEUE`/`AGENT`.
- `handledBy` (`pay.publicConversation`): `botActive && botFluxId` →
  `INTERNAL_BOT`; `botActive && !botFluxId` → `EXTERNAL_BOT`; senão
  `assignedUserId` → `AGENT`, senão `QUEUE`.
- **Quem tira o bot** (todos zeram `botActive`): atendente humano responde
  (`CS.addMessage` com `sender: AGENT` sem `keepBot`), `assign` (emite
  `conversation.assigned`), `handoff`. Mensagens da **nossa** chave usam
  `keepBot: true` e `senderName = nome da chave`.
- **`POST /conversations/:id/handoff`** (`api.handoff` → `CS.handoffFromExternalBot`):
  valida `departmentId`/`userId` (404 se inexistente); zera o bot; sem
  `userId`, `status = PENDING` (fila); grava a `note` como mensagem **interna**
  com `senderName` = nome da chave; aplica o departamento (emite
  `conversation.department_changed` se mudou); atribui ao `userId` ou tenta a
  distribuição automática da org; grava `conversation.handoff { note }`;
  notifica os atendentes (inbox, push, sino). **Não envia nada ao cliente.**
- **`POST /conversations/:id/bot { active }`** (`CS.setExternalBot`): só troca
  `botActive`, "sem aviso a ninguém" — nem evento, nem notificação. Não usar.
- **`POST /conversations/:id/finish`** pela API (`api.finishConversation`):
  `notify = null` → **não avisa o cliente**, não pede avaliação; grava
  `conversation.finished`.
- **Reabertura**: mensagem do cliente numa conversa `FINISHED` reabre **a
  mesma conversa** (mesmo `id`), `status → IN_PROGRESS`, `sessionStartedAt`
  renovado, e sai `conversation.started` antes do `message.received`.
- **Inatividade**: config de chat `finishOnInactivityWhatsapp` +
  `inactivityMinutesWhatsapp` finaliza a conversa parada (caminho
  `notify = null`, sem aviso). **Não existe timeout que devolva a conversa do
  bot externo à fila**; enquanto `botActive`, os atendentes não recebem
  som/push/sino. O vigia `BOT_TIMEOUT_HANDOFF` do gateway é obrigatório.
- **Fora do horário**: config de chat `blockChatsOffHours` e
  `widgetPreTicketOffHours`; `Organization.businessHours` guarda o horário.
  Valores da org Infominas: §8.
- **Janela de 24 h** (`api.windowOf`): calculada a partir da última mensagem
  `CLIENT`; `GET /conversations/:id` traz `whatsappWindow { open, expiresAt,
  lastInboundAt }`. Fora da janela, texto livre dá `409 WHATSAPP_WINDOW_CLOSED`.

## 5. Eventos e webhooks

- **Outbox** (`ev.record`): o evento é **gravado na mesma transação** do
  negócio (savepoint próprio: falha ao gravar não derruba a mensagem), com o
  `payload` montado na hora. Só grava se a org tem ao menos uma chave ativa.
  Entregas são criadas para cada chave que assina o tipo (`webhookEvents`).
- **Reentrega** (`dl`): backoff `30 s, 2 min, 10 min, 1 h, 6 h` (6 tentativas)
  → `FAILED`; sucesso = 2xx em 8 s. Reenvia **o mesmo JSON gravado**: o
  `conversation` de um evento reentregue é o estado **da época** (07, W9).
- **Assinatura**: `sha256=HMAC(secret, "<timestamp>.<corpo>")`, headers
  `X-Infominas-Event`, `X-Infominas-Delivery` (id da entrega, muda a cada
  tentativa), `X-Infominas-Timestamp`, `X-Infominas-Signature` (só se a chave
  tem segredo).
- **Nossas próprias chamadas geram evento** (07, W8): `/messages` →
  `message.sent` (`senderName` = nome da chave); `/finish` →
  `conversation.finished`; `/ticket` → `ticket.created`; `/handoff` →
  `conversation.handoff`; `/reply` → `message.sent` + `ticket.replied`.
- **Extras por evento** (`CS`, `TS`, `dl`): `conversation.assigned` **sem
  extras** (o `assignedUserId` novo está em `conversation`);
  `conversation.handoff { note }`; `conversation.department_changed` com os
  dados do departamento; `ticket.replied { reply: { body, messageId } }`;
  `message.status { status, error }`; `ping { message }` com `seq: 0` e
  `id: evt_ping_<ts>` (não passa pelo outbox).
- **`attachment`** (`pay.publicMessage`): `{ id, filename, mime, size, url }`,
  `url = <PUBLIC_API_URL>/integration/v1/attachments/<id>` (exige a chave).
- `GET /events?after=<seq>` (`api.events`): `id` interno bigint = `seq`;
  `{ data, nextAfter, hasMore }`, `limit` ≤ 500; filtros `types`,
  `conversationId`, `ticketNumber`.
- Retenção: eventos e entregas apagados após 30 dias (`escopo §4.1`).

## 6. Contatos

- `GET /contacts/lookup?phone=` (`tk.lookup`): `{ phone, requesters[],
  clientContacts[], clientIds[], openTickets[], conversation }`. Casa todas as
  variantes BR do número. `requesters` = **solicitantes** (`Requester`, quem
  abre chamado); `clientContacts` = **contatos do cadastro do cliente**
  (`ClientContact`: `{ id, name, email, phone, role, clientId, client }`), que
  **não** são solicitantes. `openTickets` cruza solicitante, `contactPhone` e
  conversa. `conversation` = a conversa WhatsApp individual mais recente do
  número.
- `POST /requesters` é idempotente por telefone/e-mail **dentro do mesmo
  cliente** (`created: false` quando já existe); grava E.164 com `+55`.
  Fora da v1 da Sofia (07, N3).
- Telefones saem sempre como E.164 com `+` (`pay.publicPhone`).

## 7. Como descobrir a configuração real: `scripts/descobrir-taxonomia.py`

Script **stdlib** (sem pip) que faz **somente `GET`** e grava um inventário
e um rascunho de `taxonomia.yaml` para a etapa E5.1:

```bash
export INFODESK_BASE_URL=https://infodesk.infominas.net/v1   # ou .../v1/integration/v1
export INFODESK_API_KEY=ik_live_...                           # nunca no git nem no chat
python scripts/descobrir-taxonomia.py --saida descoberta-infodesk/
```

Contra o mock (`scripts/start-all.sh` ou só o mock):

```bash
INFODESK_BASE_URL=http://localhost:9000/api INFODESK_API_KEY=ik_test_mock \
  python scripts/descobrir-taxonomia.py --saida descoberta-infodesk/
```

O que ele faz, nesta ordem: `GET /me` (valida chave, mostra org, escopos e se
a chave "conduz o atendimento") → `GET /desks` → `GET /desks/:id` de cada
mesa → `GET /catalog` (todos os itens) → `GET /departments`. Entre chamadas
dorme 100 ms (rate limit). Saídas, na pasta `--saida` (gitignored por
padrão):

| Arquivo | Conteúdo |
|---|---|
| `inventario.json` | resposta crua das rotas, para anexar ao PR da E5.1 |
| `inventario.md` | tabelas legíveis: mesas com prioridades/status/catálogo, itens de catálogo com mesa padrão, departamentos, e a **matriz prioridade × mesa** que mostra onde "Baixa/Média/Alta/Urgente" não existem |
| `taxonomia.rascunho.yaml` | rascunho no formato de `config/taxonomia.yaml`: uma categoria por item de catálogo que tenha mesa (via `defaultDeskId` ou por pertencer ao catálogo da mesa), com `mesa` e `catalogo` já com os **nomes exatos**; `departamento`, `prioridade`, `exemplos` e `nao_e` marcados `TODO` para a Infominas; `indefinida`, `mesa_triagem` e `departamento_humano_padrao` sugeridos |

O rascunho **não substitui** `config/taxonomia.yaml` sozinho: as categorias
precisam ser revistas contra o que o cliente final escreve no WhatsApp (os
itens de catálogo são a visão do técnico, não a do cliente), os `TODO`
preenchidos e `placeholder: true` removido — critério de pronto da E5.1.

## 8. O que o código NÃO responde (perguntar à Infominas)

| # | Pergunta | Onde se vê no Desk | Impacto |
|---|---|---|---|
| 1 | Chave de API para a Sofia com escopos `tickets:*`, `conversations:*`, `contacts:read`, `events:read` e a flag **"Conduz o atendimento (bot externo)"**; segredo `whsec_` | *Configurações › Integrações* | tudo; sem ela o script do §7 não roda |
| 2 | O toggle **"Exigir campos na abertura do chamado"** está ligado? | *Configurações* (org) | T10: se ligado, a Sofia precisa de cliente, solicitante, item de catálogo e prioridade em todo ticket — e cliente não identificado **não consegue abrir** |
| 3 | Alguma mesa de destino tem **campo personalizado obrigatório**? | configuração de cada mesa (builder de campos) | T10: a API recusa o ticket sem avisar antes |
| 4 | Quais **mesas** recebem chamado vindo do WhatsApp de cliente externo (Incidente Externo? Solicitações Externo? Abertura de Ticket?) e qual **catálogo** vale onde há par `X` / `X_New` | 09 + saída do script | D1 / P1: categorias e `mesa` da taxonomia |
| 5 | Para cada categoria, qual **departamento de chat** recebe o handoff, e qual é o **departamento humano padrão** | *Grupos de serviço* / saída do script | D6 / P11 |
| 6 | Os nomes de prioridade existem em **todas** as mesas de destino? (a matriz do `inventario.md` mostra) | saída do script | P4 |
| 7 | Encerrar a conversa ao abrir o ticket (`finish: true`, com a mensagem configurada e pedido de avaliação) ou manter aberta + handoff? | decisão de produto | C6 / `TICKET_FINISH_CONVERSA` |
| 8 | Valores de **inatividade do WhatsApp** (`finishOnInactivityWhatsapp`, minutos) e de **horário** (`blockChatsOffHours`, `businessHours`) | *Configurações de chat* | N1 / P7 |
| 9 | Existe **ambiente de homologação** com chave própria? | — | A1: hoje tudo é produção |
| 10 | Política de identificação: telefone conhecido dispensa CPF? cadastrar desconhecido via `POST /requesters`? | decisão de produto | N3 / E5.3 |
