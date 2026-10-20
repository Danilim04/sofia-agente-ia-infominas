# 0003 — Infodesk substitui Chatwoot/Evolution; o estado bot/humano é nosso

- **Estado:** aceita (2026-10-19)
- **Etapa:** planejamento (E1.1, E1.2, E2.1)
- **Pendências relacionadas:** P3, P5, P11

## Contexto

O Infodesk entrega mensagens por webhook (`message.received`, HMAC do corpo,
best-effort, sem reentrega) e expõe envio de mensagem, transferência de
departamento, atribuição e encerramento. Não tem labels, equipes nem campo
documentado de "quem está atendendo". O Zapin usava labels do Chatwoot para
saber se a conversa era do bot.

## Decisão

Pacote `internal/infodesk` é o único que fala com a API. O estado da conversa
(`bot` / `transferida` / `encerrada`) vive na tabela `conversas` do nosso
Postgres/SQLite: o gateway marca `transferida` ao chamar
`POST /conversations/:id/department` e ignora webhooks dela até
`conversation.finished`. Transferência = departamento (`/department`);
`/assign` fica implementado mas desligado até P11. Reconciliação por polling
de `GET /conversations` entra só quando o schema for conhecido (P3).

## Consequências

- Nenhuma dependência de recurso não documentado da API.
- Risco aceito: humano que assume sem o bot ter transferido não é detectado
  (P5) — registrado como pendência crítica para a Infominas.
- Webhook perdido = mensagem perdida até o reconciliador existir (P3).

## Alternativas descartadas

- Consultar `GET /conversations/:id` antes de cada resposta para detectar
  atribuição: campo não documentado; chamada a mais por turno sem garantia.
- Usar `/assign` para uma "pessoa bot": não há usuário do bot documentado.
