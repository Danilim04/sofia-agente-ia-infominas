# 04 — Esteira de qualidade

```
o agente escreve
   → camada 1: formatadores, linters e testes de arquitetura barram o óbvio   (hook local + CI)
   → camada 2: testes unitários, de contrato, E2E e evals barram o funcional (CI; parte no hook de commit)
   → camada 3: revisor (agente `revisor` + humano) pega o resto             (PR)
```

Nada entra na `main` sem as três camadas. As camadas 1 e 2 são **mecânicas**
(scripts); a 3 é **julgamento** com checklist.

## Camada 1 — barrar o óbvio

| Linguagem | Ferramenta | Configuração | O que barra |
|---|---|---|---|
| Python | **ruff** (lint + format) | `agente-ia/pyproject.toml` `[tool.ruff]`: `line-length = 100`, `select = ["E","F","I","B","UP","S","N","PL","RUF"]`, `ignore = ["S101"]` (assert em testes), `per-file-ignores` para `tests/`; `ruff format` | estilo, imports, bugs comuns (B), `S` = bandit (eval/exec, subprocess shell, hardcoded password) |
| Python | **mypy** | `[tool.mypy]`: `python_version = "3.12"`, `warn_unused_ignores`, `disallow_untyped_defs` em `src/classificacao`, `src/tools`, `src/identity`, `src/agent/tool_policy.py` (módulos novos nascem tipados); resto permissivo | tipos errados nos módulos críticos |
| Python | **import-linter** | `[tool.importlinter]` com contratos: (1) `layers`: `src.agent` → `src.tools` → `src.classificacao`/`src.identity` → `src.config` (camada de cima importa de baixo, nunca o inverso); (2) `forbidden`: `src.classificacao` não importa `src.agent` nem `src.tools`; `src.tools` não importa `src.agent.nodes`; `src.security` não importa `src.tools`; `src.rag` só é importado por `src.agent.nodes`, `src.tools.rag_tool`, `server`; (3) `forbidden`: `evals` não é importado por `src` | dependência circular, tool chamando o grafo, classificação acoplada ao agente |
| Python | **teste de arquitetura** `tests/test_arquitetura.py` | roda `lint-imports`; varre `agente-ia/src`, `backend/`, `contratos/`, `mock-infodesk/` por `azapfy\|zapin\|chatwoot\|mongo\|sac_\|azapers\|zprs\|zpf-` (case-insensitive) exceto caminhos em `docs/plano/legado-permitido.txt`; verifica que toda tool em `get_default_tools()` tem entrada em `POLITICAS` e que nenhum parâmetro de sessão (`telefone`, `conversation_id`, `classificacao`, `proposta`, `identificacao`) aparece no schema que o LLM vê (`tool.tool_call_schema`) | resquício do domínio antigo; F7 novo (arg de escopo exposto ao modelo) |
| Go | **gofmt** | `gofmt -l` vazio | formatação |
| Go | **go vet** | padrão | erros clássicos |
| Go | **golangci-lint** v2 | `backend/.golangci.yml`: `errcheck`, `govet`, `staticcheck`, `unused`, `gosec` (G101 credenciais, G404 rand), `revive`, `bodyclose`, `noctx`, `depguard` (proíbe `bot-azapfy/*`, `go.mongodb.org/*`, `github.com/chatwoot/*`), `gocritic`; `timeout: 3m` | erros ignorados, body não fechado, HTTP sem contexto, import proibido |
| Go | **teste de arquitetura** `backend/internal/arquitetura_test.go` | usa `golang.org/x/tools/go/packages` (ou `go list -deps -json`) para afirmar: `infodesk` não importa `engine`, `store`, `toolsapi`; `engine` não importa `toolsapi`; `toolsapi` não importa `webhook`; nenhum pacote importa `internal/{chatwoot,mongo,sac,identity}`; `store` não importa nada de `internal/` além de si; além disso grep de termos legados como no Python | camadas invertidas, legado |
| Ambos | **gitleaks** (CI) | config padrão + regra para `ik_live_`, `sk-or-v1-`, `TYPESAFE_API_KEY=` | segredo commitado |

Onde rodam: **hook `PostToolUse`** do Claude Code formata o arquivo editado
(`ruff format` + `ruff check --fix` / `gofmt -w` + `go vet` do pacote) —
feedback em segundos, sem bloquear; **hook `PreToolUse` em `git commit`** roda
`scripts/verificar.sh --rapido` e **bloqueia** o commit se falhar; **CI** roda
tudo de novo (o hook pode ser burlado, o CI não).

## Camada 2 — barrar o funcional

