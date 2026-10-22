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
| [0003](0003-infodesk-substitui-chatwoot.md) | Infodesk substitui Chatwoot/Evolution; estado bot/humano é nosso | aceita (2026-10-19); parte de transferência/reconciliação substituída por 0007 (2026-10-20) |
| [0004](0004-identificacao-telefone-mais-cpf.md) | Identificação por telefone + CPF/contrato validado por código; `GET /contacts/lookup` como primeira tentativa | aceita (2026-10-19); revisada (2026-10-20); item 4 da revisão (`POST /requesters` fora da v1) substituído por 0009 |
| [0005](0005-idempotencia-de-ticket-local.md) | Idempotência e dedup de tickets no gateway (chave própria) + reconciliação por `GET /tickets?phone=` | aceita (2026-10-19); revisada (2026-10-20) |
| [0006](0006-contratos-versionados.md) | Contratos JSON versionados em `contratos/` (fonte de verdade dos testes dos dois lados) | aceita (2026-10-20) |
| [0007](0007-handoff-caminho-unico-para-humano.md) | `POST /conversations/:id/handoff` é o caminho único de transferência para humano; `/department`, `/assign`, `/bot` fora da v1 | aceita (2026-10-20) |
| 0008 | Revisão de segurança v1 (E4.5) | a escrever (número reservado) |
| [0009](0009-cadastro-de-solicitante-pela-sofia.md) | Cadastro de solicitante pela Sofia (`POST /requesters` em duas fases; escopo `contacts:write`) | aceita (2026-10-22) |
| [0010](0010-mesa-unica-e-conversa-aberta.md) | Mesa única "Abertura de Ticket", conversa aberta depois do ticket, Comercial/Financeiro por departamento | aceita (2026-10-22) |

Nota (2026-10-20): 0004 e 0005 receberam uma seção "Revisão" datada em vez de
um ADR substituto, porque a decisão original continua valendo como fallback
e só ganhou um passo antes (lookup) ou depois (reconciliação). 0003 foi
apenas marcado como parcialmente substituído.
