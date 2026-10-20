---
name: evals-agente
description: Como escrever e rodar evals do agente Sofia — cenários de conversa (LLM scriptado + FakeJEV), evals de classificação (JEV real e fallback LLM), anonimização do histórico, métricas e critérios de regressão. Carregue ao criar/alterar qualquer arquivo em agente-ia/evals/, ao mexer em classificação/prompts/política, ou quando um PR precisar provar comportamento de conversa.
---

# Evals do agente

Plano: `docs/plano/02-classificacao.md` (seção "Dataset") e
`docs/plano/04-esteira-qualidade.md` (camada 2). Diretório: `agente-ia/evals/`.

## Três famílias

| Família | Determinística? | Quando roda | Comando |
|---|---|---|---|
| **Conversa** `evals/conversa/*.yaml` | sim (LLM scriptado + `FakeJEV` + gateway falso com `contratos/`) | todo PR (`pytest -m evals_pr`) — bloqueia | `python -m pytest -m evals_pr` |
| **Classificação** `evals/classificacao/*.jsonl` | não (JEV real) / sim (`--llm` com fake) | PR que toca classificação (com `TYPESAFE_API_KEY`); noturno | `python -m evals.classificacao --fonte sintetico\|historico --jev\|--llm` |
| **Juiz** `evals/conversa/juiz/*.yaml` | não (LLM real + juiz) | noturno | `python -m evals.juiz` |

## Escrever um cenário de conversa

Copie `evals/conversa/TEMPLATE.yaml` (formato completo em `04`, seção "Como
escrever um eval"). Regras:

- Um cenário prova **um** comportamento (nome do arquivo = o comportamento:
  `nao-abre-sem-confirmacao.yaml`, `audio-transfere.yaml`).
- `jev:` lista as respostas do `FakeJEV` por turno; `llm:` é a sequência exata
  de mensagens do LLM falso no turno (`tool` + `args` ou `texto`).
- `espera:` só com asserts observáveis: `chama`, `nao_chama`, `transfere`,
  `contem`, `nao_contem`, `bolhas`, `proposta_no_estado`, `gateway_recebeu`,
  `classificacao` (`{categoria, origem}`), `bloqueado` (safe_response).
- Cobrir sempre o **negativo** (o que NÃO pode acontecer: abrir sem "sim",
  responder com humano responsável, inventar número de chamado).
- Cenários de segurança: injeção direta, injeção via `<documento_externo>`,
  tentativa de mudar persona, tentativa de passar `telefone` como arg.

## Escrever um caso de classificação

```json
{"id": "cls-0042", "fonte": "sintetico", "estado": ["Cliente: boa tarde, a net tá caindo toda hora desde ontem"],
 "esperado": {"intencao": "reclamacao_problema", "categoria": "lentidao", "prioridade_min": "media", "frustracao_max": 2},
 "notas": "instabilidade ≠ sem_conexao"}
```

- `estado` é a lista de mensagens como `estado_para_jev` montaria (pt-BR
  coloquial, erros de digitação, abreviações: `net`, `vcs`, `pq`).
- ≥ 15 casos por categoria; inclua casos de **fronteira** entre categorias
  parecidas e casos `indefinida`.
- Nunca commite texto real sem passar por `evals/anonimizar.py` e revisão
  humana; o bruto fica em `evals/bruto/` (gitignored).

## Métricas e critérios (relatorio.py)

Acurácia e macro-F1 por pergunta; cobertura@limiar e acurácia@limiar;
calibração (10 bins, ECE); matriz de confusão; latência p50/p95. Baseline em
`evals/baseline.json`; **regressão > 2 pontos** em macro-F1 ou
acurácia@limiar falha o PR. Ao melhorar, atualize o baseline no mesmo PR com
justificativa.

## Anonimização (`evals/anonimizar.py`)

Entrada: export (CSV/JSON) em `evals/bruto/`. Substituições: CPF/CNPJ →
`<CPF>`/`<CNPJ>`, telefone → `<TEL>`, e-mail → `<EMAIL>`, CEP → `<CEP>`,
placa → `<PLACA>`, nomes de `config/nomes.txt` → `<NOME>`. Saída: JSONL +
relatório de contagem por tipo. Revisão manual obrigatória (`--amostra 50`
imprime 50 casos para conferência) antes de mover para `evals/classificacao/historico.jsonl`.

## Fakes compartilhados (`agente-ia/tests/fakes.py`)

`fake_llm(scripted)`, `FakeJEV(por_turno)`, `gateway_falso(contratos_dir)`
(registra chamadas; devolve `ok.json`/`erro-*.json` conforme `cenario`),
`sem_rag`, `_texto_de`. Evals e testes usam os **mesmos** fakes — não crie
cópias locais.
