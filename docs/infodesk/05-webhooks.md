# 05 — Eventos e webhooks

Fonte: PDF v1 (2026-10-19), seção "5. Eventos e webhooks" e "12. Boas
práticas". Configure a URL do webhook e os eventos **na chave** (01); o Desk
faz `POST` JSON assinado ao endpoint quando os eventos ocorrem.

## 5.1 Como funciona

- Cada acontecimento vira um **evento gravado no Desk, na mesma transação do
  fato** (não se perde), com número sequencial **`seq`**.
- Sucesso = **qualquer 2xx em até 8 s**. Responder rápido e processar de forma
  assíncrona.
- **Reentrega automática** em caso de falha: após **30 s, 2 min, 10 min, 1 h e
  6 h**. Depois disso a entrega fica como *Falhou* (reentregável na tela ou por
  `POST /deliveries/:id/redeliver`).
- **Pelo menos uma vez**: o mesmo evento pode chegar mais de uma vez —
  **deduplicar pelo `id`**.
- **Ordem não garantida** entre reentregas: ordenar por `seq` se precisar.
- **Retenção: 30 dias.**

## 5.2 Eventos

| Evento | Quando |
|---|---|
| `message.received` | mensagem do cliente: WhatsApp (individual e grupo), chat do agente desktop, portal |
| `message.sent` | mensagem ao cliente: atendente, bot interno, **esta API**, eco do app do WhatsApp, resposta do chamado |
| `message.status` | entrega no WhatsApp: `sent` → `delivered` → `read`, ou `failed` |
| `conversation.started` | atendimento novo (1ª mensagem ou nova sessão após 24 h/encerramento) |
| `conversation.assigned` | conversa atribuída/transferida a um atendente |
| `conversation.department_changed` | departamento alterado (`previousDepartmentId`) |
| `conversation.handoff` | o bot externo passou a conversa para humanos (`note`) |
| `conversation.finished` | conversa encerrada (`reason`: `inactivity`, `bot`, ou ausente = atendente/API) |
| `ticket.created` | chamado aberto (qualquer origem) |
| `ticket.updated` | campos alterados (`changes`: lista de campos) ou transição (`change`) |
| `ticket.replied` | resposta pública ao solicitante (`reply.body`) |
| `ticket.note_added` | nota interna (`note.body`) |
| `ticket.closed` / `ticket.reopened` / `ticket.canceled` | transições |
| `ping` | teste ("Enviar teste" / `POST /webhook/test`) |

## 5.3 Envelope

```json
{
  "id": "evt_000000000123",
  "seq": 123,
  "event": "message.received",
  "createdAt": "2026-10-19T12:00:00.000Z",
  "organizationId": "8f0c…",
  "data": { … }
}
```

| Campo | Observação |
|---|---|
| `id` | id do **evento** (`evt_…`) — chave de **deduplicação** (é o mesmo em reentregas) |
| `seq` | sequencial crescente — ordenação e cursor do feed `GET /events?after=` |
| `event` | nome (também no header `X-Infominas-Event`) |
| `createdAt` | ISO-8601 UTC |
| `organizationId` | validar contra o `organizationId` de `GET /me` |

### `data` dos eventos de mensagem (`message.received`, `message.sent`, `message.status`)

```json
{
  "conversation": { "id": "c1d2…", "channel": "WHATSAPP", "status": "PENDING", "subject": null,
                    "botActive": true, "handledBy": "EXTERNAL_BOT",
                    "assignedUserId": null, "departmentId": null, "clientId": null, "requesterId": null,
                    "resourceId": null, "ticketNumber": null,
                    "contact": { "name": "Maria", "phone": "+5531990000000", "email": null, "groupJid": null },
                    "provider": "GUPSHUP", "lastMessageAt": "…", "lastMessagePreview": "Bom dia",
                    "unreadCount": 1, "sessionStartedAt": "…", "createdAt": "…", "updatedAt": "…" },
  "contact": { "name": "Maria", "phone": "+5531990000000", "email": null, "groupJid": null },
  "message": { "id": "m9…", "conversationId": "c1d2…", "sender": "CLIENT", "senderName": null,
               "type": "TEXT", "status": null, "body": "Bom dia, minha impressora parou",
               "internal": false, "deleted": false, "replyToId": null, "createdAt": "…",
               "attachment": null }
}
```

- `handledBy`: `EXTERNAL_BOT` (bot externo conduz), `INTERNAL_BOT` (chatbot do
  Desk), `AGENT` (atendente), `QUEUE` (na fila).
- `sender`: `CLIENT`, `AGENT` (atendente, bot ou integração) ou `SYSTEM`.
- `type`: `TEXT`, `IMAGE`, `FILE`, `AUDIO` ou `CONTACT`. Mídia recebida chega
  com `attachment.url`; baixar com a chave (`GET /attachments/:id`). No
  WhatsApp, a mídia gera **duas mensagens**: o texto/legenda (ou rótulo
  `"[imagem]"`) e o arquivo.
- `message.status` traz também `"status": "sent|delivered|read|failed"` e
  `"error": { "code", "detail" } | null` (falha do provedor depois do envio:
  `error.code: "PROVIDER_ERROR"`).

### `data` dos demais eventos

- Eventos de **conversa**: `{ conversation, contact, …extras }` — extras
  citados: `previousDepartmentId` (`department_changed`), `note` (`handoff`),
  `reason` (`finished`). Campos exatos dos extras e de `assigned`
  (`userId`? `previousUserId`?): **NÃO DOCUMENTADOS** (07, W7).
