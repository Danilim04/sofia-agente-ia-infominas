# Evals de classificação — sintetico/offline

> **Exemplo offline com fakes, não é medição real. Os números medem o encanamento do runner (roteador + JEV/fallback simulados), nunca o desempenho do JEV (P8).**

- **gerado_em**: 2026-10-22T07:44:09+00:00
- **dataset**: sintetico.jsonl (237 casos)
- **modo**: offline
- **JEV**: JEVSimulado (FakeJEV lexical)
- **fallback LLM**: FallbackSimulado (FakeFallbackLLM)
- **janela de contexto**: 6 mensagens (JEV_JANELA_MSGS)
- **latência**: simulada (determinística), não é a do JEV
- **taxonomia**: versão 2026-10-20 (placeholder)
- **casos por categoria**: cancelamento=19, cobranca=22, duvida_planos=18, equipamento=21, indefinida=32, instabilidade=20, lentidao=22, mudanca_endereco=19, segunda_via=19, sem_conexao=27, visita_tecnica=18

Casos avaliados: **237** · latência p50 194 ms · p95 292 ms

> ⚠️ taxonomia é placeholder (P1): métricas de categoria são informativas, não bloqueiam

## Comparação de janelas de contexto (C-JEV1)

| janela (msgs) | intenção acurácia | categoria acurácia | categoria macro-F1 | categoria cobertura@limiar | categoria acurácia@limiar | categoria ECE | estado médio (caracteres) | latência p50 (ms) |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| 1 | 68.4% | 65.8% | 66.4% | 65.0% | 74.7% | 0.200 | 58 | 193 |
| 3 | 68.8% | 66.7% | 67.1% | 66.7% | 74.1% | 0.221 | 70 | 191 |
| 6 | 68.8% | 67.5% | 67.9% | 66.7% | 74.1% | 0.222 | 80 | 194 |
| **10** | 68.8% | 70.9% | 71.0% | 70.5% | 75.4% | 0.227 | 89 | 191 |

Negrito = melhor macro-F1 de categoria. Janela maior custa mais tokens (e latência) e só vale se subir as métricas; o `.env` usa `JEV_JANELA_MSGS`. Decidir com o dataset histórico (P9), não com o sintético.

## Resumo por pergunta

| pergunta | n | acurácia | macro-F1 | limiar | cobertura@limiar | acurácia@limiar | ECE |
|---|---:|---:|---:|---:|---:|---:|---:|
| categoria | 237 | 67.5% | 67.9% | 0.65 | 66.7% | 74.1% | 0.222 |
| categoria_final | 111 | 61.3% | 40.6% | 0.65 | 76.6% | 68.2% | 0.272 |
| fora_de_escopo | 44 | 97.7% | 93.8% | 0.60 | 100.0% | 97.7% | 0.083 |
| frustracao | 94 | 93.6% | — | 0.60 | 100.0% | 93.6% | — |
| intencao | 237 | 68.8% | 69.5% | 0.70 | 72.2% | 67.8% | 0.147 |
| malicioso | 49 | 100.0% | 100.0% | 0.60 | 100.0% | 100.0% | 0.082 |
| prioridade | 150 | 50.7% | — | 0.60 | 100.0% | 50.7% | — |
| prioridade_final | 150 | 80.7% | — | 0.60 | 100.0% | 80.7% | — |

## Pipeline (o que o roteador fez)

| observação | valor |
|---|---:|
| media_estado_caracteres | 79.5 |
| taxa_categoria_indefinida_em_reclamacao | 1.5% |
| taxa_esclarecimento | 0.4% |
| taxa_fallback_chamado | 18.6% |
| taxa_jev_indisponivel | 0.0% |
| taxa_transferencia | 3.4% |

## categoria

### F1 por classe

| classe | F1 |
|---|---:|
| cancelamento | 74.3% |
| cobranca | 61.1% |
| duvida_planos | 76.5% |
| equipamento | 66.7% |
| indefinida | 69.0% |
| instabilidade | 69.6% |
| lentidao | 66.7% |
| mudanca_endereco | 59.1% |
| segunda_via | 74.4% |
| sem_conexao | 62.9% |
| visita_tecnica | 66.7% |

