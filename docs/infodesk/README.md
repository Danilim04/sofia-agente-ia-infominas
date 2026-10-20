# API de Integração — Infominas Desk (Infodesk)

Documentação da API do **Infodesk** (sistema da Infominas de atendimento
WhatsApp/chat + gestão de chamados), convertida do PDF
`fonte-api-integracao-v1-2026-10-19.pdf` ("Documentação técnica — API de
Integração — Infominas Desk", **v1, atualizado em 19 de outubro de 2026**,
25 páginas). Substitui a versão anterior (guia de 5 páginas, guardado em
`historico/2026-06-guia-para-sistemas-parceiros.pdf`), que deixava a maior
parte do comportamento em aberto.

**Tudo que não está no PDF está marcado como "NÃO DOCUMENTADO"** e consolidado
em [07-pontos-ambiguos.md](07-pontos-ambiguos.md). Nenhum campo, valor ou
comportamento foi inventado: se um agente precisar de algo que não está aqui,
a resposta certa é "perguntar à Infominas", não assumir.

## Para quem lê isto

Agentes (Claude Code) implementando o cliente HTTP do Infodesk, as tools do
agente de IA e o receptor de webhooks. Leia o índice, abra só a área que vai
implementar e **sempre** leia o 07 antes de fixar um contrato.

## O que mudou em relação ao guia anterior (resumo para quem já implementou)

| Área | Antes (guia de 5 páginas) | Agora (v1 de 2026-10-19) |
|---|---|---|
| Assinatura do webhook | `HMAC(secret, corpo)` | **`HMAC(secret, "<timestamp>.<corpo>")`** + header `X-Infominas-Timestamp`; recusar > 5 min (05) |
| Entrega de webhook | best-effort, sem reentrega | **pelo menos uma vez**, reentrega 30 s · 2 min · 10 min · 1 h · 6 h; `seq`; feed `GET /events?after=` (05) |
| Eventos | 3 | **16** (mensagens, status de entrega, atribuição, handoff, departamento, ticket.*, ping) (05) |
| Bot ↔ humano | nenhum sinal | `handledBy`/`botActive` na conversa, `conversation.assigned`, `POST /handoff`, `POST /bot` (08) |
| Cliente/solicitante | sem busca | `GET /contacts/lookup?phone=`, `/clients`, `/requesters`, `POST /requesters` idempotente (03/04) |
| Tickets | abrir, ler, responder | + listar, `PATCH`, notas, fechar, reabrir, cancelar, escalar, abrir **a partir da conversa**, vincular (03) |
| Prioridades/status/etapas | sem rota | `GET /desks/:id` traz `statuses`, `stages`, `priorities`, `catalogItems` (02) |
| Mídia | não documentada | `type` TEXT/IMAGE/FILE/AUDIO/CONTACT, `attachment.url`, `GET /attachments/:id`; envio de `media` (04) |
| WhatsApp | só via conversa | janela de 24 h, templates, `POST /whatsapp/messages` para iniciar conversa (04) |
| Erros | só `statusCode` + `message` | **`code`** estável para decidir; rate limit 300/min por IP (06) |
| Paginação | não documentada | `{ data, nextCursor }`, `limit` 50 (máx. 100) |

## Fatos básicos

| Item | Valor |
|---|---|
| Base URL | A capa do PDF traz `https://infodesk.infominas.net/v1/integration/v1`; a versão anterior trazia `https://infodesk.infominas.net/api/integration/v1`. O PDF manda **copiar a URL base da tela Configurações › Integrações**. Configurável (`INFODESK_BASE_URL`); ver 07, A6 |
| Formato | JSON em todas as requisições e respostas |
| Autenticação | chave `ik_live_…` em **toda** chamada, `Authorization: Bearer` ou `X-Api-Key` (01) |
| Autorização | 7 escopos na chave (`tickets:*`, `conversations:*`, `whatsapp:send`, `contacts:*`, `events:read`) ou `*`; dados de referência valem para qualquer chave (01) |
| Webhooks | POST JSON assinado (HMAC-SHA256 de `timestamp.corpo`), 2xx em 8 s, reentrega automática, pelo menos uma vez, retenção 30 dias (05) |
| Paginação | `{ "data": [...], "nextCursor": "…" }`; repetir com `cursor=<nextCursor>`; `null` = fim; `limit` padrão 50, máx. 100 salvo indicação |
| Rate limit | 300 req/min por IP → `429 RATE_LIMITED` (06) |
| Ambiente de homologação | **NÃO DOCUMENTADO** (só produção aparece) — 07, A1 |
| Provedor de WhatsApp | Gupshup, Evolution ou Meta — **abstraído** pela API (templates por nome, status `sent → delivered → read | failed`) |

## Índice

