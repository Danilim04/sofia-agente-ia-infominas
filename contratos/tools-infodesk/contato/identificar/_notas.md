# Notas — `contato/identificar`

- `cliente_id: null` e `nome_cadastro: null` em `ok.json`: não há rota de
  busca de cliente no Infodesk (`docs/infodesk/07`, T4; plano P2). O
  gateway só grava `contatos` (hash + mascarado, TTL 24 h).
- `request.json` leva o CPF em claro porque é a única chamada que o recebe
  (o Python já validou o dígito verificador). O valor `12345678909` é
  fictício com DV válido. Em disco e em log só entra `***.***.789-**`.
- Para identificação por contrato, o request vai com `cpf: ""` e
  `contrato: "000123456"`, e `ok.forma` é `"contrato"`.
