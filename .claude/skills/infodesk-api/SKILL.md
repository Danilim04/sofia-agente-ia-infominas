---
name: infodesk-api
description: Como usar a API do Infodesk (Infominas Desk) neste projeto — rotas, autenticação, webhooks, erros e as lacunas conhecidas. Carregue ao implementar ou revisar qualquer código que fale com o Infodesk (backend/internal/infodesk, webhook, toolsapi, mock-infodesk) ou ao decidir um campo/comportamento da integração.
---

# Infodesk — como usar a API neste repositório

A fonte de verdade é `docs/infodesk/` (convertida do PDF oficial, 5 páginas).
**Leia antes de escrever código**: `docs/infodesk/README.md` (mapa de rotas) e
`docs/infodesk/07-pontos-ambiguos.md` (o que NÃO está documentado). Nunca
invente campo, valor de enum ou comportamento: se não está em `docs/infodesk/`,
implemente a hipótese de trabalho do item correspondente no 07, de forma
configurável, e deixe `// TODO(P3)` (ou o item) no código.

## Fatos que todo código deve respeitar

- Base: `https://infodesk.infominas.net/api/integration/v1` (config
  `INFODESK_BASE_URL`); JSON; `Authorization: Bearer <INFODESK_API_KEY>`.
- Escopos: `tickets:read|write`, `conversations:read|write`; 403 = escopo
  faltando (erro de **configuração**: alerta, não retry).
- Webhook: `POST` JSON; headers `X-Infominas-Event` e
  `X-Infominas-Signature: sha256=<hmac-hex>` = HMAC-SHA256 do **raw body** com
  `INFODESK_WEBHOOK_SECRET`. Verifique em **tempo constante** sobre os bytes
  recebidos. Responda `200` em **< 8 s** (na prática em ms: enfileire).
  **Sem reentrega**: dedup por `id` da entrega e por `data.message.id`;
  descarte `createdAt` velho; só `data.message.sender == "CLIENT"`.
- Eventos: `message.received`, `conversation.started`, `conversation.finished`.
  Só o payload do primeiro é documentado.
- Erros: `{statusCode, message}`; 401/403 definitivos, 404 definitivo, 429 e
  5xx transitórios. Mensagem do Infodesk **nunca** chega ao cliente nem ao LLM
  crua (higienize como `sac.go` fazia: `higienizarTexto`).
- `POST /tickets`: só `deskId` e `title` obrigatórios; resposta "inclui
  `number`" (tipo desconhecido → decodifique número **ou** string). **Sem
  idempotência na API**: nunca repita o POST (ADR 0005).
- Não existe: busca de cliente, listagem de tickets, atualização/fechamento de
  ticket, lista de prioridades, envio de mídia.

## Onde cada coisa vive no código (alvo)

| Preocupação | Lugar |
|---|---|
| Cliente HTTP, tipos, assinatura | `backend/internal/infodesk/` (único pacote que importa a API) |
| Receber webhook | `backend/internal/webhook/` |
| Estado bot/transferida, tickets, contatos | `backend/internal/store/` |
| Endpoints para o cérebro (`/tools/infodesk/*`) | `backend/internal/toolsapi/` |
| Categoria → mesa/departamento/prioridade | `agente-ia/config/taxonomia.yaml` + `backend/internal/taxonomia/` |
| Tools Python | `agente-ia/src/tools/infodesk_tools.py` (nunca chama o Infodesk direto) |
| Fake para testes/E2E | `mock-infodesk/` |

## Padrão do cliente Go (herdado de `sac.go`, ver `docs/zapin/CLAUDE-legado.md`)

```go
type Options struct{ BaseURL, APIKey string; HTTP *http.Client; Now func() time.Time; CacheTTL time.Duration }
func New(o Options) *Client            // defaults: timeout 15 s, TTL 10 min
func (c *Client) EnviarMensagem(ctx, convID, body string) error
func (c *Client) CriarTicket(ctx, NovoTicket) (Ticket, error)   // SEM retry
// erro HTTP: *HTTPError{Status int; Mensagem string}; (e *HTTPError) Transitorio() bool  // 429, 5xx
func VerificarAssinatura(secret string, raw []byte, header string) bool  // "sha256=" + hex, subtle.ConstantTimeCompare
```

Teste cada método com `httptest.Server` (sucesso, 401, 403, 404, 429 com
`Retry-After`, 5xx, JSON inválido) e a assinatura com vetor conhecido.

## Ao descobrir algo novo sobre a API

Atualize o doc da área em `docs/infodesk/`, marque o item do 07 como
`RESPONDIDO (data)` e ajuste `mock-infodesk/` para refletir o comportamento
real. Nunca documente só no código.
