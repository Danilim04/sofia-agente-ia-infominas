# 0005 — Idempotência e dedup de tickets no gateway

- **Estado:** aceita (2026-10-19)
- **Etapa:** planejamento (E3.1)
- **Pendências relacionadas:** P3, T5, T8

## Contexto

`POST /integration/v1/tickets` não tem chave de idempotência e não existe
listagem de tickets por contato. Um retry de rede após um POST sem resposta,
ou um cliente insistindo, pode duplicar chamados. O cliente decidiu: perguntar
o número e não duplicar (D4).

## Decisão

Tabela `tickets` no store (os dois backends) com `chave_idempotencia` única =
`sha256(conversation_id + titulo + categoria + criada_em da proposta)`,
calculada no Python a partir da proposta carimbada e enviada em
`/tools/infodesk/ticket/abrir`. O gateway faz `INSERT … status='pendente'`
**antes** do POST; conflito devolve o ticket existente. Depois do POST,
`status='aberto'` + `numero`. POST sem resposta deixa `pendente`: nunca
reenviado automaticamente; alerta `ticket_pendente` + transferência para
humano. Dedup de negócio em `preparar`: ticket `aberto` da mesma conversa, ou
do mesmo telefone + categoria nas últimas 24 h, devolve `chamado_aberto`.

## Consequências

- Nenhum ticket duplicado por retry; duplicata de negócio vira pergunta ao
  cliente.
- A listagem "meus chamados" só conhece tickets abertos pela Sofia.
- Um ticket `pendente` que na verdade foi criado no Infodesk só é
  reconciliado à mão até existir listagem (P3).

## Alternativas descartadas

- Retry do POST com backoff: pode duplicar; a API não dá como checar.
- Idempotência só por `conversation_id`: impede um segundo chamado legítimo
  (assunto novo) na mesma conversa.