| Arquivo | Conteúdo |
|---|---|
| [01-autenticacao.md](01-autenticacao.md) | Criar a chave no Desk, headers, escopos, `GET /me`, segredo do webhook e rotação |
| [02-dados-referencia.md](02-dados-referencia.md) | Mesas (`/desks`, `/desks/:id`), catálogo, atendentes, departamentos, templates |
| [03-tickets.md](03-tickets.md) | Objeto `ticket`, abrir (direto ou a partir da conversa), listar, alterar, notas, resposta, fechar/reabrir/cancelar/escalar, vincular |
| [04-conversas.md](04-conversas.md) | Conversas, mensagens (texto/template/mídia/nota), janela de 24 h, WhatsApp, contatos e solicitantes, anexos |
| [05-webhooks.md](05-webhooks.md) | 16 eventos, envelope, assinatura com timestamp, reentrega, feed `/events`, retroativo, log de entregas |
| [06-erros.md](06-erros.md) | Tabela de `code`, formato, política de tratamento |
| [07-pontos-ambiguos.md](07-pontos-ambiguos.md) | **O que a documentação ainda não responde** + itens respondidos em 2026-10-19 |
| [08-bot-externo.md](08-bot-externo.md) | Modo "conduz o atendimento": `handledBy`, handoff, `/bot`, fluxo de referência de triagem |
| [09-inventario-mesas-catalogos.md](09-inventario-mesas-catalogos.md) | Mesas e catálogos reais da Infominas (observados no painel em 2026-10-20) — insumo do P1 |

## Mapa rápido de rotas (todas sob a base `/integration/v1`)

| Método | Rota | Escopo | Doc |
|---|---|---|---|
| GET | `/me` | qualquer | 01 |
| GET | `/desks`, `/desks/:id`, `/catalog?deskId=`, `/users`, `/departments`, `/whatsapp/templates` | qualquer chave válida | 02 |
| GET | `/tickets?status=&deskId=&clientId=&requesterId=&responsibleId=&phone=&updatedSince=&limit=&cursor=` | `tickets:read` | 03 |
| GET | `/tickets/:number` | `tickets:read` | 03 |
| POST | `/tickets` | `tickets:write` | 03 |
| PATCH | `/tickets/:number` | `tickets:write` | 03 |
| POST | `/tickets/:number/notes` · `/reply` · `/close` · `/reopen` · `/cancel` · `/escalate` · `/requester` | `tickets:write` | 03 |
| POST | `/conversations/:id/ticket` | `tickets:write` + `conversations:write` | 03 |
| POST | `/conversations/:id/link-ticket` | `conversations:write` | 03 |
| POST | `/alerts` | `tickets:write` | 03 |
| GET | `/conversations?tab=&status=&channel=&assignedUserId=&departmentId=&botActive=&phone=&updatedSince=&limit=&cursor=` | `conversations:read` | 04 |
| GET | `/conversations/:id` · `/conversations/:id/messages?after=` · `/messages?since=` · `/attachments/:id` | `conversations:read` | 04 |
| POST | `/conversations/:id/messages` · `/assign` · `/department` · `/finish` · `/client` · `/requester` | `conversations:write` | 04 |
| POST | `/conversations/:id/handoff` · `/conversations/:id/bot` | `conversations:write` | 08 |
| GET | `/whatsapp/window?phone=` | `conversations:read` | 04 |
| POST | `/whatsapp/messages` | `whatsapp:send` | 04 |
| GET | `/contacts/lookup?phone=` · `/clients?q=|document=` · `/requesters?…` · `/requesters/:id` | `contacts:read` | 04 |
| POST / PATCH | `/requesters` · `/requesters/:id` | `contacts:write` | 04 |
| GET | `/events?after=&limit=&types=&conversationId=&ticketNumber=` · `/deliveries?status=` | `events:read` | 05 |
| POST | `/deliveries/:id/redeliver` · `/webhook/test` | `events:read` / — | 05 |

## O que a API ainda NÃO oferece (relevante para o desenho do agente)

- Nenhuma **chave de idempotência** em `POST /tickets` nem em
  `POST /conversations/:id/ticket` — não duplicar continua responsabilidade
  nossa (ADR 0005). Só `POST /requesters` é idempotente por telefone/e-mail.
- Nenhum **ambiente de homologação** documentado.
- Nenhuma rota de **busca de ticket por texto**; a listagem filtra por
  `phone`, `clientId`, `requesterId`, `deskId`, `status`.
- Nenhum **limite de tamanho** documentado para `title`, `description`, `body`.
- Nenhuma confirmação de que os eventos gerados **pelas nossas próprias
  chamadas** (ex.: `conversation.finished` após o nosso `/finish`,
  `message.sent` após o nosso `/messages`) chegam ao nosso webhook — assumir
  que sim (o PDF diz que `message.sent` cobre "esta API").

Ver [07-pontos-ambiguos.md](07-pontos-ambiguos.md) para a lista completa.
