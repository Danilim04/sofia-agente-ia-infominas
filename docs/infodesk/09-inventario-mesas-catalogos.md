# 09 — Inventário real: mesas de serviço e catálogos da Infominas

Fonte: **capturas de tela do painel do Infodesk** fornecidas em 2026-10-20
(telas *Mesas de serviço* e *HelpDesk › Catálogos*). **Não** vem da API: os
nomes abaixo são o que a Infominas tem cadastrado hoje; IDs, status, etapas,
prioridades por mesa e itens de cada catálogo **não aparecem nas capturas** e
devem vir de `GET /desks`, `GET /desks/:id` e `GET /catalog?deskId=` (02)
quando houver chave. Este arquivo é o insumo da pendência **P1** (taxonomia
real) do plano; `config/taxonomia.yaml` continua `placeholder: true` até o
mapeamento categoria → mesa/catálogo ser confirmado com a Infominas.

## Mesas de serviço (15, tela "Mesas de serviço")

| # | Nome (exato) | Leitura (hipótese, a confirmar) |
|---|---|---|
| 1 | Abertura de Ticket | mesa genérica de entrada/triagem? candidata a `mesa_triagem` (P11) |
| 2 | Cadastros de Usuário e E-mail - Nefroclinicas | cliente específico (Nefroclínicas): cadastros |
| 3 | Financeiro | cobrança, boletos, contratos |
| 4 | Incidente | incidente genérico |
| 5 | Incidente Externo | incidente em cliente externo |
| 6 | Incidente Interno | incidente interno da Infominas |
| 7 | Incidente Remoto | incidente atendido remotamente |
| 8 | Monitoramento | alertas de monitoramento (provável destino de `POST /alerts`) |
| 9 | Nefroclinicas | cliente específico: geral |
| 10 | Projetos | projetos |
| 11 | Solicitações | solicitação genérica (requisição de serviço) |
| 12 | Solicitações Externo | solicitação de cliente externo |
| 13 | Solicitações Interno | solicitação interna |
| 14 | Solicitações Remoto | solicitação atendida remotamente |
| 15 | Suporte Tecnico N1 | suporte de primeiro nível (sem acento no cadastro) |

Observações:

- A tela mostra **só nomes**, sem indicação de ativa/inativa nem de
  departamento. `GET /desks` devolve "mesas ativas" — a lista real pode ser
  menor.
- Grafia: "Suporte Tecnico N1" (sem acento), "Nefroclinicas" (sem acento). A
  validação da taxonomia por nome deve comparar **exatamente** como cadastrado
  (ou normalizar acentos/caixa dos dois lados e avisar quando só o nome
  normalizado bate).
- A separação **Incidente × Solicitação** (ITIL: incidente = algo quebrou;
  solicitação = pedido de serviço) e **Externo × Interno × Remoto** sugere que
  a mesa é escolhida por **tipo de chamado + origem/atendimento**, não só pelo
  assunto. Para a Sofia (WhatsApp, cliente externo), as candidatas naturais
  são **Incidente Externo** / **Incidente Remoto** e **Solicitações
  Externo** / **Solicitações Remoto**, ou a genérica **Abertura de Ticket**.
  **Hipótese, não regra** — confirmar com a Infominas (07, D1).

## Catálogos (15 visíveis, tela "HelpDesk › Catálogos", todos "Ativo")

| # | Nome (exato) | Leitura (hipótese) |
|---|---|---|
| 1 | Cadastro de Usuários | serviços de cadastro |
| 2 | Catálogo Mestre | catálogo raiz/geral |
| 3 | Consultoria | consultoria |
| 4 | Incidentes | incidentes (versão atual) |
| 5 | Incidentes - Nefroclinicas | incidentes do cliente Nefroclínicas |
| 6 | Incidentes_New | incidentes (versão nova, em migração?) |
| 7 | Monitoramento_New | monitoramento (versão nova) |
| 8 | Projetos_New | projetos (versão nova) |
| 9 | Serviços de Backup | backup |
| 10 | Serviços de Monitoramento e Prevenção | monitoramento/prevenção |
| 11 | Serviços de Suporte | suporte |
| 12 | Serviços de Suporte Software | suporte a software |
| 13 | Solicitações | solicitações (versão atual) |
| 14 | Solicitações - Nefroclinicass | solicitações Nefroclínicas (com "ss" no cadastro) |
| 15 | Solicitações_New | solicitações (versão nova) |
| — | (a lista pode continuar abaixo da área capturada) | — |

