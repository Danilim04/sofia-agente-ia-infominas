# Notas — `ticket/listar`

- `chamados[]` = os **abertos do telefone no Infodesk**
  (`GET /tickets?phone=&status=open`, qualquer origem) ∪ os que a Sofia abriu
  nos últimos 90 dias (tabela local `tickets`), sem duplicar por `numero`
  (`docs/plano/01`, A5). `categoria` é `null` para chamado que não foi aberto
  pela Sofia.
- `status_ticket` é o `status.name` **literal** do Infodesk (nome por mesa;
  `T1` respondido em 2026-10-19). Os que só existem na tabela local levam o nome
  do último evento `ticket.*`; sem nenhum, `"desconhecido"`.
- `observacao` é texto fixo do gateway que o LLM repete ao cliente:
  `"abertos no Infodesk + abertos por aqui"` (os dois lados leram),
  `"só os abertos por aqui (Infodesk indisponível)"` (a consulta ao Infodesk
  falhou) ou `"só os abertos no Infodesk"` (a parte local não está disponível).
- `erro-chamados_indisponiveis.json`: as duas partes falharam. O motivo veio do
  `toolsapi.go` anterior e foi **confirmado na E3.1** (a linha A5 do `01` já o
  lista); não muda.
- A parte local depende da extensão de leitura `ConsultaTickets` do store
  (`TicketsDoTelefone`), ainda não existente em `store.Store`: até lá o gateway
  devolve só o lado do Infodesk (`TODO(E3.1-store)`).
