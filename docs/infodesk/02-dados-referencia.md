# 02 — Dados de referência (mesas, catálogo, atendentes, departamentos, templates)

Fonte: PDF v1 (2026-10-19), seções "9.3 Referência", "2. Autenticação"
(nota sobre escopo) e "6.1 Janela de 24h e templates". Servem para escolher
mesa/prioridade/serviço ao **abrir chamados** e departamento/atendente ao
**transferir conversas**. Valem para **qualquer chave válida** (sem escopo).

| Método | Rota | Retorno (conforme PDF) |
|---|---|---|
| GET | `/desks` | mesas **ativas** |
| GET | `/desks/:id` | mesa com `statuses`, `stages`, `priorities` e `catalogItems` (se a mesa usa catálogo) |
| GET | `/catalog?deskId=` | serviços do catálogo (itens → `catalogItemId` do ticket) |
| GET | `/users` | atendentes ativos: `id`, `username`, `email`, `type`, `departmentIds` |
| GET | `/departments` | departamentos **de chat** |
| GET | `/whatsapp/templates` | templates do WhatsApp habilitados (importados do provedor) |

O PDF não traz o JSON literal de `/desks`, `/desks/:id`, `/catalog`, `/users`
e `/departments`; os campos acima são os citados em prosa. Do objeto `ticket`
(03) sabe-se o formato de cada sub-recurso quando embutido:

```json
"desk":     { "id": "d1…", "name": "Suporte" },
"status":   { "id": "s1…", "name": "Aberto", "isOpen": true, "isClose": false, "isCanceled": false },
"stage":    { "id": "e1…", "name": "Triagem" },
"priority": { "id": "p1…", "name": "Alta" },
"responsible": { "id": "u1…", "username": "joao", "email": "…" }
```

Exemplo literal de template (`GET /whatsapp/templates`):

```json
[{ "id": "…", "name": "retorno_chamado", "language": "pt_BR",
   "body": "Olá {{1}}, seu chamado {{2}} foi atualizado.", "paramCount": 2,
   "paramLabels": ["Nome", "Chamado"], "approved": true }]
```

## Conceitos (como o PDF os usa)

| Conceito | Onde aparece | Papel |
|---|---|---|
| **Mesa** (`desk`) | `POST /tickets.deskId`, `POST /tickets/:number/escalate`, `GET /tickets?deskId=` | Fila de **chamados**. Cada mesa tem seus próprios `statuses`, `stages`, `priorities` e, opcionalmente, catálogo. "Encerra respeita checklist/regras da mesa" |
| **Departamento** (`department`) | `POST /conversations/:id/department`, `POST /conversations/:id/handoff.departmentId`, `GET /users.departmentIds` | Fila de **conversas** (chat/WhatsApp). Atendentes pertencem a departamentos |
| **Catálogo / serviço** (`catalogItem`) | `POST /tickets.catalogItemId`, `GET /catalog?deskId=` | Classificação de serviço do chamado, por mesa. "Se a organização exige campos (… serviço …) a falta vira 400" |
| **Status / etapa / prioridade** | `statusId`, `stageId`, `priorityId` do ticket | Definidos **por mesa** (`GET /desks/:id`) |
| **Tipo / impacto / urgência** | `ticketType` (ex.: `INCIDENT`), `impact`, `urgency`, `priorityLevel` | Campos ITIL; mudar a prioridade ITIL via `PATCH` exige `reason` |

**Relação mesa × departamento: NÃO DOCUMENTADA.** Um usuário tem
`departmentIds`; uma mesa não tem departamento. O roteamento
"categoria → mesa do chamado + departamento do handoff" continua sendo
tabela nossa (07, D1). O inventário real está em
[09-inventario-mesas-catalogos.md](09-inventario-mesas-catalogos.md).

## O que NÃO está documentado

- JSON literal das listas (`/desks`, `/users`, `/departments`, `/catalog`).
  Tipar o mínimo citado (`id`, `name`; para `users` também `username`, `email`,
  `type`, `departmentIds`) e manter o resto como bruto.
- Se as listas são **paginadas** (regra geral da seção 4 diz que "listas
  devolvem `{ data, nextCursor }`"; para referência não há exemplo). Tratar as
  duas formas (array puro ou `{ data }`) — 07, D3.
- Valores de `users.type` (humano × bot interno?) — 07, D4.
- Campos de `catalogItems`/`/catalog` (nome, id, mesa, ativo?) — 07, D5.
- Se `/desks` devolve **só ativas** (diz "mesas ativas") e se há flag.

## Implicações para o nosso lado

- Cache em memória com TTL (10 min) e carga no boot; falha de carga no boot =
  erro de configuração explícito.
- O **mapeamento categoria → mesa / catálogo / prioridade / departamento** é
  tabela nossa (`config/taxonomia.yaml`), por **nome**, validada no boot contra
  `/desks`, `/desks/:id` (prioridades e catálogo) e `/departments` (nome
  configurado que não existe → alerta, ou falha em `STRICT_TAXONOMIA=true`).
- `priorityId` agora é **obtível** (`/desks/:id.priorities`): a prioridade
  determinística do classificador pode ir no ticket (resolve 07, T3). Mas
  prioridades são **por mesa**: a taxonomia mapeia `prioridade → nome` e o
  gateway resolve o UUID na mesa de destino.
- `statusId`/`stageId` no `POST /tickets`: **não enviar** na v1 (deixar o
  padrão da mesa).
