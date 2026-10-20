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

## A — Acesso e ambiente

| # | Pergunta | Impacto | Hipótese de trabalho / resposta | Estado |
|---|---|---|---|---|
| A1 | Existe **ambiente de homologação/sandbox** (base URL + chave de teste) ou só produção? | Sem sandbox, todos os testes de integração batem em produção | Testes de integração só contra `mock-infodesk/`; smoke em produção com número de teste da Infominas. O PDF v1 continua citando só produção | ABERTO |
| A2 | Quem configura webhook/segredo na chave? | Runbook de deploy | **Resposta:** administrador do Desk em *Configurações › Integrações*; chave e `whsec_` mostrados uma vez; *Editar*, *Rotacionar segredo*, *Revogar*, *Enviar teste*, *Entregas* na mesma tela (01). Chaves diferentes por ambiente dependem de A1 | RESPONDIDO (2026-10-19) |
| A3 | Os endpoints de referência exigem escopo? | Falta de escopo | **Resposta:** valem para qualquer chave válida (01) | RESPONDIDO (2026-10-19) |
| A4 | Limites do **rate limit** e `Retry-After` | Dimensionar polling e retries | **Resposta parcial:** 300 req/min por IP (`429 RATE_LIMITED`). `Retry-After` **não citado** → backoff exponencial, teto 30 s | RESPONDIDO parcial (2026-10-19) |
| A5 | IP de origem fixo dos webhooks (allowlist)? | Infra do endpoint | Só HMAC + timestamp + HTTPS público | ABERTO |
| A6 | **Base URL**: a capa do PDF v1 traz `https://infodesk.infominas.net/v1/integration/v1` (`v1` duplicado); o guia anterior trazia `https://infodesk.infominas.net/api/integration/v1`; o PDF manda copiar da tela de Integrações | Toda chamada | `INFODESK_BASE_URL` configurável, sem default fixo em código; valor real copiado da tela de Integrações pela Infominas; `GET /me` no boot valida | ABERTO |

## T — Tickets

| # | Pergunta | Impacto | Hipótese de trabalho / resposta | Estado |
|---|---|---|---|---|
| T1 | Schema da resposta de `POST /tickets` e `GET /tickets/:number`; tipo de `number` | Tipar o cliente Go | **Resposta:** objeto completo em 03; `number` é **inteiro**; `status` é objeto por mesa com `isOpen/isClose/isCanceled`; HTTP de sucesso ainda não citado (aceitar 200/201) | RESPONDIDO (2026-10-19) |
| T2 | Valores de `channel` | 4xx | **Resposta:** `WHATSAPP`, `WEB`, `EMAIL`, `CHAT`, `AGENT` | RESPONDIDO (2026-10-19) |
| T3 | Como obter `priorityId` | Prioridade determinística no ticket | **Resposta:** `GET /desks/:id.priorities` (por mesa). Taxonomia mapeia nome → UUID resolvido na mesa de destino (02) | RESPONDIDO (2026-10-19) |
| T4 | O que são `clientId`/`requesterId` e como obtê-los | Identificação | **Resposta:** cliente = empresa; solicitante = pessoa do cliente. `GET /contacts/lookup?phone=`, `/clients`, `/requesters`, `POST /requesters` (04). Se omitidos, o chamado fica sem vínculo (só `contactPhone`); "se a organização exige, vira 400" (T10) | RESPONDIDO (2026-10-19) |
| T5 | Listar tickets por contato/telefone (dedup, "já tenho chamado?") | Dedup | **Resposta:** `GET /tickets?phone=&status=open`, `lookup.openTickets`, `/requesters/:id.openTickets` (03/04). Dedup local continua (ADR 0005), agora com reconciliação por listagem | RESPONDIDO (2026-10-19) |
| T6 | Rota de fechamento; o agente fecha? | Encerramento | **Resposta parcial:** `POST /tickets/:number/close` existe e "respeita checklist/regras da mesa". **Se a Sofia deve fechar** continua regra de negócio pendente → v1 só comenta (`/notes`) e deixa ao humano | RESPONDIDO parcial (2026-10-19) |
| T7 | `/reply` chega na mesma conversa de WhatsApp do bot? | Mensagem duplicada | Não explicitado ("conforme o chamado"). v1 **não usa `/reply`**; fala pela conversa | ABERTO |
| T8 | Campo de origem/externalId para carimbar o bot | Auditoria | **Resposta:** `conversationId` no objeto `ticket`; `channel: WHATSAPP`; abrir via `POST /conversations/:id/ticket` já vincula. Rodapé na descrição vira opcional | RESPONDIDO (2026-10-19) |
| T9 | Limite de tamanho de `title`/`description`/`text` | 4xx | `title` ≤ 120, `description` ≤ 4000, `text` ≤ 900 por bolha (herdado) | ABERTO |
| T10 | **Quais campos a organização Infominas exige** em `POST /tickets` (cliente? solicitante? serviço/`catalogItemId`? prioridade?) e por mesa | `400 VALIDATION_ERROR` em produção | Enviar sempre `deskId`, `title`, `description`, `channel`, `contactPhone`, `priorityId`; `requesterId`/`clientId` quando o `lookup` identificar; `catalogItemId` se a taxonomia mapear. Validar em homologação (A1) | ABERTO |
| T11 | `POST /conversations/:id/ticket`: o chamado herda `clientId`/`requesterId`/`contactPhone` da conversa? Aceita `channel`? Devolve o objeto `ticket`? | Escolher entre `/tickets` e `/conversations/:id/ticket` | Assumir que herda os vínculos da conversa e devolve `ticket`; vincular solicitante na conversa **antes** de abrir (como no fluxo de referência) | ABERTO |
| T12 | `POST /conversations/:id/ticket` quando a conversa **já tem** `ticketNumber`: cria outro, erro `CONFLICT`, ou substitui? | Dedup | Checar `conversation.ticketNumber` local antes; se existir, usar `/link-ticket` + `/notes` | ABERTO |
| T13 | Formato de `messages` em `GET /tickets/:number` e corpo de `POST /alerts` | Tipagem | `messages` como bruto; `/alerts` não usado | ABERTO |

