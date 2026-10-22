# Deploy da Sofia (genérico)

Como colocar a stack para rodar em **qualquer host com Docker**. A infra da
Infominas ainda não foi definida (pendência **P10**), então este guia não
supõe provedor, domínio nem quem opera: ele diz o que a stack **exige** e o que
**falta decidir** (no fim). O estado do que não depende de P10 — compose,
imagens, variáveis — é testado; o resto é requisito.

```
Infodesk ──HTTPS──► [proxy reverso: TLS]──► gateway :8080 ──POST /chat──► brain :8001
  (webhook)              (da Infominas)        │   ▲                         │
                                               │   └──── /tools/infodesk/* ◄─┘
                                               └──► pgvector (schema `gateway` + rag_*)
```

Só o **gateway** é alcançado de fora (o webhook). Cérebro e Postgres ficam na
rede interna do compose; o gateway fala com o Infodesk pela API pública deles.

## Requisitos

| Requisito | Detalhe |
|---|---|
| Host Linux x86_64 ou ARM64 | CPU basta (torch CPU-only); ~4 GB de RAM livre e ~6 GB de disco para as imagens |
| Docker Engine + `docker compose` ≥ 2.24.4 | o override de produção usa a tag `!reset` |
| **Domínio com HTTPS público** | o Infodesk só entrega webhook em URL HTTPS pública. Quem termina o TLS (nginx, Caddy, Traefik, load balancer) é da Infominas; ele encaminha `https://<domínio>/webhook` ao gateway (`127.0.0.1:8080`, ou `gateway:8080` se o proxy rodar na rede do compose). Só `/webhook` precisa ficar exposto |
| Saída de rede | `infodesk.infominas.net` (API), `openrouter.ai` (LLM), TypeSafe (JEV) e, com `EMBEDDINGS_PROVIDER=local`, o Hugging Face (o modelo de embeddings é baixado no primeiro `sync` e fica no volume `hf-cache`) |
| Um lugar para os segredos | ver "Segredos" abaixo |
| Rotina de backup do volume `pgvector-data` | é o estado de produção |

## Subir

```bash
cp .env.example .env          # preencha as linhas da tabela "O QUE É OBRIGATÓRIO EM PRODUÇÃO"
echo 'COMPOSE_FILE=docker-compose.yml:docker-compose.prod.yml' >> .env
docker compose build          # sofia-bot:brain-<tag> e sofia-bot:gateway-<tag> (IMAGE_TAG por versão)
docker compose run --rm brain python -m src.rag.sync   # sobe o pgvector e popula o índice do RAG
docker compose up -d
deploy/healthcheck.sh https://<seu-dominio>            # portões: banco, índice, cérebro, gateway, POST /webhook
```

- No compose o cérebro **sempre lê o pgvector** (`VECTOR_BACKEND` é fiação; a
  imagem não embute índice Chroma), por isso o `sync` vem **antes** do `up -d`.
  Ao rodar o `sync` de novo com a stack no ar (base atualizada),
  `docker compose restart brain` garante que o cérebro leia o índice novo.
- A base de conhecimento placeholder vem em `agente-ia/docs/base` (já dentro da
  imagem). A fonte real da Infominas é a pendência **P6**; quando existir,
  preencha `DOCS_API_BASE_URL`/`DOCS_API_KEY` e use `--fonte api`.
- Imagens e versões: `IMAGE_REPO` (`sofia-bot`) e `IMAGE_TAG`. Para fixar uma
  versão, mude `IMAGE_TAG` e suba de novo; **rollback = voltar a tag**
  (reconstruindo o commit correspondente, enquanto não houver registry).
- **Taxonomia (`config/`)**: a pasta `agente-ia/config` é montada somente leitura
  em `/config` nos dois serviços (`TAXONOMIA_PATH=/config/taxonomia.yaml`), então
  gateway e cérebro leem o MESMO arquivo. Para usar outra pasta, defina
  `CONFIG_DIR`. Trocar a taxonomia exige reiniciar os dois (`docker compose
  restart brain gateway`). Hoje é o placeholder (P1); com a real, ligue
  `STRICT_TAXONOMIA=true`.

## Segredos

Nenhum segredo vai para o git. Os cinco que a Infominas precisa guardar:

| Variável | Origem | Onde |
|---|---|---|
| `INFODESK_API_KEY` | `ik_live_…`, criada em **Configurações › Integrações** do Desk; aparece **uma única vez** | `.env` do host (ou cofre que o gere) |
| `INFODESK_WEBHOOK_SECRET` | `whsec_…`, mostrado junto com a chave. É outro segredo, não a chave | idem |
| `TOOLS_API_TOKEN` | nosso: `openssl rand -hex 32`. Cérebro e gateway usam o mesmo valor (o compose repassa) | idem |
| `PGVECTOR_PASSWORD` | nosso: alfanumérica, entra numa URL | idem |
| `OPENROUTER_API_KEY` (e `TYPESAFE_API_KEY`) | contas do OpenRouter / TypeSafe | idem |

