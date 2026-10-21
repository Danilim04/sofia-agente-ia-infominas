---
name: infodesk-api
description: Como usar a API do Infodesk (Infominas Desk) neste projeto — rotas, autenticação, webhooks, erros e as lacunas conhecidas. Carregue ao implementar ou revisar qualquer código que fale com o Infodesk (backend/internal/infodesk, webhook, toolsapi, mock-infodesk) ou ao decidir um campo/comportamento da integração.
---

# Infodesk — como usar a API neste repositório

A fonte de verdade é `docs/infodesk/` (convertida do PDF oficial **v1 de
2026-10-19**, 25 páginas). **Leia antes de escrever código**:
`docs/infodesk/README.md` (mapa de rotas e o que mudou), `08-bot-externo.md`
(como a Sofia opera) e `07-pontos-ambiguos.md` (o que NÃO está documentado).
Nunca invente campo, valor de enum ou comportamento: se não está em
`docs/infodesk/`, implemente a hipótese de trabalho do item correspondente no
07, de forma configurável, e deixe `// TODO(P3)` (ou o item) no código.

> Código escrito sobre o guia anterior (5 páginas) tem **três quebras
> conhecidas**: assinatura do webhook (agora com timestamp), campo da
> mensagem (`text`, não `body`) e `number` do ticket (inteiro). Veja a tabela
> "O que mudou" no README.

## Fatos que todo código deve respeitar

- Base: `INFODESK_BASE_URL` (sem default fixo — a capa do PDF e o guia antigo
  divergem; copiar da tela *Configurações › Integrações*, 07 A6); rotas sob
  `/integration/v1`; JSON; `Authorization: Bearer <INFODESK_API_KEY>`.
- `GET /me` no boot: valida chave, `organizationId` e
  `handlesConversations == true`.
- Escopos: `tickets:read|write`, `conversations:read|write`, `contacts:read`,
  `events:read` (v1); `contacts:write`, `whatsapp:send` fora da v1. Dados de
  referência não exigem escopo. 403 = erro de **configuração** (alerta, não
  retry).
- Webhook: headers `X-Infominas-Event`, `X-Infominas-Delivery`,
  `X-Infominas-Timestamp` (Unix s), `X-Infominas-Signature: sha256=<hex>` =
  **HMAC-SHA256(`INFODESK_WEBHOOK_SECRET`, `"<timestamp>.<raw body>"`)**.
  Verifique em tempo constante sobre os bytes recebidos e **recuse |now − ts|
  > 5 min** (relógio injetável). Responda `200` em < 8 s (enfileire).
  **Pelo menos uma vez** com reentrega (30 s … 6 h): dedup pelo `id` do
  evento (`evt_…`), 7 dias; ordene por `seq` se precisar.
- Processe como turno do cliente só se `event == message.received` **e**
  `data.message.sender == CLIENT` **e** `data.conversation.handledBy ==
  EXTERNAL_BOT` **e** `channel == WHATSAPP` **e** `contact.groupJid == null`.
  `handledBy != EXTERNAL_BOT` ou `conversation.assigned` → humano assumiu →
  bot cala. `message.type != TEXT` ou `attachment != null` → mídia.
- 16 eventos (05). Mínimo a assinar: `message.received`, `conversation.*`,
  `ticket.*`, `ping`.
- Reconciliação: `GET /events?after=<seq>` (mesmo envelope, mesmo handler);
  persistir o maior `seq`.
- Enviar mensagem: `POST /conversations/:id/messages { "text": "…" }`
  (`internal: true` = nota interna). Resposta 201 `{ conversationId,
  messages: [...] }`.
- Transferir para humano: `POST /conversations/:id/handoff { departmentId?,
  userId?, note }`. **Não** `/department` (muda fila sem soltar o bot), **não**
  `/bot {active:false}` (ninguém é avisado), **não** `/assign` na v1.
- Identificar: `GET /contacts/lookup?phone=` → `requesters[]`, `clientIds`,
  `openTickets[]`, `conversation`. Decisão de política em 07 N3 / ADR 0004.
