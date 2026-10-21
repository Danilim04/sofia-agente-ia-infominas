# Notas — `contato/identificar`

- É o **fallback** da identificação: a primeira tentativa é o
  `GET /contacts/lookup?phone=` que o gateway faz sozinho no primeiro turno do
  episódio (ADR 0004, revisão 2026-10-20; **respondido em 2026-10-19**,
  `docs/infodesk/07` T4/N2). Quando o lookup do telefone já tinha achado um
  solicitante, `cliente_id` e `nome_cadastro` vêm preenchidos (e o
  `requester_id` é preservado em `contatos`); senão ficam `null`, como em
  `ok.json`. A API não expõe CPF do solicitante, então o CPF **não** é
  confrontado com o cadastro (`07` N3).
- `request.json` leva o CPF em claro porque é a única chamada que o recebe
  (o Python já validou o dígito verificador; o gateway valida de novo). O valor
  `12345678909` é fictício com DV válido. Em disco só `valor_hash`
  (sha256 + sal) e o mascarado `***.***.789-**`; em log só o mascarado.
- Para identificação por contrato, o request vai com `cpf: ""` e
  `contrato: "000123456"`, e `ok.forma` é `"contrato"` (mascarado
  `******456`).
