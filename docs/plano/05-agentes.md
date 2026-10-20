# 05 — Orquestração de múltiplos agentes

Arquivos reais: `.claude/agents/*.md` (um por papel), `.claude/skills/*/SKILL.md`
(procedimentos), `.claude/settings.json` (hooks), `CLAUDE.md` (regras de toda
sessão). Explicação das escolhas em `06-harness.md`.

## Papéis

| Subagente (`.claude/agents/`) | Faz | Zona típica | Skills que carrega |
|---|---|---|---|
| `implementador-go` | código Go do gateway e seus testes | `backend/internal/<pacotes da etapa>`, `backend/cmd` | `infodesk-api`, `ambiente-testes`, (`nova-tool`) |
| `implementador-python` | cérebro: classificação, grafo, guardrails, RAG, harness | `agente-ia/src/<módulos da etapa>`, `server.py`, `app.py`, `mock-infodesk/` | `jev-classificacao`, `ambiente-testes`, `evals-agente` |
| `implementador-tool` | uma tool de ponta a ponta (contrato, Go, Python, política, testes) | `contratos/`, `backend/internal/toolsapi`, `agente-ia/src/tools`, `src/agent/tool_policy.py`, `src/identity` | `nova-tool`, `infodesk-api`, `ambiente-testes` |
| `escritor-evals` | cenários, datasets, anonimização, relatórios, baseline, evals noturnos | `agente-ia/evals/`, `config/taxonomia.yaml` (placeholder), workflows de evals | `evals-agente`, `jev-classificacao` |
| `refatorador` | remoção do domínio antigo, tooling, CI, renomes, limpeza | a que a etapa declarar (Onda 0 pode ser o repo inteiro) | `refatorar-modulo`, `ambiente-testes` |
| `revisor` | revisão de PR (somente leitura) + E4.5 | nenhuma (não edita) | `revisar-pr` (+ `/code-review`, `/security-review` embutidos) |

## Modelos por papel (custo × benefício)

Só o **orquestrador** (a sessão que planeja, distribui etapas, integra PRs e
decide) roda no modelo mais forte. Todo papel executor roda em **Sonnet**
(`model: sonnet` no frontmatter de `.claude/agents/*.md`): é o ponto de
equilíbrio para escrever código com testes, refatorar com rede de segurança e
revisar diffs, a uma fração do custo. Ao abrir uma sessão por etapa
(`start_session`), passe `model: "claude-sonnet-5-5"` — a sessão herda o
modelo do orquestrador se nada for dito. Regras:

| Papel | Modelo | Por quê |
|---|---|---|
| orquestrador (esta sessão) | o mais forte disponível | decisões de arquitetura, integração entre PRs, briefings |
| implementador-go / implementador-python / implementador-tool | Sonnet | código com especificação pronta e testes como critério de pronto |
| refatorador | Sonnet | mudanças grandes, mas mecânicas e protegidas por testes e lint |
| escritor-evals | Sonnet | datasets e cenários seguem formato fixo; qualidade medida pelo relatório |
| revisor | Sonnet (+ `/code-review` e `/security-review` embutidos) | leitura de diff com checklist; o humano aprova |
| tarefas mecânicas de leitura (varrer logs, listar sobreposições, converter docs) | Haiku, quando a tarefa cabe num briefing fechado | mais barato ainda; sem decisões |

Escale para um modelo mais forte **só** quando uma etapa falhar duas vezes no
mesmo ponto com Sonnet, e registre o motivo no PR.

Os subagentes são definidos para o **Agent tool** do Claude Code (uma sessão
pode delegar a eles), mas o uso principal neste plano é **uma sessão do
Claude Code por etapa**, cada uma começando com o briefing abaixo e com o
papel indicado. Os dois modos usam os mesmos arquivos.

## Briefing por tipo de tarefa (cole no início da sessão)

```
Etapa: E<n>.<m> — <título> (docs/plano/03-etapas.md)
Papel: <implementador-go | implementador-python | implementador-tool | escritor-evals | refatorador>
Zona: <prefixos exatamente como na tabela da etapa>
Branch: onda<n>/e<n>.<m>-<slug>   (a partir de main atualizada)
Dependências já mergeadas: <IDs>  — se alguma não estiver na main, pare e avise.
Pendências que esta etapa toca: <P?> → implemente a hipótese de trabalho do doc, configurável, com TODO(P?).
Pronto quando: <critério de pronto da etapa, copiado>
Ao terminar: scripts/verificar.sh (modo da zona) verde, PR com o template, pedir revisão ao revisor.
Não faça: tocar fora da zona; inventar regra de negócio; mudar contratos sem os dois lados; apagar asserts.
```

