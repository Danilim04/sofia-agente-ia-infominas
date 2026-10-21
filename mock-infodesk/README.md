# mock-infodesk — Infodesk falso para testar a Sofia ponta a ponta

Servidor **stdlib Python 3.12+ (sem `pip install`)** que finge ser o Infodesk
(Infominas Desk) nos dois sentidos da integração, para exercitar o fluxo
`cliente (UI) → gateway Go → cérebro Python → resposta` sem WhatsApp nem
Infodesk reais. Segue a **API v1 de 2026-10-19** (`docs/infodesk/01–09`).

```
você (UI :9000) ──message.received (HMAC+timestamp)──►  gateway Go /webhook  ──►  cérebro /chat
      ▲                                                        │
      └──── POST /api/integration/v1/conversations/:id/messages ◄──┘  (Go "responde no Infodesk" = aqui)
```

- **Saída → gateway:** webhooks de `docs/infodesk/05` no envelope
  `{id: "evt_…", seq, event, createdAt, organizationId, data}`, assinados com
  `X-Infominas-Timestamp` (segundos Unix) e
  `X-Infominas-Signature: sha256=HMAC-SHA256(secret, "<timestamp>.<corpo bruto>")`,
  mais `X-Infominas-Delivery` (id da **entrega**, novo a cada tentativa) e
  `X-Infominas-Event`. O `id` do evento **não muda** em reentregas.
- **Entrada ← gateway:** a REST de `docs/infodesk/01–08` usada pela Sofia, com
  estado em memória e erros `{"statusCode", "code", "message"}` (`06`).
- **UI** em `http://localhost:9000`: você escreve como o cliente, vê as bolhas
  do bot (e as notas internas), os chamados abertos, o `handledBy` da conversa
  e os webhooks; faz o papel do atendente (**Atendente assume**, **Encerrar
  conversa**) e **reentrega o último evento** (teste de dedup).

Tudo que o PDF do Infodesk documenta está reproduzido como está. O que o PDF
**não** documenta está implementado como **hipótese de trabalho** (itens de
`docs/infodesk/07-pontos-ambiguos.md`, citados no código como
`# ver docs/infodesk/07 item X`), marcado abaixo e configurável quando faz
sentido. Quando a Infominas responder, ajuste o mock aqui e o doc da área
(skill `infodesk-api`).

## Como rodar

```bash
# sozinho (stdlib; no Windows use o python real, não o alias da Store)
python mock-infodesk/mock_infodesk.py            # UI em http://localhost:9000
MOCK_PORT=9001 MOCK_429_A_CADA=5 python mock-infodesk/mock_infodesk.py

# stack inteira (mock + cérebro + gateway já conectados)
scripts/start-all.sh
scripts/stop-all.sh

# testes (stdlib unittest; sobe o mock e um gateway falso em portas livres)
python -I mock-infodesk/mock_infodesk_test.py
```

Flags equivalentes às variáveis principais: `--port`, `--host`,
`--gateway-webhook`, `--api-key`, `--dados`.

## Variáveis de ambiente

| Variável | Default | Para quê |
|---|---|---|
| `MOCK_PORT` / `MOCK_HOST` | `9000` / `127.0.0.1` | porta/interface do servidor (`0.0.0.0` para docker; os endpoints `/mock/*` não têm autenticação) |
| `MOCK_API_KEY` | `ik_test_mock` | chave aceita em `Authorization: Bearer` **ou** `X-Api-Key` (01). Sem/errada → `401 UNAUTHORIZED` |
| `MOCK_NOME_CHAVE` | `Bot de atendimento` | nome da chave: `GET /me` → `name` e `senderName` das mensagens do bot (04; 07 item C8) |
| `MOCK_ESCOPOS` | `tickets:read,tickets:write,conversations:read,conversations:write,contacts:read,events:read` | escopos da chave (os que a Sofia usa, 01); `*` libera tudo. Escopo faltando → `403 FORBIDDEN` |
| `MOCK_429_A_CADA` | `0` (nunca) | a cada N requisições autenticadas devolve `429 RATE_LIMITED` com `Retry-After: 1` — testa backoff do cliente |
| `MOCK_SEM_REENTREGA` | `0` | `1` **desliga a reentrega automática** de webhook após falha (sem ela o mock reentrega em 30 s, 2 min, 10 min, 1 h e 6 h, como o PDF). A reentrega **manual** ("Reentregar último evento" / `POST /mock/reentregar`) continua funcionando: é o que o E2E de dedup usa |
| `MOCK_REENTREGA_ATRASOS_S` | `30,120,600,3600,21600` | atrasos (s) da reentrega automática; os testes usam valores curtos |
| `MOCK_PRIMEIRO_NUMBER` | `1001` | primeiro `number` de ticket (**sempre inteiro**, incremental) |
| `GATEWAY_WEBHOOK_URL` | `http://localhost:8080/webhook` | para onde os webhooks são enviados |
| `MOCK_WEBHOOK_SECRET` | `segredo-mock` | segredo do HMAC (= `INFODESK_WEBHOOK_SECRET` do gateway) |
| `MOCK_ORG_ID` | UUID fixo `4444…4401` | `organizationId` dos webhooks e de `GET /me` (= `INFODESK_ORGANIZATION_ID` do gateway, 07 item W5) |
| `MOCK_DADOS` | `mock-infodesk/dados.json` | mesas, departamentos, atendentes, clientes e solicitantes |
| `MOCK_FINISH_EMITE_WEBHOOK` | `1` | `POST /finish` dispara `conversation.finished` ao parceiro (07 item C5) |
| `MOCK_REFERENCIA_EXIGE_ESCOPO` | `0` | `1` faz a referência (`/desks`, `/desks/:id`, `/catalog`, `/users`, `/departments`) exigir escopo. **Não é o comportamento documentado** (01: referência vale para qualquer chave); só serve para testar o tratamento de `403` |

