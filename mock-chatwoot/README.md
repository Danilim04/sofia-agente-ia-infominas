# mock-chatwoot — harness de teste E2E (sem WhatsApp/Chatwoot real)

Front simples que **finge ser o Chatwoot** para exercitar o fluxo inteiro
`front → backend Go → cérebro Python → resposta`, incluindo o gate de identidade
e (depois) as tools. **Não exige mexer no Go**: é só transporte + um `.env`
apontando o Go para o mock.

```
você (browser)  ──"message_created"──►  Go /webhook   ──►  Python /chat
      ▲                                    │ (gate → Contrato A → grafo)
      └──── POST .../messages  ◄───────────┘  (Go "responde no Chatwoot" = no mock)
```

O mock implementa os dois lados do transporte do Chatwoot:
- **Saída→Go:** emite um JSON `message_created` idêntico ao do Chatwoot.
- **Entrada←Go:** expõe a REST API que o Go chama para responder
  (`.../conversations/{id}/messages` e `.../labels`) e mostra no chat.

## Como rodar (3 processos)

```bash
# 1) Cérebro Python (porta 8001)
cd agente-ia && source venv/bin/activate
uvicorn server:app --port 8001

# 2) Backend Go (porta 8080) — com .env apontando para o mock (ver abaixo)
cd backend && go run ./cmd/bot

# 3) Mock Chatwoot (porta 9000) — stdlib, sem pip install
cd mock-chatwoot && python3 mock_chatwoot.py
# abra http://localhost:9000
```

No front: digite um **telefone**, clique **Nova conversa** e converse. A 1ª
mensagem dispara o gate (pede login → confirma e-mail/nome → identifica).

## `.env` do Go para falar com o mock

No `backend/.env`, o essencial é apontar o `CHATWOOT_BASE_URL` para o mock e
deixar o webhook só com token (sem HMAC):

```env
PORT=8080
WEBHOOK_TOKEN=dev-token          # casar com o mock (WEBHOOK_TOKEN / --token)
WEBHOOK_SECRET=                  # vazio = sem HMAC (o mock não assina)
LABEL_BOT=fila-bot               # casar com o mock (--label-bot)
LABEL_HUMANO=fila-humano

CHATWOOT_BASE_URL=http://localhost:9000   # ◄── aponta para o MOCK
CHATWOOT_ACCOUNT_ID=1
CHATWOOT_API_TOKEN=qualquer-coisa         # o mock não valida

BRAIN_BASE_URL=http://localhost:8001
MONGO_URI=mongodb://localhost:27017       # ◄── necessário p/ o login (ver nota)
MONGO_DB=azapfy
MONGO_COLLECTION=users
```

E o mock com o mesmo token:

```bash
WEBHOOK_TOKEN=dev-token python3 mock_chatwoot.py
# ou: python3 mock_chatwoot.py --token dev-token --port 9000
```

Config do mock (env ou flags): `MOCK_PORT/--port`, `GO_WEBHOOK_URL/--go-webhook`
(default `http://localhost:8080/webhook`), `WEBHOOK_TOKEN/--token`,
`CHATWOOT_ACCOUNT_ID/--account-id`, `LABEL_BOT/--label-bot`.

## Nota: Mongo é pré-requisito do login

O gate resolve o login no **Mongo da Azapfy** (`BuscarPorLogin`) — isso o mock
**não** substitui (seria mexer no Go). Para exercitar o caminho login→confirmação
você precisa de um Mongo acessível com ao menos um usuário, ex.:

```js
// mongosh: use azapfy; db.users.insertOne(...)
{ login: "joao", nome: "João Silva", email: "joao@empresa.com",
  grupos: { "ACME": { ativo: true, grupo_user: "admin", area: "fiscal",
    bases: { "Matriz": { nome: "Matriz", sigla: "MTZ",
      modulos: { "fiscal": { ativo: true }, "financeiro": { ativo: true } } } } } } }
```

Depois, no front: telefone qualquer → `joao` → `joao@empresa.com` → identificado.
Como a identidade é cacheada por telefone (TTL), **reusar o mesmo telefone** pula
o login na próxima conversa; use um telefone diferente para testar o fluxo do zero.

## Limitações

- Só trata `message_created` (entrada do contato) e as respostas do agente; não
  simula anexos, agentes humanos digitando, etc.
- Não persiste: as transcrições vivem em memória enquanto o mock roda.

## Resolver conversa (teste do "episódio encerrado")

O botão **Resolver conversa** no front faz o que o atendente faria no
Chatwoot: marca a conversa como `resolved`, tira as etiquetas de fila e
emite `conversation_updated` (com `status` em `changed_attributes`) para o
Go — que **apaga o estado do gate** daquela conversa. A próxima mensagem
reabre a conversa (o Go readota com `fila-bot`) e o cliente é atendido na
hora, sem esperar o `GATE_FALHA_TTL`. O `toggle_status` chamado pelo Go
também emite o mesmo evento, como o Chatwoot real.
