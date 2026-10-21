# 04 — Conversas, mensagens, WhatsApp, contatos e solicitantes

Fonte: PDF v1 (2026-10-19), seções "6. WhatsApp e conversas" e "8. Contatos e
solicitantes". Uma *conversa* é a sessão de atendimento de um contato
(WhatsApp individual, grupo de WhatsApp, chat do agente desktop, portal,
e-mail). É por aqui que a Sofia **fala com o cliente**, **identifica quem
é**, **passa para humano** (ver [08-bot-externo.md](08-bot-externo.md)) e
**encerra**.

## Objeto `conversation` (literal do envelope de webhook, 05)

```json
{
  "id": "c1d2…", "channel": "WHATSAPP", "status": "PENDING", "subject": null,
  "botActive": true, "handledBy": "EXTERNAL_BOT",
  "assignedUserId": null, "departmentId": null, "clientId": null, "requesterId": null,
  "resourceId": null, "ticketNumber": null,
  "contact": { "name": "Maria", "phone": "+5531990000000", "email": null, "groupJid": null },
  "provider": "GUPSHUP", "lastMessageAt": "…", "lastMessagePreview": "Bom dia",
  "unreadCount": 1, "sessionStartedAt": "…", "createdAt": "…", "updatedAt": "…"
}
```

| Campo | Valores / observação |
|---|---|
| `channel` | `WHATSAPP`, `CHAT`, `WEB`, `EMAIL` (filtros de `GET /conversations`) |
| `status` | `PENDING`, `IN_PROGRESS`, `FINISHED` |
| `handledBy` | `EXTERNAL_BOT` (nosso bot), `INTERNAL_BOT` (chatbot do Desk), `AGENT` (atendente), `QUEUE` (na fila) |
| `botActive` | `true` enquanto o bot externo conduz |
| `assignedUserId` / `departmentId` | atendente dono / departamento; `null` = nenhum |
| `clientId` / `requesterId` | vínculos de cliente/solicitante (04, seção contatos) |
| `ticketNumber` | chamado vinculado (via `/ticket` ou `/link-ticket`) |
| `contact.groupJid` | não nulo em grupo de WhatsApp (grupos **não passam pelo bot**) |
| `provider` | `GUPSHUP` observado; Evolution/Meta também existem — irrelevante para a integração |
| `sessionStartedAt` | início da sessão atual (nova sessão após 24 h ou encerramento) |

## Objeto `message`

```json
{ "id": "m9…", "conversationId": "c1d2…", "sender": "CLIENT", "senderName": null,
  "type": "TEXT", "status": null, "body": "Bom dia, minha impressora parou",
  "internal": false, "deleted": false, "replyToId": null, "createdAt": "…",
  "attachment": null }
```

| Campo | Valores / observação |
|---|---|
| `sender` | `CLIENT`, `AGENT` (atendente, bot **ou integração**), `SYSTEM` |
| `senderName` | nome do autor (a nossa chave aparece aqui, ex.: "Bot de atendimento") |
| `type` | `TEXT`, `IMAGE`, `FILE`, `AUDIO`, `CONTACT` |
| `status` | em mensagens enviadas no WhatsApp: `queued` → `sent` → `delivered` → `read`, ou `failed` |
| `internal` | `true` = nota interna (só a equipe vê) |
| `attachment` | mídia recebida: `attachment.url`; baixar com a chave via `GET /attachments/:id`. **No WhatsApp a mídia gera duas mensagens**: o texto/legenda (ou um rótulo como `"[imagem]"`) e o arquivo |
| `replyToId` | mensagem citada |

Formato de `attachment` (campos além de `url`): **NÃO DOCUMENTADO** (07, C9).

## Ler e operar conversas

| Método | Rota | Escopo | Observação |
|---|---|---|---|
| GET | `/conversations` | `conversations:read` | filtros: `tab` (`entrada` = sem dono, `abertas`, `encerrados`, `todas`), `status` (`PENDING`/`IN_PROGRESS`/`FINISHED`), `channel`, `assignedUserId` (ou `none`), `departmentId`, `botActive`, `phone`, `updatedSince`, `limit`, `cursor` |
| GET | `/conversations/:id` | `conversations:read` | conversa + `whatsappWindow` + `recentMessages` (últimas 50, sem notas internas) |
| GET | `/conversations/:id/messages?after=<messageId>&limit=` | `conversations:read` | mensagens em ordem cronológica (retroativo por conversa) |
| GET | `/messages?since=<ISO>&channel=&limit=&cursor=` | `conversations:read` | todas as mensagens desde um instante (máx. 500/página) |
| GET | `/attachments/:id` | `conversations:read` | baixa o arquivo de uma mensagem |
| POST | `/conversations/:id/messages` | `conversations:write` | envia (abaixo) |
| POST | `/conversations/:id/assign` `{ "userId" }` | `conversations:write` | atribui a um atendente (avisa o cliente conforme a config de chat) |
| POST | `/conversations/:id/department` `{ "departmentId" }` | `conversations:write` | transfere de departamento (`null` remove) |
| POST | `/conversations/:id/finish` | `conversations:write` | encerra (**sem mensagem ao cliente**) |
| POST | `/conversations/:id/client` `{ "id" }` | `conversations:write` | vincula cliente (`null` desfaz) |
| POST | `/conversations/:id/requester` `{ "id" }` | `conversations:write` | vincula solicitante (traz o cliente junto; `null` desfaz) |
| POST | `/conversations/:id/handoff` | `conversations:write` | passa para humanos — ver 08 |
| POST | `/conversations/:id/bot` `{ "active" }` | `conversations:write` | assume/solta sem avisar — ver 08 |
| POST | `/conversations/:id/ticket` · `/link-ticket` | ver 03 | chamado a partir da conversa |

