# 06 — Erros, rate limit e paginação

Fonte: PDF v1 (2026-10-19), seções "3. Erros" e "4. Paginação".

## Formato

Todo erro tem o mesmo formato. **Decida pelo `code`**; a `message` é para
leitura humana. Alguns códigos trazem campos extras.

```json
{ "statusCode": 409, "code": "WHATSAPP_WINDOW_CLOSED",
  "message": "A janela de 24h do WhatsApp está fechada: envie um template aprovado.",
  "lastInboundAt": "2026-10-18T09:12:00.000Z", "windowExpiresAt": "2026-10-19T09:12:00.000Z" }
```

## Tabela de códigos

| HTTP | `code` | Quando |
|---|---|---|
| 400 | `VALIDATION_ERROR` | corpo/parâmetro inválido (lista em `errors`) |
| 400 | `INVALID_REFERENCE` | id de outro registro inexistente |
| 401 | `UNAUTHORIZED` | chave ausente, inválida ou revogada |
| 403 | `FORBIDDEN` | escopo insuficiente |
| 404 | `NOT_FOUND` | recurso não existe nesta organização |
| 404 | `TEMPLATE_NOT_FOUND` | template inexistente ou desativado |
| 409 | `CONFLICT` | conflito (ex.: chamado preso a outro atendimento) |
| 409 | `WHATSAPP_WINDOW_CLOSED` | texto/mídia fora da janela de 24 h, ou número sem conversa: use template |
| 409 | `WHATSAPP_NOT_CONFIGURED` | nenhum canal de WhatsApp no Desk |
| 422 | `TEMPLATE_NOT_APPROVED` | template não aprovado/importado do provedor |
| 422 | `TEMPLATE_PARAMS_MISMATCH` | parâmetros faltando ou sobrando (`paramCount`, `paramLabels`) |
| 400/422 | `MEDIA_URL_REQUIRED` / `MEDIA_DOWNLOAD_FAILED` | URL de mídia inválida, interna, inacessível ou > 16 MB |
| 429 | `RATE_LIMITED` | limite de requisições (**300/min por IP**) |
| 500 | `INTERNAL_ERROR` | erro do Desk |

Falha do provedor **depois** do envio (número inexistente, opt-out, template
recusado pela Meta) não é erro HTTP: chega como evento `message.status` com
`status: "failed"` e `error.code: "PROVIDER_ERROR"`.

Não documentado: formato do array `errors` de `VALIDATION_ERROR`; header
`Retry-After` no 429; 502/503/504 (tratar como 5xx genérico).

## Paginação

Listas devolvem `{ "data": [...], "nextCursor": "…" }`. Próxima página:
repetir a chamada com `cursor=<nextCursor>`; `nextCursor: null` = fim.
`limit` padrão **50** (máx. **100**, salvo indicação — `GET /messages` aceita
500). Exceção: `GET /events` devolve `{ data, nextAfter, hasMore }` (05).

## Política de tratamento recomendada (nossa)

Espelha a classificação transitória × definitiva do gateway
(`internal/engine/disjuntor.go`, `src/agent/falhas.py`). **Chavear pelo
`code`**, com `statusCode` como fallback.

| `code` / situação | Classe | Ação |
|---|---|---|
| `UNAUTHORIZED`, `FORBIDDEN` | **configuração** (definitiva) | não repetir; alerta ao responsável; fallback neutro ao cliente + handoff |
| `NOT_FOUND` em `GET /tickets/:number` | definitiva | "não encontrei esse número" — nunca inventar |
| `NOT_FOUND` em `/conversations/:id/*` | definitiva | conversa inexistente/encerrada → encerrar episódio local |
| `INVALID_REFERENCE` | **configuração** | `deskId`/`priorityId`/`departmentId` da taxonomia não existe mais → alerta; recarregar cache de referência |
| `VALIDATION_ERROR` | definitiva (bug nosso ou campo exigido pela organização) | logar `errors` completo (sem PII); ver 07, T10 |
| `CONFLICT` | definitiva no turno | ex.: `link-ticket` em chamado preso a outro atendimento → informar e transferir |
| `WHATSAPP_WINDOW_CLOSED` | definitiva | cliente não fala há > 24 h → encerrar episódio local; **não** enviar template na v1 |
| `WHATSAPP_NOT_CONFIGURED`, `TEMPLATE_*`, `MEDIA_*` | configuração / não usado na v1 | alerta |
| `RATE_LIMITED` | transitória | backoff (respeitar `Retry-After` se vier); **nunca** repetir um `POST /tickets` sem checar duplicata (`GET /tickets?phone=`) |
| `INTERNAL_ERROR` / timeout / rede | transitória | retry curto (2 s, 5 s) **somente** em operações idempotentes (GET, `POST /requesters`); criação de ticket só após reconciliar por listagem (ADR 0005) |

Mensagem de erro do Infodesk **nunca** vai ao cliente final nem ao prompt do
LLM em claro (A2 do Zapin: vazamento de erro interno). O `code` pode virar
`motivo` do contrato de tool (`{"status": false, "motivo": "…"}`).
