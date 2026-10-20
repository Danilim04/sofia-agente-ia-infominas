# Decisões de arquitetura (ADRs)

Registros curtos (≤ 40 linhas) de decisões que mudam como o sistema é
construído. Uma decisão nova = um arquivo novo numerado; uma decisão revista =
novo ADR que cita e substitui o anterior (nunca editar o antigo além de marcar
"substituído por").

Formato: [0000-template.md](0000-template.md). Quando escrever um ADR: sempre
que uma etapa do plano escolher algo que não estava decidido em
`docs/plano/`, ou mudar algo que estava.

| # | Decisão | Estado |
|---|---|---|
| [0001](0001-manter-gateway-go-e-cerebro-python.md) | Manter gateway Go + cérebro Python | aceita (2026-10-19) |
| [0002](0002-jev-classifica-llm-conversa.md) | JEV classifica; LLM conversa e cobre baixa confiança | aceita (2026-10-19) |
| [0003](0003-infodesk-substitui-chatwoot.md) | Infodesk substitui Chatwoot/Evolution; estado bot/humano é nosso | aceita (2026-10-19) |
| [0004](0004-identificacao-telefone-mais-cpf.md) | Identificação por telefone + CPF/contrato validado por código | aceita (2026-10-19) |
| [0005](0005-idempotencia-de-ticket-local.md) | Idempotência e dedup de tickets no gateway (chave própria) | aceita (2026-10-19) |
| [0006](0006-contratos-versionados.md) | Contratos JSON versionados em `contratos/` (fonte de verdade dos testes dos dois lados) | aceita (2026-10-20) |
| 0007 | Revisão de segurança v1 (E4.5) | a escrever |
