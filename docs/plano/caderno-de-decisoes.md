# Caderno de decisões para a homologação

Estado em 2026-10-22, depois da rodada final de desenvolvimento (Ondas 0 a 4
e as etapas de código da Onda 5). O código está pronto para testar com
pessoas. O que falta é decisão do dono do produto ou ação que só ele pode
fazer. Este caderno lista tudo, na ordem em que precisa ser resolvido.

Cada item traz a pergunta, as opções, a recomendação do orquestrador, o que
ela trava e onde a resposta entra. Depois de decidir, a resposta vira valor
de configuração ou um PR curto; nenhuma exige desenvolvimento novo, salvo
onde está escrito.

Documentos de apoio: [ADR 0008](../decisoes/0008-revisao-seguranca-v1.md)
(segurança), [ADR 0009](../decisoes/0009-cadastro-de-solicitante-pela-sofia.md)
(cadastro), [ADR 0010](../decisoes/0010-mesa-unica-e-conversa-aberta.md) (mesa
única), [taxonomia proposta](decisoes/taxonomia-proposta.md),
[`docs/infodesk/10` §8](../infodesk/10-regras-do-codigo.md),
[`deploy/README.md`](../../deploy/README.md) e o roteiro de testes em
[`homologacao.md`](homologacao.md).

## Já decidido (não precisa voltar)

| Decisão | Data | Onde está |
|---|---|---|
| Todo chamado da Sofia nasce na mesa "Abertura de Ticket" | 2026-10-22 | ADR 0010 |
| A conversa fica aberta depois do ticket (`TICKET_FINISH_CONVERSA=false`) | 2026-10-22 | ADR 0010 |
| Telefone desconhecido é cadastrado como solicitante, em duas fases | 2026-10-22 | ADR 0009 |
| Comercial e Financeiro recebem por departamento, sem pessoa | 2026-10-22 | ADR 0010 |
| Merge dos PRs pelo orquestrador até a Onda 5 | 2026-10-21 | `04-esteira-qualidade.md` |

## A. Antes do primeiro teste com pessoas no Infodesk real

Sem estes, o piloto não começa.

### A1. Quem participa do teste

O Infodesk não tem homologação, e a flag "Conduz o atendimento" vale para a
organização inteira. A lista de testadores do gateway é o que impede a Sofia
de falar com clientes reais.

| Opção | Efeito |
|---|---|
| **Lista de testadores (recomendada)** | só os números da lista falam com a Sofia; os demais recebem handoff imediato, sem mensagem, e caem na fila como hoje |
| Modo aberto | todos os clientes falam com a Sofia; só para depois do piloto |

**Decidir:** os números E.164 dos testadores (pessoas da equipe e clientes
parceiros que aceitaram testar).
**Onde:** `TESTADORES_TELEFONES` no `.env`. O gateway não sobe com a lista
vazia, salvo `SOFIA_PRODUCAO_ABERTA=true` explícito.

### A2. Onde a Sofia roda (P10)

| Pergunta | Recomendação |
|---|---|
| Host | um servidor Linux com Docker, 4 GB de RAM livre e 6 GB de disco |
| Domínio HTTPS público para o webhook | um subdomínio da Infominas, por exemplo `sofia.infominas.net`, com só `/webhook` exposto |
| Quem termina o HTTPS | o proxy reverso que a Infominas já usa |
| Onde ficam os segredos | `.env` do host com `chmod 600`, ou o cofre que a equipe já usa |
| Backup do Postgres | diário, com o volume cifrado (ADR 0008 aceita o telefone em claro no banco do gateway só com isso) |
| Quem opera e recebe alertas | uma pessoa nomeada e um canal em `ALERTA_WEBHOOK_URL` |

**Trava:** o deploy. **Onde:** `deploy/README.md` e `infra/README.md`.

### A3. Chaves e segredos

