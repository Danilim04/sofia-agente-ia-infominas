# 01 — Catálogo de ferramentas (tools)

Convenções herdadas do Zapin e mantidas:

- **Tool do agente** = função Python `@tool` em `agente-ia/src/tools/infodesk_tools.py`
  cuja docstring é a descrição enviada ao LLM. Ela **nunca fala com o
  Infodesk**: chama o gateway Go em `POST /tools/infodesk/<área>/<ação>` com o
  header `X-Tools-Token`. O gateway resolve identidade/escopo, aplica
  idempotência e chama a API do Infodesk.
- **Argumentos de sessão** (`telefone`, `conversation_id`, `classificacao`,
  `proposta`, `identificacao`) são `InjectedToolArg` + entrada em `POLITICAS`
  (`src/agent/tool_policy.py`): fora do schema do modelo, preenchidos do estado.
- **Convenção de resposta do gateway**: erro de negócio é HTTP **200** com
  `{"status": false, "erro": "<texto pt-BR para o LLM explicar>", "motivo":
  "<codigo>"}`; sucesso é `{"status": true, ...}`. HTTP 401/400/405 só para
  erro de protocolo (token, JSON, método). O LLM nunca vê mensagem crua do
  Infodesk.
- **Efeito colateral** (criar ticket, transferir) é sempre **duas fases**
  (preparar → confirmação explícita no turno seguinte → executar com a
  proposta injetada) ou **decisão de código** (transferência por mídia/falha).
- Exemplos JSON de cada endpoint vivem em `contratos/tools-infodesk/*.json` e
  são carregados nos testes dos **dois** lados (contrato Go ↔ Python).

Legenda de idempotência: **I-natural** (GET), **I-local** (gateway garante
por chave própria), **I-nenhuma** (chamada só de leitura de cache).

---

## A. Tools expostas ao LLM (Python → gateway → Infodesk)

### A1. `informar_identificacao`

| Item | Valor |
|---|---|
| Propósito | Registrar o dado cadastral que o cliente informou (CPF **ou** nº de contrato) para identificar quem abre o ticket. Obrigatório antes de abrir ticket (D5) |
| Args do LLM | `cpf: str = ""`, `contrato: str = ""` (pelo menos um) |
| Injetados | `telefone`, `conversation_id` |
| Validação (política, Python) | CPF: 11 dígitos + dígitos verificadores válidos (`src/identity/cpf.py`); contrato: regex configurável `IDENT_CONTRATO_RE` (default `^\d{4,12}$`). Inválido → recusa `{"erro": "CPF inválido; peça para o cliente conferir"}` sem chamar o gateway |
| Gateway | `POST /tools/infodesk/contato/identificar` `{telefone, conversation_id, cpf?, contrato?}` → grava em `contatos` (telefone → dado, `expires_at = now + IDENT_TTL` 24 h). Se houver rota de busca de cliente no Infodesk (P2), o gateway consulta e devolve `cliente_id`/`nome_cadastro`; sem rota, `cliente_id = null` |
| Infodesk | nenhum (v1). Futuro: `GET /clients?...` (P2) |
| Retorno | `{status: true, identificado: true, forma: "cpf"\|"contrato", cliente_id: null\|str, nome_cadastro: null\|str}` |
| Erros | `campo_invalido`, `nao_identificado` (nenhum campo) |
| Idempotência | I-local (upsert por telefone) |
| Log | CPF mascarado (`***.***.789-**`) em INFO; nunca em claro |
| Dependências | tabela `contatos` (store); `IDENT_TTL` |

### A2. `preparar_abertura_chamado`