`MOCK_NUMBER_COMO_STRING` **foi removida**: na API v1 `number` é sempre inteiro
(`03`, 07 item T1).

Cada requisição vira uma linha no stdout (`[mock] POST /api/integration/v1/tickets → 201`),
sem query string; telefones aparecem só mascarados (`***0001`).

## Rotas

Base: `http://localhost:9000/api/integration/v1` (o gateway usa
`INFODESK_BASE_URL=http://localhost:9000/api`; `/integration/v1/...` sem
`/api` também funciona, para curl). Toda resposta é JSON. Listas paginadas
devolvem `{data, nextCursor}` (`limit` padrão 50, máx. 100; `cursor` do mock é
um deslocamento opaco).

| Método | Rota | Escopo | Comportamento |
|---|---|---|---|
| GET | `/me` | nenhum | `{organizationId, keyId, name, scopes, handlesConversations: true}` (01) |
| GET | `/desks` | nenhum | as **15 mesas** do `docs/infodesk/09`, nomes exatos, `[{id, name}]` |
| GET | `/desks/:id` | nenhum | mesa com `statuses` (`isOpen/isClose/isCanceled`), `stages`, `priorities` (**Baixa, Média, Alta, Urgente**, ids próprios por mesa) e `catalogItems` |
| GET | `/catalog?deskId=` | nenhum | itens de catálogo da mesa (`[{id, name}]`; só 3 mesas têm itens, de teste) |
| GET | `/users`, `/departments` | nenhum | atendentes (`id, username, email, type, departmentIds`) e departamentos de `dados.json` |
| GET | `/contacts/lookup?phone=` | `contacts:read` | `{phone, requesters, clientContacts, clientIds, openTickets, conversation}`; solicitantes de `dados.json` (ex.: `+5531990000100` Maria Souza/ACME) |
| GET | `/tickets?status=open\|closed\|all&deskId&clientId&requesterId&phone&updatedSince&limit&cursor` | `tickets:read` | `{data, nextCursor}`; padrão `status=open`; `phone` casa `contactPhone` ou telefone do solicitante |
| POST | `/tickets` | `tickets:write` | `deskId` e `title` obrigatórios; `201` com o objeto `ticket` (03) |
| GET | `/tickets/:number` | `tickets:read` | ticket + `messages` (respostas públicas); `404 NOT_FOUND` |
| POST | `/tickets/:number/reply` | `tickets:write` | `{"body"}`; só registra no ticket (`201`) |
| POST | `/tickets/:number/notes` | `tickets:write` | `{"body"}`: nota interna (`201`) |
| GET | `/conversations?tab=&status=&channel=&assignedUserId=&departmentId=&botActive=&phone=&updatedSince=&limit=&cursor=` | `conversations:read` | `{data, nextCursor}` de objetos `conversation` (04); `tab` ∈ `entrada` (sem dono), `abertas`, `encerrados`, `todas` |
| GET | `/conversations/:id` | `conversations:read` | `conversation` + `whatsappWindow` + `recentMessages` (últimas 50, sem notas internas) |
| POST | `/conversations/:id/messages` | `conversations:write` | **`{"text"}`** (+ `"internal": true` = nota interna); `201 {conversationId, messages:[…]}`; `sender: "AGENT"`, `senderName` = nome da chave. **`body` sem `text` → `400 VALIDATION_ERROR`**. `template` e `media` não são implementados (fora da v1) |
| POST | `/conversations/:id/assign` | `conversations:write` | `{"userId"}`: `handledBy: AGENT`, bot fora, emite `conversation.assigned` |
| POST | `/conversations/:id/department` | `conversations:write` | `{"departmentId"}` ou `null`; **não** solta o bot |
| POST | `/conversations/:id/handoff` | `conversations:write` | `{departmentId?, userId?, note?}`: bot fora, `handledBy` = `QUEUE` (ou `AGENT` com `userId`), `note` vira nota interna, emite `conversation.handoff` (`data.note`) |
| POST | `/conversations/:id/requester` | `conversations:write` | `{"id"}` (ou `null` desfaz); o cliente vem junto |
| POST | `/conversations/:id/ticket` | `tickets:write` **e** `conversations:write` | `{deskId, title, description?, priorityId?, catalogItemId?, period?, finish?}`: abre o chamado com a **transcrição** (`period`: `session` padrão, `today`, `24h`, `all`) abaixo do resumo, já vinculado à conversa (`conversationId`/`ticketNumber`); `finish: true` encerra (emite `conversation.finished`) |
| POST | `/conversations/:id/link-ticket` | `conversations:write` | `{ticketNumber, finish?}`: vincula a chamado existente (anexa a transcrição como nota); chamado de outra conversa → `409 CONFLICT` |
| POST | `/conversations/:id/finish` | `conversations:write` | encerra (idempotente), sem mensagem ao cliente; emite `conversation.finished` |
| GET | `/events?after=<seq>&limit=&types=&conversationId=&ticketNumber=` | `events:read` | feed `{data, nextAfter, hasMore}` com o **mesmo envelope** dos webhooks (`limit` padrão 50, máx. 500) |