- Eventos de **chamado**: `{ ticket, …extras }` — `ticket` é o objeto da seção
  9 (03); extras: `changes`/`change` (`updated`), `reply.body` (`replied`),
  `note.body` (`note_added`).
- `ping`: `data` **NÃO DOCUMENTADO**; só responder 2xx.

## 5.4 Verificar a assinatura

Headers de cada entrega:

```http
X-Infominas-Event: message.received
X-Infominas-Delivery: 5b0e…           (id da entrega)
X-Infominas-Timestamp: 1760875200     (segundos Unix)
X-Infominas-Signature: sha256=<hex>
```

**Assinatura = `HMAC-SHA256(webhookSecret, "<timestamp>.<corpo bruto>")`**,
em hex, com prefixo `sha256=`. Validar sobre o **corpo bruto** (antes de
qualquer parse) e **recusar timestamps com mais de 5 minutos** de diferença
(proteção contra replay). Comparação em tempo constante.

> **Mudança em relação ao guia anterior:** antes era `HMAC(secret, corpo)` sem
> timestamp. Código escrito sobre o guia antigo (E1.2 `assinatura.go`,
> E1.4 `mock_infodesk.py`) **precisa mudar**.

Exemplo Python do PDF (FastAPI), resumido:

```python
raw = await request.body()
ts = request.headers.get("X-Infominas-Timestamp", "0")
sig = request.headers.get("X-Infominas-Signature", "")
expected = "sha256=" + hmac.new(SECRET, f"{ts}.".encode() + raw, hashlib.sha256).hexdigest()
if abs(time.time() - int(ts)) > 300 or not hmac.compare_digest(sig, expected):
    return Response(status_code=401)
enqueue(raw)   # processe de forma assíncrona; deduplique por id
return Response(status_code=200)
```

`X-Infominas-Delivery` é o id da **entrega** (muda a cada reentrega);
`id` do envelope é o do **evento** (não muda). Dedup pelo `id` do evento.

## 5.5 Retroativo: nunca perder nada

| Rota | Escopo | Uso |
|---|---|---|
| `GET /events?after=<seq>&limit=&types=&conversationId=&ticketNumber=` | `events:read` | feed de eventos em ordem. Guardar o maior `seq` processado e pedir `after=<seq>` |
| `GET /messages?since=<ISO>&channel=&limit=&cursor=` | `conversations:read` | todas as mensagens desde um instante (máx. 500/página) |
| `GET /conversations/:id/messages?after=<messageId>&limit=` | `conversations:read` | mensagens de uma conversa em ordem cronológica |
| `GET /deliveries?status=FAILED` | `events:read` | entregas de webhook desta chave |
| `POST /deliveries/:id/redeliver` | `events:read` | recoloca a entrega na fila |
| `POST /webhook/test` | — | envia um `ping` assinado e devolve `{ ok, statusCode, error, durationMs }` |

Resposta do feed (formato próprio, **não** o `{ data, nextCursor }` geral):

```bash
curl -s "$BASE/events?after=$ULTIMO_SEQ&limit=500" -H "Authorization: Bearer $KEY"
# → { "data": [ {envelope}, … ], "nextAfter": 1290, "hasMore": false }
```

"O webhook e o feed entregam o **mesmo envelope**, então o mesmo handler serve
para os dois." Recomendação: reconciliar **a cada minuto e ao subir o bot**.

## Implicações para o nosso lado

- O receptor de webhook é o **gateway Go**: valida HMAC com timestamp (janela
  5 min, relógio injetável `Now`), dedup por `id` do evento (**7 dias** cobre
  as reentregas de até ~7,5 h e a reentrega manual), valida `organizationId`,
  enfileira por `conversation.id` e responde `200` em milissegundos.
- **Só processar como turno do cliente** quando: `event == message.received`
  **e** `message.sender == CLIENT` **e** `conversation.handledBy ==
  EXTERNAL_BOT` **e** `conversation.channel == WHATSAPP` **e**
  `contact.groupJid == null`. Tudo o mais: atualizar estado local e logar.
- `conversation.assigned` / `handledBy != EXTERNAL_BOT` → marcar
  `transferida` localmente (humano assumiu, com ou sem handoff nosso).
- `conversation.finished` → fim de episódio (idempotente; `reason` só para
  métrica). `conversation.started` → só log (a saudação vem na 1ª
  `message.received`).
- `message.sent` com `senderName == <nome da nossa chave>` = eco das nossas
  mensagens → ignorar (dedup natural); de atendente → opcional: registrar no
  histórico local para contexto.
- `ticket.*` → atualizar `tickets.status` local (resolve a lacuna de
  "não sei se o humano fechou").
- **Reconciliador** (E3.3) deixa de ser bloqueado: `GET /events?after=<seq>`
  com o `seq` persistido no store; mesmo handler do webhook. `RECONCILIA_ATIVO`
  pode nascer ligado.
- Eventos a assinar na chave (mínimo v1): `message.received`,
  `conversation.started`, `conversation.assigned`, `conversation.handoff`,
  `conversation.finished`, `ticket.created`, `ticket.updated`, `ticket.closed`,
  `ticket.reopened`, `ticket.canceled`, `ping`. `message.sent`/`message.status`
  opcionais (volume alto; úteis para métrica de entrega).
