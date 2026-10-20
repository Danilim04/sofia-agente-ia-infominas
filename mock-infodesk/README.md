# mock-infodesk — Infodesk falso para testar a Sofia ponta a ponta

Servidor **stdlib Python 3.12+ (sem `pip install`)** que finge ser o Infodesk
(Infominas Desk) nos dois sentidos da integração, para exercitar o fluxo
`cliente (UI) → gateway Go → cérebro Python → resposta` sem WhatsApp nem
Infodesk reais.

```
você (UI :9000) ──message.received (HMAC)──►  gateway Go /webhook  ──►  cérebro /chat
      ▲                                              │
      └──── POST /api/integration/v1/conversations/:id/messages ◄──┘  (Go "responde no Infodesk" = aqui)
```

- **Saída → gateway:** emite os webhooks de `docs/infodesk/05` assinados com
  `X-Infominas-Signature: sha256=<hmac-sha256 do corpo bruto>`.
- **Entrada ← gateway:** expõe a REST de `docs/infodesk/01–06` com estado em
  memória e erros realistas no formato `{"statusCode", "message"}`.
- **UI** em `http://localhost:9000`: você escreve como o cliente, vê as bolhas
  do bot, os tickets abertos e o departamento/atendente da conversa.

Tudo que o PDF do Infodesk documenta está reproduzido como está. O que o PDF
**não** documenta está implementado como **hipótese de trabalho** (itens de
`docs/infodesk/07-pontos-ambiguos.md`), marcado abaixo e configurável. Quando
a Infominas responder, ajuste o mock aqui e o doc da área (skill `infodesk-api`).

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
| `MOCK_PORT` / `MOCK_HOST` | `9000` / `0.0.0.0` | porta/interface do servidor |
| `MOCK_API_KEY` | `ik_test_mock` | chave aceita em `Authorization: Bearer` **ou** `X-Api-Key` (01). Sem/errada → `401` |
| `MOCK_ESCOPOS` | os 4 (`tickets:read,tickets:write,conversations:read,conversations:write`) | escopos da chave; rota com escopo faltando → `403 {"statusCode":403,"message":"Escopo insuficiente. Necessário: tickets:write."}` (formato literal do PDF) |
| `MOCK_429_A_CADA` | `0` (nunca) | a cada N requisições autenticadas devolve `429` com `Retry-After: 1` — testa backoff do cliente |
| `MOCK_NUMBER_COMO_STRING` | `0` | `1` devolve `number` do ticket como **string** (`"1001"`) em vez de número — testa a tolerância do cliente Go (T1) |
| `MOCK_PRIMEIRO_NUMBER` | `1001` | primeiro `number` de ticket (incremental) |
| `GATEWAY_WEBHOOK_URL` | `http://localhost:8080/webhook` | para onde os webhooks são enviados |
| `MOCK_WEBHOOK_SECRET` | `segredo-mock` | segredo do HMAC (= `INFODESK_WEBHOOK_SECRET` do gateway) |
| `MOCK_ORG_ID` | UUID fixo `4444…4401` | `organizationId` dos webhooks (= `INFODESK_ORGANIZATION_ID` do gateway, W5) |
| `MOCK_DADOS` | `mock-infodesk/dados.json` | mesas, atendentes e departamentos (UUIDs fixos) |
| `MOCK_FINISH_EMITE_WEBHOOK` | `1` | `POST /finish` dispara `conversation.finished` para o parceiro (hipótese C5) |
| `MOCK_REFERENCIA_EXIGE_ESCOPO` | `0` | `1` faz `/desks`, `/users`, `/departments` exigirem escopo (hipótese A3) |

Cada requisição vira uma linha no stdout (`[mock] POST /api/integration/v1/tickets → 201`);
telefones aparecem só mascarados (`***0001`).

## Rotas

Base: `http://localhost:9000/api/integration/v1` (o gateway usa
`INFODESK_BASE_URL=http://localhost:9000/api`; `/integration/v1/...` sem
`/api` também funciona, para curl). Toda resposta é JSON.

| Método | Rota | Escopo | Comportamento |
|---|---|---|---|
| GET | `/desks`, `/users`, `/departments` | nenhum (A3) | listas de `dados.json`: mesas **Suporte Técnico**, **Financeiro**, **Triagem**; departamentos **Suporte**, **Financeiro**, **Atendimento**; 2 atendentes |
| POST | `/tickets` | `tickets:write` | `deskId` e `title` obrigatórios (senão `400`); `deskId` desconhecido → `400`; devolve `201` com o ticket (`number` incremental) |
| GET | `/tickets/:number` | `tickets:read` | `404` se não existe |
| POST | `/tickets/:number/reply` | `tickets:write` | `{"body"}` obrigatório; só registra no ticket (`201`) |
| GET | `/conversations?tab=&departmentId=` | `conversations:read` | lista; `tab` inválido → `400` |
| GET | `/conversations/:id` | `conversations:read` | conversa + `messages [{id, body, sender, createdAt}]` |
| POST | `/conversations/:id/messages` | `conversations:write` | `{"body"}`; registra com `sender: "BOT"` e mostra como bolha do bot; conversa encerrada → `404` |
| POST | `/conversations/:id/assign` | `conversations:write` | `{"userId"}` (de `/users`; `null` desatribui) |
| POST | `/conversations/:id/department` | `conversations:write` | `{"departmentId"}` ou `null` para remover (literal do PDF) |
| POST | `/conversations/:id/finish` | `conversations:write` | encerra (idempotente) e emite `conversation.finished` |

Erros: `401` chave ausente/errada, `403` escopo, `404` recurso, `405` método,
`429` (configurável), `400` validação — sempre `{"statusCode": N, "message": "..."}`.

