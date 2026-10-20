# API de Integração — Infominas Desk (Infodesk)

Documentação da API do **Infodesk** (sistema interno da Infominas de atendimento
WhatsApp/chat + gestão de tickets), convertida do PDF
`fonte-documentacao-automacao-infodesk.pdf` (5 páginas, "API de Integração —
Infominas Desk — Guia para sistemas parceiros"). **Tudo que não está no PDF está
marcado como "NÃO DOCUMENTADO"** e consolidado em
[07-pontos-ambiguos.md](07-pontos-ambiguos.md). Nenhum campo, valor ou
comportamento foi inventado: se um agente precisar de algo que não está aqui,
a resposta certa é "perguntar à Infominas", não assumir.

## Para quem lê isto

Agentes (Claude Code) implementando o cliente HTTP do Infodesk, as tools do
agente de IA e o receptor de webhooks. Leia o índice, abra só a área que vai
implementar e **sempre** leia o 07 antes de fixar um contrato.

## Fatos básicos

| Item | Valor |
|---|---|
| Base URL (produção) | `https://infodesk.infominas.net/api` |
| Prefixo de versão | `/integration/v1` → rotas ficam `https://infodesk.infominas.net/api/integration/v1/...` |
| Formato | JSON em todas as requisições e respostas (`Content-Type: application/json`) |
| Autenticação | chave de API `ik_live_…` em **toda** chamada (ver 01) |
| Autorização | por **escopos** na chave: `tickets:read`, `tickets:write`, `conversations:read`, `conversations:write` |
| Webhooks | POST JSON assinado com HMAC-SHA256 (`X-Infominas-Signature`), best-effort, timeout 8 s (ver 05) |
| Ambiente de homologação | **NÃO DOCUMENTADO** (só a base de produção aparece) |

## Índice

| Arquivo | Conteúdo |
|---|---|
| [01-autenticacao.md](01-autenticacao.md) | Chave de API, headers aceitos, escopos, erros 401/403 |
| [02-dados-referencia.md](02-dados-referencia.md) | Mesas (`desks`), atendentes (`users`), departamentos (`departments`) |
| [03-tickets.md](03-tickets.md) | Abrir ticket, consultar por número, responder ao solicitante |
| [04-conversas.md](04-conversas.md) | Listar/ler conversas, enviar mensagem, atribuir, transferir departamento, encerrar |
| [05-webhooks.md](05-webhooks.md) | Eventos, payload, assinatura HMAC, semântica de entrega |
| [06-erros.md](06-erros.md) | Códigos HTTP e formato do corpo de erro |
| [07-pontos-ambiguos.md](07-pontos-ambiguos.md) | **Tudo que a documentação não responde** — perguntas abertas à Infominas |

## Mapa rápido de rotas

| Método | Rota | Escopo | Doc |
|---|---|---|---|
| GET | `/integration/v1/desks` | (não informado) | 02 |
| GET | `/integration/v1/users` | (não informado) | 02 |
| GET | `/integration/v1/departments` | (não informado) | 02 |
| POST | `/integration/v1/tickets` | `tickets:write` | 03 |
| GET | `/integration/v1/tickets/:number` | `tickets:read` | 03 |
| POST | `/integration/v1/tickets/:number/reply` | `tickets:write` | 03 |
| GET | `/integration/v1/conversations` | `conversations:read` | 04 |
| GET | `/integration/v1/conversations/:id` | `conversations:read` | 04 |
| POST | `/integration/v1/conversations/:id/messages` | `conversations:write` | 04 |
| POST | `/integration/v1/conversations/:id/assign` | `conversations:write` | 04 |
| POST | `/integration/v1/conversations/:id/department` | `conversations:write` | 04 |
| POST | `/integration/v1/conversations/:id/finish` | `conversations:write` | 04 |
| (webhook) | `message.received`, `conversation.started`, `conversation.finished` | — | 05 |

## O que a API NÃO oferece (relevante para o desenho do agente)

- Nenhuma rota para **buscar cliente/contato** (por telefone, CPF, nome).
- Nenhuma rota para **listar tickets** (por contato, por telefone, por status) —
  só consulta por `number` conhecido.
- Nenhuma rota para **atualizar/fechar ticket** (status, prioridade, mesa).
- Nenhuma rota para **listar prioridades** (mas `priorityId` é um UUID).
- Nenhuma **chave de idempotência** na abertura de ticket — não duplicar é
  responsabilidade do nosso lado.
- Nenhuma **reentrega** de webhook — perda de evento tem de ser reconciliada
  por polling de `GET /conversations`.
- Nenhum envio de **mídia** (só `body` texto) e nenhum campo de anexo
  documentado no payload recebido.

Ver [07-pontos-ambiguos.md](07-pontos-ambiguos.md) para a lista completa e o
impacto de cada lacuna.
