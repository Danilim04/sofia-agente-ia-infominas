# Evals do agente Sofia

Plano: `docs/plano/02-classificacao.md` (seção "Dataset de avaliação") e
`docs/plano/04-esteira-qualidade.md` (camada 2). Skill: `.claude/skills/evals-agente/`.
Este pacote vive **fora** de `src/` e nunca é importado por ele.

Estado em E1.5: **scaffolding**. Formatos, anonimizador, dataset sintético,
relatório e runners existem e são testados; o classificador real (E2.2) e a
execução dos cenários de conversa (E3.5) ainda não. Enquanto
`config/taxonomia.yaml` tiver `placeholder: true` (P1), as métricas de
categoria são informativas.

## Famílias

| Família | Onde | Determinística? | Roda | Comando |
|---|---|---|---|---|
| **Conversa** | `conversa/*.yaml` | sim (LLM scriptado + `FakeJEV` + gateway falso) | todo PR, bloqueia (`evals-pr`) | `pytest -m evals_pr evals/` |
| **Classificação** | `classificacao/*.jsonl` | `--llm` sim (fakes) · `--jev` não (JEV real) | PR que toca classificação; noturno | `python -m evals.classificacao --fonte sintetico\|historico [--jev\|--llm]` |
| **Juiz** | `conversa/juiz/*.yaml` (E4.4) | não (LLM real + juiz) | noturno | `python -m evals.juiz` |

Hoje: os cenários de conversa carregam e validam (teste verde) e a execução
está `xfail` ("runner entra em E3.5"); `--jev` e `--llm` caem no
`classificador_nulo` (sempre `outro`/`indefinida`/`media`, confiança 0; também
acessível por `--nulo`), que serve para provar o encanamento e produzir um
relatório — não para calibrar nada (`--atualizar-baseline` recusa nesse caso).

## Layout

```
evals/
  formatos.py          # dataclasses + validadores (campo e motivo) dos dois formatos
  relatorio.py         # métricas e Markdown
  anonimizar.py        # CLI: export bruto → JSONL com <CPF>/<TEL>/...
  baseline.json        # métricas de referência por "<fonte>/<modo>" (vazio até E2.4)
  classificacao/       # __main__ (CLI), sintetico.jsonl (v0, 189 casos), historico.jsonl (P9, não versionado ainda)
  conversa/            # TEMPLATE.yaml, cenários, runner.py (esqueleto), test_cenarios.py
  exemplos/            # export fictício para testar o anonimizador
  tests_evals/         # testes unitários do pacote (pytest evals/)
  bruto/               # GITIGNORED: export real com PII
  relatorios/          # GITIGNORED: saída dos runners
```

## Como rodar

```bash
cd agente-ia
venv/Scripts/python.exe -m pytest evals/ -q                 # tudo: unitários + cenários (xfail)
venv/Scripts/python.exe -m pytest -m evals_pr evals/ -q     # só o que bloqueia PR
venv/Scripts/python.exe -m evals.classificacao --fonte sintetico --llm
venv/Scripts/python.exe -m evals.classificacao --fonte sintetico --jev --relatorio evals/relatorios/jev.md
venv/Scripts/python.exe -m evals.conversa                   # lista e valida os cenários
venv/Scripts/python.exe -m ruff check evals && venv/Scripts/python.exe -m mypy evals
```

(No Linux/macOS, `venv/bin/python`.) Os testes não usam rede nem `.env`; o
`conftest.py` troca qualquer `TYPESAFE_API_KEY`/`OPENROUTER_API_KEY` presente
por um valor falso.

`scripts/verificar.sh --rapido` hoje cobre `src` e `tests`; os comandos acima
são a verificação deste pacote até a esteira incluí-los (ajuste em
`pyproject.toml`/`verificar.sh`, fora da zona de E1.5).

## Escrever um caso de classificação

Uma linha JSON em `classificacao/sintetico.jsonl` (ou `historico.jsonl`):

```json
{"id": "cls-0042", "fonte": "sintetico",
 "estado": ["Cliente: ta caindo", "Sofia: A conexão fica fora ou volta sozinha?", "Cliente: volta, mas cai o dia todo"],
 "esperado": {"intencao": "reclamacao_problema", "categoria": "instabilidade", "prioridade_min": "media", "frustracao_max": 1},
 "notas": "fronteira sem_conexao → instabilidade"}
```

- `estado`: mensagens como `estado_para_jev` montaria (`Cliente:`/`Sofia:`),
  pt-BR coloquial, com gíria, abreviação e erro de digitação.
- `esperado.intencao` ∈ `INTENCOES` de `formatos.py` (as 7 do `02`);
  `categoria` ∈ `config/taxonomia.yaml`; `prioridade_min` é o **piso**
  (acerto se o previsto for ≥); `frustracao_min`/`frustracao_max` são piso e
  teto do score de frustração (o piso ≥ 2 marca os casos irritados: é
  `frustracao ≥ 3` que dispara transferência); `malicioso_min` e
  `fora_de_escopo_min` são pisos dos Nouls (injeção, off-topic). Frustração e
  Nouls ainda não são avaliados pelo runner (TODO(E2.4)).
- Divergência registrada: o `02` escreve `prioridade` e `frustracao_min`; a
  skill `evals-agente` escreve `prioridade_min` e `frustracao_max`. Este pacote
  aceita `prioridade_min`, `frustracao_min` e `frustracao_max`; TODO(E2.4):
  alinhar o texto do `02` (fora da zona de E1.5).
