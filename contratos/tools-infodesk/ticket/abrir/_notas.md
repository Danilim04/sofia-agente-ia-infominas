# Notas — `ticket/abrir`

- `chave_idempotencia` em `request.json` é o sha256 real de
  `conversation_id + proposta.titulo + proposta.categoria + proposta.criada_em`
  dos valores deste mesmo arquivo (ADR 0005). Um teste pode recalcular e
  comparar.
- `proposta` no request = `ok.proposta` do `preparar` **+ `criada_em`**
  (carimbado pelo Python). O gateway não usa nenhum argumento do LLM aqui.
- `prioridade.id`: `null` em `ok.json` porque o UUID é resolvido **por mesa** (`GET /desks/:id.priorities`, `docs/infodesk/07` T3, respondido em 2026-10-19); o gateway devolve o UUID quando a mesa tem a prioridade pedida e `null` (com alerta `taxonomia_invalida`) quando não tem — nesse caso o `priorityId` é omitido no POST.
- Conflito na chave com ticket já `aberto` devolve o **mesmo** `ok.json`
  (com o `numero` existente); com ticket ainda `pendente` devolve
  `erro-em_andamento.json`. POST sem resposta é decidido por listagem: se o ticket aparece, sucesso
  normal (sem segundo POST); se não aparece (ou a listagem também falha), a linha
  fica `pendente` e a resposta é `erro-infodesk_indisponivel.json` + alerta
  `ticket_pendente` + handoff (nunca repete o POST). `400 VALIDATION_ERROR` e
  `400 INVALID_REFERENCE` também viram `infodesk_indisponivel` para o LLM, com
  alerta de configuração (`07` T10). `403` vira `erro-escopo_insuficiente.json`.
- Um segundo `abrir` com a mesma chave devolve o resultado da **reconciliação por listagem** (`GET /tickets?phone=&status=open`, casando `conversationId`; ADR 0005, revisão 2026-10-20), nunca um segundo POST. O ticket nasce por `POST /conversations/:id/ticket` (`period: session`, `finish` = `TICKET_FINISH_CONVERSA`, default `false` — `07` C6).
- `transferido` reflete `TRANSFERIR_APOS_TICKET` (default `true`, revisar
  com P11).
