# Notas — `ticket/preparar`

- `prioridade.id`: o UUID é resolvido **por mesa** (`GET /desks/:id.priorities`;
  `docs/infodesk/07` T3, respondido em 2026-10-19). O gateway devolve o UUID da
  prioridade pedida na mesa de destino (elevada a `prioridade_minima` da
  categoria) e `null` quando a mesa não a tem — então o `priorityId` é omitido
  no POST e sai um alerta `taxonomia_invalida`. Mesa, departamento e item de
  catálogo também vêm da taxonomia (`config/taxonomia.yaml`), nunca do LLM.
- `identificacao` no request é a visão mascarada do estado
  (`{forma, valor_mascarado}`) — decisão da regra 5 (PII só mascarada fora do
  gateway), confirmada na E2.3 e refletida na linha A2 do 01 (E3.7). O gateway ignora o corpo e
  valida contra a tabela `contatos` pelo `telefone` (TTL 24 h). Sem entrada
  válida (lookup, CPF ou contrato) → `erro-nao_identificado`.
- `erro-chamado_aberto.json` carrega `chamados_abertos`: o chamado vinculado à
  conversa, os locais abertos (mesma conversa, ou mesmo telefone + categoria na
  janela `TICKET_DEDUP_JANELA`) e os abertos do telefone no Infodesk
  (`DEDUP_CONSULTA_INFODESK=true`; `07` T5 respondido). Os que não foram abertos
  pela Sofia vêm com `categoria: null`. Em `categoria_sem_mapa` o gateway alerta
  o responsável e o LLM só oferece atendente.
- `rodape` é texto fixo do gateway (`TICKET_RODAPE=true`), anexado à `descricao`
  no `abrir`; a identificação aparece **só mascarada** (para o lookup, o nome do
  cadastro abreviado: `solicitante Maria S.`). Com `TICKET_RODAPE=false` vem `""`.
- A `proposta` carimbada no estado Python ganha `criada_em` e `turno`; o
  `abrir` envia a proposta com `criada_em` (ver `ticket/abrir/_notas.md`).
