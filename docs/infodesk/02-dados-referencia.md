# 02 — Dados de referência (mesas, atendentes, departamentos)

Fonte: PDF, seção "2. Dados de referência". Servem para escolher
mesa/atendente/departamento ao **abrir tickets** ou **transferir conversas**.

| Método | Rota | Retorno (conforme PDF) |
|---|---|---|
| GET | `/integration/v1/desks` | `[{ id, name }]` — mesas de serviço |
| GET | `/integration/v1/users` | `[{ id, username, email }]` — atendentes |
| GET | `/integration/v1/departments` | `[{ id, name }]` — departamentos |

Exemplo (formato inferido do retorno descrito; o PDF não traz o JSON literal):

```http
GET /api/integration/v1/desks
Authorization: Bearer ik_live_...
```

```json
[
  { "id": "<uuid>", "name": "Suporte Técnico" },
  { "id": "<uuid>", "name": "Financeiro" }
]
```

## O que NÃO está documentado

- Escopo exigido (nenhum citado) — assumir que a chave com qualquer escopo
  lê; **confirmar**.
- Paginação, filtros, ordenação — nenhum parâmetro citado.
- Campos além dos listados (ex.: `active`, `departmentId` numa mesa, `deskId`
  num usuário) — desconhecidos. **Não** dependa de campos extras.
- Relação entre **mesa** (`desk`) e **departamento** (`department`): o ticket é
  aberto numa **mesa** (`deskId`), a conversa é transferida para um
  **departamento** (`departmentId`). O PDF não diz se há vínculo entre os dois
  conceitos (uma mesa pertence a um departamento?). Impacta diretamente o
  **roteamento** do agente — ver 07, item D1.
- Se os IDs são estáveis (UUID) — os exemplos usam `<uuid-...>`; tratar como
  opacos.

## Implicações para o nosso lado

- Esses três endpoints são **cacheáveis** (mudam raramente). Padrão do Zapin:
  cache em memória com TTL (como `SAC_CONFIG_TTL`, ex.: 10 min) e carga no
  boot; falha de carga no boot = erro de configuração explícito.
- O **mapeamento categoria → mesa/departamento** do agente NÃO vem desta API: é
  tabela de configuração nossa (por `name` ou por `id`), validada no boot contra
  o que a API devolve (nome configurado que não existe → alerta).