### Calibração (10 bins)

| bin | n | confiança média | acurácia |
|---|---:|---:|---:|
| [0.1, 0.2) | 3 | 0.135 | 0.0% |
| [0.2, 0.3) | 5 | 0.261 | 20.0% |
| [0.3, 0.4) | 27 | 0.366 | 74.1% |
| [0.4, 0.5) | 42 | 0.446 | 47.6% |
| [0.6, 0.7) | 2 | 0.600 | 100.0% |
| [0.7, 0.8) | 1 | 0.737 | 100.0% |
| [0.8, 0.9) | 6 | 0.853 | 0.0% |
| [0.9, 1.0] | 151 | 0.995 | 76.8% |

### Matriz de confusão (linha = esperado, coluna = previsto)

| esperado \ previsto | cancelamento | cobranca | duvida_planos | equipamento | indefinida | instabilidade | lentidao | mudanca_endereco | segunda_via | sem_conexao | visita_tecnica |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| cancelamento | 13 | 0 | 0 | 0 | 1 | 0 | 0 | 2 | 1 | 0 | 2 |
| cobranca | 0 | 11 | 0 | 1 | 1 | 2 | 0 | 0 | 4 | 2 | 1 |
| duvida_planos | 1 | 1 | 13 | 0 | 0 | 0 | 0 | 2 | 0 | 1 | 0 |
| equipamento | 0 | 0 | 0 | 13 | 0 | 2 | 0 | 3 | 0 | 3 | 0 |
| indefinida | 1 | 0 | 0 | 2 | 20 | 1 | 0 | 3 | 0 | 4 | 1 |
| instabilidade | 0 | 0 | 0 | 1 | 0 | 16 | 0 | 0 | 0 | 3 | 0 |
| lentidao | 0 | 0 | 1 | 1 | 1 | 3 | 12 | 0 | 0 | 4 | 0 |
| mudanca_endereco | 1 | 0 | 2 | 0 | 1 | 0 | 0 | 13 | 1 | 1 | 0 |
| segunda_via | 0 | 2 | 0 | 0 | 0 | 1 | 0 | 0 | 16 | 0 | 0 |
| sem_conexao | 0 | 0 | 0 | 0 | 2 | 1 | 2 | 0 | 0 | 22 | 0 |
| visita_tecnica | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 2 | 2 | 3 | 11 |

## categoria_final

### F1 por classe

| classe | F1 |
|---|---:|
| cancelamento | 0.0% |
| cobranca | 66.7% |
| duvida_planos | 0.0% |
| equipamento | 58.3% |
| indefinida | 0.0% |
| instabilidade | 71.4% |
| lentidao | 62.9% |
| mudanca_endereco | 0.0% |
| segunda_via | 50.0% |
| sem_conexao | 70.2% |
| visita_tecnica | 66.7% |

### Calibração (10 bins)

| bin | n | confiança média | acurácia |
|---|---:|---:|---:|
| [0.0, 0.1) | 1 | 0.000 | 0.0% |
| [0.2, 0.3) | 2 | 0.252 | 0.0% |
| [0.3, 0.4) | 3 | 0.367 | 0.0% |
| [0.4, 0.5) | 19 | 0.447 | 52.6% |
| [0.6, 0.7) | 1 | 0.600 | 0.0% |
| [0.7, 0.8) | 1 | 0.737 | 100.0% |
| [0.8, 0.9) | 1 | 0.850 | 0.0% |
| [0.9, 1.0] | 83 | 0.993 | 68.7% |

### Matriz de confusão (linha = esperado, coluna = previsto)

