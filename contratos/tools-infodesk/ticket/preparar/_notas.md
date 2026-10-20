# Notas — `ticket/preparar`

- `prioridade.id: null` na proposta: não há rota para listar prioridades
  (`docs/infodesk/07`, T3). `priorityId` é omitido no POST e a prioridade
  calculada vai no `description`; `prioridade.nome` continua preenchido.
- `identificacao` no request é a visão mascarada do estado
  (`{forma, valor_mascarado}`), **não** o `{cpf|contrato}` escrito na linha A2
  do 01 — hipótese de trabalho pela regra 5 (PII só mascarada fora do
  gateway); `TODO(E2.3)`: confirmar e atualizar o 01. O gateway valida contra
  a tabela `contatos` pelo `telefone` (TTL 24 h). Sem entrada válida →
  `erro-nao_identificado`.
- `erro-chamado_aberto.json` carrega `chamados_abertos` (dedup local,
  ADR 0005); em `categoria_sem_mapa` o gateway alerta o responsável e o LLM
  só oferece atendente.
- `rodape` é texto fixo do gateway, anexado à `descricao` no `abrir`; o CPF
  aparece **só mascarado**.
- A `proposta` carimbada no estado Python ganha `criada_em` e `turno`; o
  `abrir` envia a proposta com `criada_em` (ver `ticket/abrir/_notas.md`).
