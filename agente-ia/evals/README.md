# Evals do agente Sofia

Plano: `docs/plano/02-classificacao.md` (seção "Dataset de avaliação") e
`docs/plano/04-esteira-qualidade.md` (camada 2). Skill: `.claude/skills/evals-agente/`.
Este pacote vive **fora** de `src/` e nunca é importado por ele.

Estado em E2.4: o runner de classificação usa o `src.classificacao.classificar()`
**real** em três modos (`--offline`, `--jev`, `--llm`), mede calibração, janelas de
contexto (C-JEV1) e tudo que o `02` define, e roda no CI sem rede. **Nenhuma
medição do JEV existe ainda**: não há `TYPESAFE_API_KEY` (P8), então
`baseline.json` segue vazio e o que sai do modo offline mede o encanamento, não o
JEV. Enquanto `config/taxonomia.yaml` tiver `placeholder: true` (P1), as métricas
de categoria são informativas (os testes de categoria com JEV real ficam `xfail`).

## Famílias

| Família | Onde | Determinística? | Roda | Comando |
|---|---|---|---|---|
| **Conversa** | `conversa/*.yaml` | sim (LLM scriptado + `FakeJEV` + gateway falso) | todo PR, bloqueia (`evals-pr`) | `pytest -m evals_pr evals/` |
| **Classificação** | `classificacao/*.jsonl` | `--offline` sim (JEV/LLM simulados) · `--jev`/`--llm` não (serviços reais) | `--offline` todo PR; `--jev` em PR que toca classificação e noturno | `python -m evals.classificacao --fonte sintetico\|historico [--offline\|--jev\|--llm] [--janelas]` |
| **Juiz** | `conversa/juiz/*.yaml` (E4.4) | não (LLM real + juiz) | noturno | `python -m evals.juiz` |

Os cenários de conversa carregam, validam e **executam** contra o grafo
real (E3.5: LLM scriptado + `FakeJEV` + gateway falso; `python -m evals.conversa`
imprime o resultado de cada um — só os de `conversa/*.yaml`, não os de `pendentes/`).
O classificador nulo (`--nulo`: sempre `outro`/`indefinida`/`media`, confiança 0)
continua como smoke do encanamento.

## Layout

```
evals/
  formatos.py          # dataclasses + validadores (campo e motivo) dos dois formatos
  relatorio.py         # métricas (ECE, macro-F1, cobertura@limiar…), comparação de janelas e Markdown
  anonimizar.py        # CLI: export bruto → JSONL com <CPF>/<TEL>/...
  baseline.json        # métricas de referência por "<fonte>/<modo>" (vazio: falta a chave do JEV, P8)
  classificacao/       # __main__ (CLI), adaptador.py (liga ao classificar() real), simulado.py (JEV/LLM do modo offline),
                       #   sintetico.jsonl (v1, 237 casos), historico.jsonl (P9, não versionado ainda)
  conversa/            # TEMPLATE.yaml, cenários, runner.py, test_cenarios.py
  exemplos/            # export fictício (anonimizador) e relatorio-offline-exemplo.md (relatório offline commitado)
  tests_evals/         # testes unitários do pacote (pytest evals/)
  bruto/               # GITIGNORED: export real com PII
  relatorios/          # GITIGNORED: saída dos runners
```

## Como rodar

```bash
cd agente-ia
venv/Scripts/python.exe -m pytest evals/ -q                 # tudo: unitários + cenários (xfail)
venv/Scripts/python.exe -m pytest -m evals_pr evals/ -q     # só o que bloqueia PR
venv/Scripts/python.exe -m evals.classificacao --fonte sintetico --offline   # sem rede nem chave (o padrão)
venv/Scripts/python.exe -m evals.conversa                   # lista e valida os cenários
venv/Scripts/python.exe -m ruff check evals && venv/Scripts/python.exe -m mypy evals
```

(No Linux/macOS, `venv/bin/python`.) Os testes não usam rede nem `.env`; o
`conftest.py` troca qualquer `TYPESAFE_API_KEY`/`OPENROUTER_API_KEY` presente
por um valor falso (exceto nos testes marcados `jev`/`llm_real`, que existem para
usar a chave real e ficam fora da rodada por default).

`scripts/verificar.sh --rapido` já cobre `evals/` (ruff, mypy e pytest).

