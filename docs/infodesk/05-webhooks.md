# 05 — Webhooks (receber mensagens e eventos)

Fonte: PDF, seção "5. Webhooks". Configure `webhookUrl` e `webhookEvents` **na
chave**; o Desk faz `POST` JSON ao endpoint quando os eventos ocorrem.

## Eventos disponíveis

| Evento | Quando dispara |
|---|---|
| `message.received` | Mensagem recebida do cliente (WhatsApp ou chat) |
| `conversation.started` | Nova conversa / nova sessão de atendimento |
| `conversation.finished` | Conversa encerrada |

Não há evento de **ticket** (criado/atualizado/fechado) nem de **atribuição**
(humano assumiu) nem de **mensagem enviada por atendente**.

## Formato do payload (único exemplo do PDF: `message.received`)

```json
{
  "id": "<id-da-entrega>",
  "event": "message.received",
  "createdAt": "2026-06-29T12:00:00.000Z",
  "organizationId": "…",
  "data": {
    "conversationId": "…",
    "channel": "WHATSAPP",
    "contact": { "name": "Maria", "phone": "+5531990000000" },
    "message": { "id": "…", "body": "Bom dia, preciso de ajuda", "sender": "CLIENT", "createdAt": "…" }
  }
}
```

| Campo | Observação |
|---|---|
| `id` | id da **entrega** — use para **deduplicação** (como o delivery-id do Chatwoot no Zapin) |
| `event` | nome do evento (também no header `X-Infominas-Event`) |
| `createdAt` | ISO-8601 UTC (`Z`) — use para descartar eventos velhos (`EVENTO_IDADE_MAX` do Zapin) |
| `organizationId` | id da organização; **validar** contra o esperado (multi-tenant) |
| `data.conversationId` | chave da conversa (thread) |
| `data.channel` | `WHATSAPP` observado; `WEB`/outros **não documentados** |
| `data.contact.name` / `.phone` | nome do perfil e telefone E.164 — **PII**, mascarar em log |
| `data.message.id` | id da mensagem — segunda chave de dedup (equivale ao WAID/`source_id` do Zapin) |
| `data.message.body` | texto. **Mídia/áudio/anexo: nenhum campo documentado** (07, W3) |
| `data.message.sender` | `CLIENT` observado; outros valores **não documentados** |

Payloads de `conversation.started` e `conversation.finished`: **NÃO
DOCUMENTADOS** (assumir ao menos `data.conversationId`; confirmar).

Dica do PDF: ao receber `message.received`, responda com
`POST /integration/v1/conversations/{conversationId}/messages`.

## Verificação de assinatura (HMAC)

Cada entrega inclui os headers:

```http
X-Infominas-Event: message.received
X-Infominas-Signature: sha256=<hmac-hex>
```

A assinatura é o **HMAC-SHA256 do corpo bruto (raw body)** usando o
`webhookSecret` da chave. Verificar em **tempo constante** sobre os bytes
exatos recebidos (antes de qualquer parse/re-serialização) — mesmo padrão do
`signature.go` do Zapin, trocando o formato (`sha256=` prefixo, sem
timestamp).

Não documentado: há proteção contra **replay** (timestamp assinado)? Não há —
a assinatura é só do corpo. Mitigação nossa: dedup por `id` de entrega +
descarte por `createdAt` antigo.

## Semântica de entrega (recomendações do PDF)

- **Responda 2xx rapidamente** — timeout de entrega: **8 s**. Processe de forma
  **assíncrona** (enfileirar e devolver `200` imediatamente; o processamento
  pelo LLM leva mais que 8 s).
- A entrega é **best-effort, sem reentrega automática**. Para reconciliar, faça
  **polling de `GET /integration/v1/conversations`** periodicamente.

## Implicações para o nosso lado

- O receptor de webhook é o **gateway Go** (substitui `internal/webhook` +
  `internal/chatwoot` do Zapin): valida HMAC, dedup por `id` e `message.id`,
  descarta evento velho, valida `organizationId`, enfileira por
  `conversationId` e responde `200` em milissegundos.
- Como não há reentrega, o gateway precisa de um **reconciliador** (job
  periódico que lista conversas em `tab=entrada`/`atendimento` e procura
  mensagens de `CLIENT` posteriores à última processada). O schema dessa
  listagem não está documentado → o job fica **bloqueado até a resposta da
  Infominas** (07, C1/C2).
- `conversation.finished` = fim de episódio → limpar estado da conversa
  (equivalente ao `HandleConversationUpdated(status=resolved)` do Zapin).
- `conversation.started` sem mensagem: não responder nada até a primeira
  `message.received` (evitar saudação dupla) — a confirmar com o
  comportamento real.
