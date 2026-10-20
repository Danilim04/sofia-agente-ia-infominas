<!-- Título: tipo(escopo): resumo [E<onda>.<n>]  — ex.: feat(infodesk): cliente Go do Infodesk [E1.2] -->

## Etapa

- **ID:** E_._  (ver `docs/plano/03-etapas.md`)
- **Zona declarada:** `...`
- **Pendências tocadas:** P_ (hipótese de trabalho implementada em `...`, `TODO(P_)` no código) / nenhuma

## O que muda

<!-- 3–6 linhas: o que entrou, o que saiu, decisões tomadas. Se mudou um contrato (contratos/, Contrato A, tool_policy), diga aqui. -->

## Como verifiquei

```
scripts/verificar.sh --rapido      # ok / falhou em ...
scripts/verificar.sh --completo    # ok / não aplicável porque ...
```

<!-- cole as linhas finais do script; testes novos e o que cada um prova -->

## Checklist (preenchido pelo autor; o revisor confere — `.claude/skills/revisar-pr`)

- [ ] Só arquivos da zona da etapa (ou justificado acima)
- [ ] Tool nova/alterada tem entrada em `POLITICAS`; nenhum arg de sessão no schema do LLM
- [ ] Efeito colateral (ticket, transferência) continua em duas fases / decisão de código
- [ ] Nenhum dado pessoal (CPF, telefone, nome) em log INFO ou em prompt em claro
- [ ] Erro do Infodesk / interno não chega ao cliente nem ao LLM cru
- [ ] Retry só em operação idempotente; `POST /tickets` protegido pela chave local
- [ ] Mudança em `store.Store` cobre SQLite **e** Postgres com teste
- [ ] `contratos/` atualizado nos dois lados quando o JSON mudou
- [ ] `docs/plano/legado-permitido.txt` só encolheu
- [ ] Persona Sofia (pt-BR, "você", ≤ 1 emoji) nos textos novos dos dois lados
- [ ] Docs/ADR atualizados se uma decisão mudou

🤖 Generated with [Claude Code](https://claude.com/claude-code)