Observações:

- A tela lista **catálogos**, não os **itens** (serviços) dentro deles; o
  ticket recebe `catalogItemId` (item), e `GET /catalog?deskId=` devolve
  "serviços do catálogo" de uma mesa. A relação catálogo ↔ mesa **não é
  visível** nas capturas (07, D5).
- Os sufixos `_New` e os pares "Incidentes"/"Incidentes_New",
  "Solicitações"/"Solicitações_New" indicam **migração em andamento**;
  perguntar qual vale antes de mapear.
- "Solicitações - Nefroclinicass" tem erro de digitação no cadastro; se virar
  destino, comparar pelo **id**, não pelo nome.

## Departamentos de chat

**Ainda não observados** (nenhuma captura da tela de Departamentos). São o
destino do `handoff` (08). Pendência 07, D6.

## O que falta para fechar o P1

1. `GET /desks` + `GET /desks/:id` para cada mesa (ids, `statuses`, `stages`,
   `priorities`, `catalogItems`).
2. `GET /catalog?deskId=` por mesa (itens → `catalogItemId`).
3. `GET /departments` (destinos do handoff).
4. Decisão da Infominas: para cada categoria da Sofia, **qual mesa, qual item
   de catálogo (se exigido), qual departamento e qual prioridade** — e quais
   campos a organização exige no `POST /tickets` (07, T10).

**Atualização 2026-10-21:** os itens 1–3 são feitos de uma vez por
`scripts/descobrir-taxonomia.py` com a chave da API (10, §7), que também gera
um rascunho da taxonomia a partir do catálogo. O item 4 virou o questionário
de 10, §8. O resultado real está na seção seguinte.

## O que a API devolveu em 2026-10-21 (chave `sofia-agente-ia`, produção)

Fonte: `scripts/descobrir-taxonomia.py` contra
`https://infodesk.infominas.net/v1/integration/v1` (só `GET`). Os ids ficam
em `descoberta-infodesk/inventario.json` (gitignored; regenerável). Isto
**responde os itens 1–3 acima** e muda a leitura das hipóteses anteriores.

### Mesas (15 ativas) — como estão configuradas

| Mesa | Prioridades | Status abertos | Etapas | Catálogo | Leitura |
|---|---|---|---|---|---|
| Incidente | Baixa, **Media**, Alta, **Crítica** | Aberto, Em Atendimento, Atendimento Externo, Atendimento Remoto | Pendente → INTERNO → REMOTO → EXTERNO → Aguardando data agendada → Fechado | 38 itens, todos `INCIDENT` | **candidata principal** para reclamação: completa; a divisão interno/remoto/externo é **etapa**, não mesa |
| Solicitações | Baixa, **Media**, Alta, **Crítica** | Aberto, Atendimento Remoto/Externo/Interno, Aguardando retorno (terceiros/cliente/Infominas) | Pendente → EXTERNO → INTERNO → REMOTO → Aguardando data agendada → Fechado | 48 itens, todos `REQUEST` | **candidata principal** para pedido de serviço |
| Suporte Tecnico N1 | Baixa, Media, Alta | Aberto, Em Atendimento | Triagem | não usa | candidata a `mesa_triagem` (sem catálogo → sem exigência de serviço) |
| Abertura de Ticket | Alta, Baixa | Aberto, Em Atendimento | Pendente → Triagem → Fechado | 399 itens (catálogo mestre, compartilhado com Projetos) | genérica; sem "Media" |
| Monitoramento | Baixa, Media, Alta | Aberto, Em Atendimento, Atendimento Externo | Pendente → N2 → Fechado | 6 itens (monitoramento/preventivas) | alertas; não é destino do WhatsApp |
| Financeiro | **Normal** (só) | Aberto | Analise | não usa | cobrança/boleto; prioridade única |
| Projetos | Baixa, Media, Alta | Aberto, Atendimento Remoto/Externo | Pendente → Levantamento de requisitos → Fechado | 399 (mesmo catálogo de Abertura de Ticket) | não é WhatsApp |
| Nefroclinicas; Cadastros de Usuário e E-mail - Nefroclinicas | Baixa, Media, Alta | Aberto | Pendente → Fechado | 27 / 48 (cópias dos catálogos de Incidente/Solicitações) | cliente específico |
| Incidente Externo / Interno / Remoto | **nenhuma** | **nenhum** | **nenhuma** | 27 (compartilhado entre as três) | **cascas sem configuração**: ticket aberto aí nasce sem status — não usar |
| Solicitações Externo / Interno / Remoto | **nenhuma** | nenhum aberto (Externo tem só "Fechado") | — | 48 (compartilhado entre as três) | idem: não usar |

