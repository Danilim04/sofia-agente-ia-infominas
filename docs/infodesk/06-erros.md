# 06 — Erros

Fonte: PDF, seção "6. Erros".

| Código | Significado |
|---|---|
| 401 | Chave ausente, inválida ou revogada |
| 403 | Escopo insuficiente para a operação |
| 404 | Recurso não encontrado |
| 429 | Limite de requisições excedido |
| 4xx | Erro de validação (corpo com `message`) |

Formato de erro:

```json
{ "statusCode": 403, "message": "Escopo insuficiente. Necessário: tickets:write." }
```

Não documentado: `5xx`, header `Retry-After` no 429, limites numéricos do rate
limit, formato de erro de validação com vários campos (é sempre uma única
`message` string?).

## Política de tratamento recomendada (nossa)

Espelha a classificação de falhas do Zapin (`internal/engine/disjuntor.go`,
`src/agent/falhas.py`: transitória × definitiva):

| Situação | Classe | Ação |
|---|---|---|
| 401 / 403 | **configuração** (definitiva) | não repetir; alerta ao responsável; o agente responde ao cliente com fallback neutro e transfere para humano |
| 404 em `GET /tickets/:number` | definitiva | "não encontrei esse número" — nunca inventar |
| 404 em `POST /conversations/:id/...` | definitiva | conversa inexistente/encerrada → encerrar episódio local |
| 429 | **transitória** | backoff (respeitar `Retry-After` se existir); nunca repetir um `POST /tickets` sem checar duplicata |
| 4xx validação | definitiva | logar `message` completa (sem PII); corrigir payload — é bug nosso |
| 5xx / timeout / rede | transitória | retry curto (2 s, 5 s) **somente** em operações idempotentes (GET, `messages` com dedup nossa); `POST /tickets` só com proteção de duplicata |

Mensagem de erro do Infodesk **nunca** vai ao cliente final nem ao prompt do
LLM em claro (A2 do Zapin: vazamento de erro interno).
