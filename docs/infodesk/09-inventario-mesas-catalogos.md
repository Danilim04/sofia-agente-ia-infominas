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
