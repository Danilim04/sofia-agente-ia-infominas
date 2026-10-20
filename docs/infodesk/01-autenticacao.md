# 01 — Autenticação, escopos e configuração da chave

Fonte: PDF v1 (2026-10-19), "Passo a passo no Desk: criar a chave" e seção
"2. Autenticação e escopos".

## Como a chave nasce (lado Infominas, painel do Desk)

Feito por um **administrador** em **Configurações › Integrações**:

1. A tela mostra a **URL base da API** (botão de copiar), o formulário de nova
   chave e a lista de chaves ativas.
2. Nova chave: **Nome** (aparece como **autor das mensagens do bot** no inbox),
   **permissões** (escopos; "o mínimo necessário" ou *Acesso total*),
   **Conduz o atendimento** (liga o modo *bot externo* — ver
   [08-bot-externo.md](08-bot-externo.md); **só uma chave por organização**
   pode ter isso), **URL do webhook** e os **eventos** a receber (agrupados).
3. Ao criar, o Desk mostra **uma única vez** a chave de API (`ik_live_…`) e o
   **segredo do webhook** (`whsec_…`). São dois segredos distintos.
4. **Enviar teste**: o Desk faz um `POST` assinado com o evento `ping` e mostra
   o HTTP e o tempo de resposta (também via `POST /webhook/test`, ver 05).
5. **Entregas**: log de cada evento enviado (Entregue / Na fila / Falhou),
   tentativas, resposta do nosso servidor e **Reentregar** manual.
6. **Editar** troca escopos, eventos, URL e modo bot sem gerar outra chave.
   **Rotacionar segredo** ao trocar de servidor. **Revogar** se vazar.

## Enviar a chave

Em **todas** as chamadas, de uma das duas formas (equivalentes):

```http
Authorization: Bearer ik_live_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

```http
X-Api-Key: ik_live_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

Recomendação nossa: `Authorization: Bearer`.

## Testar a chave — `GET /me`

```json
{ "organizationId": "…", "keyId": "…", "name": "Bot de atendimento",
  "scopes": ["tickets:read", "conversations:write", "whatsapp:send"],
  "handlesConversations": true }
```

`organizationId` daqui é o valor a conferir contra o `organizationId` dos
webhooks; `handlesConversations` diz se a chave é o bot externo.

## Escopos

| Escopo | Libera |
|---|---|
| `tickets:read` | listar e consultar chamados |
| `tickets:write` | abrir, alterar, comentar, responder, encerrar, reabrir, cancelar, escalar chamados; alertas |
| `conversations:read` | listar e ler conversas e mensagens, retroativo de mensagens, anexos, janela de 24 h |
| `conversations:write` | enviar mensagem em conversa, nota interna, atribuir, transferir, encerrar, handoff, vínculos |
| `whatsapp:send` | enviar WhatsApp para um número (inclusive novo) |
| `contacts:read` | identificar telefone, buscar clientes e solicitantes |
| `contacts:write` | cadastrar solicitante |
| `events:read` | feed de eventos e log/reentrega de webhooks |
| `*` | tudo |

**Dados de referência** (`/desks`, `/desks/:id`, `/users`, `/departments`,
`/catalog`, `/whatsapp/templates`) valem para **qualquer chave válida**, sem
escopo específico.

Escopo insuficiente → `403 FORBIDDEN` (ver 06).

## Erros relacionados

| HTTP | `code` | Significado |
|---|---|---|
| 401 | `UNAUTHORIZED` | chave ausente, inválida ou revogada |
| 403 | `FORBIDDEN` | escopo insuficiente |

## Implicações para o nosso lado

- Dois segredos em config: `INFODESK_API_KEY` (`ik_live_…`) e
  `INFODESK_WEBHOOK_SECRET` (`whsec_…`). Nunca em código, log ou prompt;
  mascarar nos logs.
- Escopos que a Sofia precisa na v1 (menor privilégio):
  `tickets:read`, `tickets:write`, `conversations:read`, `conversations:write`,
  `contacts:read`, `events:read`. `contacts:write` só se o plano adotar
  `POST /requesters` (ver 07, N3); `whatsapp:send` só para mensagens
  proativas/templates (fora da v1).
- A chave da Sofia deve ter **Conduz o atendimento** ligado — é o que faz a
  conversa nascer com `handledBy: EXTERNAL_BOT` e silencia os atendentes até o
  handoff (08). O **nome da chave** é o nome que o atendente vê como autor das
  mensagens do bot: usar "Sofia" (ou o que a Infominas preferir).
- `GET /me` no boot: valida chave, confere `organizationId` esperado e
  `handlesConversations == true`; divergência = erro de configuração explícito.
- Pedir **chave separada para homologação** se existir ambiente (07, A1).