Proteja o `.env` (`chmod 600`, dono = quem opera o compose). Se o time preferir
um cofre ou as variáveis do CI, basta que o ambiente do `docker compose` chegue
com os mesmos nomes — a lista completa e o que cada uma muda está no topo do
`.env.example`. `python deploy/checar-env.py` garante que o exemplo cobre toda
variável que o código lê e que nenhum segredo foi preenchido nele.

## Trocar a `INFODESK_BASE_URL`

A base real é `https://infodesk.infominas.net/v1` (o cliente aceita a raiz com
ou sem `/integration/v1`; `docs/infodesk/10` §1). Para apontar para outro
ambiente (homologação, mock):

```bash
sed -i 's|^INFODESK_BASE_URL=.*|INFODESK_BASE_URL=https://outro-host/v1|' .env
docker compose up -d gateway      # só o gateway lê esta variável
docker compose logs gateway | head   # o boot chama GET /me: confere chave, organização e "conduz o atendimento"
```

A chave e o segredo do webhook pertencem a **um** ambiente: trocar a base exige
trocar também `INFODESK_API_KEY` e `INFODESK_WEBHOOK_SECRET`. Hoje só existe
produção (`docs/infodesk/07`, A1); chave de homologação é pendência.

## Cadastrar o webhook no Infodesk

No painel do Desk (**Configurações › Integrações**, `docs/infodesk/01`), na
chave da Sofia:

1. **Nome**: "Sofia" (é o autor das mensagens do bot no inbox). **Conduz o
   atendimento**: ligado. **Permissões**: `tickets:read`, `tickets:write`,
   `conversations:read`, `conversations:write`, `contacts:read`,
   `events:read` (`contacts:write` só se o plano adotar `POST /requesters`).
2. **URL do webhook**: `https://<seu-dominio>/webhook` — acrescente
   `?token=<WEBHOOK_TOKEN>` se definir esse segredo extra no `.env`. O gateway
   loga a URL esperada no boot (usa `PUBLIC_BASE_URL`).
3. **Eventos** (os que o gateway trata): `message.received`, `message.sent`,
   `conversation.started`, `conversation.assigned`, `conversation.handoff`,
   `conversation.department_changed`, `conversation.finished`,
   `ticket.created`, `ticket.updated`, `ticket.closed`, `ticket.reopened`,
   `ticket.canceled` (mais `ping`, que o Desk envia no teste). Os demais são
   aceitos e ignorados.
4. Copie a chave (`ik_live_…`) e o segredo (`whsec_…`) **na hora**, para
   `INFODESK_API_KEY` e `INFODESK_WEBHOOK_SECRET` — o Desk não mostra de novo
   (só rotaciona).
5. **Enviar teste** no Desk: o `ping` assinado deve voltar `200` (HMAC e janela
   de 5 min conferem). Se vier `401`, veja a hora do host (NTP) e o segredo.
6. Rotação do segredo (ao trocar de servidor): "Rotacionar segredo" no Desk,
   atualize `INFODESK_WEBHOOK_SECRET` e `docker compose up -d gateway`. Entre o
   clique e o restart o gateway recusa entregas; o Desk reentrega (backoff de
   30 s a 6 h) e o reconciliador recupera pelo feed `GET /events`.

## Operação

```bash
docker compose ps                         # saúde dos três serviços
docker compose logs -f gateway brain      # logs (alertas: `sofia_alerta`)
deploy/healthcheck.sh https://<seu-dominio>
```

- **Backup**: o volume `pgvector-data` (`docker compose exec -T pgvector
  pg_dump -U rag rag > backup.sql`). `brain-data` guarda o histórico das
  conversas (checkpoints) — perdê-lo não perde tickets, só a memória do turno.
- **Atualizar**: `git pull && docker compose up -d --build`; rode o `sync` de
  novo se a base de conhecimento mudou.
- **CI**: `.github/workflows/deploy.yml` (manual, `workflow_dispatch`) valida
  compose, variáveis e as duas imagens. **Não entrega em nenhum host** — a
  etapa de entrega depende de P10.

## O que falta decidir (P10) — nada disto está no repositório

Host e provedor · domínio e quem termina o HTTPS · onde moram os segredos ·
como as imagens chegam ao host (build no host ou registry) · quem faz o backup
do Postgres · quem opera e recebe os alertas (`ALERTA_WEBHOOK_URL`) · ambiente
de homologação do Infodesk. Lista com responsáveis em [`infra/README.md`](../infra/README.md).
