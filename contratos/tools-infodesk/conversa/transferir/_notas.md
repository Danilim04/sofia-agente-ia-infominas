# Notas — `conversa/transferir`

- `categoria` no request é opcional no 01 (`categoria?`); quando ausente ou
  sem mapa, o gateway usa `DEPARTAMENTO_HUMANO_PADRAO`. O exemplo envia
  `sem_conexao` → departamento `Suporte`.
- `motivo` é o enum do LLM (`pedido_cliente | fora_do_escopo |
  nao_resolvido`). Transferências por mídia, falha, frustração e confiança
  não passam por aqui (decisão de código na engine).
- `mensagem_ao_cliente` é texto fixo do gateway, que **ele mesmo envia**; o
  LLM só confirma em uma frase.
- `erro-infodesk_indisponivel.json`: o gateway marca a conversa como
  `transferida` localmente mesmo assim (o bot cala) e dispara alerta.