## Evals de classificação: os modos

`python -m evals.classificacao --fonte sintetico|historico [modo] [opções]`.
Todos os modos (menos `--nulo`) executam o `src.classificacao.classificar()`
**real** — estado montado por `estado_para_jev` com PII mascarada, uma chamada com
as 6 perguntas, roteador, limiares `JEV_LIMIAR_*`, fallback, esclarecimento. O que
muda é quem responde:

| modo | JEV | fallback LLM | precisa de | pytest |
|---|---|---|---|---|
| `--offline` (padrão) | `JEVSimulado` (classe `FakeJEV` de `tests/fakes.py`, lexical e determinístico) | `FallbackSimulado` (`FakeFallbackLLM`) | nada | roda por default |
| `--jev` | JEV real (`JEVTypeSafe`, modelo pinado em `JEV_MODEL`) | LLM real se houver `OPENROUTER_API_KEY`; senão nenhum, com aviso | `TYPESAFE_API_KEY` (P8) | `-m jev` |
| `--llm` | nenhum | LLM real (`FallbackLLMEstruturado`) | `OPENROUTER_API_KEY` | `-m llm_real` |
| `--nulo` | — | — | nada | smoke do encanamento |

```bash
# offline, sem rede: gera evals/relatorios/<data>-classificacao-sintetico-offline.md (+ .json)
venv/Scripts/python.exe -m evals.classificacao --fonte sintetico --offline
# comparação de janelas de contexto 1/3/6/10 (C-JEV1); --janela N escolhe a do detalhe (padrão 6)
venv/Scripts/python.exe -m evals.classificacao --fonte sintetico --offline --janelas
venv/Scripts/python.exe -m evals.classificacao --fonte sintetico --janelas 1,6 --jev   # lista própria
# JEV real (precisa da chave): sem ela o comando sai com código 2 e diz o porquê
TYPESAFE_API_KEY=... OPENROUTER_API_KEY=... venv/Scripts/python.exe -m evals.classificacao --fonte sintetico --jev --janelas
# só o LLM de fallback
OPENROUTER_API_KEY=... venv/Scripts/python.exe -m evals.classificacao --fonte sintetico --llm
# testes que usam a chave real (excluídos por default)
TYPESAFE_API_KEY=... venv/Scripts/python.exe -m pytest -m jev evals/ -q
OPENROUTER_API_KEY=... venv/Scripts/python.exe -m pytest -m llm_real evals/ -q
```

Saídas: `evals/relatorios/<data>-classificacao-<fonte>-<modo>[-janelas].md` e um
`.json` ao lado com as métricas planas (gitignored). Código de saída: 0 ok · 1
regressão acima da tolerância contra `baseline.json` · 2 dataset inválido, chave
ausente ou argumento errado.

**O modo offline nunca é desempenho do JEV.** O `JEVSimulado` é um casador de
palavras que só enxerga o `estado` que o `classificar()` lhe manda (nada do
`esperado`), com latência sintética e ECE próprio; ele existe para provar que o
runner, o roteador, o mascaramento de PII, a janela de contexto e o relatório
funcionam no CI. Todo relatório offline abre com a faixa "Exemplo offline com
fakes, não é medição real", e `--atualizar-baseline` o recusa. O exemplo
commitado (`exemplos/relatorio-offline-exemplo.md`) é desse modo, tem teste que o
mantém em dia e é só uma amostra do formato: **não cite os números dele**.

A comparação de janelas (`--janelas`) repete a rodada com `JEV_JANELA_MSGS` =
1, 3, 6 e 10 (a janela inclui a mensagem atual) e acrescenta uma tabela com
acurácia da intenção, acurácia/macro-F1/cobertura@limiar/acurácia@limiar/ECE da
categoria, tamanho médio do estado enviado e latência. No sintético só 39 casos
têm histórico (15 passam de 6 mensagens, 3 passam de 10): a tabela serve para ver
o mecanismo; a decisão da C-JEV1 precisa do histórico real (P9).

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
  `fora_de_escopo_min` são pisos dos Nouls (injeção, off-topic) e
  `malicioso_max`/`fora_de_escopo_max` são os tetos (mensagem legítima: o Noul
  não pode disparar). Desde a E2.4 o runner avalia frustração (faixa) e os dois
  Nouls (binários: `sim` = ≥ piso, `nao` = ≤ teto; confiança `|2p − 1|`); caso sem
  nenhum desses campos não entra na pergunta. Atenção: o `previsto` da matriz sai
  do piso/teto do caso, não de `p > 0.5` (p = 0.7 com piso 0.8 aparece como `nao`).