| Tipo | Onde | Como | Bloqueia merge? |
|---|---|---|---|
| **Unitários Go** | `backend/internal/**/*_test.go` | `go test -race -cover ./...`; fakes à mão (`fakeInfodesk`, `fakeBrain`, `relogioFalso`), table-driven quando há ≥ 3 casos; `store_test.go` roda nos dois backends (`PG_TEST_URL` no CI via `services: postgres`) | sim |
| **Unitários Python** | `agente-ia/tests/` | `pytest` (sem rede por default); fixtures compartilhadas em `tests/fakes.py`: `fake_llm(scripted)`, `FakeJEV(respostas)`, `gateway_falso(contratos)`, `sem_rag`; `_texto_de` para `content` str/lista | sim |
| **Contrato Go ↔ Python** | `contratos/**` | Go: `toolsapi` decodifica cada `request.json` e produz saída igual a `ok.json`/`erro-*.json` (campos e tipos); Python: `infodesk_tools` + `tool_policy` aceitam cada `ok.json`/`erro-*.json` e o `efeitos.py` reage certo; Contrato A idem em `server.py` e `brain/client.go` | sim |
| **E2E com mock-infodesk** | `backend/internal/e2e` (tag `e2e`) | sobe mock + gateway + cérebro (LLM e JEV falsos por env `LLM_FAKE=1`, `JEV_FAKE=1`), 5 cenários | sim (job separado, ~2 min) |
| **Evals de conversa (determinísticos)** | `agente-ia/evals/conversa/*.yaml` | `pytest -m evals_pr`: LLM scriptado + `FakeJEV`; asserts sobre tools chamadas/não chamadas, transferência, texto contém/não contém, proposta exigida antes de abrir | sim |
| **Evals de classificação (JEV real)** | `agente-ia/evals/classificacao` | `python -m evals.classificacao --jev --fonte sintetico` em PR **só se** o PR toca `src/classificacao/`, `config/taxonomia.yaml` ou `evals/classificacao/` (path filter) e o secret existe; compara com `evals/baseline.json` | sim para esses caminhos; regressão > 2 pts em macro-F1 ou acurácia@limiar falha |
| **Evals noturnos** | `evals-noturnos.yml` | JEV real no histórico + LLM real com juiz (rubrica persona/exatidão/não-inventa/pede-confirmação) | não (abre issue automática em regressão) |
| **Cobertura** | CI | Go ≥ 70 % por pacote novo; Python ≥ 80 % em `src/classificacao`, `src/tools`, `src/agent/tool_policy.py` (`pytest-cov --cov-fail-under` por módulo via `coverage report --fail-under` com include) | sim |

### Como escrever um eval (resumo; detalhe na skill `evals-agente`)

```yaml
# agente-ia/evals/conversa/abre-ticket-apos-confirmacao.yaml
id: conversa-010
descricao: reclamação simples vira ticket só depois do "sim"
sessao: {telefone: "+5531999990001", nome_contato: "Maria", contato: null}
jev:                       # respostas do FakeJEV por turno (turno 1, 2, ...)
  - {intencao: [reclamacao_problema, 0.93], categoria: [sem_conexao, 0.88], prioridade: [2, 0.7], frustracao: [1, 0.8], malicioso: 0.02, fora_de_escopo: 0.01}
  - {intencao: [confirmacao, 0.95], categoria: [sem_conexao, 0.9], prioridade: [2, 0.7], frustracao: [1, 0.8], malicioso: 0.01, fora_de_escopo: 0.01}
turnos:
  - cliente: "minha internet caiu de novo, já reiniciei o modem"
    llm:     # script do LLM falso: sequência de AIMessages do turno
      - tool: informar_identificacao   # (o cliente já mandou CPF em cenário anterior? não: então o LLM pergunta)
        args: {}
        pular: true
      - texto: "Poxa, Maria! Me passa seu CPF ou o número do contrato para eu registrar? 🙏"
    espera: {nao_chama: [abrir_chamado], contem: ["CPF"]}
  - cliente: "123.456.789-09"
    llm:
      - tool: informar_identificacao
        args: {cpf: "123.456.789-09"}
      - tool: preparar_abertura_chamado
        args: {titulo: "Sem conexão após reiniciar modem", descricao: "Cliente relata queda total..."}
      - texto: "Vou registrar assim: ... Posso abrir?"
    espera: {chama: [informar_identificacao, preparar_abertura_chamado], nao_chama: [abrir_chamado], proposta_no_estado: true}
  - cliente: "sim"
    llm:
      - tool: abrir_chamado
        args: {}
      - texto: "Pronto! Seu chamado é o 4821 ---  A equipe de Suporte assume daqui. 😊"
    espera: {chama: [abrir_chamado], gateway_recebeu: {endpoint: /tools/infodesk/ticket/abrir, chave_idempotencia: "*"}, contem: ["4821"], bolhas: 2}
```

