# Notas — `ticket/abrir`

- `chave_idempotencia` em `request.json` é o sha256 real de
  `conversation_id + proposta.titulo + proposta.categoria + proposta.criada_em`
  dos valores deste mesmo arquivo (ADR 0005). Um teste pode recalcular e
  comparar.
- `proposta` no request = `ok.proposta` do `preparar` **+ `criada_em`**
  (carimbado pelo Python). O gateway não usa nenhum argumento do LLM aqui.
- `prioridade.id: null` em `ok.json`: T3 (sem rota de prioridades).
- Conflito na chave com ticket já `aberto` devolve o **mesmo** `ok.json`
  (com o `numero` existente); com ticket ainda `pendente` devolve
  `erro-em_andamento.json`. POST sem resposta deixa `pendente` e devolve
  `erro-infodesk_indisponivel.json` (nunca repete o POST).
- `transferido` reflete `TRANSFERIR_APOS_TICKET` (default `true`, revisar
  com P11).