- Divergência registrada: o `02` escreve `prioridade` e `frustracao_min`; a
  skill `evals-agente` escreve `prioridade_min` e `frustracao_max`. Este pacote
  aceita `prioridade_min`, `frustracao_min` e `frustracao_max`. Alinhar o texto do
  `02` (que está em `docs/`, fora da zona de evals) fica como follow-up.
- Hipótese de trabalho (`perguntas.py` da E2.2 não fixa isto; revisar com o P9): pedidos de serviço sem reclamação
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

## Coletar conversas reais do Infodesk (E3.5)

`scripts/coletar-conversas-infodesk.py` (stdlib, **só `GET`**, chave só do
ambiente) transforma as conversas de WhatsApp do Infodesk em matéria-prima de
eval. Tudo que ele grava vai para `evals/bruto/` (gitignored — o script se
recusa a gravar onde o git não ignora) e o terminal recebe só contagens.

```bash
set -a && . ./.env && set +a            # INFODESK_BASE_URL e INFODESK_API_KEY
python scripts/coletar-conversas-infodesk.py --meses 12 --nomes-extra evals/bruto/nomes-extra.txt
#  → evals/bruto/conversas-whatsapp-<data>.json        bruto (contato e senderName reais)
#  → evals/bruto/conversas-whatsapp-<data>-pre.json    senderName/contato viram token, id vira hash
#  → evals/bruto/conversas-whatsapp-<data>-nomes.txt   nomes de contato/autor (+ extras) p/ o anonimizador
cd agente-ia
venv/Scripts/python.exe -m evals.anonimizar evals/bruto/conversas-whatsapp-<data>-pre.json \
    --saida evals/bruto/conversas-whatsapp-anon.jsonl \
    --nomes evals/bruto/conversas-whatsapp-<data>-nomes.txt
# varredura própria (telefone com/sem +55, CPF, CNPJ, e-mail, CEP, URL, nomes) → 0 ocorrências
# revisão humana (endereços e nomes fora da lista não têm regex) ANTES de qualquer commit (D12)
```

- Descarta nota interna, grupos de WhatsApp e conversas em laço de eco (≥ 500
  mensagens com < 5 % de corpos distintos — conversa bot ↔ bot); agrupa por
  `conversationId` em ordem cronológica e junta `status`, `departmentId` e
  `ticketNumber` de `GET /conversations`.
- `--de-arquivo <bruto.json>` refaz o `-pre.json` e o `-nomes.txt` sem chamar a
  API (útil ao ajustar a lista de nomes).
- A lista de nomes deixa de fora palavras que o corpus escreve quase sempre em
  minúscula (`rede`, `todo`): um contato chamado "Rede Fulano" não pode virar
  `<NOME>` no texto inteiro.
- Os cenários `conversa/pendentes/real-*.yaml` são **reescritos** a partir dos
  padrões (frase parafraseada, telefone `+5531999990xxx`, nome fictício) e ainda
  **não foram validados pelo CI**: ficam fora do glob do runner até alguém
  promovê-los (ver "Como rodar depois"). Nenhum arquivo do bruto nem do
  anonimizado entra no git antes da revisão.
- `python -m evals.pre_rotular` monta `evals/bruto/historico.candidato.jsonl` (um
  caso por conversa, as 400 mais recentes) **sem chamar o JEV**: `esperado: null`,
  `rotulador: "pendente"`. Com `--jev` vira pré-rótulo (ver abaixo).
- `xfail: "motivo"` no topo de um cenário marca um comportamento correto que a
  Sofia ainda não tem (xfail estrito: se passar, o teste falha e o marcador sai).
  `contato: telefone|cpf|contrato` na `sessao` diz que o gateway já identificou
  o cliente por essa forma.

## Como rodar depois (nada disto foi executado na E3.5 de coleta)

