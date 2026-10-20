# Plano de migração: Chroma → pgvector (seed + validação em produção)

**Data:** 2026-09-01 · **Pré-requisito:** código do sync incremental no ar
(`src/rag/sync.py` e afins, ver Contrato B). Todo o fluxo abaixo foi ensaiado
localmente com as docs reais: seed de 4 docs/57 chunks, segundo ciclo
idempotente (4 inalterados, ~1 s) e **paridade de 100%** entre Chroma e
pgvector nas consultas canônicas.

**Atualização 2026-09-04 — a fonte oficial existe:** a API **AzapDocs**
(`https://intranet.azapfy.com.br/api/v1/integrations/docs`, Contrato B v1.0,
retenção de tombstone 60 dias) está no ar e as docs de `agente-ia/docs/` já
foram inseridas no espaço **"Zapin - Base de Conhecimento"**. O seed passa a
ser `--fonte api` (a fonte local fica só para dev). O pipeline inteiro foi
ensaiado contra um mock fiel da API (mesmos caminhos, header, `410`, `/me`):
4 novos/57 chunks, reexecução idempotente, paridade 100% com o Chroma,
retriever em modo `pgvector` e tombstone removendo. **Pendências para a
virada real**: (1) chave `azk_…` gerada no AzapDocs → GitHub Secret
`DOCS_API_KEY` (o valor colado no chat em 2026-09-04 não era uma chave: a API
respondeu 401 pedindo `azk_…`); (2) secret `PGVECTOR_PASSWORD`; (3) após o
seed e o `verificar` em produção, `VECTOR_BACKEND=pgvector` no
`prod.env.tpl`.

## Princípio

A migração é **chaveável e sem janela de risco**: o sync sempre grava no
pgvector; a chave `VECTOR_BACKEND` decide apenas de onde o agente **lê**.
O Chroma continua intacto durante toda a migração — rollback é trocar a chave
de volta e reiniciar (segundos).

```
Fase 1 (seed)      : sync → pgvector       | agente lê Chroma  (nada muda p/ cliente)
Fase 2 (sombra)    : verificar paridade    | agente lê Chroma  (nada muda p/ cliente)
Fase 3 (virada)    : VECTOR_BACKEND=pgvector → agente lê pgvector
Fase 4 (rotina)    : cron diário do sync + alerta por exit code
Rollback (sempre)  : VECTOR_BACKEND=chroma + restart
```

## Fase 0 — Postgres+pgvector na VPS

**Feito no `docker-compose.yml` (2026-09-04)**: serviço `pgvector` com
healthcheck, volume `pgvector-data`, porta publicada só em dev
(`docker-compose.prod.yml` reseta). A senha vem de `PGVECTOR_PASSWORD`
(GitHub Secret obrigatório — `render-env.sh` aborta sem ele); as URLs são
fiação do compose (`PGVECTOR_URL` no brain, `PG_URL` no gateway).

Atenção: o mesmo Postgres guarda o **estado do gateway Go** (schema
`gateway`: dedup de webhooks, `gate_state`, cache `identities`). A instância
é estado de produção do bot, não só cache do RAG — o volume precisa de backup,
e `brain` e `gateway` só sobem depois do `pgvector` ficar saudável.

Referência do serviço (já aplicado):

```yaml
  pgvector:
    image: pgvector/pgvector:pg16
    restart: unless-stopped
    environment:
      POSTGRES_DB: rag
      POSTGRES_USER: rag
      POSTGRES_PASSWORD: ${PGVECTOR_PASSWORD}   # gerar e guardar no .env
    volumes:
      - pgvector-data:/var/lib/postgresql/data
```

No `.env` do brain (os defaults mantêm tudo como está até a Fase 3):

```bash
PGVECTOR_URL=postgresql://rag:${PGVECTOR_PASSWORD}@pgvector:5432/rag
VECTOR_BACKEND=chroma        # explícito: ainda lendo do Chroma
```

## Fase 1 — Seed das documentações existentes (AUTOMÁTICA no deploy)

**Desde 2026-09-04 o seed é um passo do `deploy/deploy-remote.sh`**: depois
de subir o `pgvector` e antes de subir o brain, o deploy roda
`docker compose run --rm -T brain python -m src.rag.sync --fonte api`. O
agente nasce lendo um índice populado; o cron diário mantém. O texto abaixo
vale para rodar à mão (ex.: depois de corrigir um documento na fonte).

O seed usa o MESMO pipeline da rotina diária (a primeira carga é só o
incremental vendo tudo como novo — não existe caminho especial a validar).
A fonte é a API do AzapDocs (`DOCS_API_BASE_URL` + `DOCS_API_KEY` no `.env`);
o ciclo começa pelo preflight `GET /me`, que aborta com o motivo legível
(chave, API do espaço ou acesso do dono) antes de tocar no índice:

```bash
# na VPS, em /opt/azapfy-bot (o container do brain tem o código e o modelo)
docker compose run --rm -T brain python -m src.rag.sync --fonte api --dry-run  # só o plano
docker compose run --rm -T brain python -m src.rag.sync --fonte api            # seed de fato
# (dev/local: python -m src.rag.sync --fonte local usa os docs/*.md)
```

Saída esperada: `Sync concluído: N novos, ... 0 falhas` e exit 0. Rodar de
novo em seguida tem que dar `N inalterados` em ~1 s (idempotência).