Consequências para a taxonomia:

- **A hipótese "mesa por origem (Externo/Remoto)" caiu**: essas mesas estão
  vazias; a origem é uma **etapa** dentro de `Incidente`/`Solicitações`, que o
  atendente escolhe. A Sofia abre em `Incidente` (algo quebrou) ou
  `Solicitações` (pedido) e deixa a etapa no padrão (`Pendente`).
- **Prioridades por mesa não têm grafia uniforme**: `Media` (sem acento) e
  `Crítica` (não "Urgente") nas duas mesas principais; `Normal` no Financeiro;
  Abertura de Ticket sem "Media". O bloco `prioridades` da taxonomia deve ser
  `media: "Media"`, `urgente: "Crítica"`; o gateway resolve por mesa e omite
  `priorityId` onde o nome não existe (P4).
- **Nenhum item de catálogo tem `defaultDeskId`** e os catálogos são
  compartilhados entre famílias de mesas, então "qual mesa" vem da taxonomia,
  não do catálogo. O item de catálogo **é opcional** no ticket, mas se enviado
  tem de pertencer ao catálogo da mesa (10, §3).
- Os catálogos de `Incidente` e `Solicitações` já estão na linguagem do
  usuário final ("Lentidão na Internet", "Não imprime", "Redefinir Senha",
  "Email não envia/recebe") e agrupados por **área** (Rede e Internet,
  E-mail, Impressora, Computadores e Periféricos, Cadastros e Acessos,
  Software, Sistema Operacional, Servidor, Antivírus, Arquivos, Ramal,
  Sharepoint, Consultoria, Gerenciamento de Arquivos, Monitoramento Proativo,
  Preventivas, Sistema). O rascunho por área (`--mesas Incidente,Solicitações
  --por area`) dá **24 categorias** + `indefinida`, ponto de partida da E5.1.
- Higiene do catálogo (avisar a Infominas, não bloqueia): áreas duplicadas
  por grafia ("Computadores e  Periféricos" com dois espaços × "Computadores e
  Periféricos"; "Redes" × "Rede e Internet"; "Softwares" × "Software"),
  "Nâo scaneia", "Hardaware", "Subistuição"; 108 itens sem `ticketType`.

### Departamentos de chat (3) — destinos do handoff (resolve D6)

| Departamento | Leitura |
|---|---|
| Técnicos | candidato a `departamento_humano_padrao` e destino de toda categoria técnica |
| Administrativo / Financeiro | destino de cobrança/boleto/contrato |
| Comercial | destino de planos/upgrade/novos serviços |

### Catálogo da organização

758 itens ao todo (`GET /catalog` sem `deskId`): 285 `REQUEST`, 217
`INCIDENT`, 66 `CHANGE`, 44 `PROJECT`, 18 `PROBLEM`, 10 `MANAGED_SERVICE`,
10 `CONSULTING`, 108 sem tipo. Só os 38 + 48 das mesas principais interessam
à Sofia.

### O que continua em aberto depois da API

Só decisões (10, §8): quais mesas a Sofia usa (proposta: `Incidente`,
`Solicitações`, `Financeiro`; triagem em `Suporte Tecnico N1` ou `Abertura de
Ticket`), departamento humano padrão (proposta: `Técnicos`), o toggle
"Exigir campos na abertura do chamado" e campos personalizados obrigatórios
nas mesas escolhidas, `finish` após ticket, horário/inatividade.
