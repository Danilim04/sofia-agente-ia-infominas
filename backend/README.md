# bot-azapfy — gateway Chatwoot + gate de identidade

Backend em Go que recebe os webhooks do **Chatwoot** (WhatsApp), resolve a
**identidade** do usuário no edge (telefone → base própria → login → MongoDB da
Azapfy → confirmação de um dado) e, **só quando autenticado**, encaminha a
mensagem ao **cérebro Python** (LangGraph) via o **Contrato A** (`POST /chat`).

As **tools de dados (MCP)** — rastrear nota, chamados, pesquisar notas etc. —
ficam para a **fase seguinte**; este serviço entrega a fundação + a 1ª tool
(resolução de identidade).

## Fluxo

```
WhatsApp → Chatwoot ─webhook→ [bot-azapfy]
   (HMAC/token, dedup por delivery, worker pool)
        └─ higiene de envelope (StripAssinatura) + dedup por WAID (source_id)
           + descarte de eventos velhos (EVENTO_IDADE_MAX)
        └─ fila FIFO POR CONVERSA + coalescência (DEBOUNCE_JANELA=8s,
           teto DEBOUNCE_TETO=20s): a rajada do cliente vira UM turno;
           nunca há dois turnos simultâneos na mesma conversa
        └─ gate de identidade (FSM + base própria em Postgres [PG_URL] ou
           SQLite; GateFalha expira após GATE_FALHA_TTL e é APAGADO quando a
           conversa é resolvida no Chatwoot — conversation_updated)
             1ª msg → pede login → resolve login → Mongo (BuscarPorLogin + projeção)
                    → confirma e-mail/nome (tolerante a texto ao redor)
                    → cacheia (telefone→perfil, TTL)
        │  (identificado)
        │  Contrato A: POST /chat {conversation_id, mensagem, identidade, telefone}
        ▼
   [cérebro Python] → reply → FormatWhatsApp + QuebrarMensagem → Chatwoot
```

A identidade é **resolvida no Go** e nunca é um argumento escolhido pelo LLM —
base do controle de acesso mecânico (não por prompt). O perfil transportado é
**mínimo**: empresas/bases com acesso ativo, módulos ativos e `grupo_user`.

### Resolução do login (determinístico + fallback de IA)

Ao receber a mensagem de login, o gate (`resolverLogin` em `gate.go`) tenta nesta ordem:

1. **Determinístico** (`loginCandidatos`): a mensagem crua, sua versão em
   minúsculas, um CPF/CNPJ formatado puro (`105.966.936-64` → `10596693664`),
   um **e-mail embutido na frase** (regex) e **sequências de exatamente 11/14
   dígitos embutidas** (tolera pontuação trocada, ex.: `105.966.936.64`).
   Dígitos soltos de frases não formam 11/14 dígitos por acaso, e o lookup no
   Mongo é a validação real.
2. **Fallback de IA** (`LoginExtractor`, opcional): se nada casa, o gate chama
   `POST /extract-login` no cérebro para extrair o login embutido numa frase
   livre (ex.: *"meu login é joao"*).

O lookup no Mongo é a **validação real** — candidatos inexistentes simplesmente
não casam, então tentar vários não tem risco. O valor extraído pela IA é só um
CANDIDATO: a autorização continua sendo Mongo + confirmação de um dado. O
fallback é **fail-soft**: IA fora do ar → segue como "não encontrado" e pede de
novo (não derruba o atendimento). `extractor` é injetado em `identity.New(...)`
(nil = sem fallback de IA).

### Persona "Zapin" (PT-BR neutro)

As mensagens do gate (`msgPedirLogin`, `saudacao`, confirmação etc.) usam o tom
**caloroso em português neutro** do **Zapin**, o atendente virtual da Azapfy —
alinhado ao `SYSTEM_PROMPT_AGENTE` do cérebro (o sotaque mineiro foi removido
por decisão pós-Conversa 34). O nome vive na const `nomeAssistente`; trocar ali
muda todas as saudações. O tom é afetuoso, mas os passos continuam claros.

