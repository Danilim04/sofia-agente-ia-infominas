# Notas — Contrato A (gateway Go → cérebro Python)

- `chat-request.json` / `chat-response.json`: exatamente o bloco "Contrato
  A — mudanças" de `docs/plano/01-ferramentas.md`. `contato` é `null`
  quando o telefone nunca se identificou; `estado_conversa` nunca chega
  `transferida` (defesa).
- `chat-response.json` mostra `acoes: [transferir]` com `origem:
  frustracao`; os outros valores de `origem` são `confianca` e
  `pedido_cliente`. O gateway executa a ação pelo caminho único de
  transferência.
- `chat-response-erro.json`: envelope `erro` herdado do gateway anterior
  (`classe: transitoria | definitiva`, `id_ocorrencia` com prefixo `SOF-`,
  `causa` resumida sem PII). Com `erro`, `reply` é vazio e `classificacao`
  é `null`; o gateway decide repetir ou transferir.
- `encerrar-episodio-*`: `conversation.finished` → o cérebro limpa
  `proposta_chamado` e `classificacao` (a identificação é por telefone, no
  gateway). `proposta_limpa` diz se havia proposta pendente. `status: "ok"`
  (string, não booleano) é a forma que os dois lados já têm
  (`backend/internal/brain`, `agente-ia/server.py`) e fica assim.
- `classificar-*` (**só evals/ops**, mesmo `X-Tools-Token`): o 01 não fixa
  o JSON; o formato aqui (`mensagem` + `historico[{autor, texto}]` →
  `classificacao` + `frustracao` + `seguranca` + `esclarecimento`) é
  **hipótese de trabalho** para a E2.2 confirmar (`TODO(E2.2)`). `autor` é
  `cliente` ou `sofia`.