Formato de `whatsappWindow` em `GET /conversations/:id`: presumivelmente o
mesmo de `GET /whatsapp/window` (abaixo) — **não explicitado**.

## Enviar mensagem numa conversa — `POST /conversations/:id/messages`

Mesmo corpo de `POST /whatsapp/messages` **sem `to`**: um de `text`,
`template` (só WhatsApp) ou `media` (+ `caption`); opcionalmente `replyToId`.
Com `"internal": true` + `text` grava **nota interna**. Funciona em qualquer
canal; as regras de janela de 24 h valem só para WhatsApp individual.

```json
{ "text": "Olá! Posso ajudar?" }
```
```json
{ "template": { "name": "retorno_chamado", "language": "pt_BR", "params": ["Maria", "#4521"] } }
```
```json
{ "media": { "url": "https://arquivos.exemplo.com/boleto.pdf", "filename": "boleto.pdf", "caption": "Segue o boleto" } }
```
```json
{ "text": "Cliente confirmou que reiniciou o roteador.", "internal": true }
```

> **Atenção (mudança em relação ao guia anterior):** o campo é **`text`**,
> não `body`. O guia de 5 páginas mostrava `{ "body": "…" }`.

Resposta `201` (exemplo de `/whatsapp/messages`; presumido igual aqui):

```json
{ "conversationId": "c1d2…",
  "messages": [{ "id": "m9…", "sender": "AGENT", "senderName": "Bot de atendimento", "type": "TEXT",
                 "body": "Olá! Posso ajudar?", "status": "queued", "…": "…" }] }
```

"Mensagem enviada pela integração **não tira o bot de cena** e leva o nome da
chave como autor." O resultado de entrega chega depois por `message.status`.

Limite de tamanho de `text` e tratamento de markdown/quebra de linha: **NÃO
DOCUMENTADOS** (07, C7). `media.url` precisa ser pública e ≤ 16 MB
(`MEDIA_URL_REQUIRED` / `MEDIA_DOWNLOAD_FAILED`, 06).

## WhatsApp: janela de 24 h e templates

- O WhatsApp só aceita **texto livre ou mídia até 24 h depois da última
  mensagem do cliente**. Fora da janela, ou para um número que nunca falou,
  só **template aprovado**. O Desk aplica a regra para qualquer provedor e
  responde `409 WHATSAPP_WINDOW_CLOSED` (com `lastInboundAt`,
  `windowExpiresAt`).
- `GET /whatsapp/templates`: templates habilitados (ver 02).
- `GET /whatsapp/window?phone=+5531990000000` (`conversations:read`):

```json
{ "phone": "+5531990000000", "open": false, "expiresAt": null, "lastInboundAt": null, "conversationId": null }
```

- `POST /whatsapp/messages` (`whatsapp:send`): **abre ou reaproveita** a
  conversa de WhatsApp do número e envia. Corpo: `to` + um de `text`,
  `template`, `media`; opcionais `contactName`, `clientId`, `requesterId` e
  `bot` (`true` = a conversa fica com o bot externo; padrão = modo da chave).
  Resposta `201` como acima.

## Contatos e solicitantes

Conceitos: **cliente** (`client`, empresa: "ACME Ltda", busca por nome/razão
social ou CPF/CNPJ) e **solicitante** (`requester`, pessoa do cliente: nome,
e-mail, telefone, ramal, `clientId`). O contato da conversa (`contact`) é só
o perfil do WhatsApp.

