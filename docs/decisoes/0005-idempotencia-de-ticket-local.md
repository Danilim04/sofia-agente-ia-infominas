# 0005 — Idempotência e dedup de tickets no gateway

- **Estado:** aceita (2026-10-19); **revisada em 2026-10-20** (seção "Revisão" abaixo; a chave local continua)
- **Etapa:** planejamento (E3.1, E3.3)
- **Pendências relacionadas:** P3 (respondida em 2026-10-19), T5 (respondida), T8 (respondida), `docs/infodesk/07` T11, T12

## Revisão 2026-10-20 — API v1: listagem existe, idempotência não

`POST /tickets` e `POST /conversations/:id/ticket` continuam **sem chave de
idempotência** (`docs/infodesk/03`), então a tabela `tickets` com
`chave_idempotencia`, o `INSERT … pendente` antes do POST e a regra "nunca
reenviar o POST" **permanecem**. O que muda:

1. **Reconciliação por listagem** passa a existir: `GET
   /tickets?phone=<E.164>&status=open` casando `ticket.conversationId ==
   conversation_id` (preferido) ou `title` + `createdAt ≥ proposta.criada_em`.
   O gateway faz essa busca **imediatamente** após um POST sem resposta
   (antes de responder à tool) e o reconciliador (E3.3) repete pelos
   `pendente` até `TICKET_PENDENTE_TTL` (10 min) → `aberto` ou `falha` +
   alerta. Um `pendente` nunca mais depende de reconciliação à mão.
2. O ticket nasce por `POST /conversations/:id/ticket` (`period: session`,
   `finish: false`), que já grava `conversationId` no objeto — é isso que
   torna o casamento da reconciliação determinístico.
3. Dedup de negócio em `preparar` consulta também o Infodesk
   (`GET /tickets?phone=&status=open`, `DEDUP_CONSULTA_INFODESK=true`), não só
   a tabela local; "meus chamados" lista o Infodesk por telefone ∪ local.
4. `number` é inteiro; `tickets.numero` idem. `ticket.*` por webhook/feed
   atualiza `status_infodesk`/`encerrado` (o humano fechar deixa de ser
   invisível).
5. Em aberto (`07`): o que o Desk faz se a conversa **já tem**
   `ticketNumber` (T12) — até lá o gateway checa `conversas.ticket_number`
   antes de abrir; e se o ticket pela conversa herda os vínculos (T11).

O texto original segue; "até existir listagem (P3)" está superado.

---

## Contexto

`POST /integration/v1/tickets` não tem chave de idempotência e não existe
listagem de tickets por contato. Um retry de rede após um POST sem resposta,
ou um cliente insistindo, pode duplicar chamados. O cliente decidiu: perguntar
o número e não duplicar (D4).

## Decisão

Tabela `tickets` no store (os dois backends) com `chave_idempotencia` única =
`sha256(conversation_id + titulo + categoria + criada_em da proposta)`,
calculada no Python a partir da proposta carimbada e enviada em
`/tools/infodesk/ticket/abrir`. O gateway faz `INSERT … status='pendente'`
**antes** do POST; conflito devolve o ticket existente. Depois do POST,
`status='aberto'` + `numero`. POST sem resposta deixa `pendente`: nunca
reenviado automaticamente; alerta `ticket_pendente` + transferência para
humano. Dedup de negócio em `preparar`: ticket `aberto` da mesma conversa, ou
do mesmo telefone + categoria nas últimas 24 h, devolve `chamado_aberto`.

## Consequências

- Nenhum ticket duplicado por retry; duplicata de negócio vira pergunta ao
  cliente.
- A listagem "meus chamados" só conhece tickets abertos pela Sofia.
- Um ticket `pendente` que na verdade foi criado no Infodesk só é
  reconciliado à mão até existir listagem (P3).

## Alternativas descartadas

- Retry do POST com backoff: pode duplicar; a API não dá como checar.
- Idempotência só por `conversation_id`: impede um segundo chamado legítimo
  (assunto novo) na mesma conversa.
