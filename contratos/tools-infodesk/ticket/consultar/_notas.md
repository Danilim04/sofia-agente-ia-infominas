# Notas — `ticket/consultar`

JSON não aceita comentário; o que precisa de explicação fica aqui.

- `status_ticket` e `atualizado_em` em `ok.json`: o schema de
  `GET /tickets/:number` **não está documentado** (`docs/infodesk/07`, T1).
  `status_ticket` é string: o texto do Infodesk ou o fallback `"desconhecido"`
  (como fixa o 01); `atualizado_em` fica `null` até T1. O validador confere
  só a presença das chaves.
- `posse`: a verificação de posse (telefone do ticket = telefone da conversa)
  é local, pela tabela `tickets`. Quando o ticket não foi aberto pela Sofia e
  o Infodesk não expõe `contactPhone` (P3), o endpoint devolve
  `erro-posse_nao_verificada.json` — nunca o status de um ticket alheio.
- `identificacao` no request é a visão **mascarada** do estado
  (`{forma, valor_mascarado}`), não o `{cpf|contrato}` da linha A4 do 01 —
  mesma hipótese de `ticket/preparar/_notas.md` (`TODO(E2.3)`). O gateway
  resolve identidade pela tabela `contatos` via `telefone`, nunca pelo corpo.