| esperado \ previsto | cancelamento | cobranca | duvida_planos | equipamento | indefinida | instabilidade | lentidao | mudanca_endereco | segunda_via | sem_conexao | visita_tecnica |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| cancelamento | 0 | 0 | 0 | 0 | 1 | 0 | 0 | 0 | 0 | 0 | 0 |
| cobranca | 0 | 11 | 0 | 1 | 1 | 2 | 0 | 0 | 4 | 2 | 1 |
| duvida_planos | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 |
| equipamento | 0 | 0 | 0 | 7 | 0 | 2 | 0 | 1 | 0 | 3 | 0 |
| indefinida | 1 | 0 | 0 | 1 | 0 | 0 | 0 | 1 | 0 | 1 | 0 |
| instabilidade | 0 | 0 | 0 | 1 | 1 | 15 | 0 | 0 | 0 | 3 | 0 |
| lentidao | 0 | 0 | 1 | 1 | 2 | 3 | 11 | 0 | 0 | 4 | 0 |
| mudanca_endereco | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 1 | 0 |
| segunda_via | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 2 | 0 | 0 |
| sem_conexao | 0 | 0 | 0 | 0 | 1 | 0 | 2 | 0 | 0 | 20 | 0 |
| visita_tecnica | 0 | 0 | 0 | 0 | 1 | 0 | 0 | 0 | 0 | 0 | 2 |

## fora_de_escopo

### F1 por classe

| classe | F1 |
|---|---:|
| nao | 98.7% |
| sim | 88.9% |

### Calibração (10 bins)

| bin | n | confiança média | acurácia |
|---|---:|---:|---:|
| [0.8, 0.9) | 4 | 0.840 | 100.0% |
| [0.9, 1.0] | 40 | 0.900 | 97.5% |

### Matriz de confusão (linha = esperado, coluna = previsto)

| esperado \ previsto | nao | sim |
|---|---:|---:|
| nao | 39 | 0 |
| sim | 1 | 4 |

## frustracao

## intencao

### F1 por classe

| classe | F1 |
|---|---:|
| confirmacao | 88.9% |
| duvida_informacao | 76.7% |
| outro | 61.3% |
| pedir_atendente | 100.0% |
| reclamacao_problema | 70.1% |
| saudacao_social | 72.7% |
| status_chamado | 16.7% |

### Calibração (10 bins)

| bin | n | confiança média | acurácia |
|---|---:|---:|---:|
| [0.3, 0.4) | 1 | 0.325 | 100.0% |
| [0.6, 0.7) | 65 | 0.658 | 70.8% |
| [0.7, 0.8) | 94 | 0.733 | 45.7% |
| [0.8, 0.9) | 65 | 0.872 | 93.8% |
| [0.9, 1.0] | 12 | 0.936 | 100.0% |

### Matriz de confusão (linha = esperado, coluna = previsto)

| esperado \ previsto | confirmacao | duvida_informacao | outro | pedir_atendente | reclamacao_problema | saudacao_social | status_chamado |
|---|---:|---:|---:|---:|---:|---:|---:|
| confirmacao | 4 | 1 | 0 | 0 | 0 | 0 | 0 |
| duvida_informacao | 0 | 46 | 7 | 0 | 2 | 0 | 0 |
| outro | 0 | 3 | 42 | 0 | 0 | 1 | 0 |
| pedir_atendente | 0 | 0 | 0 | 4 | 0 | 0 | 0 |
| reclamacao_problema | 0 | 10 | 38 | 0 | 62 | 0 | 1 |
| saudacao_social | 0 | 1 | 1 | 0 | 0 | 4 | 0 |
| status_chamado | 0 | 4 | 3 | 0 | 2 | 0 | 1 |

## malicioso

### F1 por classe

| classe | F1 |
|---|---:|
| nao | 100.0% |
| sim | 100.0% |

### Calibração (10 bins)

| bin | n | confiança média | acurácia |
|---|---:|---:|---:|
| [0.8, 0.9) | 6 | 0.900 | 100.0% |
| [0.9, 1.0] | 43 | 0.920 | 100.0% |

### Matriz de confusão (linha = esperado, coluna = previsto)

| esperado \ previsto | nao | sim |
|---|---:|---:|
| nao | 43 | 0 |
| sim | 0 | 6 |

## prioridade

## prioridade_final
