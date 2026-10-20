# Notas — `ticket/consultar`

JSON não aceita comentário; o que precisa de explicação fica aqui.

- `status_ticket: null` e `atualizado_em: null` em `ok.json`: o schema de
  `GET /tickets/:number` **não está documentado** (`docs/infodesk/07`, T1).
  O 01 prevê `"desconhecido"` como texto de fallback; até a resposta da
  Infominas o contrato fixa `null` e o Go decide o texto na E3.1
  (`TODO(T1)`). O validador confere só a presença da chave.
- `posse`: a verificação de posse (telefone do ticket = telefone da conversa)
  é local, pela tabela `tickets`. Quando o ticket não foi aberto pela Sofia e
  o Infodesk não expõe `contactPhone` (P3), o endpoint devolve
  `erro-posse_nao_verificada.json` — nunca o status de um ticket alheio.
- `identificacao` no request é a visão **mascarada** do estado
  (`{forma, valor_mascarado}`); o gateway resolve identidade pela tabela
  `contatos` via `telefone`, nunca pelo corpo (skill `nova-tool`).
