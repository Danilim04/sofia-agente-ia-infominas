# 04 — Conversas (chat / WhatsApp)

Fonte: PDF, seção "4. Conversas (chat / WhatsApp)". Uma *conversa* é a sessão
de atendimento de um contato (WhatsApp ou chat web). É por aqui que o agente
**fala com o cliente**, **transfere para humano** e **encerra**.

## Listar — `GET /integration/v1/conversations` (escopo `conversations:read`)

Query opcional:

| Param | Valores | Observação |
|---|---|---|
| `tab` | `entrada` \| `meus` \| `atendimento` \| `encerrados` | semântica exata **NÃO DOCUMENTADA** (ver 07, C1); `meus` presumivelmente relativo ao usuário dono da chave |
| `departmentId` | uuid | filtra por departamento |

Schema da resposta **NÃO DOCUMENTADO** (lista de quê? paginação? campos como
`assignedUserId`, `departmentId`, `status`, `contact`?). Uso recomendado pelo
PDF: **reconciliação** periódica, já que webhooks são best-effort.

## Ler uma conversa (com mensagens) — `GET /integration/v1/conversations/:id` (escopo `conversations:read`)

Devolve a conversa com suas mensagens. Schema **NÃO DOCUMENTADO**; o único
formato de mensagem conhecido é o do webhook:
`{ "id", "body", "sender": "CLIENT", "createdAt" }`. Outros valores de `sender`
(atendente? sistema? bot?) não estão documentados.

## Enviar mensagem — `POST /integration/v1/conversations/:id/messages` (escopo `conversations:write`)

```json
{ "body": "Posso ajudar em algo mais?" }
```

- Conversa de **WhatsApp**: a mensagem é entregue ao contato pelo provedor
  automaticamente.
- Conversa de **chat**: fica disponível na conversa.
- Só `body` (texto). Mídia, templates, botões: **não documentados**.
- Limite de tamanho: **não documentado** (o WhatsApp impõe ~4096 chars; o
  Zapin quebra em 900 — manter).
- Resposta da chamada: **não documentada** (id da mensagem? 201?).

É a rota que a "Dica" do PDF manda usar para responder a `message.received`:
`POST /integration/v1/conversations/{conversationId}/messages`.

## Atribuir a um atendente (transferir) — `POST /integration/v1/conversations/:id/assign` (escopo `conversations:write`)

```json
{ "userId": "<uuid-do-atendente>" }
```

`userId` vem de `GET /users`. O PDF não diz o que acontece com a conversa
(muda de `tab`? o atendente é notificado? o webhook `message.received` continua
chegando ao parceiro?) — ver 07, C3/C4. Também não há forma documentada de
**desatribuir** (`null`?) nem de atribuir a uma **fila** sem escolher pessoa.

## Transferir de departamento — `POST /integration/v1/conversations/:id/department` (escopo `conversations:write`)

```json
{ "departmentId": "<uuid-do-departamento>" }
```

`// ou null para remover` (texto literal do PDF): `{"departmentId": null}`
remove o departamento. `departmentId` vem de `GET /departments`.

Esta é a rota natural para **roteamento para fila**: o agente classifica, o
código escolhe o departamento e transfere. Se a Infominas usa "fila" =
departamento ou = mesa, é decisão de negócio pendente (07, D1).

## Encerrar — `POST /integration/v1/conversations/:id/finish` (escopo `conversations:write`)

Sem corpo documentado. Encerra a conversa. O PDF não diz se dispara
`conversation.finished` para o próprio parceiro (loop a tratar) nem se uma
nova mensagem do contato reabre a mesma conversa ou cria outra (07, C5).

## Implicações para o nosso lado

- O **id da conversa** (`conversationId` do webhook) é a chave natural de
  isolamento do agente (equivale ao `conversation_id` do Chatwoot no Zapin:
  `thread_id` do grafo, fila FIFO por conversa, estado do gate).
- "Transferir para humano" tem **duas** rotas candidatas: `/department`
  (fila) e `/assign` (pessoa). O plano assume `/department` como padrão e
  `/assign` só se a Infominas mapear filas a pessoas — decisão pendente.
- **Detectar que um humano assumiu** (para o bot calar) não tem sinal
  documentado no webhook; a hipótese de trabalho é consultar
  `GET /conversations/:id` e olhar um campo de atribuição — que **não está
  documentado**. Ponto crítico, ver 07, C3.
