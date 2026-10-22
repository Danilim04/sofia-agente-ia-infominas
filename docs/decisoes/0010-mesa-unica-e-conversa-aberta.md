# 0010 — Mesa única de abertura e conversa aberta depois do ticket

- **Estado:** aceita (2026-10-22, decisão do produto)
- **Etapa:** E5.3 (registro); efeitos em E5.1 e E5.7
- **Pendências relacionadas:** `docs/infodesk/07` C6, D1, D6; `docs/infodesk/10` §8 linhas 4, 5 e 7

## Contexto

O plano supunha categoria → mesa (`Incidente`, `Solicitações`, `Financeiro`; `docs/plano/02`) e deixava
aberto se a Sofia encerra a conversa ao abrir o ticket (`07` C6). O produto decidiu.

## Decisão

1. **Todo chamado da Sofia nasce na mesa "Abertura de Ticket"** (entrada única, triagem humana; prática
   da Infominas desde ago/2026). A classificação define só **prioridade** e **item/área do catálogo**,
   não a mesa. Fato (`docs/infodesk/09`): a mesa só tem as prioridades `Alta` e `Baixa`.
2. **A conversa fica aberta depois do ticket**: `TICKET_FINISH_CONVERSA=false` é decisão, não hipótese.
   `finish: true` dispara a mensagem do Desk e a pesquisa de satisfação (painel: ligada).
3. **Comercial e Financeiro: só repassar ao setor** (departamento de chat), sem depender de pessoa nem de
   `userId`; o handoff segue o ADR 0007.
4. **Risco (fato do código, `docs/infodesk/10` §4):** handoff para departamento **sem membros** deixa a
   conversa em `PENDING` na fila e **ninguém é notificado**. Departamento de destino sem atendente ativo é
   pendência de configuração para o responsável; checar `GET /users` × `departmentIds` no boot é
   follow-up, fora desta etapa.

**Segue ABERTO** (hipótese configurável): `IDENT_EXIGIR_CPF`, `DEPARTAMENTO_HUMANO_PADRAO`, departamento
por categoria e horário do atendimento humano.

## Consequências

- A taxonomia real (E5.1) deixa de mapear categoria → mesa; `mesa_triagem` e a mesa de toda categoria
  coincidem. Só prioridade, item/área e, quando decidido, departamento variam por categoria.
- Sem `finish`, não há pesquisa de satisfação ao abrir chamado; a conversa termina pelo atendente ou pelo
  `conversation.finished` (inatividade desligada no Desk).
- Revisar o item 4 quando a Infominas disser quem atende Comercial/Financeiro.

## Alternativas descartadas

- Mesas por categoria: contraria a triagem humana. `finish: true`: pesquisa a cada ticket.
- Transferir Comercial/Financeiro a uma pessoa (`userId`): dependeria de escala individual.
