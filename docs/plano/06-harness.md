# 06 — Harness do Claude Code: o que foi configurado e por quê

## Objetivo

Várias sessões/agentes trabalhando em paralelo produzirem código
**consistente** (mesmo padrão, mesma esteira, mesmos contratos) sem depender
da memória de uma conversa. Tudo que um agente precisa está em arquivos
versionados; o `CLAUDE.md` só aponta.

## Estrutura

```
CLAUDE.md                          # uma tela: stack, comandos, regras inegociáveis, onde está o resto
.claude/
├── settings.json                  # hooks (formatar, guardar commit, zona) + permissões
├── hooks/
│   ├── _lib.sh                    # leitura do JSON do hook sem jq (node/python/grep)
│   ├── formatar.sh                # PostToolUse Edit|Write: ruff/gofmt no arquivo editado (nunca bloqueia)
│   ├── guardar-commit.sh          # PreToolUse Bash: `git commit` roda scripts/verificar.sh --rapido; falhou → bloqueia
│   └── verificar-zona.sh          # PreToolUse Edit|Write: bloqueia edição fora de ZONA_ARQUIVOS (se definida)
├── skills/                        # procedimentos (carregados sob demanda pela description)
│   ├── infodesk-api/              # como usar a API (ponteiro para docs/infodesk/)
│   ├── jev-classificacao/         # como o JEV é usado aqui (ponteiro para docs/plano/02)
│   ├── nova-tool/                 # checklist + modelos Python/Go de tool
│   ├── evals-agente/              # famílias de evals, formatos, métricas
│   ├── refatorar-modulo/          # procedimento de remoção/renome seguro
│   ├── revisar-pr/                # checklist do revisor + formato do comentário
│   └── ambiente-testes/           # venv, go, lint, verificar.sh, mock, stack local, Windows
└── agents/                        # um por papel do plano (05-agentes.md)
    ├── implementador-go.md  implementador-python.md  implementador-tool.md
    ├── escritor-evals.md    refatorador.md           revisor.md
.githooks/pre-commit               # mesmo verificar.sh --rapido para humanos (scripts/instalar-hooks-git.sh)
scripts/verificar.sh               # esteira local única (hook, pre-commit e CI chamam o mesmo)
.github/PULL_REQUEST_TEMPLATE.md   # etapa, zona, pendências, verificação, checklist
docs/decisoes/                     # ADRs (template + 5 decisões desta sessão)
docs/plano/legado-permitido.txt    # allowlist do teste de arquitetura — só encolhe
```

## Por que cada escolha

**`CLAUDE.md` enxuto.** O antigo tinha 25 KB misturando arquitetura, histórico
de incidentes e procedimentos; todo agente pagava esse contexto em toda
sessão, e a maior parte não se aplicava à tarefa. Agora ele cabe numa tela e
contém só o que vale **sempre** (stack, comandos, 10 regras inegociáveis,
mapa de onde está o resto). O conteúdo antigo foi para
`docs/zapin/CLAUDE-legado.md` como referência do `refatorador`.

**Skills em vez de prompt longo.** Procedimento é carregado quando a
`description` casa com a tarefa (ex.: "ao tocar tool_policy.py"), não em todas
as sessões. Cada skill é autossuficiente, curta, e aponta para o doc de
verdade (`docs/infodesk/`, `docs/plano/0x`) em vez de duplicá-lo. Antes de
escrever, o catálogo foi consultado: há um plugin oficial `code-review` da
Anthropic e esta instalação já traz `/code-review` e `/security-review`
embutidos — `revisar-pr` os usa em vez de reimplementar; a TypeSafe publica
uma skill genérica do JEV (`claude plugin install typesafe@typesafe-ai`) —
`jev-classificacao` cobre só o que é específico deste projeto e indica a
oficial para a API em si. Não havia skill de Go/Python/pytest no catálogo do
usuário.

**Subagentes por papel.** Cada `.claude/agents/*.md` fixa: o que lê (e o que
não precisa ler), a zona, as regras inegociáveis do papel e o formato da
entrega. Assim dois `implementador-go` em sessões diferentes produzem código
do mesmo jeito. O `revisor` só tem ferramentas de leitura: não "conserta de
passagem".