| Item | Valor |
|---|---|
| Propósito | Dry-run: valida e devolve a proposta canônica do ticket. Não cria nada |
| Args do LLM | `titulo: str` (1 linha, ≤120), `descricao: str` (≤4000, relato do cliente em 3ª pessoa + fatos coletados: desde quando, já reiniciou, endereço se citado) |
| Injetados | `telefone`, `conversation_id`, `classificacao` (do estado: `{categoria, prioridade, confianca, origem: jev\|llm\|esclarecimento}`), `identificacao` (`{cpf\|contrato}`) |
| Precondição (`exigir`) | identificação presente no estado (senão recusa `"peça o CPF ou o número do contrato antes"`); classificação com `categoria != None` (senão recusa `"faça uma pergunta de esclarecimento antes"`); não há protocolo citado na última mensagem (regra ZB do Zapin) |
| Gateway | `POST /tools/infodesk/ticket/preparar` `{telefone, conversation_id, titulo, descricao, categoria, prioridade, identificacao}` → sanitiza (controle, tamanho), resolve `categoria → {deskId, departmentId, priorityId?}` pela tabela (`taxonomia.yaml` carregada no boot e validada contra `/desks` e `/departments`), **dedup local**: se `tickets` tem ticket `aberto` da mesma conversa OU do mesmo telefone + mesma categoria nas últimas `TICKET_DEDUP_JANELA` (24 h) → `motivo: "chamado_aberto"` com `chamados_abertos: [{numero, titulo, categoria, aberto_em}]` |
| Infodesk | nenhum (usa cache de `/desks`, `/departments`) |
| Retorno | `{status: true, proposta: {titulo, descricao, categoria, mesa: {id, nome}, departamento: {id, nome}, prioridade: {nome, id\|null}, identificacao_mascarada, rodape}}` — `rodape` é o texto fixo anexado à descrição (`[Aberto pela Sofia · conversa <id> · CPF ***.***.789-**]`) |
| Erros | `campo_invalido`, `categoria_sem_mapa` (categoria não está na tabela → alerta ao responsável + sugere transferir), `chamado_aberto`, `nao_identificado`, `referencia_indisponivel` (cache de mesas vazio) |
| Idempotência | I-nenhuma (puro) |
| Efeito no estado | `tools_node` carimba `proposta_chamado = {proposta, criada_em, turno}` (validade 15 min, como no Zapin) |

### A3. `abrir_chamado`

| Item | Valor |
|---|---|
| Propósito | Cria o ticket **exatamente** como preparado e confirmado |
| Args do LLM | **nenhum** |
| Injetados | `proposta` (do estado), `telefone`, `conversation_id` |
| Precondição | `_exigir_proposta_confirmada` do Zapin: proposta existe, dentro de 15 min, turno = turno da proposta + 1, última mensagem humana é confirmação explícita (`CONFIRMACOES_EXPLICITAS`, ajustada à persona: inclui "sim sofia", "pode abrir", "confirmo") |
| Gateway | `POST /tools/infodesk/ticket/abrir` `{telefone, conversation_id, proposta, chave_idempotencia}` onde `chave_idempotencia = sha256(conversation_id + proposta.titulo + proposta.categoria + proposta.criada_em)` calculada no **Python** a partir da proposta carimbada. Gateway: (1) `INSERT tickets(chave, conversation_id, telefone, categoria, status='pendente')` — conflito na chave → devolve o ticket já existente (`numero` se houver, ou `motivo: "em_andamento"` se ainda pendente); (2) `POST /tickets` ao Infodesk com `{deskId, title, description + rodape, channel: "WHATSAPP", contactPhone, priorityId?}`; (3) `UPDATE tickets SET numero, status='aberto', raw_resposta`; (4) `POST /conversations/:id/department {departmentId}` (best-effort; falha vira alerta, não desfaz o ticket); (5) marca conversa `transferida` se `TRANSFERIR_APOS_TICKET=true` (default **true**, revisar com P11) |
| Infodesk | `POST /integration/v1/tickets`; `POST /integration/v1/conversations/:id/department` |
| Retorno | `{status: true, numero: "<number>", mesa, departamento, prioridade, transferido: bool}` |
| Erros | `sem_proposta`, `em_andamento` (retry concorrente), `infodesk_indisponivel` (5xx/timeout **sem** ter recebido `number` → ticket fica `pendente`; reconciliador tenta descobrir se foi criado — **impossível sem listagem (P3)**; então: alerta ao responsável + mensagem ao cliente "não consegui registrar agora, um atendente vai assumir" + transferência), `escopo_insuficiente` (403 → alerta de configuração) |
| Idempotência | **I-local** por `chave_idempotencia` (unique index). Retry de rede após `POST` sem resposta **não** repete o POST (status `pendente` bloqueia) |
| Log | `ticket_aberto numero=… conversa=… categoria=… mesa=…` (sem CPF/telefone) |

