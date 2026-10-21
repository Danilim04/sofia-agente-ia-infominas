# 08 — Bot externo (modo "conduz o atendimento") e fluxo de referência

Fonte: PDF v1 (2026-10-19), seções "7. Bot externo (triagem)", "10. Fluxo de
referência: bot de triagem", "Passo a passo" item 7 e "12. Boas práticas".
É o modo em que a Sofia opera.

## Regras do modo bot externo

Com **Conduz o atendimento** ligado na chave (**só uma chave por
organização**):

1. Todo atendimento novo de **WhatsApp individual** (1ª mensagem do número, ou
   nova sessão após 24 h ou após o encerramento) **nasce com o bot**:
   `handledBy: "EXTERNAL_BOT"` já no primeiro `message.received`.
2. O chatbot interno do Desk **não roda** e os atendentes **não são chamados**
   (sem som, push nem sino). A conversa aparece no inbox com o selo **Bot** e
   as mensagens do bot assinadas pelo **nome da chave**. **Um atendente pode
   assumir a qualquer momento (isso tira o bot).**
3. O bot conversa por `POST /conversations/:id/messages` (04). Mensagem
   enviada pela integração não tira o bot de cena.
4. Para passar a humanos: **handoff**.
5. `POST /conversations/:id/bot { "active": true|false }` assume ou solta a
   conversa **sem avisar ninguém**.

**Grupos de WhatsApp** e o **chat do agente desktop** (que é sobre chamado já
aberto) **não passam pelo bot**.

## Handoff — `POST /conversations/:id/handoff` (`conversations:write`)

```json
{ "departmentId": "…",
  "note": "Cliente Maria (ACME). Impressora HP do financeiro sem imprimir desde ontem. Chamado #4521 aberto." }
```

Efeitos: o bot sai; a conversa **volta para a fila** (ou vai para o `userId`
informado, ou para a **distribuição automática** da organização); a `note`
vira **nota interna**; os atendentes são **notificados**. Evento
`conversation.handoff`.

Campos: `departmentId` (opcional), `userId` (opcional), `note`. Se `note` é
obrigatório e se há limite de tamanho: **não dito**.

## Fluxo de referência do PDF (bot de triagem)

```
Cliente manda "oi" no WhatsApp
└─► webhook message.received (handledBy=EXTERNAL_BOT, conversation.started antes)
1. GET /contacts/lookup?phone=…            → quem é, cliente, chamados abertos
2. POST /conversations/:id/messages        → "Olá Maria! É sobre o chamado #4510 ou algo novo?"
3. (cliente responde → message.received)
4a. Assunto novo:
    POST /conversations/:id/requester {id}            (se identificado)
    ou POST /requesters {name, clientId, phone, conversationId}  (se novo)
    POST /conversations/:id/ticket {deskId, title, description, priorityId}
    POST /conversations/:id/messages "Abri o chamado #4521 para você."
4b. Chamado existente:
    POST /conversations/:id/link-ticket {ticketNumber: 4510}
    POST /tickets/4510/notes "Cliente informou que…"
5. Precisa de humano?   POST /conversations/:id/handoff {departmentId, note: "resumo"}
   Resolveu sozinho?    POST /conversations/:id/finish
└─► reconciliação periódica: GET /events?after=<último seq>
```

Proativo (o Desk inicia, **fora da v1**): `POST /whatsapp/messages { to,
template }` → `message.status` → cliente responde → `message.received`
(janela aberta: texto livre liberado).

## Boas práticas (seção 12, literal em resumo)

- Responder o webhook em menos de 8 s e processar numa fila.
- Deduplicar por `id` do evento; reconciliar por `GET /events?after=` ao
  iniciar e periodicamente.
- Guardar o `conversationId`; usar **`handledBy` / `botActive`** para saber se
  ainda é a vez do bot falar.
- Antes de mandar texto livre para quem não falou nas últimas 24 h, consultar
  `/whatsapp/window` ou tratar `409 WHATSAPP_WINDOW_CLOSED` enviando template.
- Uma chave por sistema, com o mínimo de escopos; revogar chaves vazadas;
  rotacionar o segredo do webhook ao trocar de servidor.

## Como a Sofia mapeia isto (hipóteses de trabalho; decisões no plano)

| Momento | Rota | Observação |
|---|---|---|
| Mensagem chega | `message.received` com `handledBy == EXTERNAL_BOT`, `channel == WHATSAPP`, `groupJid == null`, `sender == CLIENT` | qualquer outra combinação → só estado/log (07, N5) |
| Humano assumiu | `conversation.assigned` ou `handledBy != EXTERNAL_BOT` em qualquer evento | estado local `transferida`; bot cala até `conversation.finished` (regra ZD-1 mantida, agora com sinal real) |
| Identificar | `GET /contacts/lookup?phone=` | primeira tentativa; CPF/contrato por código quando não resolve (07, N3; ADR 0004) |
| Conversar | `POST /conversations/:id/messages { text }` | bolhas ≤ 900 chars |
| Resumo para a equipe | `POST /conversations/:id/messages { text, internal: true }` | opcional; evita rodapé no ticket |
| Abrir chamado | `POST /conversations/:id/ticket { deskId, title, description, priorityId, period: "session", finish: false }` | preferido a `POST /tickets` (03); duas fases + idempotência local (ADR 0005) |
| Chamado já existe | `POST /conversations/:id/link-ticket` + `POST /tickets/:n/notes` | quando o cliente fala de chamado aberto (`lookup.openTickets`) |
| Transferir para humano | `POST /conversations/:id/handoff { departmentId, note }` | substitui `/department` como caminho único de transferência; `note` = resumo gerado por código a partir da classificação + proposta (não texto livre do LLM sem sanitizar) |
| Encerrar | `POST /conversations/:id/finish` | só quando o cliente diz que não precisa de mais nada **e** não há chamado pendente (regra de negócio, 07, C6) |
| Nunca | `POST /conversations/:id/bot { active: false }`, `/assign`, `/whatsapp/messages`, `/reply`, `/close` | fora da v1 ou sem sinal ao cliente (07, N4) |