**Embeddings (2026-09-04):** o índice pgvector de produção usa
`openai/text-embedding-3-small` pelo `/embeddings` do OpenRouter
(`EMBEDDINGS_PROVIDER=openrouter` no `prod.env.tpl`; 1536 dims; a base
inteira custa < 1 centavo para reindexar). O deploy roda o sync com
`--reconstruir-se-modelo-mudou`: trocar o modelo no template + push refaz o
índice no novo espaço vetorial. O cron diário segue estrito (aborta se o
modelo divergir). O Chroma embutido continua local (`all-MiniLM-L6-v2`).

## Fase 2 — Validação do índice (sem LLM)

A paridade com o Chroma (`python -m src.rag.verificar`) deixou de ser o
critério: a base do AzapDocs é maior e diferente dos 4 `docs/*.md` (14 docs
em 2026-09-04), e os rótulos `source` são títulos, não nomes de arquivo. O
teste passa a ser consultar o índice diretamente, pelo mesmo retriever que o
agente usa, sem gastar LLM:

```bash
# na VPS, em /opt/azapfy-bot
docker compose exec -T brain python -m src.rag.consultar "como funciona o aplicativo do motorista?"
docker compose exec -T brain python -m src.rag.consultar "quais documentos o transporte exige?" --k 5
```

Critérios: `backend=pgvector`, fontes plausíveis para a pergunta, trechos com
o assunto certo. Contagens do índice:
`docker compose exec -T pgvector psql -h 127.0.0.1 -U rag -d rag -c "select titulo, deleted, chunk_count from rag_documentos order by titulo"`.

## Fase 3 — Virada (FEITA em 2026-09-04) e critérios de sucesso em produção

`VECTOR_BACKEND=pgvector` está no `deploy/env/prod.env.tpl`: todo deploy sobe
o brain lendo o pgvector. O Chroma baked na imagem continua existindo só como
rollback.

Validar, nessa ordem:
1. `GET /health` ok e log `rag_warmup_ok` no startup (o warm-up já exercita o
   caminho pgvector inteiro: embedding + consulta SQL);
2. smoke E2E real: mandar 2–3 perguntas de produto pelo WhatsApp de
   homologação e conferir resposta com citação válida;
3. logs `rag_resultado total=3 fontes=[...]` com fontes plausíveis (e nenhum
   `rag_falhou`) — em DEBUG, conferir a query;
4. Langfuse: latência do turno sem degradação (a consulta pg é local, deve
   ficar igual ou melhor);
5. deixar 1 dia de conversas reais e revisar `rag_falhou`/`validar_citacoes`.

**Rollback** (qualquer sinal ruim): `VECTOR_BACKEND=chroma` + restart. O
Chroma não foi tocado. Investigar com calma, sem pressão de produção.

## Fase 4 — Rotina diária + alerta

**Instalada pelo ansible** (role `base`, converge a cada deploy): o script
`/usr/local/bin/azapfy-sync-docs` (fonte em
`infra/ansible/roles/base/files/`) roda `docker compose run --rm -T brain
python -m src.rag.sync --fonte api` como o usuário `deploy`, com dois crons:

| quando (UTC → Brasília) | comando | papel |
|---|---|---|
| todo dia 06:30 → 03:30 | `azapfy-sync-docs` | incremental (tombstone = remoção) |
| domingo 07:00 → 04:00 | `azapfy-sync-docs --full` | reconciliação completa (§6.4 do Contrato B) |

- Log no journal da VM: `journalctl -t azapfy-sync`. Exit != 0 = ciclo com
  falha/trava → POST JSON no `SYNC_ALERTA_WEBHOOK` do `.env`, se definido.
  **Falha nunca degrada o índice** (o de ontem continua servindo), mas falha
  silenciosa por dias = base defasada — configure o webhook.
- `TravaRemocaoMassa` no log = remoções acima do teto; conferir com o dono da
  base e, se legítimo, rodar uma vez à mão:
  `azapfy-sync-docs --permitir-remocao-em-massa`.
- Rodar à mão a qualquer momento: `sudo -u deploy azapfy-sync-docs --dry-run`.

## Fase 5 — AzapDocs: o que a implementação faz de diferente do texto do contrato

- **Autenticação**: chave `azk_…` no header `X-API-Key` (a API também aceita
  `Authorization: Bearer`, a forma do contrato). Gerada no AzapDocs com os
  escopos `DOCS_LIST` (inventário) e `DOCS_READ` (conteúdo).
- **Caminhos**: `DOCS_API_BASE_URL` é a URL da COLEÇÃO. Inventário =
  `GET {base}` (`limit` 1–500, `cursor` opaco), documento = `GET {base}/{id}`,
  diagnóstico = `GET {base}/me`.
- **`410 Gone`** num documento = virou tombstone entre a listagem e a leitura:
  conta como falha do doc no ciclo (exit 1, alerta) e a remoção vem pela
  listagem do ciclo seguinte. `404` = id fora dos espaços da chave.
- **Preflight `/me`** antes de cada ciclo: a resposta diz qual das três
  condições caiu (chave, `apiHabilitada` do espaço, `donoTemAcesso`).

Os cenários 1–11 da §5 do contrato têm teste automatizado espelhado em
`tests/test_sync.py` (mesma numeração) — divergência de comportamento da API
na homologação se verifica reproduzindo o cenário no teste.

## Aposentadoria do Chroma (depois de ≥1 semana estável)

Remover `CHROMA_PERSIST_DIR` do deploy e o diretório `chroma_db/` — mas só
depois do período de observação; até lá ele é o plano B de graça.
