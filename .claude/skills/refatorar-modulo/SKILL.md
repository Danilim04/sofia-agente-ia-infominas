---
name: refatorar-modulo
description: Checklist para refatorar ou remover um módulo do Zapin sem quebrar contratos — o que pode sair, o que tem de sobreviver, como provar que nada quebrou e como encolher a lista de legado permitido. Carregue nas etapas de remoção do domínio antigo (E0.*, E1.1, E1.3, E3.2, E4.6) ou ao renomear/mover qualquer pacote.
---

# Refatorar um módulo do Zapin

Referência do que existia: `docs/zapin/CLAUDE-legado.md` (arquitetura
antiga, com os porquês). O que deve existir: `docs/plano/00-visao-geral.md`
(tabela "O que muda").

## Antes de tocar

1. Leia a etapa em `docs/plano/03-etapas.md` e a **zona**. Fora da zona, não edite
   (defina `ZONA_ARQUIVOS` na sessão para o hook impor).
2. `grep -rn` pelo símbolo/pacote que vai sair: liste **todos** os pontos de uso,
   inclusive testes e `contratos/`. Se um uso está fora da zona, pare e relate.
3. Rode `scripts/verificar.sh --rapido` e guarde o resultado: é o seu baseline.

## Invariantes que sobrevivem a qualquer refatoração

- Contrato A (`POST /chat`): campos só **aumentam** (compatível); mudanças vão
  em `contratos/contrato-a/` e nos dois lados no mesmo PR.
- `store.Store`: toda mudança nos dois backends + `store_test.go`.
- `tool_policy.POLITICAS`: nenhuma tool sem entrada; nenhum arg de sessão no schema.
- Nós do grafo continuam **fábricas com injeção de dependência** e funções puras
  que devolvem delta do estado; nunca mutar estado persistido.
- Guardrails de saída (citações, vazamento interno, ação sem ferramenta) ficam.
- Engine Go: fila por conversa, coalescência, disjuntor, transferência única,
  bolhas — podem mudar de dependência, não de comportamento (testes existentes
  são a especificação; adapte os fakes, não apague asserts).

## Remover um pacote/módulo (passo a passo)

1. Crie a interface que o resto do código vai usar (`Transporte`, `ClienteJEV`...),
   faça o módulo antigo satisfazê-la, troque os usos, rode os testes.
2. Só então apague o módulo antigo e seus testes **específicos**; testes de
   comportamento que o usavam ganham um fake.
3. Remova variáveis de ambiente mortas em `config` + `.env.example` (os dois níveis) + `docker-compose*.yml` + `deploy/env/*.tpl` + workflows.
4. Renomeie strings de domínio (`zapin_*`, `ZPF-`, "Azapfy") só dentro da zona.
5. Edite `docs/plano/legado-permitido.txt`: remova as linhas dos caminhos que
   ficaram limpos. **Nunca adicione linha.**
6. Rode `--rapido`; depois `--completo` se tocou Go e Python.

## Renomear (módulo Go, coleção, prefixos)

- Módulo Go: `go.mod` + `gofmt -r`/`sed` em todos os imports + `go build ./...`.
- Constantes de domínio (nome do assistente, prefixo de ID): **um** lugar por
  processo (`nomeAssistente` no Go; `prompts.py`/`falhas.py` no Python);
  mantenha os dois alinhados e cite isso no PR.

## Prova de que nada quebrou (cole no PR)

- `scripts/verificar.sh --rapido` verde; diff de testes mostrando que asserts
  de comportamento foram mantidos.
- `grep -rni "<termo removido>" backend agente-ia/src` vazio ou coberto por `legado-permitido.txt`.
- Para Go: `go vet` + teste de arquitetura (`arquitetura_test.go`) sem exceção nova.
- Para Python: `lint-imports` verde; `test_arquitetura.py` verde.