### A4. `consultar_status_chamado`

| Item | Valor |
|---|---|
| Propósito | Responder "como está meu chamado nº X?" |
| Args do LLM | `numero: str` (dígitos) |
| Injetados | `telefone`, `conversation_id`, `identificacao` |
| Validação | `numero` só dígitos (1–10) |
| Gateway | `POST /tools/infodesk/ticket/consultar` `{telefone, conversation_id, numero, identificacao}` → **posse**: (a) se `tickets` local tem `numero` com o mesmo telefone → ok; (b) senão, se o schema do Infodesk expuser `contactPhone`/`requester` (P3) → compara; (c) senão → `motivo: "posse_nao_verificada"` (o LLM diz que não consegue confirmar por aqui e oferece atendente). Com posse: `GET /tickets/:number` |
| Infodesk | `GET /integration/v1/tickets/:number` |
| Retorno | `{status: true, numero, status_ticket: "<string do Infodesk ou 'desconhecido'>", titulo, aberto_em, atualizado_em, mesa}` — campos desconhecidos até P3 são `null` |
| Erros | `nao_encontrado` (404), `posse_nao_verificada`, `infodesk_indisponivel` |
| Idempotência | I-natural |

### A5. `listar_meus_chamados`

| Item | Valor |
|---|---|
| Propósito | "Quais chamados eu tenho?" — **só os que a Sofia abriu** (a API não lista tickets) |
| Args do LLM | nenhum |
| Injetados | `telefone`, `conversation_id` |
| Gateway | `POST /tools/infodesk/ticket/listar` → `SELECT` em `tickets` por telefone, últimos 90 dias; opcionalmente atualiza status via `GET /tickets/:number` (≤5 por chamada) |
| Retorno | `{status: true, total, chamados: [{numero, titulo, categoria, status_ticket, aberto_em}], observacao: "lista inclui só chamados abertos por aqui"}` |
| Idempotência | I-natural |

### A6. `transferir_para_atendente`

| Item | Valor |
|---|---|
| Propósito | O LLM pede transferência quando o cliente exige humano ou o assunto foge do que a Sofia pode resolver. (As transferências por mídia, falha, frustração alta e baixa confiança são **decisão de código**, não desta tool) |
| Args do LLM | `motivo: Literal["pedido_cliente", "fora_do_escopo", "nao_resolvido"]` |
| Injetados | `telefone`, `conversation_id`, `classificacao` (para escolher o departamento) |
| Precondição | conversa ainda com o bot (estado local) |
| Gateway | `POST /tools/infodesk/conversa/transferir` `{conversation_id, motivo, categoria?}` → departamento = `taxonomia[categoria].departamento` ou `DEPARTAMENTO_HUMANO_PADRAO`; `POST /conversations/:id/department`; marca `conversas.estado = transferida`; registra `falhas`/ocorrência com ID `SOF-AAAAMMDD-HHMM-NN`; vigia de alertas |
| Infodesk | `POST /integration/v1/conversations/:id/department` (e `/assign` se P11 definir pessoa) |
| Retorno | `{status: true, departamento: {nome}, mensagem_ao_cliente: "<texto fixo do gateway>"}` — o **gateway** envia a mensagem de aviso; o LLM só confirma em uma frase |
| Erros | `ja_transferida`, `infodesk_indisponivel` (→ alerta; o gateway marca transferida localmente mesmo assim para o bot calar) |
| Idempotência | I-local (estado da conversa) |

### A7. `consultar_base_conhecimento` (não é tool: nó `retrieve`)

Mantido do Zapin como etapa fixa do grafo (retrieval-first), **condicionada à
intenção** `duvida_informacao` ou `outro` (para reclamação pura o RAG é
pulado, economizando latência). Fonte dos docs: P6. Sem cobertura → o prompt
manda oferecer chamado/atendente, nunca inventar.