Erros: sempre `{"statusCode", "code", "message"}` com os `code` de `06` —
`VALIDATION_ERROR` (com `errors`), `INVALID_REFERENCE`, `UNAUTHORIZED`,
`FORBIDDEN`, `NOT_FOUND`, `CONFLICT`, `RATE_LIMITED`, `INTERNAL_ERROR`.
Operar numa conversa encerrada → `404 NOT_FOUND`.

**Não implementadas** (fora da v1 da Sofia ou sem uso no plano): `PATCH/close/
reopen/cancel/escalate` de ticket, `/tickets/:n/requester`, `/conversations/:id/
{messages GET,bot,client}`, `/messages`, `/attachments`, `/clients`,
`/requesters`, `/whatsapp/*`, `/alerts`, `/deliveries`, `/webhook/test`.
Eventos **não emitidos**: `message.sent`, `message.status`,
`conversation.department_changed`, `ticket.*`, `ping` (07 item W8: o real emite os
ecos das nossas próprias ações; sem eles o E2E não prova "ignorar eco" nem
"`ticket.created` já conhecido é no-op" — pendência para a E2.1/E3.3).

### Endpoints de controle (só do mock, sem chave)

| Método | Rota | Para quê |
|---|---|---|
| GET | `/` | UI |
| GET | `/mock/estado?telefone=+55...` | conversa do telefone (com mensagens, notas e eventos da UI), tickets, últimos webhooks, config — a UI faz poll aqui |
| POST | `/mock/cliente/mensagem` | `{"telefone","nome","texto"}` — o cliente escreveu: cria/reusa a conversa e emite os webhooks |
| POST | `/mock/cliente/audio` | `{"telefone","nome"}` — "áudio": `message.received` com `type: AUDIO`, `body` vazio e `attachment.url` |
| POST | `/mock/cliente/encerrar` | `{"telefone"}` — o atendente encerrou: `FINISHED` + `conversation.finished` |
| POST | `/mock/atendente/assume` | `{"telefone","userId?"}` — **atendente assume** (padrão: 1º de `/users`): `conversation.assigned`, `handledBy: AGENT`, `botActive: false` |
| POST | `/mock/reentregar` | `{"telefone?"}` — **reentrega o último evento** (do telefone, se vier): mesmo `id`/`seq`, novo `X-Infominas-Delivery` e novo timestamp/assinatura |
| POST | `/mock/reset` | zera tickets, conversas, eventos e entregas (útil no E2E); o `seq` **não** volta atrás |
| GET | `/mock/config` | config efetiva (sem segredos) |

## Webhooks emitidos

Quando o cliente escreve (UI ou `/mock/cliente/mensagem`):

1. se é a **primeira mensagem** de uma conversa nova → `conversation.started`;
2. sempre → `message.received`.

