# Notas — `cadastro/cadastrar`

- Sem argumentos do LLM. `proposta` é a que o `preparar` devolveu, carimbada no estado
  Python com `criada_em` (o `turno` é de controle e não vai no contrato). O gateway recalcula a
  `chave`, exige uma linha `proposta` da **mesma conversa e telefone**, com até 15 min, e só
  então faz `POST /requesters {name, clientId, phone, email?, conversationId}` UMA vez.
- `requester_id` e `solicitante.nome` (abreviado, "Maria S.") alimentam o estado
  `identificacao = {forma: "cadastro", valor_mascarado, requester_id}`.
- `criado: false` quando o Infodesk já tinha o solicitante (idempotente por telefone/e-mail
  no mesmo cliente, `docs/infodesk/10` §6). Repetir `cadastrar` com a mesma chave devolve o
  resultado guardado, sem novo POST.
- `erro-infodesk_indisponivel.json`: rede, 5xx ou 400 do Infodesk. A linha continua
  `proposta`; repetir é seguro porque o Infodesk é idempotente.