1. **Pré-rotular o dataset com o JEV** (hipótese a revisar por humano, não
   gabarito; precisa de `TYPESAFE_API_KEY`, vinda de `JEV_API_TOKEN`; ~315
   chamadas, 4 em paralelo). O rascunho da taxonomia por área vem de
   `scripts/descobrir-taxonomia.py --mesas "Incidente,Solicitações" --por area --saida descoberta-infodesk/`:

   ```bash
   set -a && . ./.env && set +a && export TYPESAFE_API_KEY="$JEV_API_TOKEN"
   cd agente-ia
   venv/Scripts/python.exe -m evals.pre_rotular --jev --taxonomia ../descoberta-infodesk/taxonomia.rascunho.yaml
   ```

   Saída: `evals/bruto/historico.candidato.jsonl` com `esperado`, `confianca` e
   `rotulador: "jev-prelabel"`. Depois da revisão humana, tire a chave `confianca`
   e mova para `classificacao/historico.jsonl`.
2. **Executar os cenários reais.** O runner só enxerga `conversa/*.yaml`. Para
   promover: `git mv evals/conversa/pendentes/real-*.yaml evals/conversa/`, rode
   `python -m evals.conversa` e `pytest -m evals_pr evals/conversa/test_cenarios.py`
   e leia as falhas. O que a Sofia ainda não faz fica com `xfail: "motivo"` (estrito);
   comportamento errado do cenário se corrige no YAML. Cenários com
   `xfail` já escritos: `real-assunto-comercial-planos` e `real-financeiro-segunda-via`
   (a categoria só é fixada para `reclamacao_problema`). Atenção: as categorias dos
   cenários são as do placeholder (P1); com a taxonomia real (E5.1) é preciso
   remapeá-las (e-mail, VPN, acesso e impressora hoje não têm categoria).
3. **Replay no mock** (cérebro + gateway + mock-infodesk): `scripts/start-all.sh`
   (no Windows, com `agente-ia/venv` acessível) e, para cada turno do cenário:

   ```bash
   curl -sS -X POST http://localhost:9000/mock/cliente/mensagem      -H 'Content-Type: application/json; charset=utf-8'      -d '{"telefone":"+5531999990100","nome":"Marta","texto":"<fala do cliente>"}'
   curl -sS "http://localhost:9000/mock/estado?telefone=%2B5531999990100"   # bolhas e tickets
   ```

   Use um `OPENROUTER_API_KEY` real e `TYPESAFE_API_KEY` (JEV). O `curl` do Git
   Bash no Windows pode mandar a acentuação fora de UTF-8; prefira um cliente
   Python/`urllib` com `json.dumps(...).encode("utf-8")`.
4. **O que depende da chave do OpenRouter**: o LLM do cérebro (todo turno que
   passa pelo agente) e o fallback LLM da classificação. Sem ela só rodam os
   cenários de evals (LLM scriptado), o webhook assinado do mock → gateway, o JEV
   e o caminho de resposta padrão (injeção de prompt). Antes de usar uma chave
   real, confira se `APP_TITLE` não tem caractere não-ASCII (o header `X-Title`
   quebra a chamada com `UnicodeEncodeError`).

## Interpretar o relatório (`relatorio.py`)

O runner grava `evals/relatorios/<data>-classificacao-<fonte>-<modo>.md` e um `.json`
com as métricas planas (0–100). Abra pelo topo: a faixa em negrito diz se o
número é medição (`--jev`/`--llm`) ou exemplo offline com fakes (não é medição).
Depois vêm o contexto (modo, JEV, fallback, janela, taxonomia — placeholder ou
não), o resumo por pergunta, o pipeline e, por pergunta, F1 por classe,
calibração e matriz de confusão. As perguntas do relatório:

