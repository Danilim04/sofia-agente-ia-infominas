# 0006 — Contratos JSON versionados em `contratos/`

- **Estado:** aceita (2026-10-20)
- **Etapa:** E1.6
- **Pendências relacionadas:** T1, T3, T4, P2, P3

## Contexto

Gateway Go e cérebro Python se falam por dois contratos HTTP (Contrato A
`POST /chat` e `/tools/infodesk/*`), escritos em duas linguagens por sessões
de agente que rodam em paralelo (E2.1, E2.3 e E3.1 dependem da E1.6). Sem um
artefato comum, cada lado tipa o que entende do `01-ferramentas.md` e a
divergência só aparece no E2E. Vários campos ainda são `null` por pendências
do Infodesk (T1, T3, T4) e precisam mudar nos dois lados ao mesmo tempo.

## Decisão

Os exemplos JSON em `contratos/` são a **fonte de verdade executável**:
`contrato-a/*.json` e `tools-infodesk/<área>/<ação>/{request,ok,erro-<motivo>}.json`,
com `_notas.md` ao lado para o que JSON não explica. Os testes de contrato
dos dois lados **carregam esses arquivos** (Go: `internal/toolsapi`,
`internal/brain`; Python: `src/tools`, `server.py`). `contratos/validar.py`
(stdlib, `python -I`) checa forma, motivos iguais à coluna "Erros" do 01 e
esquema de chaves, e roda no job `contratos` do CI. Mudança de contrato =
**um PR** que toca o 01, o JSON, o Go e o Python; PR de um lado só é recusado
na revisão. Erro de negócio é sempre HTTP 200 `{status:false, erro, motivo}`.

## Consequências

- Sessões paralelas partem do mesmo exemplo. Campos pendentes ficam `null` num
  lugar só, com a pendência nomeada; trocar o valor é um PR rastreável.
- Custo: campo novo exige JSON + `validar.py` + dois lados; o validador
  compara chaves, não tipos (tipo fica nos testes de cada lado).
- Rever quando T1/T3 forem respondidos e se passar de ~15 endpoints.

## Alternativas descartadas

- **OpenAPI gerado** de um lado — o outro vira consumidor de segunda classe;
  gerar em duas linguagens não compensa para 6 endpoints (pode vir depois).
- **Protobuf/gRPC** — inviabiliza curl, mock-infodesk e `httpx` nos testes.
- **Tipos duplicados à mão** (como no Zapin) — gerou as divergências que
  esta etapa corrige.
