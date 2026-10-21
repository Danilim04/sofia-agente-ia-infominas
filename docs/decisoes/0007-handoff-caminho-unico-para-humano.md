# 0007 — Handoff é o caminho único de transferência para humano

- **Estado:** aceita (2026-10-20); substitui parcialmente 0003
- **Etapa:** planejamento (E2.1, E3.1, E3.3; correções de E1.2 e E1.4)
- **Pendências relacionadas:** `docs/infodesk/07` C4, C6, C10, D6, N1, N4; P11

## Contexto

A API v1 do Infodesk (2026-10-19) introduziu o modo **bot externo**: a
conversa de WhatsApp nasce com `handledBy: EXTERNAL_BOT`, os atendentes não
são chamados e existe `POST /conversations/:id/handoff { departmentId?,
userId?, note }` que tira o bot, devolve a conversa à fila (ou a uma pessoa,
ou à distribuição automática), grava a `note` como nota interna e
**notifica** os atendentes (`docs/infodesk/08`). O plano antigo (0003)
transferia com `POST /conversations/:id/department`, que na v1 só muda a
fila **sem soltar o bot**; `/assign` avisa o cliente com mensagem do Desk que
não controlamos; `POST /conversations/:id/bot { active: false }` solta sem
avisar ninguém.

## Decisão

Toda transferência para humano — tool `transferir_para_atendente`, pedido
explícito detectado no Go, mídia, falha do cérebro/disjuntor, frustração,
baixa confiança, horário, vigia de conversa sem resposta e handoff pós-ticket
(`TRANSFERIR_APOS_TICKET`) — passa por **um único ponto** da engine
(`transferencia.go`) que chama `infodesk.Handoff(convID, {DepartmentID,
Note})`. `departmentId` vem de `taxonomia[categoria].departamento` ou
`DEPARTAMENTO_HUMANO_PADRAO` (D6); `userId` não é usado até P11. A **`note`
é montada por código** (motivo, categoria + confiança, identificação
mascarada, nº do ticket) — nunca texto livre do LLM. A mensagem de aviso ao
cliente é enviada pela Sofia **antes** do handoff (o handoff não fala com o
cliente, C10). Em seguida `conversas.estado = transferida`; o gateway cala
até `conversation.finished`. `/department`, `/assign` e `/bot` ficam fora da
v1 (`/department` e `/assign` permanecem no cliente Go, sem chamador; `/bot`
não é implementado). A `Transporte` da engine troca `TransferirDepartamento`
por `Handoff`. O inverso também vale: `conversation.assigned` ou
`handledBy != EXTERNAL_BOT` em qualquer evento marca `transferida` mesmo sem
handoff nosso (humano assumiu), com métrica `humano_assumiu_sem_handoff`.

## Consequências

- Atendentes são notificados e recebem o resumo da triagem como nota
  interna; nenhuma dependência de configuração de chat do Desk.
- Se o handoff falhar (5xx/rede), o gateway marca `transferida` localmente
  mesmo assim (bot cala) e o vigia/reconciliador repete; alerta
  `handoff_falhou`.
- Rever quando: a Infominas definir os departamentos de chat (D6) e se o
  Desk tem timeout próprio para bot sem resposta (N1); P11 mapear fila →
  pessoa (`userId`); C6 decidir `finish` após ticket.

## Alternativas descartadas

- `/department` (0003) — não solta o bot nem notifica; o atendente veria a
  conversa com selo "Bot" e o bot continuaria respondendo.
- `/assign` para uma pessoa fixa — mensagem ao cliente fora do nosso
  controle (C10) e exige mapear filas a pessoas (P11).
- `/bot { active: false }` — ninguém é avisado; cliente fica em `QUEUE` sem
  sinal (N4).