**Hooks.** `PostToolUse` formata o arquivo editado na hora (feedback em
segundos; nunca bloqueia, para não travar uma sessão por ferramenta ausente).
`PreToolUse` em `Bash` intercepta `git commit` e roda a esteira rápida
(`scripts/verificar.sh --rapido`): falhou → exit 2 e o commit não acontece —
espelha a camada 1+2 do `04`. `PreToolUse` em `Edit|Write` impõe a zona quando
`ZONA_ARQUIVOS` está definida. Os hooks chamam o **mesmo** script que o
`.githooks/pre-commit` (humanos) e o CI: uma esteira, três portas. Sem `jq`
na máquina, `_lib.sh` lê o JSON com node ou python.

**Permissões.** `settings.json` libera os comandos de verificação (sem prompt
a cada `go test`/`pytest`) e nega leitura de `.env` e do dataset bruto
(`evals/bruto/`), além de `git push --force`.

**Isolamento.** Uma etapa = uma branch = um worktree = uma zona (`05`).
Zonas disjuntas por onda são desenho do plano; o hook de zona é a trava
mecânica; a tabela de "arquivos compartilhados com dono" cobre o resto.

**Templates só do que é usado.** PR template (lido pelo revisor), template de
ADR (cinco ADRs já o usam), modelo de tool dentro da skill `nova-tool`,
template de eval dentro de `evals/` (criado em E1.5 com o runner, não antes).
Nada de pasta vazia.

**Esteira antes do código.** A Onda 0 existe porque hooks e CI precisam de
`pyproject.toml`, `.golangci.yml`, `ci.yml` e testes de arquitetura; até lá o
`verificar.sh` avisa em vez de falhar por ferramenta ausente (e o CI, com
`CI=true`, é estrito).

## Como um agente novo começa uma sessão neste repositório

1. O `CLAUDE.md` é carregado sozinho. Leia-o inteiro (é uma tela).
2. Receba o briefing (`05-agentes.md`): etapa, papel, zona, branch. Sem
   briefing, abra `docs/plano/03-etapas.md` e pergunte qual etapa.
3. Leia a etapa e as skills listadas nela (o papel em `.claude/agents/<papel>.md`
   diz a ordem). Leia **só** os arquivos da zona e os docs apontados.
4. Defina `ZONA_ARQUIVOS` (se a sessão permite env) e crie a branch no worktree.
5. Implemente. O hook formata a cada edição; `scripts/verificar.sh --rapido` a
   qualquer momento; o commit só passa com a esteira verde.
6. Prove o critério de pronto, abra o PR com o template, peça ao `revisor`.
7. Dúvida de regra de negócio não respondida pelos docs: **não assuma** —
   implemente a hipótese de trabalho do item em `docs/infodesk/07` ou da
   pendência `P<n>`, marque `TODO(P<n>)` e relate no PR.

## Windows (máquina do Daniel)

Hooks e scripts são bash (Git Bash). O alias `python` da Microsoft Store pode
falhar; crie o venv com o interpretador real
(`%LOCALAPPDATA%\Python\bin\python.exe`) e os scripts passam a usar
`venv/Scripts/python.exe`. `jq` não está instalado; `_lib.sh` usa `node`.

**Fim de linha.** O clone tinha `core.autocrlf=true`, o que deixava todo
arquivo em CRLF no working tree: `gofmt -l` reprovava os 151 arquivos e um
script bash com CRLF nem executa. Foi adicionado `.gitattributes` com
`* text=auto eol=lf` (vence o `autocrlf`) e o working tree foi normalizado
para LF (sem diff no índice). Num clone novo o `.gitattributes` já resolve;
num clone antigo rode `git ls-files -z | xargs -0 sed -i 's/\r$//'` uma vez.
Os hooks normalizam caminhos `C:\...` e `/c/...` (`posix()` em `_lib.sh`),
porque o Claude Code pode mandar qualquer um dos dois.

## Manutenção do harness

- Nova tarefa recorrente → nova skill (curta, com `description` que dispare
  no momento certo), nunca parágrafo novo no `CLAUDE.md`.
- Nova decisão → ADR em `docs/decisoes/`; `CLAUDE.md` só muda se a regra for
  inegociável para **toda** sessão.
- Hook novo → script em `.claude/hooks/` chamando `scripts/`; nunca lógica
  inline no JSON.
