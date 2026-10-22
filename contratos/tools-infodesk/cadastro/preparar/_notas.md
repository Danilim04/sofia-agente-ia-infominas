# Notas — `cadastro/preparar`

- ADR 0009 (decisão do produto de 2026-10-22). Fluxo: o LLM entrega `nome`, `empresa` e
  `email` (opcional) como o cliente escreveu; **o código valida** (nome com ≥ 2 palavras,
  e-mail por regex, empresa não vazia), o gateway confere o `lookup` do telefone e busca a
  empresa com `GET /clients?q=`. O `clientId` **nunca** passa pelo LLM nem volta nesta
  resposta: fica na linha `cadastros` do gateway, que é a única proposta que o `cadastrar`
  aceita executar.
- `proposta` não leva nome nem e-mail (o `ToolMessage` vai ao checkpoint/trace); `empresa.nome` é a empresa que o gateway escolheu — o resumo ao cliente usa ela.
- `proposta.chave` é o sha256 (hex, **em 8 grupos de 8 separados por `-`** para a máscara de CPF do cérebro não corromper corridas de 11 dígitos) de `conversation_id + telefone + cliente_id + nome
  normalizado + e-mail` calculado pelo **gateway**; o Python só a repassa. O valor do
  exemplo é ilustrativo (`cliente_id` fictício).
- `telefone_mascarado`: só os 4 últimos dígitos. O telefone completo nunca sai do gateway.
- `erro-empresa_ambigua.json` carrega `candidatos` com **só o nome** de cada empresa (até 5):
  o cliente diz qual é a dele e o LLM chama `preparar` de novo com esse nome; o gateway
  escolhe o candidato por código (um só, ou nome idêntico ao digitado).
- `erro-cadastro_transferido.json`: depois de `CADASTRO_MAX_TENTATIVAS` (2) falhas de empresa
  e com `CADASTRO_SEM_CLIENTE=transferir` (hipótese, `TODO(decisão do produto)`) o gateway já
  avisou o cliente e passou a conversa por handoff com nota por código.
- `erro-ja_cadastrado.json`: o lookup achou solicitante para o telefone; cadastrar de novo
  criaria duplicata. O LLM segue pelo CPF/contrato (`informar_identificacao`).