Defina na sessão `ZONA_ARQUIVOS="<zona1>:<zona2>"` para o hook
`verificar-zona.sh` **bloquear** edições fora da zona (sem a variável o hook é
inerte e a zona vale só por convenção).

## O que cada agente recebe de contexto (e só isso)

| Papel | Lê sempre | Lê se a etapa tocar | Não precisa ler |
|---|---|---|---|
| implementador-go | `CLAUDE.md`, etapa, `01` seção B, skill `infodesk-api`, pacote-alvo | `nova-tool` (endpoint novo), `docs/infodesk/<área>` | `02-classificacao`, evals |
| implementador-python | `CLAUDE.md`, etapa, módulo-alvo, `ambiente-testes` | `02` + `jev-classificacao` (classificação), `evals-agente` (prova de conversa) | código Go |
| implementador-tool | `CLAUDE.md`, etapa, linha da tool no `01`, `nova-tool`, `infodesk-api`, `toolsapi.go`, `tool_policy.py` | `docs/infodesk/03`/`04` | `02` além da seção "quem decide" |
| escritor-evals | `CLAUDE.md`, etapa, `evals-agente`, `02` seção dataset, `04` camada 2 | `jev-classificacao` | código Go |
| refatorador | `CLAUDE.md`, etapa, `refatorar-modulo`, `00` tabela de mudanças, `docs/zapin/CLAUDE-legado.md` | módulos-alvo | `02` |
| revisor | `CLAUDE.md`, `revisar-pr`, etapa do PR, diff | `01`/`02` linhas relevantes | — |

O `CLAUDE.md` cabe numa tela e aponta para tudo isso; **nenhum agente precisa
desta conversa de planejamento**.

## Como evitam conflito

1. **Uma etapa = uma branch = um worktree = uma zona.** Dentro de uma onda, as
   zonas são disjuntas por construção (tabela em `03`). Conflito de merge
   dentro da onda é bug do plano: relate.
2. **Branch**: `onda<n>/e<n>.<m>-<slug>` a partir de `main`. Rebase diário na
   `main` (ou merge queue). Sem force-push (bloqueado em `settings.json`).
3. **Commit**: `tipo(escopo): resumo [E<n>.<m>]` com `tipo ∈ feat|fix|refactor|test|docs|chore|ci`
   e `escopo` = pacote/módulo (`infodesk`, `engine`, `classificacao`, `tools`, `evals`, `harness`).
   Rodapé `Co-Authored-By` conforme a convenção da conta.
4. **Arquivos compartilhados** têm dono por onda: `contratos/` (implementador-tool),
   `config/taxonomia.yaml` (escritor-evals enquanto placeholder; depois a Infominas via PR),
   `tests/fakes.py` (quem cria primeiro; outros só adicionam, com nomes novos),
   `docs/plano/legado-permitido.txt` (só remoção; qualquer agente),
   `docs/decisoes/` (qualquer agente pode **adicionar** ADR; nunca editar os existentes).
5. **Interfaces entre zonas** são decididas **antes** da onda (no plano) e
   materializadas em `contratos/` ou em uma assinatura declarada na etapa
   (ex.: gancho `efeito_pos_tool` definido por E2.2, preenchido por E2.3).
6. **Ordem de merge** dentro da onda é livre; entre ondas, a próxima só começa
   com as dependências na `main` (mapa em `03`).
7. **Revisão**: `revisor` comenta → autor corrige → humano aprova → merge
   (squash, mantendo o `[E<n>.<m>]` no título).

## Rodar várias sessões ao mesmo tempo (fluxo recomendado)

1. Atualize `main`. Para cada etapa da onda: nova sessão do Claude Code em
   worktree próprio (`EnterWorktree` ou `git worktree add ../sofia-e2.3 -b onda2/e2.3-tools-python`),
   `ZONA_ARQUIVOS` definido, briefing colado.
2. Cada sessão termina abrindo PR e pedindo revisão (`revisor` na própria
   sessão ou numa sessão separada só com o diff).
3. Você (humano) aprova e mergeia; quando todas as etapas de que a próxima
   onda depende estiverem na `main`, repita.
4. Pendências resolvidas no meio do caminho (P1, P3…) viram etapa nova em `03`
   (Onda 5) — não interrompa a onda corrente para absorvê-las.