## C — Conversas

| # | Pergunta | Impacto | Hipótese de trabalho / resposta | Estado |
|---|---|---|---|---|
| C1 | Schema de `GET /conversations`, paginação, `tab` | Reconciliador | **Resposta:** objeto `conversation` (04), filtros completos, paginação por cursor; `tab`: `entrada` = sem dono, `abertas`, `encerrados`, `todas`. O reconciliador deve usar **`GET /events?after=`** (05) em vez de varrer conversas | RESPONDIDO (2026-10-19) |
| C2 | Schema de mensagens; valores de `sender` | Contexto | **Resposta:** objeto `message` (04); `sender` ∈ `CLIENT`, `AGENT`, `SYSTEM`; `recentMessages` (50) em `GET /conversations/:id`; `GET /conversations/:id/messages?after=` | RESPONDIDO (2026-10-19) |
| C3 | **Como saber que um humano assumiu** | Bot responde por cima do humano | **Resposta:** `conversation.handledBy`/`botActive` em todo evento de mensagem + evento `conversation.assigned`; "um atendente pode assumir a qualquer momento (isso tira o bot)". Gateway cala quando `handledBy != EXTERNAL_BOT` (08) | RESPONDIDO (2026-10-19) |
| C4 | Transferir para humano = `/department` ou `/assign`? Existe fila? | Tool de roteamento | **Resposta:** `POST /conversations/:id/handoff { departmentId?, userId?, note }` — volta para a fila (ou `userId`, ou distribuição automática), `note` vira nota interna, atendentes notificados (08). `/department` muda fila sem soltar o bot | RESPONDIDO (2026-10-19) |
| C5 | `/finish` dispara `conversation.finished` para nós? Nova mensagem reabre ou cria outra? | Loop; ciclo de vida | **Resposta parcial:** `finished.reason` ∈ `inactivity`, `bot`, ausente (atendente/API) — o valor `bot` sugere que o nosso `/finish` gera evento. Nova sessão "após 24 h ou encerramento" = `conversation.started` (mesmo `id`? **não dito**). Tratar `finished` como idempotente; estado local por `conversation.id` + `sessionStartedAt` | RESPONDIDO parcial (2026-10-19) |
| C6 | O bot encerra a conversa depois de abrir o ticket? | Regra de negócio | Opções agora: `/conversations/:id/ticket { finish: true }` (cliente recebe mensagem configurada com o número) ou manter aberta + `handoff`. **Decisão de negócio pendente**; v1: `finish: false` + handoff | ABERTO |
| C7 | Limite de `text`; markdown/negrito/quebra de linha | Formatação | 900 chars por bolha, bolhas separadas; `*negrito*` do WhatsApp — confirmar se o Desk reescreve | ABERTO |
| C8 | Autor das mensagens do bot no painel | UX do atendente | **Resposta:** o **nome da chave** (`senderName`) — pedir chave chamada "Sofia" | RESPONDIDO (2026-10-19) |
| C9 | Formato de `message.attachment` (além de `url`: `mimeType`? `filename`? `size`? `id` para `/attachments/:id`?) | Mídia | Não baixar anexos na v1; `type != TEXT` ou `attachment != null` → mídia | ABERTO |
| C10 | `/assign` "avisa o cliente conforme a config de chat" — qual mensagem? E o `handoff` avisa? | UX | v1 não usa `/assign`; a Sofia avisa o cliente **antes** do handoff com a mensagem dela | ABERTO |