## Pacotes

| Pacote | Papel |
|--------|-------|
| `internal/chatwoot` | tipos do webhook + cliente REST + verificação HMAC |
| `internal/webhook` | endpoint HTTP, autenticação, dedup, pool de workers |
| `internal/store` | dedup, estado do gate, base própria (telefone→perfil) — Postgres (`PG_URL`, schema `gateway`, o mesmo do RAG) ou SQLite (`DB_PATH`, dev) |
| `internal/mongo` | **1ª tool**: `BuscarPorLogin` + `Projetar` (grupos→empresas, só `ativo`) |
| `internal/identity` | gate FSM (pede login → resolve login determinístico/IA → confirma → identifica/roteia) |
| `internal/brain` | cliente do cérebro: Contrato A (`POST /chat`) + `ExtrairLogin` (`POST /extract-login`, satisfaz `identity.LoginExtractor`) |
| `internal/engine` | orquestra webhook → higiene/dedup WAID → fila por conversa (coalescer.go) → gate → cérebro → formatação WhatsApp (whatsapp.go) → Chatwoot |
| `cmd/bot` | wiring + servidor HTTP |

## Rodar

```bash
cp .env.example .env   # preencher Chatwoot + MONGO_URI + BRAIN_BASE_URL
go run ./cmd/bot
```

Pré-requisitos: Mongo da Azapfy acessível e o cérebro Python no ar
(`uvicorn server:app --port 8001` na pasta `../agente-ia` deste monorepo).

## Testes

```bash
go test ./...
```

- `internal/mongo`: projeção do doc de exemplo (AZAPERS incluído; **AZAPFY
  excluído** por `ativo=false`; módulos inativos fora).
- `internal/identity`: fluxo feliz (login → confirmação → identificado →
  encaminhar), cache hit em nova conversa, login inexistente/inativo → humano,
  CPF formatado normaliza sem chamar a IA, login via fallback de IA, e IA
  indisponível não quebra (pede de novo). Usa `fakeExtractor` (sem rede).

## Smoke test (curl)

Simula um `message_created` do Chatwoot (requer Mongo + cérebro no ar):

```bash
curl -sS "http://localhost:8080/webhook?token=$WEBHOOK_TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'X-Chatwoot-Delivery: dev-1' \
  -d '{
    "event": "message_created",
    "message_type": "incoming",
    "content": "oi",
    "private": false,
    "sender": {"type": "contact", "phone_number": "+5511999990001"},
    "conversation": {"id": 123, "labels": ["fila-bot"]}
  }'
```

O bot responde pedindo o login; envie o login e depois o e-mail cadastrado em
mensagens subsequentes (mesma `conversation.id`) para concluir a identificação.

## Contrato A (Go → Python)

```jsonc
// POST {BRAIN_BASE_URL}/chat
{ "conversation_id": "123", "canal": "whatsapp", "mensagem": "...",
  "identidade": { "encontrado": true, "login": "...", "nome": "...",
                  "empresas": [ { "grupo_empresa": "...", "grupo_user": "...",
                    "area": "...", "bases": [ { "nome": "...", "sigla": "...",
                    "modulos_ativos": ["..."] } ] } ] },
  "telefone": "..." }
// resposta: { "reply": "...", "acoes": [], "fontes": ["..."] }
```

### `POST /extract-login` (fallback de resolução de login)

Usado pelo gate quando a normalização determinística não casa nenhum usuário. O
`login` devolvido é só um CANDIDATO — quem autoriza é o gate (Mongo + confirmação).

```jsonc
// POST {BRAIN_BASE_URL}/extract-login
{ "mensagem": "meu login é joao" }
// resposta: { "login": "joao" }   // login: null se não houver identificador
```
