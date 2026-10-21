# Notas — `ticket/consultar`

JSON não aceita comentário; o que precisa de explicação fica aqui.

- `status_ticket` é o `status.name` **literal** do Infodesk (objeto por mesa;
  `docs/infodesk/07` T1, respondido em 2026-10-19). Sem nome legível o gateway
  cai nos booleanos (`"encerrado"`, `"cancelado"`, `"em andamento"`) e, sem
  nada, em `"desconhecido"` — como em `ok.json`. `atualizado_em` é o
  `updatedAt` do ticket (`null` quando a resposta não o traz). Campos a mais
  (`etapa`, prazo) só por PR de contrato (ADR 0006).
- **Posse**, decidida por código nesta ordem (`docs/plano/01`, A4; `07` P3/T5
  respondidos): (a) a tabela local tem esse `numero` com o mesmo telefone;
  (b) `ticket.conversationId` é de uma conversa deste telefone; (c)
  `contactPhone` ou `requester.phone` igual ao telefone (E.164, tolerando o 9º
  dígito); (d) `requester.id` é o `requester_id` do lookup. Nenhuma prova →
  `erro-posse_nao_verificada.json` e **nenhum** campo do ticket na resposta.
- `identificacao` no request é a visão **mascarada** do estado
  (`{forma, valor_mascarado}`), não o `{cpf|contrato}` da linha A4 do 01 —
  mesma hipótese de `ticket/preparar/_notas.md` (`TODO(E2.3)`). O gateway
  ignora o corpo e resolve a identidade pela tabela `contatos` via `telefone`.
- Textos vindos do Infodesk (`titulo`, `status_ticket`) são DADO: o gateway os
  limpa de controle e os limita antes de devolver.