## W — Webhooks

| # | Pergunta | Impacto | Hipótese de trabalho / resposta | Estado |
|---|---|---|---|---|
| W1 | Payload de `conversation.started`/`finished` | Saudação, fim | **Resposta:** `{ conversation, contact, …extras }`; `finished.reason` (05) | RESPONDIDO (2026-10-19) |
| W2 | `message.received` dispara para mensagens do atendente ou da API? | Loop | **Resposta:** não — essas vêm em `message.sent` (`sender: AGENT`, `senderName` = nome da chave ou do atendente) | RESPONDIDO (2026-10-19) |
| W3 | Mídia no webhook | Transferência por anexo | **Resposta:** `message.type` ∈ `IMAGE/FILE/AUDIO/CONTACT` + `attachment.url`; duas mensagens por mídia no WhatsApp (05) | RESPONDIDO (2026-10-19) |
| W4 | Formato dos ids; estabilidade em reentrega | Dedup | **Resposta:** `id` do evento (`evt_…`) é estável entre reentregas; `X-Infominas-Delivery` muda. Dedup por `id` do evento, 7 dias | RESPONDIDO (2026-10-19) |
| W5 | `organizationId` fixo por chave? | Multi-tenant | **Resposta:** `GET /me.organizationId` (01) | RESPONDIDO (2026-10-19) |
| W6 | `webhookSecret` = chave de API? Rotação? | Runbook | **Resposta:** segredo separado `whsec_…`, mostrado uma vez, rotacionável sem trocar a chave (01) | RESPONDIDO (2026-10-19) |
| W7 | Campos exatos dos extras de `conversation.assigned` (`userId`? `previousUserId`?), `handoff` (`note` só?) e `data` do `ping` | Tipagem | Tipar só `conversation`/`contact`; extras como bruto; `ping` → 200 sem parse | ABERTO |
| W8 | Eventos gerados pelas **nossas próprias** chamadas chegam ao nosso webhook (`message.sent` da nossa mensagem, `finished` do nosso `/finish`, `ticket.created` do nosso ticket)? | Loop/dedup | Assumir que sim (o quadro diz "esta API"); ignorar `message.sent` com `senderName` = nome da chave; `ticket.created` com `number` já conhecido = no-op | ABERTO |
| W9 | Durante uma **reentrega** (até 6 h depois), o `conversation` do envelope reflete o estado **da época** ou o atual? | Decisão "bot ainda conduz?" | Não confiar no `handledBy` de evento velho: comparar `createdAt` com o estado local; em dúvida, `GET /conversations/:id` | ABERTO |

## D — Dados de referência e roteamento