| Item | Obrigatório | Onde |
|---|---|---|
| `OPENROUTER_API_KEY` | sim | `.env` do host e `agente-ia/.env` para testar local |
| `INFODESK_API_KEY` e `INFODESK_WEBHOOK_SECRET` | sim | chave da Sofia no painel do Desk (A4) |
| `TOOLS_API_TOKEN`, `PGVECTOR_PASSWORD`, `IDENT_HASH_SAL` | sim | gerar com `openssl rand -hex 32` |
| `TYPESAFE_API_KEY` (JEV, P8) | não | sem ela a classificação usa o LLM de reserva; com ela os limiares podem ser calibrados (E5.2) |
| Secrets do GitHub `OPENROUTER_API_KEY` e `TYPESAFE_API_KEY` | não | só para os evals noturnos rodarem de verdade |

### A4. Chave da Sofia no Desk

Hoje existe a chave `sofia-agente-ia` com escopo `*` e **sem** a flag de bot
externo. Para o piloto:

1. Ligar **Conduz o atendimento** só no dia do piloto, com a lista de A1 já
   configurada.
2. Nome da chave: "Sofia" (é o autor das mensagens no inbox).
3. URL do webhook e eventos: `deploy/README.md`, seção "Cadastrar o webhook".
4. Escopo: o `*` atual serve; se quiser o mínimo, inclua `contacts:write`
   por causa do cadastro (ADR 0009).

### A5. Departamentos do handoff (P11)

| Pergunta | Recomendação |
|---|---|
| Departamento padrão (`DEPARTAMENTO_HUMANO_PADRAO`) | `Técnicos` |
| Cada departamento tem atendentes ativos? | conferir em *Grupos de serviço*: handoff para departamento sem membros fica na fila sem notificar ninguém |
| Departamento por categoria | a coluna "Departamento proposto" da taxonomia proposta: as áreas técnicas em `Técnicos`, `projetos` e `consultoria` em `Comercial`; boleto/cobrança não têm área no catálogo e vão ao padrão (ver A6, Q6) |

### A6. Taxonomia real (P1, E5.1)

As perguntas da [taxonomia proposta](decisoes/taxonomia-proposta.md). Q1 já está decidida: as categorias são as 17 áreas do catálogo do Infodesk (decisão de 2026-10-22).

| Pergunta | Recomendação |
|---|---|
| Q1. Categorias | **decidida**: as 17 áreas do catálogo de "Abertura de Ticket" + `indefinida` |
| Q2. Departamento por categoria e padrão | ver A5 |
| Q3. A mesa "Abertura de Ticket" só tem prioridades `Alta` e `Baixa` | `media` sem `priorityId` (a triagem escolhe); `urgente` como `Alta`; sem piso por categoria |
| Q4. Item de catálogo no ticket | omitir; a triagem humana escolhe |
| Q6. O catálogo não tem área financeira nem comercial de planos: boleto, cobrança e preço caem em `indefinida` e vão ao departamento padrão | seguir só o catálogo; se o volume financeiro justificar, pedir à Infominas uma área financeira no catálogo |

**Depois de decidir:** um PR promove `taxonomia.proposta.yaml` a
`taxonomia.yaml`, remapeia os testes e os 21 cenários reais parados em
`evals/conversa/pendentes/` (2 precisam de reescrita pela Q6 e 2 dependem da E5.3), e liga `STRICT_TAXONOMIA=true`. É a única
decisão que gera um PR de tamanho médio.

## B. Comportamento da Sofia (pode ajustar durante o piloto)

| # | Pergunta | Opções | Recomendação | Onde |
|---|---|---|---|---|
| B1 | Telefone já cadastrado no Infodesk dispensa CPF? | sim / não | **sim**: o toggle de campos obrigatórios está desligado e o lookup já traz o solicitante | `IDENT_EXIGIR_CPF` |
| B2 | Empresa não encontrada no cadastro | transferir para humano / só orientar | **transferir** | `CADASTRO_SEM_CLIENTE`, `CADASTRO_MAX_TENTATIVAS` |
| B3 | Horário humano do WhatsApp e feriados (P7) | o expediente das mesas (seg–sex 08:00–18:00) ou outro | o expediente das mesas, com a lista de feriados da Infominas | `HORARIO_HUMANO`, `HORARIO_FUSO`, `FERIADOS`, `FORA_DO_HORARIO_MODO` |
| B4 | Base de conhecimento (P6) | documentos reais / manter os 3 placeholders | **3 a 5 documentos reais** antes do piloto (serviços, horário, canais); os placeholders são fictícios | `agente-ia/docs/base/` ou `DOCS_API_*` |
| B5 | Palavras que valem como confirmação | manter a lista herdada / tirar "claro", "certo", "por favor" | **tirar os três**: confirmam por educação, não por decisão | código, `CONFIRMACOES_EXPLICITAS` em `tool_policy.py` (PR curto) |
| B6 | Prazos do turno | 70 s cérebro, 80 s chamada, 105 s teto | **aprovar**; pior caso perto de 100 s | `BRAIN_TURNO_PRAZO`, `BRAIN_TIMEOUT`, `TURNO_TETO` |
| B7 | A Sofia fecha chamado? | não / sim quando o cliente disser que resolveu | **não na v1**: o Desk exige checklist da mesa | nenhum |
| B8 | Transferir para humano logo depois do ticket | sim / não | **sim**, como está | `TRANSFERIR_APOS_TICKET` |