---

## B. Operações internas do gateway (não expostas ao LLM)

| Operação | Pacote Go | Infodesk | Observações |
|---|---|---|---|
| Receber webhook | `internal/webhook` | `message.received`, `conversation.started`, `conversation.finished` | HMAC-SHA256 do raw body (`X-Infominas-Signature: sha256=…`), `subtle.ConstantTimeCompare`; `200` imediato; dedup por `id` (7 dias) e por `data.message.id`; descarta `createdAt` > `EVENTO_IDADE_MAX` (10 min); valida `organizationId` se configurado; só `sender == "CLIENT"` segue |
| Enviar mensagem | `internal/infodesk` `EnviarMensagem(ctx, convID, body)` | `POST /conversations/:id/messages` | `DividirBolhas` + `FormatWhatsApp` (ajustar dialeto: o Infodesk entrega ao WhatsApp — negrito `*x*`; confirmar C7) + `QuebrarMensagem(900)`; pausa `BOLHA_PAUSA` entre bolhas; retry 2× só em 5xx/rede com dedup própria (não reenviar bolha já aceita) |
| Transferir departamento | `internal/infodesk` `TransferirDepartamento(ctx, convID, deptID)` | `POST /conversations/:id/department` | `null` remove — não usado |
| Atribuir atendente | `internal/infodesk` `Atribuir(ctx, convID, userID)` | `POST /conversations/:id/assign` | implementado no cliente, **não** chamado na v1 (P11) |
| Encerrar conversa | `internal/infodesk` `Encerrar(ctx, convID)` | `POST /conversations/:id/finish` | implementado, não chamado na v1 (C6) |
| Dados de referência | `internal/infodesk` `Mesas()`, `Departamentos()`, `Atendentes()` + cache TTL 10 min | `GET /desks`, `/departments`, `/users` | carregados no boot; `taxonomia.yaml` validada contra eles (nome inexistente → boot falha em `STRICT_TAXONOMIA=true`, senão alerta) |
| Abrir ticket | `internal/infodesk` `CriarTicket(ctx, NovoTicket) (Ticket, error)` | `POST /tickets` | tipos: `NovoTicket{DeskID, Title, Description, Channel, ContactPhone, PriorityID *string, ClientID *string, RequesterID *string}`; `Ticket{Number string; Bruto map[string]any}` — **`Number` tipado como string** até P3 confirmar (aceita número ou string no decode) |
| Consultar ticket | `internal/infodesk` `Ticket(ctx, number)` | `GET /tickets/:number` | devolve `Bruto` + campos conhecidos |
| Responder ticket | `internal/infodesk` `ResponderTicket(ctx, number, body)` | `POST /tickets/:number/reply` | implementado, **não usado** na v1 (T7) |
| Listar conversas | `internal/infodesk` `Conversas(ctx, tab, deptID)` | `GET /conversations` | usado pelo **reconciliador** (bloqueado por C1) |
| Ler conversa | `internal/infodesk` `Conversa(ctx, id)` | `GET /conversations/:id` | reconciliador e checagem "humano assumiu" (P5) |
| Reconciliador | `internal/reconciliador` | `GET /conversations` + `GET /conversations/:id` | a cada `RECONCILIA_INTERVALO` (2 min): conversas com mensagem `CLIENT` posterior à última processada → injeta como se fosse webhook (mesmo dedup). **Entregue só após P3/C1** |
| Transferência por código | `internal/engine/transferencia.go` | `POST /conversations/:id/department` | origens: `cliente` (pedido explícito detectado no Go: regex `atendente|humano|pessoa de verdade`), `anexo` (body vazio), `cerebro` (falha definitiva/esgotou retries), `disjuntor`, `frustracao` (vinda do cérebro em `acoes`), `confianca` (idem), `horario` (fora do horário humano — P7) |
| Alertas | `internal/alerta` | — | mesmos tipos do Zapin, renomeados (`sofia_alerta`, prefixo `[Sofia]`), + `taxonomia_invalida`, `ticket_pendente` (POST sem resposta) |

