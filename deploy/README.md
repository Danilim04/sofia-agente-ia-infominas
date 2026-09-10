# Deploy

Como o bot chega em produção. Três ferramentas, cada uma com um papel:

| Ferramenta | Papel | Onde |
|---|---|---|
| **Ansible** | Estado da VM: user deploy, hardening, ufw, docker, `/opt/azapfy-bot`, cron do sync | `infra/ansible/` — 1º job da pipeline |
| **Pipeline** | Deploy via `deploy/deploy-remote.sh --build-on-host` (build NA VM) | `.github/workflows/deploy.yml` |
| **Terraform** | VM (Hetzner) + DNS (Cloudflare) — **só da VM antiga**, ver abaixo | `infra/terraform/` — não se aplica à VM atual |

## O servidor

Uma única VM (`152.53.208.178`, provisionada pela Azapfy em 2026-09-10:
**Debian 13 "trixie", ARM64/aarch64**, 12 vCPU / 23 GB / 755 GB, hostname
`omni-route.azapfy.com.br`) — **compartilhada com a stack omni-route**: é nela
que rodam o Traefik (80/443), o Chatwoot, a Evolution API e a API do omni do
tenant azapfy. O bot roda AO LADO, sem publicar porta nenhuma na internet:

```
WhatsApp → Evolution → Chatwoot ──webhook──► gateway (Go)  ──POST /chat──► brain (Python)
                ▲                            http://azapfy-bot:8080        http://brain:8001
                └────── respostas via API ─────────┘
        (tudo por DENTRO da rede docker omniroute_default / azapfy-bot_default)
```

- O gateway entra na rede `omniroute_default` com o alias **`azapfy-bot`** —
  o webhook do Chatwoot aponta para `http://azapfy-bot:8080/webhook?token=…`
  e o gateway fala com a API do Chatwoot em `http://chatwoot-rails:3000`
  (nome do serviço no compose do omni-route; a API do omni é `http://api:3333`).
- O cérebro só existe na rede interna da stack do bot (`azapfy-bot_default`).
- **A VM é ARM64.** O runner do GitHub é x86_64, então as imagens são
  construídas **na própria VM** (`deploy/build-on-host.sh`, chamado pelo
  `deploy-remote.sh --build-on-host`): as fontes rastreadas pelo git vão por
  rsync para `/opt/azapfy-bot/src` e o `docker build` roda lá, com cache de
  camadas local. Nada de QEMU, nada de registry.
- Estado no servidor: os volumes do compose — **`pgvector-data`** (Postgres:
  índice do RAG + estado do gateway no schema `gateway`; precisa de backup),
  `brain-data` (checkpoints de conversa) e `gateway-data` (sqlite legado, só
  para migração manual) — e os arquivos `.chatwoot-token`/`.zapin-credentials`
  (gravados pela pipeline do Chatwoot; o rsync nunca os toca). O `.env` é
  reescrito por inteiro a cada deploy.
- Rotina da base de conhecimento: o ansible (role `base`) instala
  `/usr/local/bin/azapfy-sync-docs` e dois crons do usuário `deploy`
  (`30 6 * * *` e `0 7 * * 0`). **Atenção ao fuso**: a VM atual está em
  `Europe/Berlin` (a antiga era UTC), então hoje isso dá 01:30/02:30 em
  Brasília — inofensivo, mas ajuste os horários na role se quiser 03:30 BRT.
  Log: `journalctl -t azapfy-sync`. Runbook: `plano-migracao-pgvector.md`.
- Fonte única do IP: `infra/ansible/inventories/prod/hosts.yml` (a pipeline lê
  de lá). A VM anterior (Hetzner `62.238.63.240`, x86_64/Ubuntu) foi desativada;
  o root `infra/terraform` descreve **aquela** VM (import do id Hetzner) e não
  gerencia a atual, que foi provisionada fora do nosso Terraform.

## Pipeline (`.github/workflows/deploy.yml`)