| Método | Rota | Escopo | Observação |
|---|---|---|---|
| GET | `/contacts/lookup?phone=+5531990000000` | `contacts:read` | **identifica o telefone**: solicitantes, contatos de cliente, `clientIds`, chamados abertos e a conversa ativa do número |
| GET | `/clients?q=ACME` ou `?document=12.345.678/0001-90` | `contacts:read` | clientes por nome/razão social ou CPF/CNPJ |
| GET | `/requesters?phone=&email=&q=&clientId=` | `contacts:read` | busca solicitantes |
| GET | `/requesters/:id` | `contacts:read` | solicitante + cliente + `openTickets` |
| POST | `/requesters` | `contacts:write` | cadastra **sem duplicar** e, opcionalmente, já vincula |
| PATCH | `/requesters/:id` `{ "name?", "email?", "phone?", "extension?", "clientId?" }` | `contacts:write` | corrige o cadastro (`""` apaga e-mail/telefone/ramal) |
| POST | `/conversations/:id/requester` `{ "id" }` | `conversations:write` | vincula à conversa (`null` desfaz); o cliente vem junto |
| POST | `/tickets/:number/requester` `{ "id" }` | `tickets:write` | vincula ao chamado (`null` desfaz) |

### `GET /contacts/lookup?phone=` — resposta literal

```json
{
  "phone": "+5531990000000",
  "requesters": [{ "id": "r1…", "name": "Maria Souza", "email": "maria@acme.com.br", "phone": "+5531990000000",
                   "clientId": "k1…", "client": { "id": "k1…", "name": "ACME Ltda" } }],
  "clientContacts": [],
  "clientIds": ["k1…"],
  "openTickets": [ { "number": 4510, "title": "VPN não conecta", "…": "…" } ],
  "conversation": { "id": "c1d2…", "handledBy": "EXTERNAL_BOT", "…": "…" }
}
```

`clientContacts`: formato **NÃO DOCUMENTADO** (contatos de cliente que não
são solicitantes?) — 07, N2.

### `POST /requesters` — cadastrar e vincular numa chamada só

```json
{ "name": "Maria Souza", "clientId": "k1…", "phone": "+5531990000000", "email": "maria@acme.com.br",
  "conversationId": "c1d2…", "ticketNumber": 4521 }
```

Resposta `200` (o solicitante, com `created`):

```json
{ "id": "r1…", "name": "Maria Souza", "email": "maria@acme.com.br", "phone": "+5531990000000",
  "extension": null, "clientId": "k1…", "canOpenWhatsapp": true,
  "client": { "id": "k1…", "name": "ACME Ltda" }, "openTickets": 1, "created": true, "…": "…" }
```

**Idempotente**: se o cliente já tem alguém com o mesmo telefone (com/sem
`+55`, com/sem o 9º dígito) ou o mesmo e-mail, devolve esse cadastro com
`created: false`. `conversationId` e `ticketNumber` opcionais: com eles o
solicitante já sai vinculado (e o cliente é preenchido onde faltar).

Fluxo típico do PDF: `lookup` → não achou → pergunta nome e empresa →
`GET /clients?q=` → `POST /requesters` com `conversationId`.

## Implicações para o nosso lado

- O **`conversationId`** continua a chave de isolamento do agente
  (`thread_id`, fila FIFO por conversa, estado).
- **Humano assumiu** agora é detectável: `handledBy != EXTERNAL_BOT` /
  `botActive == false` na conversa (vem em todo evento de mensagem) e o
  evento `conversation.assigned`. Resolve 07, C3 — o gateway deve calar
  **também** quando um atendente assume sem o bot ter transferido.
- **Transferir para humano** na v1 = `POST /conversations/:id/handoff`
  (08), não `/department`. `/department` fica para mudar de fila **sem**
  soltar o bot; `/assign` só se a Infominas mapear filas a pessoas.
- **Identificação do cliente** (ADR 0004): `GET /contacts/lookup?phone=` é
  a rota de busca que o ADR previa como P2. Telefone conhecido → `requesterId`
  + `clientId` sem pedir CPF; desconhecido → fluxo atual (CPF/contrato por
  código) ou, se o plano adotar, cadastro via `POST /requesters`. Decisão de
  plano, não de implementação (07, N3).
- **Mídia**: `type != TEXT` ou `attachment != null` → regra atual (transferir
  com aviso) continua válida; não baixar anexos na v1. Atenção às **duas
  mensagens** por mídia no WhatsApp (o rótulo `"[imagem]"` é texto).
- **Canal**: só `channel == WHATSAPP` **e** `contact.groupJid == null` passa
  pelo bot; o resto o gateway ignora (loga).
- **Nota interna** (`internal: true`) é a forma de deixar o resumo da triagem
  visível só para a equipe sem poluir o WhatsApp — alternativa ao rodapé no
  ticket.
- `/whatsapp/messages`, templates e janela de 24 h **não entram na v1** (a
  Sofia só responde dentro de conversa ativa). Tratar `409
  WHATSAPP_WINDOW_CLOSED` como erro definitivo (cliente sumiu por > 24 h):
  encerrar episódio local.
