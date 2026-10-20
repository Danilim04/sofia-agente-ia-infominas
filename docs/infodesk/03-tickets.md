# 03 — Chamados (tickets)

Fonte: PDF v1 (2026-10-19), seção "9. Chamados" e "11. Outras rotas".
Prefixo de todas as rotas: `/integration/v1`.

## 9.1 Objeto `ticket` (literal do PDF)

```json
{
  "id": "t1…", "number": 4521, "title": "Impressora do financeiro não imprime",
  "description": "…", "channel": "WHATSAPP", "ticketType": "INCIDENT",
  "impact": null, "urgency": null, "priorityLevel": null,
  "desk": { "id": "d1…", "name": "Suporte" },
  "status": { "id": "s1…", "name": "Aberto", "isOpen": true, "isClose": false, "isCanceled": false },
  "stage": { "id": "e1…", "name": "Triagem" },
  "priority": { "id": "p1…", "name": "Alta" },
  "catalogItemId": null,
  "client": { "id": "k1…", "name": "ACME Ltda" },
  "requester": { "id": "r1…", "name": "Maria Souza", "email": "…", "phone": "+5531990000000" },
  "resourceId": null, "responsible": { "id": "u1…", "username": "joao", "email": "…" },
  "contactPhone": null, "conversationId": "c1d2…",
  "isClosed": false, "closedAt": null, "isDraft": false,
  "serviceDueAt": "…", "solutionDueAt": "…", "firstRespondedAt": null,
  "customValues": null, "createdAt": "…", "updatedAt": "…"
}
```

Pontos que importam:

- **`number` é inteiro** (4521) e é a chave pública do chamado (todas as rotas
  usam `:number`). `id` é UUID interno.
- **Estado** vem do objeto `status` (`isOpen`/`isClose`/`isCanceled`) e dos
  atalhos `isClosed`/`closedAt`. Os nomes de status são **por mesa**.
- `conversationId` liga o chamado à conversa de origem; `contactPhone` é o
  telefone informado na abertura (pode ser `null` quando há `requester`).
- `GET /tickets/:number` traz também **`messages`** (histórico público do
  chamado). Formato das mensagens: **NÃO DOCUMENTADO** (presumivelmente o
  mesmo de 04/05).
- Prazos SLA: `serviceDueAt`, `solutionDueAt`, `firstRespondedAt`.

## 9.2 Rotas

| Método | Rota | Escopo | Uso |
|---|---|---|---|
| GET | `/tickets?status=open\|closed\|all&deskId&clientId&requesterId&responsibleId&phone&updatedSince&limit&cursor` | `tickets:read` | lista (padrão: **abertos**); paginada |
| GET | `/tickets/:number` | `tickets:read` | chamado + histórico público |
| POST | `/tickets` | `tickets:write` | abre (campos abaixo) |
| PATCH | `/tickets/:number` | `tickets:write` | `title`, `description`, `statusId`, `stageId`, `priorityId`, `responsibleId`, `catalogItemId`, `clientId`, `requesterId`, `impact`, `urgency` (com `reason` ao mudar a prioridade ITIL), `customValues` |
| POST | `/tickets/:number/notes` `{ "body" }` | `tickets:write` | nota interna |
| POST | `/tickets/:number/reply` `{ "body" }` | `tickets:write` | resposta ao solicitante (e-mail/WhatsApp, conforme o chamado) |
| POST | `/tickets/:number/close` | `tickets:write` | encerra (respeita checklist/regras da mesa) |
| POST | `/tickets/:number/reopen` | `tickets:write` | reabre |
| POST | `/tickets/:number/cancel` `{ "reason" }` | `tickets:write` | cancela |
| POST | `/tickets/:number/escalate` `{ "deskId" }` | `tickets:write` | muda de mesa |
| POST | `/tickets/:number/requester` `{ "id" }` | `tickets:write` | vincula/desvincula (`null`) o solicitante; o cliente vem junto se o chamado não tiver |
| POST | `/conversations/:id/ticket` | `tickets:write` + `conversations:write` | **abre chamado a partir da conversa** (abaixo) |
| POST | `/conversations/:id/link-ticket` `{ "ticketNumber", "finish?" }` | `conversations:write` | vincula a chamado existente (anexa a transcrição da sessão) |
| POST | `/alerts` | `tickets:write` | alerta de ferramenta externa (RMM/firewall/backup) vira chamado, deduplicado por origem + tipo + recurso. Corpo **NÃO DOCUMENTADO** |