O runner (`evals/conversa/runner.py`) monta `build_graph(llm=fake_llm(script),
tools=get_default_tools(), buscar_chunks=sem_rag, jev=FakeJEV(...))` com
`httpx` apontado para um gateway falso que responde com os JSON de `contratos/`
e registra as chamadas. Um cenário é um teste `pytest` parametrizado.

## O que bloqueia o merge (branch protection da `main`)

Checks obrigatórios (nomes dos jobs em `ci.yml`): `lint-python`, `lint-go`,
`arquitetura`, `test-python`, `test-go`, `contratos`, `e2e`, `evals-pr`,
`gitleaks`. Mais: **1 aprovação humana** (Daniel ou quem ele delegar),
conversa resolvida, branch atualizada com a `main` (merge queue ou rebase),
sem force-push. O agente `revisor` comenta no PR, mas sua aprovação **não**
substitui a humana (D13).

## Camada 3 — o revisor

Dois revisores por PR: o agente `revisor` (`.claude/agents/revisor.md`, carrega
`revisar-pr`) e um humano. O agente roda **antes** do humano e deixa um
comentário estruturado; o humano lê o diff com o comentário ao lado.

Checklist do revisor (resumo — a skill `revisar-pr` tem a versão completa):

1. **Escopo**: o PR faz só a etapa `E<n>.<m>`? Tocou arquivos fora da zona?
2. **Contratos**: mudou JSON de `contratos/` ou Contrato A sem atualizar os dois lados e os testes?
3. **Segurança (OWASP LLM)**: argumento de sessão no schema do LLM? Tool nova sem `POLITICAS`? Efeito colateral sem duas fases? Texto do Infodesk/erro interno chegando ao cliente? CPF/telefone em log INFO? HMAC sem tempo constante? Segredo em código?
4. **Idempotência e falhas**: retry em operação com efeito? `POST /tickets` sem proteção? Falha transitória × definitiva classificada?
5. **Determinismo**: decisão de negócio (fila, prioridade, confirmação) feita pelo LLM onde devia ser código?
6. **Testes**: existe teste para cada recusa de política, cada `motivo` de erro, cada ramo do roteador? Teste de arquitetura passou sem adicionar exceção em `legado-permitido.txt`?
7. **Persona**: mensagens novas seguem o tom da Sofia (pt-BR, "você", ≤ 1 emoji, sem prometer prazo)? Dos dois lados (prompt e gateway)?
8. **Docs**: `docs/plano/01` ou `02` precisam de atualização? ADR quando a decisão muda?
9. **Legado**: `legado-permitido.txt` só encolheu?

Ferramentas: o revisor usa `/code-review` (embutido nesta instalação do
Claude Code) para bugs de correção e `/security-review` para o item 3, e o
checklist acima para o resto. O plugin oficial `code-review` da Anthropic está
disponível no catálogo caso se queira o fluxo multi-agente; não é
obrigatório.

## Scripts

| Script | Faz |
|---|---|
| `scripts/verificar.sh --rapido` | ruff format --check + ruff check + mypy + lint-imports + `pytest` (unit) + gofmt + go vet + `go test ./...` (sem race) + teste de arquitetura. Alvo: < 90 s |
| `scripts/verificar.sh --completo` | `--rapido` + golangci-lint + `go test -race` + contratos + e2e (sobe mock) + `pytest -m evals_pr` |
| `scripts/verificar.sh --so-python` / `--so-go` | para sessões numa zona só |
| `.claude/hooks/formatar.sh` | chamado pelo hook `PostToolUse`; formata o arquivo editado conforme a extensão; nunca falha a sessão (avisa se a ferramenta não está instalada) |
| `.claude/hooks/guardar-commit.sh` | chamado pelo hook `PreToolUse` em comandos `git commit`; roda `--rapido` e devolve exit 2 (bloqueia) com o resumo da falha |
| `.claude/hooks/verificar-zona.sh` | `PreToolUse` em Edit/Write: se `ZONA_ARQUIVOS` (lista separada por `:`) estiver definida no ambiente da sessão e o caminho não casar com nenhum prefixo, bloqueia com mensagem |
| `scripts/instalar-hooks-git.sh` | `git config core.hooksPath .githooks` — o mesmo `verificar.sh --rapido` como `pre-commit` para humanos |

## Métricas de saúde (dashboards mínimos, E4.3)

- % de turnos decididos pelo JEV sem fallback (cobertura@limiar) e acurácia
  medida nos evals noturnos.
- % de conversas transferidas por origem (`cliente`, `anexo`, `confianca`,
  `frustracao`, `cerebro`, `disjuntor`).
- Tickets abertos / pendentes / falha por dia; duplicatas evitadas
  (`chamado_aberto`).
- p95 de latência do turno (webhook → última bolha).