Cada um é um envelope de `docs/infodesk/05` com `data.conversation` **completo**
(objeto `conversation` de `04`: `handledBy: "EXTERNAL_BOT"`, `botActive: true`,
`status: "PENDING"`, `contact.groupJid: null`, `sessionStartedAt`,
`ticketNumber`…), `data.contact` e, nos eventos de mensagem, `data.message`
(`type`, `attachment`, `internal`, `senderName`…):

```json
{
  "id": "evt_000000000002",
  "seq": 2,
  "event": "message.received",
  "createdAt": "2026-10-20T12:00:00.000Z",
  "organizationId": "<MOCK_ORG_ID>",
  "data": {
    "conversation": { "id": "<uuid>", "channel": "WHATSAPP", "status": "PENDING", "subject": null,
                      "botActive": true, "handledBy": "EXTERNAL_BOT", "assignedUserId": null,
                      "departmentId": null, "clientId": null, "requesterId": null, "resourceId": null,
                      "ticketNumber": null,
                      "contact": { "name": "Maria", "phone": "+5531990000001", "email": null, "groupJid": null },
                      "provider": "GUPSHUP", "lastMessageAt": "…", "lastMessagePreview": "Bom dia",
                      "unreadCount": 1, "sessionStartedAt": "…", "createdAt": "…", "updatedAt": "…" },
    "contact": { "name": "Maria", "phone": "+5531990000001", "email": null, "groupJid": null },
    "message": { "id": "<uuid>", "conversationId": "<uuid>", "sender": "CLIENT", "senderName": null,
                 "type": "TEXT", "status": null, "body": "Bom dia, preciso de ajuda",
                 "internal": false, "deleted": false, "replyToId": null, "createdAt": "…",
                 "attachment": null }
  }
}
```

Outros eventos: `conversation.assigned` (`/assign` ou botão "Atendente assume"),
`conversation.handoff` (`data.note`), `conversation.finished` (`/finish`,
`finish: true`, botão "Encerrar conversa"; sem `reason`).

Headers: `Content-Type: application/json`, `X-Infominas-Event`,
`X-Infominas-Delivery` (uuid novo a **cada tentativa**), `X-Infominas-Timestamp`
(segundos Unix) e `X-Infominas-Signature: sha256=<hex>` com
`HMAC-SHA256(MOCK_WEBHOOK_SECRET, "<timestamp>.<corpo bruto>")`. O receptor deve
validar sobre o corpo bruto e recusar timestamps com mais de 5 min de diferença.

Entrega **síncrona (a 1ª tentativa), timeout 8 s**; sucesso = qualquer 2xx. Em
falha: registro no log, no painel "Webhooks emitidos" da UI e numa nota na
conversa, e **reentrega automática** (30 s, 2 min, 10 min, 1 h, 6 h) — a menos
que `MOCK_SEM_REENTREGA=1`. Pelo menos uma vez: o mesmo evento pode chegar mais
de uma vez; deduplique pelo `id` do evento. Com o gateway fora do ar, no
Windows cada tentativa leva alguns segundos (`localhost` tenta IPv6 e IPv4); use
`GATEWAY_WEBHOOK_URL=http://127.0.0.1:8080/webhook` se incomodar.

Corpos JSON devem ser UTF-8 (senão `400`). No Git Bash do Windows, `curl -d
'...'` com acentos pode sair em cp1252: grave o JSON num arquivo e use
`--data-binary @arquivo.json`.

## O que é HIPÓTESE (não está no PDF)