Dispara a cada push na `main` (ou `Run workflow` com uma tag à escolha):

1. **ansible** — converge o estado da VM (`configure-server.yml`, role `base`;
   funciona em Ubuntu e Debian — repo do Docker e systemd-resolved são
   resolvidos pela distro real).
2. **test-go** — `go vet` + `go test` do gateway (paralelo ao ansible).
3. **deploy** — renderiza o `.env` dos secrets (`deploy/env/render-env.sh`,
   fail-fast se faltar secret) e roda `deploy/deploy-remote.sh --build-on-host`:
   rsync da allowlist + fontes → **build nativo na VM** → `.env` → **Postgres
   up + ingestão da base (AzapDocs → pgvector, na imagem nova do brain)** →
   `compose up --wait` → `healthcheck.sh` (portões: pgvector, **índice do RAG
   não-vazio**, gateway, brain, caminho do webhook pela rede do Chatwoot).
   Falha parcial da ingestão (um doc vazio na fonte) não derruba o deploy;
   índice vazio derruba. **Sem o secret `DOCS_API_KEY`** o passo de ingestão
   usa a **fonte local** (`docs/*.md` do repo, já dentro da imagem) para o bot
   subir com alguma base; o cron diário (`--fonte api`) vai falhar no journal
   até a chave existir. Quando ela chegar: cadastre o secret e rode o Deploy
   (a base oficial é acrescentada; os ids não colidem). Para tirar os docs do
   repo do índice depois disso, na VM:
   `cd /opt/azapfy-bot && docker compose run --rm -T brain python -m src.rag.sync --fonte local --docs-dir /tmp --remover-ausentes --permitir-remocao-em-massa`.

**Rollback**: `Run workflow` com uma `version` anterior (a VM guarda as 3
últimas tags de cada serviço) ou re-rodar a pipeline num commit antigo.

### Secrets (Settings → Secrets and variables → Actions)

| Secret | O que é |
|---|---|
| `SSH_PRIVATE_KEY` | chave privada do user `deploy` da VM (par autorizado via Ansible; chave **dedicada ao CI**, não a pessoal) |
| `OPENROUTER_API_KEY` | chave do OpenRouter (cérebro) |
| `MONGO_URI` | Mongo da Azapfy (lookup de usuário do gate) |
| `WEBHOOK_TOKEN` | segredo do `?token=` da URL de webhook cadastrada no Chatwoot |
| `TOOLS_API_TOKEN` | segredo compartilhado cérebro↔gateway (tools SAC) |
| `PGVECTOR_PASSWORD` | senha do Postgres do compose (RAG + estado do gateway); só alfanumérica, entra numa URL |
| `DOCS_API_KEY` | chave `azk_…` do AzapDocs (escopos `DOCS_LIST` + `DOCS_READ`) — fonte da base de conhecimento |
| `SAC_API_TOKEN` · `SAC_SERVICE_COD` · `CHATWOOT_API_TOKEN` · `SYNC_ALERTA_WEBHOOK` · `LANGFUSE_*` | opcionais (SAC; o token do Chatwoot é só bootstrap/override; webhook avisado quando o sync diário falha; tracing) |

| Variable | O que é |
|---|---|
| `INBOX_ID` | caixa do Chatwoot cujas conversas novas o gateway põe na `fila-bot`. Vazia = `0` = todas. Os ids são da instância — a pipeline "Configurar Chatwoot" lista as caixas ao final. |

O `CHATWOOT_API_TOKEN` de verdade é **estado da VM**: a pipeline "Configurar
Chatwoot" grava o access token do usuário "Zapin" em
`/opt/azapfy-bot/.chatwoot-token` (o valor nunca sai da máquina) e o
`apply-server-state.sh` o aplica no `.env` — nesta hora e em todo Deploy.

Sem registry de imagens ⇒ sem secrets de Docker Hub/GHCR.

## Primeira vez num servidor novo

