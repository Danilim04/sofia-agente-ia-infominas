# Ambiente de dev (E3.4)

Como subir a Sofia inteira na sua máquina, sem Infodesk nem WhatsApp reais.
Para rodar os testes e a esteira, veja a skill `ambiente-testes`.

```
mock-infodesk :9000 ──webhook──► gateway Go :8080 ──POST /chat──► cérebro :8001
       ▲                              │                              │
       └──── API v1 do Infodesk ◄─────┘◄──── /tools/infodesk/* ◄──────┘
Chainlit (agente-ia/app.py): fala com o cérebro direto, no Contrato A
```

## Subir

```bash
cp scripts/dev.env.example scripts/dev.env   # opcional: ajustes locais (fora do git)
scripts/start-all.sh                         # mock → cérebro → gateway, espera cada porta
scripts/start-all.sh --chainlit              # + Chainlit em http://localhost:8000
scripts/stop-all.sh                          # sobrou algo nas portas? (--chainlit inclui :8000)
```

O script lê `scripts/dev.env` (ou, sem ele, `scripts/dev.env.example`), força a
cola entre os três serviços (chave, segredo do webhook, `TOOLS_API_TOKEN` ↔
`INFODESK_TOOLS_TOKEN`), espera `/health` de cada um, confere no log do gateway
que a taxonomia carregou sem problemas e imprime onde abrir cada coisa. O
ambiente do shell vence o arquivo (`MOCK_PORT=9100 scripts/start-all.sh`). Logs
em `scripts/logs/{mock,cerebro,gateway,chainlit}.log`.

**Segredos ficam fora do git.** `dev.env.example` só tem valores de dev que
casam os serviços entre si. A `OPENROUTER_API_KEY` (e `TYPESAFE_API_KEY`,
Langfuse) vai em `agente-ia/.env` ou no ambiente do shell; `scripts/dev.env` e
`scripts/logs/` são ignorados (`scripts/.gitignore`). Sem a chave o cérebro
sobe e responde `/health`, mas qualquer turno que chame o LLM falha. Não há
LLM/JEV falsos na stack (`LLM_FAKE`/`JEV_FAKE` entram com o E2E, E4.1).

## Onde abrir

| O quê | Onde |
|---|---|
| Cliente + atendente simulados (WhatsApp/Infodesk) | <http://localhost:9000> — escreva como o cliente; "Atendente assume" e "Encerrar conversa" fazem o papel humano |
| Cliente simulado no Contrato A, com `classificacao` e `acoes` | `scripts/start-all.sh --chainlit` → <http://localhost:8000> |
| Cérebro | <http://localhost:8001/health> · `/docs` |
| Gateway | <http://localhost:8080/health> · webhook em `/webhook` |

## Taxonomia de dev

O placeholder versionado (`agente-ia/config/taxonomia.yaml`) usa mesas que o
mock não tem (só "Financeiro" coincide), então contra o mock quase toda
categoria dava `categoria_sem_mapa`. `scripts/dev/taxonomia.dev.yaml` tem as
**mesmas categorias**, mas com `mesa` e `departamento` que existem em
`mock-infodesk/dados.json`; o `dev.env.example` aponta `TAXONOMIA_PATH` para ela
(cérebro e gateway leem o mesmo arquivo) e liga `STRICT_TAXONOMIA=true`, então
o gateway nem sobe se o mock mudar e a taxonomia deixar de casar.

**É só para dev e não é o mapeamento real da Infominas** (pendência P1): as
escolhas ("Comercial → Solicitações" etc.) são conveniência do mock. O
mapeamento de verdade vem do export do Infodesk e substitui o placeholder
versionado, não este arquivo. O teste `agente-ia/tests/test_app.py` garante que
toda mesa e todo departamento do arquivo de dev existem em `dados.json` e que
as categorias seguem iguais às do placeholder.

## Chainlit (`agente-ia/app.py`)

Simula o gateway falando com o cérebro, no mesmo processo
(`server.processar_chat`), com o `ChatRequest` do Contrato A completo:

1. Pede **telefone** (use `5531990000100` = Maria Souza do mock) e **nome**.
2. Pergunta se o contato chega **identificado** (`contato.forma = "telefone"`,
   como o lookup do gateway manda) ou **não identificado** (`contato = null`).
3. Cada mensagem vai ao cérebro com `conversation_id` novo por sessão e
   `estado_conversa = "bot"`; a resposta chega em uma mensagem por bolha (`---`).
4. Um passo "Contrato A" mostra `classificacao`, `acoes`, `fontes` e `erro` do
   turno. O painel mascara o telefone (`****0100`).
5. Uma ação `transferir` silencia a Sofia (como o gateway); `/encerrar` simula
   `conversation.finished` e `/trocar-telefone` simula outro cliente.

As tools do Infodesk falam com o gateway; por isso suba a stack antes (ou use
`--chainlit`, que já passa `INFODESK_TOOLS_*`).

**Limite:** o `conversation_id` do Chainlit não existe no store do gateway, e
`consultar_chamado`/`listar_chamados` exigem a conversa lá. O Chainlit serve
para classificação, conversa, `acoes` e preparar/abrir chamado; o fluxo
completo de tools (posse de ticket) só vale pela UI do mock em `:9000`. O
`erro` aparece no painel, mas o gateway repetiria a falha transitória e
transferiria na definitiva — isso o Chainlit não simula. Requer `chainlit` 2.x
(`cl.Action(payload=…)`); o `agente-ia/requirements.txt` ainda aceita 1.x —
ajuste fora desta etapa.

## Windows

Rode no **Git Bash**. O script testa cada Python de verdade (o alias da Store
quebra), prefere `agente-ia/venv`, converte caminhos para o formato nativo e
compila o gateway para `scripts/.bin/` (o pid vira o do próprio gateway); como
no Git Bash não há `setsid`, ao sair ele chama `stop-all.sh` para limpar o que
sobrar nas portas.