| pergunta | o que é avaliado |
|---|---|
| `intencao`, `categoria` | a resposta **bruta do JEV** (escolha + `confidence`). É isto que a cobertura@limiar, a acurácia@limiar e a calibração medem: o JEV decidiria sozinho quando `confidence ≥ limiar`. Sem JEV na rodada (`--llm`, ou JEV fora naquele turno) vale a resposta do fallback, com confiança 0 |
| `categoria_final` | a categoria que o **roteador fixou** (com fallback, esclarecimento e `indefinida`), só nos casos de `reclamacao_problema` — único caso em que o roteador classifica categoria. É o que o cliente vive; compare com `categoria` para ver quanto o fallback e o esclarecimento recuperam |
| `prioridade` | nível **bruto do JEV** (Score) contra `prioridade_min`; ordinal |
| `prioridade_final` | a prioridade que o roteador fixou (default da categoria + piso `prioridade_minima` da taxonomia) contra `prioridade_min`; o piso é regra de código, então esta mede o sistema, não o JEV |
| `frustracao` | nível do JEV dentro da faixa `frustracao_min..frustracao_max`; ordinal |
| `malicioso`, `fora_de_escopo` | Noul binário (ver "Escrever um caso"); confiança `|2p − 1|`, limiar 0.6 ⇔ `p ≥ 0.8` ou `p ≤ 0.2` |

Cada métrica, por pergunta (`intencao`, `categoria`, `prioridade`…):

| métrica | o que diz | critério inicial (02) |
|---|---|---|
| acurácia | fração de acertos | — |
| macro-F1 | média do F1 por classe (classe rara pesa igual); só para perguntas exatas | categoria ≥ 0.85 no histórico |
| cobertura@limiar | fração que o JEV decidiria sozinho (`confianca ≥ limiar`); limiares vêm dos argumentos `--limiar-*` (defaults do `02`) | quanto maior, menos fallback LLM |
| acurácia@limiar | acurácia **só** nos decididos pelo JEV | ≥ 0.95 |
| ECE | erro de calibração (10 bins): confiança 0.8 deveria acertar ~80 %; só para perguntas exatas | ≤ 0.08 |
| matriz de confusão | linha = esperado, coluna = previsto: onde o modelo troca categorias parecidas | guia para `nao_e`/`exemplos` da taxonomia |
| latência p50/p95 | do classificador, por caso (*nearest-rank*) | p95 < 2 s (timeout do JEV) |

`prioridade` e `frustracao` são ordinais com piso/faixa: têm acurácia e cobertura, não
F1, confusão nem calibração (a confiança é do score exato, o acerto é "dentro
da faixa": o ECE não significaria nada). Leitura típica: cobertura baixa com
acurácia@limiar alta → limiar conservador demais; cobertura alta com
acurácia@limiar < 0.95 → subir o limiar ou melhorar `nao_e`; ECE alto com
confiança sempre perto de 1 → o JEV está sobreconfiante, suba o limiar até a
acurácia@limiar chegar a 0.95 (a calibração em 10 bins mostra em que faixa de
confiança a acurácia cai).

Seção **Pipeline** (contexto, sem critério de aceite): `taxa_fallback_chamado`
(quanto do tráfego gastaria o LLM), `taxa_esclarecimento` (perguntas de
esclarecimento), `taxa_transferencia`, `taxa_jev_indisponivel` (JEV fora do ar no
turno — alerta do `02` se > 5 %), `taxa_categoria_indefinida_em_reclamacao` e
`media_estado_caracteres` (tamanho do estado enviado, que cresce com a janela).

Se o JEV falhar em algum turno (`taxa_jev_indisponivel` > 0), o relatório avisa:
nesses turnos `intencao`/`categoria` valem a resposta do fallback com confiança 0, o
que rebaixa a acurácia e distorce a calibração do JEV — resolva a indisponibilidade
e rode de novo antes de gravar baseline.

O `.json` ao lado leva `modo`, `simulado` e `banner`: quem consome o JSON (E4.4,
gráficos) vê que o offline não é medição.

Latência: no `--jev`/`--llm` é medida no cliente (a chamada inteira do
`classificar()`, fallback incluso); no offline é sintética.

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
- Hoje está vazio de propósito: nem o classificador nulo nem o modo offline
  (fakes) são referência — `--atualizar-baseline` recusa o offline. O primeiro
  baseline real exige `--jev` com `TYPESAFE_API_KEY` (P8): rode, leia o relatório,
  grave com justificativa no mesmo PR. Uma janela diferente de 6 (`--janela N`)
  usa a chave `<fonte>/<modo>@jN`, para não ser comparada com a de referência.
- Mexeu em `perguntas.py`, `taxonomia.yaml` ou limiares? Rode `--jev --janelas`
  antes e depois e cole o resumo no PR (skill `jev-classificacao`).