### Endpoints de controle (só do mock, sem chave)

| Método | Rota | Para quê |
|---|---|---|
| GET | `/` | UI |
| GET | `/mock/estado?telefone=+55...` | conversa do telefone (com mensagens e eventos), tickets, últimos webhooks, config — a UI faz poll aqui |
| POST | `/mock/cliente/mensagem` | `{"telefone","nome","texto"}` — o cliente escreveu: cria/reusa a conversa e emite os webhooks |
| POST | `/mock/cliente/audio` | `{"telefone","nome"}` — "áudio": `message.received` com `body` vazio (W3) |
| POST | `/mock/cliente/encerrar` | `{"telefone"}` — o atendente encerrou: `FINISHED` + `conversation.finished` |
| POST | `/mock/reset` | zera tickets, conversas e entregas (útil no E2E) |
| GET | `/mock/config` | config efetiva (sem segredos) |

## Webhooks emitidos

Quando o cliente escreve (UI ou `/mock/cliente/mensagem`):

1. se é a **primeira mensagem** de uma conversa nova → `conversation.started`;
2. sempre → `message.received` com o payload **exato** de `docs/infodesk/05`:

```json
{
  "id": "<uuid da entrega>",
  "event": "message.received",
  "createdAt": "2026-10-20T12:00:00.000Z",
  "organizationId": "<MOCK_ORG_ID>",
  "data": {
    "conversationId": "<uuid>",
    "channel": "WHATSAPP",
    "contact": { "name": "Maria", "phone": "+5531990000001" },
    "message": { "id": "<uuid>", "body": "Bom dia, preciso de ajuda", "sender": "CLIENT", "createdAt": "…" }
  }
}
```

Headers: `Content-Type: application/json`, `X-Infominas-Event: <evento>`,
`X-Infominas-Signature: sha256=<hex do HMAC-SHA256 do corpo bruto com MOCK_WEBHOOK_SECRET>`.
Entrega **síncrona, timeout 8 s, sem reentrega** (como o PDF): falha fica no
log, no painel "Webhooks emitidos" da UI e numa nota na conversa. Com o
gateway fora do ar, no Windows cada tentativa leva alguns segundos
(`localhost` tenta IPv6 e IPv4); use `GATEWAY_WEBHOOK_URL=http://127.0.0.1:8080/webhook`
se incomodar.

Corpos JSON devem ser UTF-8 (senão `400 Corpo inválido`). No Git Bash do
Windows, `curl -d '...'` com acentos pode sair em cp1252: grave o JSON num
arquivo e use `--data-binary @arquivo.json`.

`conversation.started` e `conversation.finished` levam `data.conversationId`,
`data.channel` e `data.contact` (sem `message`) — hipótese W1.

## O que é HIPÓTESE (não está no PDF)

| Item (07) | Hipótese implementada aqui | Como mudar |
|---|---|---|
| **C1** | schema do item de `GET /conversations`: `{id, status, channel, contact:{name,phone}, departmentId, assignedUserId, lastMessageAt, createdAt, finishedAt}`; `status` ∈ `OPEN`/`FINISHED`; `tab=entrada` = abertas sem atendente, `meus`/`atendimento` = abertas com atendente, `encerrados` = `FINISHED`; sem paginação | `MockInfodesk._conversa_externa` / `listar_conversas` |
| **C2** | `sender` das mensagens do bot = `"BOT"`; `GET /conversations/:id` devolve `messages` em ordem cronológica | `enviar_mensagem` |
| **C5** | `/finish` emite `conversation.finished` ao parceiro; mensagem nova do mesmo telefone depois do `finish` abre **outra** conversa (novo id); operação em conversa encerrada → `404` | `MOCK_FINISH_EMITE_WEBHOOK`, `_conversa_do_telefone` |
| **W1** | payload de `started`/`finished` = `data.{conversationId, channel, contact}` | `montar_payload` |
| **W3** | áudio/mídia = `message.received` com `body: ""` | botão 🎤 / `/mock/cliente/audio` |
| **T1** | `POST /tickets` → `201` com `{id, number, title, description, status:"OPEN", deskId, channel, clientId, requesterId, contactPhone, priorityId, createdAt, updatedAt, replies}`; `number` numérico (ou string com `MOCK_NUMBER_COMO_STRING=1`) | `criar_ticket` |
| **A3** | `/desks`, `/users`, `/departments` não exigem escopo | `MOCK_REFERENCIA_EXIGE_ESCOPO=1` |
| **A4** / 06 | `429` traz `Retry-After: 1` | `contar_para_429` |
| 04 | `assign` com `userId: null` desatribui | `atribuir` |

`clientId`, `requesterId` e `priorityId` são aceitos e ecoados sem validação
(não há rota para obtê-los — T3/T4).

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
ou o `404`, e a UI mostra isso no painel.

Smoke manual: abra a UI, escreva uma mensagem → aparece a bolha do cliente e
`conversation.started` + `message.received` no painel; com o gateway no ar, a
resposta chega como bolha do bot; abra um ticket por curl e ele aparece em
"Tickets criados":

```bash
curl -s -X POST http://localhost:9000/api/integration/v1/tickets \
  -H "Authorization: Bearer ik_test_mock" -H "Content-Type: application/json" \
  -d '{"deskId":"11111111-1111-4111-8111-111111111101","title":"Impressora não imprime","contactPhone":"+5531990000001"}'
```

## Limitações

- Estado só em memória (morre com o processo); `/mock/reset` zera.
- Não simula atendente humano digitando (não há evento para isso no PDF) nem
  mídia além do `body` vazio.
- Um único "tenant": `organizationId` fixo.
