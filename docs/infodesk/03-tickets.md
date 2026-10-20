# 03 — Tickets

Fonte: PDF, seção "3. Tickets". Três operações: abrir, consultar por número,
responder ao solicitante.

## Abrir ticket — `POST /integration/v1/tickets` (escopo `tickets:write`)

Campos: **`deskId` e `title` são obrigatórios**; os demais são opcionais.

```bash
curl -X POST https://infodesk.infominas.net/api/integration/v1/tickets \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{
    "deskId": "<uuid-da-mesa>",
    "title": "Impressora não imprime",
    "description": "Cliente relata erro ao imprimir.",
    "channel": "WEB",
    "clientId": "<uuid-opcional>",
    "requesterId": "<uuid-opcional>",
    "contactPhone": "+5531990000000",
    "priorityId": "<uuid-opcional>"
  }'
```

| Campo | Obrig. | Tipo (observado) | Observação |
|---|---|---|---|
| `deskId` | sim | uuid | mesa de serviço (de `GET /desks`) |
| `title` | sim | string | título do chamado |
| `description` | não | string | descrição livre |
| `channel` | não | string | exemplo usa `"WEB"`; no webhook aparece `"WHATSAPP"`. **Lista de valores NÃO DOCUMENTADA** |
| `clientId` | não | uuid | id do cliente (empresa?) — **não há rota para obtê-lo** |
| `requesterId` | não | uuid | id do solicitante (pessoa?) — **não há rota para obtê-lo** |
| `contactPhone` | não | string E.164 | `+55DDDNÚMERO` |
| `priorityId` | não | uuid | **não há rota para listar prioridades** |

**Resposta:** "o ticket criado (inclui `number`)". O PDF **não mostra o JSON**
da resposta nem o status HTTP (presumivelmente `201` ou `200`). Só o campo
`number` é garantido. Veja 07, item T1.

## Consultar ticket — `GET /integration/v1/tickets/:number` (escopo `tickets:read`)

Consulta **pelo número** (`number`, devolvido na criação). Schema da resposta
**NÃO DOCUMENTADO** (status? mesa? histórico? datas?). Não existe listagem de
tickets nem busca por telefone/contato.

## Responder ao solicitante — `POST /integration/v1/tickets/:number/reply` (escopo `tickets:write`)

```json
{ "body": "Olá! Já estamos verificando seu chamado." }
```

"A resposta vai ao solicitante pelos canais configurados (e-mail/WhatsApp),
como uma resposta de atendente."

Observação: isto é diferente de `POST /conversations/:id/messages` (04). O
`reply` fala com o **solicitante do ticket** pelos canais do ticket; o
`messages` fala com o **contato da conversa** de chat/WhatsApp. O PDF não
explica se um ticket aberto com `contactPhone` de uma conversa WhatsApp ativa
faz o `reply` cair **na mesma conversa** ou numa nova sessão — ver 07, item T4.

## Implicações para o nosso lado

- **Idempotência é nossa**: a API não tem chave de idempotência. Antes de abrir,
  o nosso gateway precisa checar na **própria** base se já existe ticket para
  (conversa, episódio) e, em caso de retry de rede após `POST` sem resposta,
  **não** reenviar cegamente (padrão "proposta → confirmação → execução única"
  do Zapin, `toolsapi.go`, ZB-1).
- `title`/`description` são escritos pelo nosso lado a partir da conversa — o
  LLM **propõe**, o código **formata** e o cliente **confirma** antes do POST
  (LLM08, duas fases).
- `deskId` e `priorityId` são decididos por **classificação determinística +
  tabela de configuração**, nunca pelo modelo livre (ver `docs/plano/02-classificacao.md`).
- `contactPhone` vem do webhook (`data.contact.phone`), já em E.164.
- `channel`: usar `"WHATSAPP"` quando a origem for a conversa WhatsApp
  (valor observado no webhook) — **confirmar** com a Infominas que é aceito no
  POST (07, T2).