| Item (07) | Hipótese implementada aqui | Onde mudar |
|---|---|---|
| **A6** | prefixos `/api/integration/v1` e `/integration/v1` (a base URL real é outra) | `PREFIXOS` |
| **C5** | `/finish` emite `conversation.finished` (sem `reason`); mensagem nova do mesmo telefone depois do `finish` abre **outra** conversa (novo id); operação em conversa encerrada → `404` | `MOCK_FINISH_EMITE_WEBHOOK`, `_conversa_do_telefone` |
| **C9** | mídia: o mock emite **uma** mensagem (`type: AUDIO`, `body: ""`, `attachment: {url}`); o WhatsApp real gera duas (rótulo + arquivo, 04). Campos do `attachment` além de `url` desconhecidos | `mensagem_do_cliente` |
| **D3 / D4 / D5** | `/desks`, `/users`, `/departments`, `/catalog` devolvem **array puro**; `users.type = "AGENT"`; itens de catálogo só `{id, name}` | `dados.json`, `listar_*` |
| **T1 / T13** | `status` inicial = 1º status aberto da mesa; `messages` do ticket = respostas públicas; sem SLA (`serviceDueAt` etc. `null`); resposta de `/notes` = `{id, body, internal: true, createdAt}`; default de `channel` em `POST /tickets` = `WEB` | `_abrir_ticket`, `_ticket_externo` |
| **T10** | só `deskId` e `title` são obrigatórios (a organização real pode exigir mais → `400`) | `_abrir_ticket` |
| **T11** | `POST /conversations/:id/ticket` herda `clientId`/`requesterId` e o telefone da conversa e devolve o objeto `ticket` | `abrir_ticket_da_conversa` |
| **T12** | conversa que já tem `ticketNumber`: o mock cria **outro** chamado (sem `CONFLICT`) | `abrir_ticket_da_conversa` |
| 06 | `link-ticket` com `ticketNumber` inexistente → `400 INVALID_REFERENCE` (o real poderia ser `404`); telefone digitado na UI/`/mock/*` é normalizado para E.164 (`+` + dígitos) antes de ir no webhook | `vincular_ticket`, `mensagem_do_cliente` |
| **W7** | `conversation.assigned` não leva extras (`userId`/`previousUserId` desconhecidos); `handoff` leva `note` | `_assumir`, `handoff` |
| **W9** | `data.conversation` é o retrato do **momento** do evento; reentrega e `/events` repetem o mesmo retrato | `_evento` |
| 04 | `status` da conversa: `PENDING` (nova/fila), `IN_PROGRESS` (com atendente), `FINISHED`; `whatsappWindow` = formato de `/whatsapp/window`; `/assign` com `userId: null` manda para a fila | `_conversa_externa`, `_janela_whatsapp`, `_assumir` |
| 08 | `handoff` sem `userId` volta para a fila (`QUEUE`), nunca para "distribuição automática"; `note` é opcional | `handoff` |
| 06 | formato de `errors` em `VALIDATION_ERROR` = `[{field, message}]`; `Retry-After: 1` no 429; método errado numa rota conhecida → `404 NOT_FOUND` (o PDF não cita 405); `limit` acima do máximo é cortado | `invalido`, `contar_para_429`, `_rota_api` |
| 03 / 04 | códigos de sucesso não citados: `201` em `POST /tickets`, `/reply`, `/notes`, `/messages`, `/conversations/:id/ticket`; `200` nos demais | `ROTAS` |
| — | teto de 1 MB por corpo (`400` acima disso); `Transfer-Encoding: chunked` não suportado | `CORPO_MAX_BYTES` |

## Usar com o gateway

Variáveis do gateway (`backend/.env`) para falar com o mock:

```env
INFODESK_BASE_URL=http://localhost:9000/api
INFODESK_API_KEY=ik_test_mock
INFODESK_WEBHOOK_SECRET=segredo-mock
INFODESK_ORGANIZATION_ID=44444444-4444-4444-8444-444444444401
```

`scripts/start-all.sh` já sobe os três processos com isso. **Atenção:** o
gateway só passa a falar com o mock a partir da etapa **E2.1** (webhook +
cliente Infodesk ligados); até lá o mock emite os webhooks, registra a falha
ou o `404`, e a UI mostra isso no painel (com `MOCK_SEM_REENTREGA=1` para não
acumular reentregas de 30 s em diante).

Smoke manual: abra a UI, escreva uma mensagem → aparece a bolha do cliente e
`conversation.started` + `message.received` no painel; com o gateway no ar, a
resposta chega como bolha do bot; abra um chamado por curl e ele aparece em
"Tickets criados" (os ids das mesas vêm de `GET /desks`):

```bash
KEY=ik_test_mock   # = MOCK_API_KEY
API=http://localhost:9000/api/integration/v1
DESK=$(curl -s "$API/desks" -H "Authorization: Bearer $KEY" | python -c 'import json,sys; print(next(d["id"] for d in json.load(sys.stdin) if d["name"]=="Suporte Tecnico N1"))')
curl -s -X POST "$API/tickets" \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d "{\"deskId\":\"$DESK\",\"title\":\"Impressora não imprime\",\"contactPhone\":\"+5531990000001\"}"
```

## Limitações

- Estado só em memória (morre com o processo); `/mock/reset` zera.
- Não simula atendente humano digitando (só assumir/encerrar) nem mídia além
  de um "áudio" sem conteúdo.
- Um único "tenant": `organizationId` fixo. Os departamentos de chat de
  `dados.json` são de teste (ainda não observados — 07 item D6); as mesas
  têm os nomes reais (09), mas ids, statuses, etapas, prioridades e itens de
  catálogo são inventados.