## C. Ações que só você pode fazer

| # | Ação | Por quê |
|---|---|---|
| C1 | Adicionar `"Bash(git rm *)"` à lista `allow` de `.claude/settings.local.json` | a limpeza final precisa remover 6 caminhos legados do Zapin em `deploy/` e `infra/`; um deles, `deploy/env`, não exige os segredos novos e faria o gateway reiniciar em loop |
| C2 | Trocar `BRAIN_TURNO_PRAZO=40` para 70, ou apagar a linha, no seu `agente-ia/.env` | o valor antigo é menor que o prazo das tools e pode impedir o cérebro de subir |
| C3 | Ligar o serviço do Docker nesta máquina uma vez | o build das imagens nunca foi testado localmente |
| C4 | Variável de repositório `E2E_ESTRITO=1` no GitHub | o E2E passa a falhar se o número do chamado deixar de chegar ao cliente |
| C5 | Na Onda 5, voltar a fazer você mesmo o merge dos PRs | a autorização ao orquestrador vale só até a homologação |

## D. Decisões que o orquestrador tomou (para você vetar se quiser)

| Decisão | Motivo | Onde |
|---|---|---|
| Telefone em claro no banco do gateway | é a chave da conversa, da posse de ticket e da lista de testadores | ADR 0008 |
| Número do chamado enviado por mensagem fixa do gateway | não depende do texto do LLM nem do turno terminar | ADR 0008 |
| Fail-closed entre componentes | segredo ausente impede o boot | ADR 0008 |
| Pedidos de boleto e preço também recebem categoria | chegam ao departamento certo; custo: mais consultas ao LLM de reserva sem o JEV | E3.10, `02-classificacao.md` |
| Retry do `/chat` pode pedir nova confirmação ao cliente | o sentido da falha é seguro: não abre chamado | ADR 0008, M6 |

## E. Pendências técnicas conhecidas

Não dependem de decisão de produto, mas precisam ser tratadas antes da
produção aberta.

| Item | Estado | O que fazer |
|---|---|---|
| Dependências Python com vulnerabilidade | o `pip-audit` do CI acha 12 pacotes afetados no `requirements-prod.lock`, e nenhum tem correção dentro das faixas atuais; o job só avisa | uma etapa que sobe as faixas, regenera o lock e roda a suíte inteira; risco de quebra em LangChain e LangGraph |
| Calibração dos limiares do JEV (E5.2) | sem chave do JEV e sem histórico rotulado | depende de A3 (chave) e do export do histórico (P9) |
| Validação do reconciliador em produção (E5.4) | só testado contra o mock | 1 hora de piloto sem turno duplicado nem perdido, conferido no checklist de `homologacao.md` |
| Gateway fora do ar com a flag ligada | o Desk não devolve à fila conversas do bot sem resposta; o vigia só repara depois que o gateway volta | monitorar o `/health` e desligar a flag do Desk se o gateway cair |

## Ordem sugerida

1. C1, C2 e C3, que levam minutos.
2. A6 com A5, numa conversa só: gera o PR da taxonomia.
3. A2 e A3: infraestrutura e chaves.
4. B1 a B5: valores de configuração.
5. Teste local com os casos reais (`homologacao.md`, parte 1).
6. A1 e A4 no dia do piloto (`homologacao.md`, parte 2).