1. Atualize o `ansible_host` em `infra/ansible/inventories/prod/hosts.yml`.
2. Autorize a chave do CI no user `deploy`. Com uma chave que JÁ entra na VM
   (como a pessoal de quem provisionou), rode a role passando a pública do CI:
   ```bash
   cd infra/ansible
   ansible-playbook playbooks/site.yml --private-key ~/.ssh/<sua-chave> \
     -e "{\"deploy_ssh_public_key\": \"$(cat ~/.ssh/azapfy_deploy.pub)\"}"
   ```
   (Se nem isso entrar, `playbooks/bootstrap.yml` conecta como root.) A VM
   reprovisionada com o MESMO IP muda a chave de host: limpe a antiga com
   `ssh-keygen -R <ip>` antes, ou passe `ANSIBLE_SSH_ARGS`/`SSH_EXTRA_OPTS`
   com `-o UserKnownHostsFile=…`.
3. Cadastre os secrets acima (a privada do CI vira `SSH_PRIVATE_KEY`) e rode a
   pipeline **Deploy** (converge o estado via Ansible e sobe a stack). Numa VM
   nova o deploy percebe que `.chatwoot-token` não existe e roda o
   `setup-bot.sh` sozinho antes de subir o gateway (que não inicia sem
   `CHATWOOT_API_TOKEN`) — o passo 4 fica para rotações e para listar caixas.
4. Rode a pipeline **Configurar Chatwoot (caixa do bot)** — ela provisiona o
   bot **pela API do omni-route** (`deploy/chatwoot/setup-bot.sh`,
   idempotente): usuário "Zapin" via `POST /api/users/workspace`, token
   via login workspace do bot (vira estado da VM, `.chatwoot-token`),
   etiquetas e webhook pela API do Chatwoot (o mesmo caminho do front do
   omni), verificação via `GET /api/auth/workspace/me`, e recarrega o
   gateway. **Sem passo manual**: ao final dela o bot está atendendo. Ela
   também **lista as caixas de entrada** da conta: se o bot deve atender só
   uma, grave o id na variable `INBOX_ID` e rode o Deploy de novo.
   Conversa nova entra na fila do bot pelo próprio gateway
   (`conversation_created` → aplica `fila-bot`, escopo pela `INBOX_ID` do
   `.env`) — sem automation rule no Chatwoot, que o omni-route não gerencia.

## Break-glass (deploy da máquina local, sem Actions)

```bash
IMAGE_TAG=v-manual-1 OPENROUTER_API_KEY=… MONGO_URI=… \
  WEBHOOK_TOKEN=… TOOLS_API_TOKEN=… PGVECTOR_PASSWORD=… DOCS_API_KEY=… \
  ./deploy/env/render-env.sh > /tmp/prod.env

./deploy/deploy-remote.sh --host 152.53.208.178 --version v-manual-1 \
  --env-file /tmp/prod.env --key ~/.ssh/azapfy_deploy --build-on-host
```

`--build-on-host` é obrigatório a partir de uma máquina x86_64 (a VM é ARM64);
sem a flag o script builda localmente e manda por `docker save|load`, o que só
serve se a sua máquina tiver a mesma arquitetura da VM. Diagnóstico na VM:
`cd /opt/azapfy-bot && docker compose ps`, `docker compose logs -f gateway brain`
e `./deploy/healthcheck.sh`.

## DNS (desligado)

Hoje nada do bot é exposto na internet. Quando precisar de URL pública
(ex.: webhook externo): descomente os labels Traefik no
`docker-compose.prod.yml` (o Traefik do omni-route já termina TLS nesta VM) e
crie o registro DNS no provedor — o root Terraform/Cloudflare de
`infra/terraform` era da VM antiga e precisaria ser readequado antes.

## O que a pipeline NÃO faz

- **Terraform** não roda no CI (e não descreve a VM atual).
- **pytest do cérebro** não roda no CI (suíte pesada, torch): rode local.
- **Escolha da caixa (`INBOX_ID`)** é decisão do operador: a pipeline do
  Chatwoot só lista as caixas.
