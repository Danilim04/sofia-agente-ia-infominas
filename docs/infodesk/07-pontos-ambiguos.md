# 07 — Pontos ambíguos e perguntas abertas à Infominas

Lista consolidada do que a documentação (5 páginas) **não responde** e que
impacta a implementação. Cada item tem: o que falta, por que importa, e a
**hipótese de trabalho** que o plano adota enquanto não há resposta (sempre a
mais conservadora). Agentes implementando **não** devem "resolver" um item
daqui assumindo um valor — devem codificar a hipótese de forma configurável e
deixar o item referenciado no código (`// ver docs/infodesk/07 item X`).

Estado: `ABERTO` = sem resposta; `RESPONDIDO` = atualizar o doc da área e
marcar aqui com a data.

## A — Acesso e ambiente

| # | Pergunta | Impacto | Hipótese de trabalho | Estado |
|---|---|---|---|---|
| A1 | Existe **ambiente de homologação/sandbox** (base URL + chave `ik_test_`?) ou só produção? | Sem sandbox, todos os testes de integração batem em produção (tickets reais, mensagens reais para WhatsApp real) | Testes de integração só contra um **mock local do Infodesk** (como o `mock-chatwoot/` do Zapin); smoke em produção com um número de telefone de teste da Infominas | ABERTO |
| A2 | Quem configura `webhookUrl`, `webhookEvents` e `webhookSecret` na chave? Há painel? Podemos ter chaves diferentes por ambiente? | Define o runbook de deploy | Pedido manual à Infominas por ambiente | ABERTO |
| A3 | Os endpoints de referência (`/desks`, `/users`, `/departments`) exigem escopo? Qual? | Pode faltar escopo na chave | Pedir chave com os 4 escopos; tratar 403 como erro de configuração | ABERTO |
| A4 | Limites do **rate limit** (req/min) e se o 429 traz `Retry-After` | Dimensionar polling e retries | Polling de reconciliação ≤ 1 req/min; retry com backoff exponencial, teto 30 s | ABERTO |
| A5 | Existe IP de origem fixo dos webhooks (para allowlist) e a API aceita apenas HTTPS com certificado válido no nosso lado? | Infra/segurança do endpoint | Só HMAC + HTTPS público (mesmo modelo do Zapin atrás do Traefik) | ABERTO |

## T — Tickets

| # | Pergunta | Impacto | Hipótese de trabalho | Estado |
|---|---|---|---|---|
| T1 | **Schema completo da resposta** de `POST /tickets` e de `GET /tickets/:number` (campos: `id`, `number`, `status`, `deskId`, `priorityId`, `createdAt`, `updatedAt`, histórico?) e código HTTP de sucesso | Sem `status` não dá para responder "e o meu chamado?"; sem o schema não dá para tipar o cliente Go | Tipar só `number` (string ou int? **também desconhecido**) como obrigatório; o resto `map[string]any` até resposta | ABERTO |
| T2 | Valores válidos de `channel` (`WEB`, `WHATSAPP`, …?) e se é obrigatório coerir com a origem | Enviar valor inválido = 4xx | Enviar `"WHATSAPP"` para tickets vindos do WhatsApp (valor visto no webhook); configurável | ABERTO |
| T3 | Como obter `priorityId`? Não há `GET /priorities`. Quais prioridades existem e qual é a default se omitido? | Sem isso a **prioridade determinística** do JEVI não tem onde ser escrita | Omitir `priorityId` na v1 e registrar a prioridade calculada no `description` (ex.: "[Prioridade sugerida: ALTA]") até termos a tabela de UUIDs | ABERTO |
| T4 | `clientId` / `requesterId`: o que são (empresa × pessoa?), como obtê-los sem rota de busca, e o que acontece se omitidos (o Infodesk cria/associa o contato pelo `contactPhone`?) | Identificação do cliente e vínculo do ticket à pessoa certa | Omitir ambos; enviar `contactPhone`; **a identificação do cliente é problema nosso** (ver plano, decisão I1) | ABERTO |
| T5 | Não há rota para **listar tickets por contato/telefone**. Como o agente responde "já tenho um chamado aberto?" e como evita **duplicar** um ticket que um humano abriu por outro canal? | Dedup só é possível contra a nossa própria base | Dedup local: tabela `tickets` no gateway (conversa, telefone, número, categoria, data); perguntar ao cliente o número se ele disser que já tem chamado | ABERTO |
| T6 | Não há rota de **atualização/fechamento** de ticket. O agente nunca fecha? E se o cliente disser "já resolveu"? | Fluxo de encerramento | Agente só **comenta** via `/reply` ("cliente informou que resolveu") e transfere o fechamento para humano | ABERTO |
| T7 | `POST /tickets/:number/reply`: a mensagem chega ao **mesmo** WhatsApp/conversa onde o cliente está falando com o bot, ou abre outra sessão? Aparece como "atendente" com qual nome? | Risco de mensagem duplicada/confusa para o cliente | O bot **não usa `/reply`** na v1; fala sempre pela conversa (`/messages`). `/reply` fica reservado a notificação futura | ABERTO |
| T8 | Existe algum **campo de origem/externalId** (ex.: `source`, `metadata`) para carimbar que o ticket foi aberto pelo bot e amarrar ao `conversationId`? | Auditoria e dedup | Carimbar no `description` um rodapé fixo `[Aberto pelo assistente virtual · conversa <id>]` | ABERTO |
| T9 | Há limite de tamanho para `title`/`description`? | 4xx inesperado | `title` ≤ 120 chars, `description` ≤ 4000 (herdado do Zapin) | ABERTO |

## C — Conversas