### Contrato A (gateway → cérebro) — mudanças

```jsonc
// POST /chat
{
  "conversation_id": "uuid-infodesk",
  "canal": "whatsapp",
  "mensagem": "texto coalescido da rajada",
  "telefone": "+5531990000000",
  "nome_contato": "Maria",              // data.contact.name do webhook (pode ser apelido)
  "contato": {                           // null se nunca identificado
    "forma": "cpf", "valor_mascarado": "***.***.789-**", "cliente_id": null, "nome_cadastro": null
  },
  "estado_conversa": "bot"              // bot | transferida (nunca chega transferida; defesa)
}
// resposta
{
  "reply": "texto com bolhas separadas por ---",
  "acoes": [ {"tipo": "transferir", "origem": "frustracao"|"confianca"|"pedido_cliente", "motivo": "..."} ],
  "classificacao": {"intencao": "...", "categoria": "...", "prioridade": "...", "confianca": 0.91, "origem": "jev"},
  "fontes": [],
  "erro": null
}
```

`acoes` passa a ser **usado** pelo gateway (no Zapin era sempre vazio): `transferir` é executado pelo caminho único de transferência. `classificacao` vai para o log/alerta e para a tabela `conversas` (auditoria).

Endpoints do cérebro: `POST /chat`, `POST /encerrar-episodio` (limpa `proposta_chamado`, `classificacao`, `identificacao` **não** — ela é por telefone no gateway), `GET /health`, `POST /classificar` (**só para evals/ops**: roda o JEV/roteador sem o grafo; protegido pelo mesmo `X-Tools-Token`).

---

## C. Tabelas novas do `store` (os dois backends)

| Tabela | Colunas | Uso |
|---|---|---|
| `processed_events` | `delivery_id`, `created_at` | dedup (existente) |
| `conversas` | `conversation_id` PK, `telefone`, `estado` (`bot`/`transferida`/`encerrada`), `ultima_msg_id`, `ultima_msg_em`, `classificacao_json`, `updated_at` | estado bot/humano; reconciliação; fim de episódio |
| `contatos` | `telefone` PK, `forma`, `valor_mascarado`, `valor_hash` (sha256 + sal), `cliente_id`, `nome_cadastro`, `expires_at` | identificação com TTL; o CPF em claro **não** é persistido (só hash + mascarado; o rodapé do ticket recebe o mascarado) |
| `tickets` | `chave_idempotencia` PK, `conversation_id`, `telefone`, `numero` NULL, `categoria`, `mesa_id`, `departamento_id`, `prioridade`, `status` (`pendente`/`aberto`/`falha`), `raw_resposta`, `created_at`, `updated_at` | idempotência, dedup, listagem local |
| `falhas` | como no Zapin, `ID` com prefixo `SOF-` | ocorrências/transferências |

Toda mudança em `store.Store` vale para SQLite e Postgres e ganha caso em
`store_test.go` (regra herdada).

---

## D. Matriz tool × política × efeito

| Tool | Injeta | Valida | Exige | Efeito colateral | Fase |
|---|---|---|---|---|---|
| `informar_identificacao` | telefone, conversation_id | cpf (DV), contrato (regex) | — | grava cache | única |
| `preparar_abertura_chamado` | telefone, conversation_id, classificacao, identificacao | titulo/descricao (tamanho) | identificado; categoria definida; sem protocolo citado | nenhum (estado: proposta) | 1 de 2 |
| `abrir_chamado` | telefone, conversation_id, proposta | — | proposta válida + confirmação explícita no turno seguinte | **cria ticket**, transfere depto | 2 de 2 |
| `consultar_status_chamado` | telefone, conversation_id, identificacao | numero | — | nenhum | única |
| `listar_meus_chamados` | telefone, conversation_id | — | — | nenhum | única |
| `transferir_para_atendente` | telefone, conversation_id, classificacao | motivo (enum) | conversa com o bot | **transfere** | única (sem confirmação: o pedido do cliente é a confirmação) |