## Abrir chamado — `POST /tickets`

**`deskId` e `title` obrigatórios.** Opcionais: `description`, `channel`
(`WHATSAPP`, `WEB`, `EMAIL`, `CHAT`, `AGENT`), `clientId`, `requesterId`,
`contactPhone`, `priorityId`, `catalogItemId`, `statusId`, `stageId`,
`responsibleId`, `ticketType`, `impact`, `urgency`, `customValues`.

> "Se a organização exige campos (cliente, solicitante, serviço, prioridade),
> a falta vira `400`." — quais campos a **Infominas** exige: **NÃO
> DOCUMENTADO** (07, T10). O inventário de catálogos (09) sugere que
> "serviço" (`catalogItemId`) pode ser exigido em algumas mesas.

Resposta: o ticket criado (objeto acima). Código HTTP de sucesso **não
citado** (o exemplo de `/whatsapp/messages` usa `201`; aceitar 200/201).

## Abrir chamado a partir da conversa — `POST /conversations/:id/ticket`

```json
{ "deskId": "d1…", "title": "Impressora do financeiro não imprime",
  "description": "Resumo da triagem: HP M428 do financeiro, erro 49. Reiniciar não resolveu.",
  "priorityId": "p1…", "catalogItemId": "i1…", "period": "session", "finish": false }
```

- A **transcrição da conversa** entra na descrição, abaixo do resumo.
  `period`: `session` (padrão), `today`, `24h`, `all`.
- `finish: true` **encerra a conversa** e o cliente recebe a **mensagem
  configurada** (no Desk) com o número do chamado.
- Presume-se que o chamado sai vinculado à conversa (`conversationId`) e ao
  cliente/solicitante vinculados a ela — **não explicitado** (07, T11).
- Não há chave de idempotência; o PDF não diz o que acontece se a conversa já
  tem `ticketNumber` (07, T12).

## Vincular a chamado existente — `POST /conversations/:id/link-ticket`

`{ "ticketNumber": 4510, "finish": false }` — anexa a transcrição da sessão ao
chamado; o fluxo de referência (08) usa em seguida
`POST /tickets/4510/notes` para registrar o que o cliente disse.

## Responder ao solicitante — `POST /tickets/:number/reply`

"Resposta ao solicitante (e-mail/WhatsApp, conforme o chamado)". Gera evento
`ticket.replied` e, pelo quadro de eventos, também `message.sent` ("resposta
do chamado"). Se chega **na mesma conversa de WhatsApp** do bot: **não
explicitado** (07, T7 continua aberto).

## Implicações para o nosso lado

- **Idempotência continua nossa** (ADR 0005): nenhuma das rotas de criação
  tem chave de idempotência. Mas agora dá para **reconciliar** um `POST` sem
  resposta: `GET /tickets?phone=<E.164>&status=open` (ou pelo
  `conversationId` no objeto) descobre se o ticket foi criado antes de qualquer
  nova tentativa. Isso **desbloqueia** o caso `infodesk_indisponivel` de
  `01-ferramentas.md` A3.
- **Preferir `POST /conversations/:id/ticket`** a `POST /tickets` para
  chamados que nascem no WhatsApp: a transcrição vai junto, o vínculo com a
  conversa é automático e `finish` opcional. Decisão a registrar no plano
  (ver 07, T11/T12 e a nota de impacto no PR).
- "Já tenho um chamado aberto?" → `GET /tickets?phone=&status=open` ou
  `GET /contacts/lookup?phone=` (`openTickets`). Resolve T5.
- Status do chamado para o cliente: usar `status.name` + `isClosed`; nunca
  inventar nome de status (são por mesa).
- `priorityId`: resolver na mesa de destino (02); `catalogItemId`: só se a
  taxonomia mapear (09).
- `title`/`description` continuam escritos pelo nosso lado (LLM propõe, código
  formata, cliente confirma). Rodapé `[Aberto pela Sofia · conversa <id>]`
  fica **opcional** agora que `conversationId` existe no objeto.
- Fechamento: `POST /close` existe, mas "respeita checklist/regras da mesa"
  → pode falhar com 4xx; a v1 **não fecha** chamados (regra de negócio
  pendente, 07, T6).