| # | Pergunta | Impacto | Hipótese de trabalho | Estado |
|---|---|---|---|---|
| C1 | **Schema** de `GET /conversations` (item: `id`, `status`, `channel`, `contact`, `departmentId`, `assignedUserId`, `lastMessageAt`?), paginação e semântica de `tab` (`meus` = do dono da chave?) | O reconciliador (polling) e a detecção de "humano assumiu" dependem disso | Tipar como `[]map[string]any`; reconciliador entregue **depois** da resposta | ABERTO |
| C2 | Schema de `GET /conversations/:id` com mensagens: valores de `sender` (`CLIENT`, `USER`?, `BOT`?, `SYSTEM`?), ordenação, paginação, campos de mídia | Reconstruir contexto após perda de webhook; distinguir mensagens nossas das do atendente | Só `CLIENT` é tratado; o resto ignorado até resposta | ABERTO |
| C3 | **Como saber que um atendente humano assumiu a conversa** (o bot deve calar — regra ZD-1 do Zapin)? Há campo `assignedUserId`/`status` na conversa? O webhook `message.received` continua chegando depois do `/assign`? | **Crítico**: sem isso o bot responde por cima do humano | Gateway marca localmente a conversa como `transferida` ao chamar `/department` ou `/assign` e **ignora** webhooks dela até `conversation.finished`; humano que assume sem o bot transferir **não é detectado** (risco aceito e documentado) | ABERTO |
| C4 | "Transferir para fila/setor" na Infominas é `/department` (departamento) ou `/assign` (pessoa)? Existe conceito de fila? O `/assign` exige que o atendente esteja online? | Define a tool de roteamento | Roteamento por **departamento**; `/assign` não usado na v1 | ABERTO |
| C5 | `POST /finish`: dispara `conversation.finished` para o próprio parceiro? Nova mensagem do contato depois reabre a mesma conversa (mesmo `conversationId`) ou cria outra? | Loop de eventos; ciclo de vida do estado local | Tratar `conversation.finished` sempre como idempotente; estado local chaveado por `conversationId` com TTL | ABERTO |
| C6 | O bot deve **encerrar** a conversa depois de abrir o ticket, ou deixá-la aberta para o humano? | Regra de negócio | Não encerra na v1; só transfere | ABERTO |
| C7 | Limite de tamanho do `body` em `/messages`; suporte a quebra de linha, negrito `*x*`, emojis | Formatação WhatsApp | 900 chars por mensagem, bolhas separadas por `---` como no Zapin; negrito conforme o WhatsApp (`*x*`) — **confirmar** se o Infodesk reescreve markdown | ABERTO |
| C8 | Mensagens do bot via `/messages` aparecem no painel do Infodesk com qual autor? Dá para identificar como "assistente virtual"? | UX do atendente humano ao assumir | Prefixo nenhum; pedir à Infominas um usuário/nome "Assistente virtual" na chave | ABERTO |

## W — Webhooks

| # | Pergunta | Impacto | Hipótese de trabalho | Estado |
|---|---|---|---|---|
| W1 | Payload de `conversation.started` e `conversation.finished` | Saudação, fim de episódio | Assumir `data.conversationId` (+ `contact`); ignorar o resto | ABERTO |
| W2 | `message.received` dispara também para mensagens que **o atendente humano** envia? E para mensagens enviadas via API (`/messages`)? | Loop bot↔bot | Processar só `sender == "CLIENT"`; qualquer outro valor é ignorado e logado | ABERTO |
| W3 | Mensagens de **mídia** (áudio, imagem, PDF): chegam no webhook? Com quais campos? `body` vazio? | O Zapin transfere para humano ao receber anexo (ZD-2) | `body` vazio ou ausente → tratar como mídia → transferir para humano com aviso | ABERTO |
| W4 | Formato exato do `id` de entrega e do `message.id` (UUID? estáveis em reentrega manual?) | Dedup | Strings opacas, dedup por 7 dias (como o Zapin) | ABERTO |
| W5 | O `organizationId` é fixo por chave? Qual é o nosso? | Validação multi-tenant | Configurável (`INFODESK_ORGANIZATION_ID`); vazio = não valida (com warning no boot) | ABERTO |
| W6 | O `webhookSecret` é o mesmo da chave de API ou um segredo separado? Pode ser rotacionado sem trocar a chave? | Runbook de rotação | Dois segredos separados na config (`INFODESK_API_KEY`, `INFODESK_WEBHOOK_SECRET`) | ABERTO |

## D — Dados de referência e roteamento

| # | Pergunta | Impacto | Hipótese de trabalho | Estado |
|---|---|---|---|---|
| D1 | Qual é a relação **mesa (desk) × departamento**? O ticket vai para a mesa; a conversa, para o departamento. Para cada categoria de problema, qual mesa **e** qual departamento? | É a **tabela de roteamento** inteira do agente | Tabela de config `roteamento.yaml`: `categoria → {deskName, departmentName, prioridade}`; validada no boot contra `/desks` e `/departments` | ABERTO (depende de regra de negócio) |
| D2 | A lista de mesas/departamentos muda com frequência? Precisa de cache invalidável? | TTL do cache | TTL 10 min + reload no boot | ABERTO |

## Como usar este arquivo

- Perguntas para a Infominas: enviar as colunas "Pergunta" dos blocos A, T, C,
  W, D de uma vez (um e-mail/reunião), pedindo exemplos de JSON reais
  (`POST /tickets` resposta, `GET /conversations`, `GET /conversations/:id`,
  payloads dos 3 eventos).
- Ao receber resposta: atualizar o doc da área (01–06), marcar `RESPONDIDO
  (AAAA-MM-DD)` aqui e abrir/ajustar a etapa correspondente em
  `docs/plano/03-etapas.md`.