- Ticket: `POST /conversations/:id/ticket { deskId, title, description,
  priorityId?, catalogItemId?, period: "session", finish: false }` (preferido)
  ou `POST /tickets` (`deskId`, `title` obrigatórios; `channel` ∈ WHATSAPP,
  WEB, EMAIL, CHAT, AGENT). Resposta = objeto `ticket` com **`number` int**,
  `status {name,isOpen,isClose,isCanceled}`, `desk`, `priority`,
  `conversationId`. **Sem idempotência na API**: nunca repita o POST (ADR
  0005); reconcilie por `GET /tickets?phone=&status=open`.
- Prioridades/status/etapas/catálogo são **por mesa**: `GET /desks/:id`.
  Taxonomia mapeia nomes; o gateway resolve UUID na mesa de destino.
- Erros: `{statusCode, code, message, ...}` — **decida pelo `code`**
  (`UNAUTHORIZED`, `FORBIDDEN`, `NOT_FOUND`, `INVALID_REFERENCE`,
  `VALIDATION_ERROR`, `CONFLICT`, `WHATSAPP_WINDOW_CLOSED`, `RATE_LIMITED`
  300/min por IP, `INTERNAL_ERROR`). Transitórios: `RATE_LIMITED`, 5xx, rede.
  Mensagem do Infodesk **nunca** chega ao cliente nem ao LLM crua.
- Paginação: `{ data, nextCursor }`, `limit` ≤ 100 (`/events`: `{ data,
  nextAfter, hasMore }`).
- Ainda não existe: sandbox, chave de idempotência, limites de tamanho,
  lista de departamentos/itens de catálogo reais (09).

## Onde cada coisa vive no código (alvo)

| Preocupação | Lugar |
|---|---|
| Cliente HTTP, tipos, assinatura | `backend/internal/infodesk/` (único pacote que importa a API) |
| Receber webhook | `backend/internal/webhook/` |
| Estado bot/transferida, tickets, contatos, `seq` de eventos | `backend/internal/store/` |
| Reconciliação por `/events` | `backend/internal/reconciliador/` |
| Endpoints para o cérebro (`/tools/infodesk/*`) | `backend/internal/toolsapi/` |
| Categoria → mesa/catálogo/departamento/prioridade | `agente-ia/config/taxonomia.yaml` + `backend/internal/taxonomia/` |
| Tools Python | `agente-ia/src/tools/infodesk_tools.py` (nunca chama o Infodesk direto) |
| Fake para testes/E2E | `mock-infodesk/` |

## Padrão do cliente Go (herdado de `sac.go`, ver `docs/zapin/CLAUDE-legado.md`)

```go
type Options struct{ BaseURL, APIKey string; HTTP *http.Client; Now func() time.Time; CacheTTL time.Duration }
func New(o Options) *Client            // defaults: timeout 15 s, TTL 10 min
func (c *Client) Me(ctx) (Me, error)
func (c *Client) EnviarMensagem(ctx, convID, texto string, interna bool) (Mensagem, error)
func (c *Client) Handoff(ctx, convID string, h Handoff) error
func (c *Client) CriarTicketDaConversa(ctx, convID string, t NovoTicket) (Ticket, error)   // SEM retry
func (c *Client) Tickets(ctx, FiltroTickets) ([]Ticket, error)                            // reconciliação do POST
func (c *Client) Lookup(ctx, telefone string) (Lookup, error)
func (c *Client) Eventos(ctx, apos int64, limite int) (PaginaEventos, error)
// erro HTTP: *HTTPError{Status int; Code, Mensagem string}; (e *HTTPError) Transitorio() bool  // RATE_LIMITED, 5xx
func VerificarAssinatura(secret string, raw []byte, timestamp, header string, agora time.Time) bool
```

Teste cada método com `httptest.Server` (sucesso, 401, 403, 404, 429 com e
sem `Retry-After`, 5xx, JSON inválido, `code` desconhecido) e a assinatura com
vetor conhecido **incluindo o timestamp** e os casos "timestamp velho" e
"timestamp futuro".

## Ao descobrir algo novo sobre a API

Atualize o doc da área em `docs/infodesk/`, marque o item do 07 como
`RESPONDIDO (data)` e ajuste `mock-infodesk/` para refletir o comportamento
real. Nunca documente só no código.
