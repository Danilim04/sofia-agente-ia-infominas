# bot-infominas — gateway Go da Sofia

Backend em Go que recebe os webhooks do **Infodesk** (WhatsApp da Infominas),
enfileira e coalesce as mensagens por conversa, encaminha ao **cérebro
Python** (LangGraph) pelo **Contrato A** (`POST /chat`) e responde pelo
transporte. Não há gate de login: a identificação do cliente acontece na
conversa, dentro do cérebro, validada por código (ADR 0003). Plano completo em
`docs/plano/`; API do Infodesk em `docs/infodesk/`.

## Fluxo

```
WhatsApp → Infodesk ─webhook→ [gateway]
   (HMAC do corpo cru / ?token=, dedup por id de entrega, worker pool)
        └─ engine.Inbound neutro {conversation_id, telefone, nome, texto,
           msg_id, tem_midia, criado_em}  (E2.1: montado do payload)
        └─ dedup por msg_id + descarte de eventos velhos (EVENTO_IDADE_MAX)
        └─ fila FIFO POR CONVERSA + coalescência (DEBOUNCE_JANELA=8s,
           teto DEBOUNCE_TETO=20s): a rajada do cliente vira UM turno;
           nunca há dois turnos simultâneos na mesma conversa
        └─ mídia → aviso + transferência · "atendente"/"humano" → transferência
        │  Contrato A: POST /chat {conversation_id, canal, mensagem, telefone}
        ▼
   [cérebro Python] → reply → bolhas (---) → FormatWhatsApp → QuebrarMensagem
                                           → Transporte.EnviarMensagem
```

Estado desta etapa (E1.1): o `main.go` sobe com um **transporte nulo** (loga
em vez de enviar) e o webhook só autentica/deduplica/enfileira — o payload do
Infodesk e o cliente da API entram em E1.2/E2.1.

## Pacotes

| Pacote | Papel |
|--------|-------|
| `internal/webhook` | endpoint HTTP, autenticação (HMAC + token), dedup por id de entrega, pool de workers |
| `internal/engine` | `Transporte` (interface), `Inbound`, fila por conversa (coalescer.go), disjuntor, transferência única (transferencia.go), persona (persona.go), formatação WhatsApp (whatsapp.go) |
| `internal/brain` | cliente do cérebro: Contrato A (`POST /chat`, `POST /encerrar-episodio`) |
| `internal/store` | dedup e ocorrências (`falhas`) — Postgres (`PG_URL`, schema `gateway`) ou SQLite (`DB_PATH`); E2.1 adiciona conversas/contatos/tickets |
| `internal/alerta` | vigia de alertas (`sofia_alerta`: falha definitiva, taxa de falhas, disjuntor) |
| `internal/toolsapi` | API interna `/tools/infodesk/*` para o cérebro (esqueleto + `ping`; handlers em E3.1) |
| `internal/config` | variáveis de ambiente (ver `.env.example`) |
| `internal/arquitetura` | testes de dependência entre pacotes |
| `cmd/bot` | wiring + servidor HTTP |

## Rodar

```bash
cp .env.example .env   # preencher INFODESK_BASE_URL, INFODESK_API_KEY, BRAIN_BASE_URL
go run ./cmd/bot
```

Pré-requisito: o cérebro Python no ar (`uvicorn server:app --port 8001` em
`../agente-ia`). Stack completa com mock do Infodesk: `scripts/start-all.sh`.

## Testes

```bash
go test ./...                      # SQLite sempre
PG_TEST_URL=postgresql://rag:rag@localhost:5432/rag go test ./internal/store/   # + Postgres
```

Os testes da engine usam `fakeTransporte` e `fakeBrain` (sem rede) e são a
especificação do comportamento: coalescência, disjuntor, transferência única,
mídia, pedido de atendente, alertas, bolhas.

## Contrato A (Go → Python)

```jsonc
// POST {BRAIN_BASE_URL}/chat
{ "conversation_id": "uuid-infodesk", "canal": "whatsapp", "mensagem": "...", "telefone": "+55..." }
// resposta: { "reply": "...", "acoes": [], "classificacao": {...}, "fontes": [], "erro": null }
```

Exemplos e testes dos dois lados em `contratos/contrato-a/`.
