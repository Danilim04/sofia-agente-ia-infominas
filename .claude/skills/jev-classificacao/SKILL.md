---
name: jev-classificacao
description: Como o JEV (TypeSafe System One) é usado neste projeto para classificar intenção, categoria, prioridade, frustração e segurança, com fallback LLM e limiares. Carregue ao mexer em agente-ia/src/classificacao, config/taxonomia.yaml, no nó classificar do grafo, nos limiares JEV_LIMIAR_* ou nos evals de classificação.
---

# JEV neste projeto

Plano completo: `docs/plano/02-classificacao.md` (leia antes). ADR: `docs/decisoes/0002`.

## O serviço (o que está documentado pela TypeSafe)

- `POST https://api.typesafe.ai/v1/systemone`, `Authorization: Bearer $TYPESAFE_API_KEY`,
  corpo `{state, model, questions}`. SDK: `pip install typesafe-sdk` →
  `AsyncTypeSafeClient(model=...)`, `await client.system_one(state=..., questions={...})`.
- Perguntas: `Choice(instructions, criteria={nome: descricao|objeto|None})` →
  `.choice`, `.probabilities`, `.confidence`; `Score(instructions, criteria=[níveis])`
  → `.score`, `.probabilities`, `.confidence`; `Noul(instructions)` → `.noul` (0–1, sem confidence; use `|2p-1|`).
- Acesso às respostas: `resp.choices["intencao"].choice`, `resp.scores["prioridade"].score`, `resp.nouls["malicioso"].noul`.
- `confidence` de Choice = `(p_max − 1/n)/(1 − 1/n)`: concentração, não prova de acerto.
- Várias perguntas na **mesma** chamada (paralelas, uma latência). Limite 64k tokens/req, 32k/pergunta, até 255 opções.
- Modelos: `jev-1.13.0` (estável), `jev-1.14.0` (`jev-preview`). **Pine a versão** (`JEV_MODEL`).
- Erros: `TypeSafeAPIError(status, request_id)`; 429 com `retry-after` (o SDK retenta com `RetryPolicy`).
- Inglês é a língua primária; pt-BR precisa ser **medido** nos evals.
- Skill oficial genérica (opcional, para a API em si): `claude plugin marketplace add typesafe-ai/skills && claude plugin install typesafe@typesafe-ai`. Esta skill cobre o que é **específico deste projeto**.

## Regras do projeto

1. **Uma chamada por turno** com TODAS as perguntas de
   `src/classificacao/perguntas.py` (único lugar com perguntas e limiares).
2. **Estado enviado** = `estado_para_jev(messages)`: últimas 6 mensagens
   (`Cliente:`/`Sofia:`), ≤ 2.000 tokens, CPF/telefone **mascarados**, sem
   system prompt, sem conteúdo de `<documento_externo>`.
3. **Roteador é código** (`roteador.py`): aplica `JEV_LIMIAR_*` (tabela no
   `02`), decide `origem ∈ {jev, llm, esclarecimento, indefinida}`, torna a
   categoria pegajosa no episódio, gera `acoes` (`transferir` por
   `pedir_atendente`, frustração ≥ 3, confiança baixa após esclarecimento).
4. **Fallback LLM** (`fallback_llm.py`): `get_classifier_llm().with_structured_output(ClassificacaoLLM)`
   com as **mesmas classes** de `taxonomia.yaml`; temperatura 0; fail-open → `indefinida`.
5. **JEV indisponível** (timeout 2 s, 5xx, 429 esgotado): fail-soft para o
   fallback LLM + métrica `jev_indisponivel`; nunca derruba o turno.
6. A taxonomia vem de `config/taxonomia.yaml` (`taxonomia.py` gera os
   `criteria`: `{"what": descricao, "not_for": nao_e, "examples": exemplos}`).
   Enquanto `placeholder: true`, os evals de categoria são `xfail` e o boot avisa.
7. O LLM de conversa recebe só o resultado ("Categoria: sem_conexao (alta
   confiança)") — nunca a lista de mesas/departamentos nem a tarefa de escolher.
8. Toda mudança em `perguntas.py`, `taxonomia.yaml` ou limiar exige rodar
   `python -m evals.classificacao --fonte sintetico --jev` (precisa
   `TYPESAFE_API_KEY`) **e** `--llm`, e colar o resumo no PR. Regressão > 2 pts
   em macro-F1/acurácia@limiar bloqueia.

## Esqueleto esperado

```python
# src/classificacao/tipos.py
@dataclass(frozen=True)
class Classificacao:
    intencao: str; categoria: str | None; prioridade: str; frustracao: int
    confiancas: dict[str, float]; origem: Literal["jev", "llm", "esclarecimento", "indefinida"]
    malicioso: float; fora_de_escopo: float; modelo: str; latencia_ms: int

# src/classificacao/jev.py
class ClienteJEV(Protocol):
    async def perguntar(self, estado: str, perguntas: dict) -> RespostaJEV: ...
class JEVTypeSafe(ClienteJEV): ...      # SDK real, timeout 2 s, RetryPolicy(max_retries=2)
class FakeJEV(ClienteJEV): ...          # tests/fakes.py: respostas scriptadas por pergunta/turno

# src/classificacao/roteador.py
async def classificar(messages, anterior: Classificacao | None, jev: ClienteJEV, fallback) -> Classificacao
```

Testes obrigatórios: cada linha da tabela de limiares; JEV fora; pegajosidade;
mascaramento no estado; `retrieve` condicional à intenção (no `test_graph`).