| # | Pergunta | Impacto | Hipótese de trabalho / resposta | Estado |
|---|---|---|---|---|
| D1 | Relação **mesa × departamento**; para cada categoria, qual mesa (chamado) **e** qual departamento (handoff)? | Tabela de roteamento | Continua **sem vínculo documentado** (02). Inventário real em 09: 15 mesas, 15 catálogos, departamentos **ainda não observados**. Tabela nossa `taxonomia.yaml` (`categoria → {mesa, catalogo?, departamento, prioridade}`) validada no boot | ABERTO (regra de negócio) |
| D2 | Frequência de mudança; cache | TTL | TTL 10 min + reload no boot; `INVALID_REFERENCE` força reload | ABERTO (baixo impacto) |
| D3 | `/desks`, `/users`, `/departments`, `/catalog` são paginados (`{ data, nextCursor }`) ou array puro? | Cliente Go | Aceitar as duas formas | ABERTO |
| D4 | Valores de `users.type` | Filtrar bots internos | Bruto; não usado na v1 | ABERTO |
| D5 | Campos de `catalogItems`/`/catalog` e relação catálogo × mesa (os 15 catálogos de 09 não têm mesa visível) | `catalogItemId` | Não enviar `catalogItemId` até o export P1 dizer qual item por categoria | ABERTO |
| D6 | **Departamentos de chat** da Infominas (nomes) — a tela de Departamentos ainda não foi capturada | Destino do handoff | `DEPARTAMENTO_HUMANO_PADRAO` configurável; pedir a lista (ou `GET /departments` com a chave) | ABERTO |

## N — Novos conceitos (bot externo, identificação)

| # | Pergunta | Impacto | Hipótese de trabalho | Estado |
|---|---|---|---|---|
| N1 | Com **Conduz o atendimento** ligado, o que acontece com conversas que o bot **não responde** (ex.: gateway fora do ar por 1 h)? Ficam com selo Bot sem ninguém ver? Há timeout que devolve à fila? | Cliente sem resposta | O gateway, ao detectar falha definitiva do cérebro, faz `handoff` com nota de erro (já previsto); **vigia** local: conversa com `EXTERNAL_BOT` sem resposta nossa em `BOT_TIMEOUT_HANDOFF` (5 min) → handoff automático. Perguntar se o Desk tem timeout próprio | ABERTO |
| N2 | Formato de `lookup.clientContacts` e diferença para `requesters` | Identificação | Ignorar `clientContacts` na v1 | ABERTO |
| N3 | **Política de identificação** (ADR 0004): com `GET /contacts/lookup?phone=` disponível, telefone conhecido dispensa CPF? Telefone desconhecido → pedir CPF (atual) ou nome+empresa + `POST /requesters` (fluxo do PDF)? | ADR 0004, tools A1/A2 | Manter ADR 0004 (CPF/contrato validado por código) **acrescentando** o `lookup` como primeira tentativa (`IDENT_EXIGIR_CPF=false` quando o lookup resolve). Cadastro via `POST /requesters` **não** na v1 (cria dado no Infodesk a partir do que o cliente disse) | ABERTO (decisão de plano) |
| N4 | `POST /conversations/:id/bot { active: false }` vs `handoff`: o `/bot` solta sem notificar — a conversa fica em `QUEUE` sem ninguém avisado? | Uso indevido | Nunca usar `/bot { active:false }`; só `handoff` | ABERTO |
| N5 | Chat do agente desktop e portal geram `message.received` mas "não passam pelo bot" — chegam com `handledBy` ≠ `EXTERNAL_BOT`? | Filtro do gateway | Filtrar por `channel == WHATSAPP` **e** `handledBy == EXTERNAL_BOT` **e** `groupJid == null` | ABERTO |

## Como usar este arquivo

- Perguntas para a Infominas: enviar as linhas `ABERTO` de uma vez, pedindo
  exemplos de JSON reais onde falta (`/desks/:id`, `/catalog`, `/departments`,
  `conversation.assigned`, `attachment`).
- Ao receber resposta: atualizar o doc da área (01–06, 08), marcar
  `RESPONDIDO (AAAA-MM-DD)` aqui e abrir/ajustar a etapa correspondente em
  `docs/plano/03-etapas.md`.