- Hipótese de trabalho (TODO(E2.2)): pedidos de serviço sem reclamação
  ("manda o boleto", "quero cancelar") recebem intenção `outro`; perguntas
  ("como cancelo?") recebem `duvida_informacao`. **Desempate**: se a mensagem
  tem pedido e pergunta ("quero cancelar, como faço?"), a pergunta vence —
  a resposta útil é informativa antes de qualquer ação. `status_chamado` só
  para chamado já aberto (pedido de cancelamento sem retorno é
  `reclamacao_problema`). Se `perguntas.py` definir diferente, ajuste o
  dataset no mesmo PR.
- ≥ 15 por categoria; anote casos de fronteira em `notas` com a palavra
  "fronteira" (o teste conta). `historico` exige `rotulador`.

## Escrever um cenário de conversa

Copie `conversa/TEMPLATE.yaml` (comentado) para `conversa/<comportamento>.yaml`.
Regras: um comportamento por arquivo; `jev` tem **uma** resposta por turno;
o último passo de `llm` é sempre um `texto`; `espera` só com chaves de
`CHAVES_ESPERA`; **todo cenário tem um negativo** (`nao_chama`, `nao_contem`
ou `bloqueado`) — `test_cenarios.py` falha sem ele. Os fakes vêm de
`tests/fakes.py` (não copie para cá).

## Anonimizar o histórico (P9)

```bash
# 1. export do Infodesk em evals/bruto/ (gitignored, negado ao agente)
venv/Scripts/python.exe -m evals.anonimizar evals/bruto/export.csv \
    --saida evals/bruto/historico-anon.jsonl --coluna-texto mensagem \
    --nomes config/nomes.txt --amostra 50
# 2. ler os 50 da amostra (e grep por dígitos/arrobas no arquivo todo) — revisão humana obrigatória
# 3. rotulagem por alguém da Infominas (planilha → script importa; E2.4) preenche esperado/rotulador
# 4. só então mover para evals/classificacao/historico.jsonl e commitar
```

Tokens: `<CPF>` `<CNPJ>` `<TEL>` `<EMAIL>` `<CEP>` `<PLACA>` `<NOME>`.
Nomes só saem com a lista (`config/nomes.txt`, gitignored; exemplo em
`exemplos/nomes.txt`): monte-a a partir da coluna de contato do próprio
export. A busca não distingue caixa, então **não liste sozinhos nomes que
também são palavras comuns** (Luz, Rosa, Flor, Sol, Neves): "a luz do modem"
viraria "a <NOME> do modem" e destruiria o sinal de `sem_conexao`; use o nome
completo ("Luz Silva"). **Endereço** (rua, número, bairro) não tem regex: é
o principal alvo da revisão manual, junto com apelidos e nomes fora da lista. Regra de projeto: **sobre-mascarar é seguro, sub-mascarar não** —
um protocolo de 8 dígitos virar `<CEP>` é aceitável; um CPF escapar, não.
Teste rápido: `python -m evals.anonimizar evals/exemplos/export-exemplo.csv --saida /tmp/x.jsonl --nomes evals/exemplos/nomes.txt --amostra 3`.

## Interpretar o relatório (`relatorio.py`)

O runner grava `evals/relatorios/classificacao-<fonte>-<modo>.md` e um `.json`
com as métricas planas (0–100). Por pergunta (`intencao`, `categoria`,
`prioridade`):

| métrica | o que diz | critério inicial (02) |
|---|---|---|
| acurácia | fração de acertos | — |
| macro-F1 | média do F1 por classe (classe rara pesa igual); só para perguntas exatas | categoria ≥ 0.85 no histórico |
| cobertura@limiar | fração que o JEV decidiria sozinho (`confianca ≥ limiar`); limiares vêm dos argumentos `--limiar-*` (defaults do `02`) | quanto maior, menos fallback LLM |
| acurácia@limiar | acurácia **só** nos decididos pelo JEV | ≥ 0.95 |
| ECE | erro de calibração (10 bins): confiança 0.8 deveria acertar ~80 %; só para perguntas exatas | ≤ 0.08 |
| matriz de confusão | linha = esperado, coluna = previsto: onde o modelo troca categorias parecidas | guia para `nao_e`/`exemplos` da taxonomia |
| latência p50/p95 | do classificador, por caso (*nearest-rank*) | p95 < 2 s (timeout do JEV) |

`prioridade` é ordinal com piso (`previsto ≥ prioridade_min`): tem acurácia e
cobertura, não F1, confusão nem calibração (a confiança é do score exato, o
acerto é "≥ piso": o ECE não significaria nada). Leitura típica: cobertura baixa com
acurácia@limiar alta → limiar conservador demais; cobertura alta com
acurácia@limiar < 0.95 → subir o limiar ou melhorar `nao_e`.

## Baseline e regressão (`baseline.json`)

```json
{"versao": 1, "atualizado_em": null, "justificativa": "...", "tolerancia_pontos": 2.0,
 "metricas": {"sintetico/jev": {"categoria.macro_f1": 91.3, "categoria.acuracia_no_limiar": 97.0, "...": 0}}}
```

- O runner compara `metricas["<fonte>/<modo>"]` com a rodada atual; queda
  **> `tolerancia_pontos`** em qualquer `*.macro_f1` ou `*.acuracia_no_limiar`,
  ou **aumento** > tolerância em `*.ece` (menor é melhor; no JSON o ECE está
  em pontos: 0.08 = 8.0), devolve exit 1 (o CI falha). Chave ausente = nada a
  comparar.
- Atualizar (só ao melhorar, ou com decisão registrada):
  `python -m evals.classificacao --fonte sintetico --jev --atualizar-baseline --justificativa "perguntas.py: exemplos de instabilidade"`
  e commitar o `baseline.json` **no mesmo PR**, com a justificativa no texto do PR.
- Hoje está vazio de propósito: o classificador nulo não é referência. O
  primeiro baseline real entra em E2.4 com `--jev` (precisa de `TYPESAFE_API_KEY`).
