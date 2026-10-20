# Notas — `ticket/listar`

- `chamados[].status_ticket: null`: mesmo motivo de `ticket/consultar`
  (schema de `GET /tickets/:number` pendente — `docs/infodesk/07`, T1). A
  lista vem da tabela local `tickets`; a atualização de status pelo Infodesk
  é opcional (≤ 5 por chamada) e, quando falha, a lista é devolvida assim
  mesmo com `null`.
- `erro-chamados_indisponiveis.json`: o 01 **não** lista erro para esta tool.
  O motivo `chamados_indisponiveis` é herdado do `toolsapi.go` anterior para
  "não consegui ler a lista local agora" e entra aqui como **hipótese de
  trabalho** para o contrato ter ≥ 1 erro. `TODO(E3.1)`: confirmar e
  registrar na linha A5 do 01 (fora da zona desta etapa).
- A lista só inclui chamados abertos pela Sofia (T5); `observacao` é texto
  fixo do gateway que o LLM repete ao cliente.
