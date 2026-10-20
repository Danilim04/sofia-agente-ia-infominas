# 0002 — JEV classifica; o LLM conversa e cobre a baixa confiança

- **Estado:** aceita (2026-10-19)
- **Etapa:** planejamento (implementação em E2.2)
- **Pendências relacionadas:** P1, P8, P9

## Contexto

O produto precisa classificar intenção, categoria, prioridade e tom em cada
turno para rotear e abrir o ticket certo. LLMs classificam bem, mas sem
confiança calibrada, com latência de segundos e custo por token de saída. O
JEV (TypeSafe) devolve rótulo + `probabilities` + `confidence` em centenas de
ms, numa chamada com várias perguntas.

## Decisão

Toda classificação passa primeiro pelo JEV (`src/classificacao/`), numa única
chamada por turno com as perguntas de `perguntas.py`. O roteador em código
aplica limiares (`JEV_LIMIAR_*`): acima → decisão fixa no estado; faixa
intermediária → fallback LLM com saída estruturada nas **mesmas** classes;
abaixo → pergunta de esclarecimento (1 por episódio) e depois `indefinida` /
transferência. Segurança (malicioso/off-topic) também migra para o JEV, com a
regex mantida na frente. Modelo pinado (`JEV_MODEL=jev-1.13.0`). Mesa,
departamento e prioridade saem de `config/taxonomia.yaml`, nunca do texto do
LLM.

## Consequências

- Latência e custo menores no caminho comum; confiança explícita permite
  handoff por incerteza (D8).
- Dependência de um serviço externo em early access: fail-soft para LLM e
  disjuntor são obrigatórios; evals noturnos medem o desempenho em pt-BR
  (a doc da TypeSafe diz que inglês é a língua primária).
- Os limiares são hipóteses até o dataset real (P9) — ADR novo com os valores
  calibrados em E5.2.

## Alternativas descartadas

- Só LLM: sem confiança calibrada, mais caro e lento; o cliente pediu
  determinismo onde possível.
- Modelo próprio (sklearn/fastText) treinado no histórico: viável no futuro
  como terceiro nível, mas exige dataset rotulado que ainda não existe.
