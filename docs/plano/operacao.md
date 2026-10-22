# Operação — o que olhar quando algo quebra (E4.3)

Guia de plantão da Sofia. Três lugares dizem o que está acontecendo, nesta ordem:

1. **`GET /health` do gateway** (`:8080/health`) — foto do que está carregado e do reconciliador.
2. **Alertas** (`sofia_alerta` no log do gateway + webhook opcional `ALERTA_WEBHOOK_URL`) — o que pede ação humana.
3. **Logs estruturados** — a trilha de cada conversa, por `conversation_id`.

Nada disso leva CPF, telefone ou nome do cliente em claro (regra 5 do `CLAUDE.md`). Conversa e ticket aparecem por ID/número.

## 1. Logs estruturados

O gateway escreve **JSON** (`slog`, stdout); o cérebro escreve **linhas** `nome campo=valor …` (o nome do evento é o primeiro token). O mesmo nome de evento significa a mesma coisa nos dois lados. Nível por `LOG_LEVEL` (`info` default).

| Evento | Quem emite | Nível | Campos | Quando |
|---|---|---|---|---|
| `chat_request` | cérebro | INFO | `conversation_id`, `canal`, `telefone` (mascarado, 3 últimos), `identificado`, `estado_conversa`, `len_mensagem` | chegou um turno (`POST /chat`). A mensagem do cliente **não** entra |
| `classificacao` | cérebro | INFO | `intencao`, `categoria`, `origem` (`jev`/`llm`/`esclarecimento`/`indefinida`), `prioridade`, `frustracao`, `jev` (o JEV respondeu?), `esclarecer`, `acoes` | o roteador decidiu o turno |
| `tool_exec` | cérebro | INFO | `nome`, `args` (só os **nomes** dos argumentos) | o agente chamou uma tool. Valores só em DEBUG (`tool_exec_args`), mascarados. **Exceção:** `agent_tool_calls` (`agent/nodes.py`) ainda loga os valores dos args em INFO; só a máscara do formatter os protege (ver seção 6) |
| `transferencia` | cérebro | INFO | `conversation_id`, `origem` (`frustracao`/`confianca`/`pedido_cliente`) | o cérebro **pediu** a transferência (ação devolvida ao gateway) |
| `transferencia` | gateway | INFO | `id_ocorrencia`, `conversation_id`, `origem`, `departamento`, `fora_do_horario` (E5.5: a transferência foi fora do horário humano) | o handoff foi **executado** (`engine/transferencia.go` → `alerta.LogTransferencia`) |
| `ticket_aberto` | gateway | INFO | `numero`, `conversation_id`, `categoria`, `mesa`, `reconciliado` | o ticket existe no Infodesk (`reconciliado=true`: foi achado pela listagem, não pelo POST). `toolsapi/abrir.go` → `alerta.LogTicketAberto` |
| `sofia_alerta` | gateway | ERROR | `tipo`, `titulo`, `texto`, `id_ocorrencia`, `conversation_id`, `link` | um alerta saiu (tabela da seção 4). `titulo` e `texto` passam por `alerta.MascararPII` |
| `sofia_falha` | gateway | ERROR | `id_ocorrencia`, `conversation_id`, `origem`, `classe`, `causa`, `detalhe` | uma transferência por falha foi registrada; o `id_ocorrencia` (`SOF-AAAAMMDD-HHMM-NN`) é o mesmo no cérebro (`chat_erro`) e na tabela `falhas` |
| `jev_indisponivel` | cérebro | WARNING | `erro` (só o tipo da exceção) | o JEV falhou neste turno; o roteador seguiu em fail-soft |
| `sofia_metrica` | cérebro e gateway | INFO (cérebro) / WARN (gateway) | `metrica`, `total` | um contador em memória subiu (zera no restart) |

Os campos de cada evento são **contrato**: `agente-ia/src/observability/eventos.py` (cérebro) e `backend/internal/alerta/eventos.go` (gateway) têm o catálogo, e os testes (`test_observability_logs.py`, `alerta/eventos_test.go`) fazem o código de verdade emitir o evento e conferem nome e campos. Mudou um campo? Mude o catálogo, esta tabela e o teste.

**Correlação:** `conversation_id` está em todos; `id_ocorrencia` liga cérebro × gateway × tabela `falhas`; `numero` é o ticket no Infodesk.

**Máscara:** o cérebro instala um formatter (`FormatadorPII`) nos handlers do logger-raiz e do `uvicorn`/`uvicorn.error` (não no `uvicorn.access`, cujo formatter lê `record.args`) que mascara CPF, telefone, e-mail, números longos e os nomes do contato (WhatsApp e cadastro) em **qualquer** mensagem de log, em qualquer nível, inclusive traceback. Ficam intactos: `SOF-…`, UUID, data/hora e `conversation_id=`/`id_ocorrencia=` com valor em formato de ID (sem 9+ dígitos seguidos). É rede de segurança, não licença para logar PII: não pega telefone fixo sem DDD nem nome que o sistema não conhece.

### Métricas

Contadores em memória + o log `sofia_metrica` (sem servidor de métricas).

| Métrica | Onde | Como alertar |
|---|---|---|
| `jev_indisponivel` | cérebro | cada turno em que o JEV falhou e o fail-soft cobriu. O log `jev_indisponivel_alerta` (ERROR) já sai quando a taxa passa de `JEV_ALERTA_INDISPONIVEL` (5 %) na janela `JEV_ALERTA_JANELA_S` (15 min). **Só conta com `LOG_LEVEL` ≤ WARNING** (o evento é WARNING) |
| `humano_assumiu_sem_handoff` | gateway | um atendente assumiu uma conversa que o bot não transferiu |

## 2. Trace (Langfuse)

Desligado por padrão: sem `LANGFUSE_PUBLIC_KEY` e `LANGFUSE_SECRET_KEY` o cérebro roda normal e loga `langfuse_tracing desativado`. Ligado:

- 1 trace por turno, `session_id` = `conversation_id`, `user_id` = telefone **mascarado**;
- **tags** filtráveis no painel: `canal:whatsapp`, `intencao:<…>`, `categoria:<…>`, `origem:<…>` (as três últimas entram depois que o grafo classificou; só rótulos da taxonomia, nunca texto livre);
- **sem PII em nenhum span:** o cliente Langfuse é criado com `mask=` (`observability/pii.py`), que o SDK aplica a `input`, `output` e `metadata` de todo span — inclusive o span de entrada de tool, onde a política injeta o `telefone`. Chaves `telefone`, `cpf`, `nome*`, `contrato`, `valor`, `email` viram `<PII>`; texto livre perde CPF, telefone, e-mail, nº longo e o nome do contato; `<documento_externo>` é omitido.
- **Exceção do turno não vai ao SDK:** o span fecha com `level=ERROR` e só o **nome** do tipo (`status_message`); a mensagem e o stacktrace (que podem ter CPF/telefone) ficam no log mascarado, não no trace. Os spans do `CallbackHandler` do SDK (`on_tool_error`) guardam `str(erro)` por conta própria: não deixe mensagem de erro de tool carregar PII.
- Falha do Langfuse nunca derruba um turno: vira `langfuse_tracing_falhou etapa=… erro=<Tipo>` (WARNING) e o atendimento segue sem trace.

## 3. `GET /health` do gateway

Sempre **200** enquanto o processo atende (o healthcheck do compose mede "está vivo"; reiniciar o gateway não conserta Infodesk fora do ar). Quem decide é o campo `status`:

```json
{
  "status": "degradado",
  "motivos": ["feed_sem_escopo"],
  "quando": "2026-10-22T12:00:00Z",
  "uptime_s": 3600,
  "referencia":    {"carregada": true, "ultima_leitura_ok": "…", "ultimo_erro_em": null},
  "taxonomia":     {"habilitada": true, "carregada": true, "valida": true, "placeholder": true, "problemas": 0},
  "piloto":        {"ativo": false, "modo": "aberto", "numeros": 0},
  "horario_humano": {"configurado": true, "dentro": true, "fora_do_horario_modo": "avisar"},
  "reconciliador": {"ativo": true, "ultimo_seq": 120, "ultimo_seq_webhook": 125,
                    "feed": {"estado": "sem_escopo", "ultima_leitura_ok": null, "ultimo_erro_em": "…", "ultimo_erro": "HTTP 403 FORBIDDEN"}}
}
```

Sem segredo, URL, nome nem telefone: só flags, contagens, instantes e a **classe** do erro (`HTTP <status> <code>`), nunca o corpo da resposta.

| Campo | Significa |
|---|---|
| `referencia.carregada` | a última leitura de mesas/catálogo/departamentos do Infodesk (cache de referência) deu certo. `ultimo_erro` aparece se a última leitura falhou |
| `taxonomia.habilitada` | `TOOLS_API_TOKEN` definido (sem ele não há API de tools nem abertura de ticket, e a taxonomia não é cobrada) |
| `taxonomia.valida` | habilitada, carregada **no boot** e **0** divergências com o Infodesk |
| `taxonomia.inferida` | o boot não carregou e a referência foi lida depois: `carregada=true`, mas ninguém conferiu as divergências (`valida=false`, motivo `taxonomia_nao_verificada`). Reinicie o gateway ou veja o alerta `taxonomia_invalida` |
| `taxonomia.placeholder` | `config/taxonomia.yaml` ainda é a hipótese de trabalho (P1): não é degradação, é aviso |
| `taxonomia.problemas` | divergências vistas no carregamento do boot (nomes de mesa/departamento/prioridade que o Infodesk não tem) |
| `piloto.modo` / `piloto.ativo` / `piloto.numeros` | `modo` = `piloto` (lista de testadores ligada) ou `aberto` (todo cliente fala com a Sofia);  a lista de testadores (`TESTADORES_TELEFONES`) está ligada e quantos números únicos tem; **nunca** os números. Não degrada o status: é uma escolha. Ver a seção 7 |
| `horario_humano.configurado` / `dentro` / `fora_do_horario_modo` | `HORARIO_HUMANO` está configurado e se AGORA é horário de atendimento humano (e o modo de `FORA_DO_HORARIO_MODO`). Não configurado = `dentro` sempre `true`. Fora do horário não degrada o status: é rotina (seção 10) |
| `reconciliador.ativo` | `RECONCILIA_ATIVO` |
| `reconciliador.ultimo_seq` / `ultimo_seq_webhook` | cursor do feed processado pelo reconciliador × maior `seq` visto pelo webhook. Parado com tráfego = feed/ciclo travado |
| `reconciliador.feed.estado` | `ok`, `sem_leitura` (ainda não leu neste processo), `sem_escopo` (403), `falhando` (rede/5xx) |

**Motivos de `degradado`** (vocabulário fechado): `referencia_nao_carregada`, `taxonomia_nao_carregada`, `taxonomia_invalida`, `taxonomia_nao_verificada`, `feed_sem_escopo`, `feed_falhando`, `feed_sem_leitura` (reconciliador ligado, nenhuma leitura do feed 5 min depois de subir: loop morto), `reconciliador_desligado`, `store_indisponivel`.

Cérebro: `GET :8001/health` continua `{"status":"ok"}` (processo vivo, grafo compilado).

## 4. Quando algo quebra

Cada caso: **sintoma → onde olhar → causa provável → o que fazer**.

### Webhook devolvendo 401 (mensagens não chegam)
- **Sintoma:** o Infodesk reclama de falha de entrega; ninguém responde no WhatsApp; nenhum `evento enfileirado`.
- **Onde olhar:** log WARN `assinatura inválida` (campo `motivo`), `token de webhook inválido` ou `webhook com timestamp fora da janela`; alerta `webhook_timestamp_fora_da_janela` (entrega **autêntica** fora de `WEBHOOK_JANELA`).
- **Causa:** `INFODESK_WEBHOOK_SECRET` (ou `WEBHOOK_TOKEN` na URL) diferente do cadastrado no Infodesk; relógio do servidor fora; corpo reescrito por proxy (a assinatura é do `timestamp.corpo` exato).
- **Fazer:** conferir o segredo dos dois lados; `chronyc tracking`/`timedatectl`; reentregar. Enquanto isso o reconciliador recupera pelo feed `/events` (veja "403 no feed" se ele também estiver cego).

### Handoff não confirmado (cliente ficou sem ninguém)
- **Sintoma:** a conversa está muda localmente (`transferida`) mas nenhum atendente foi avisado.
- **Onde olhar:** alerta `handoff_falhou` (com `conversation_id`) → log `sofia_falha` (mesmo `id_ocorrencia`) → log ERROR `transferir: handoff falhou; conversa fica muda localmente…`; depois os logs `handoff pendente: …` do reconciliador; se o reenvio continua falhando, alerta `handoff_nao_confirmado`. Alerta `bot_sem_resposta_handoff` = o vigia transferiu uma conversa sem resposta há mais que `BOT_TIMEOUT_HANDOFF`.
- **Causa:** Infodesk recusou/indisponível no `POST /conversations/:id/handoff`; `DEPARTAMENTO_HUMANO_PADRAO` inválido; chave sem escopo.
- **Fazer:** se o texto do alerta disser que a marca não foi gravada ("NINGUÉM vai repetir"), **transfira à mão no painel agora**. Senão o reconciliador repete (backoff até `RECONCILIA_BACKOFF_MAX`).

### Ticket pendente (POST sem resposta confiável)
- **Sintoma:** o cliente não recebeu o número do chamado, ou ele existe no Infodesk e a Sofia não sabe.
- **Onde olhar:** ERROR `abrir: POST do ticket sem resposta confiável; reconciliando por listagem (sem novo POST)`; depois INFO `ticket pendente reconciliado` (campos `chave`, `ticket`) ou ERROR `ticket pendente não apareceu no Infodesk dentro do prazo; marcado como falha`. Alertas `ticket_pendente_falhou` (passou de `TICKET_PENDENTE_TTL` sem aparecer) e `ticket_pendente_nao_checado` (TTL vencido e a listagem não respondeu). `ticket_aberto` com `reconciliado=true` confirma o achado.
- **Causa:** timeout/5xx depois do envio do POST (o ticket pode existir — ADR 0005).
- **Fazer:** **nunca** reabrir à mão sem procurar antes no Infodesk pela conversa/telefone (a API não tem chave de idempotência; um 2º POST duplica). Achou: o reconciliador amarra o número. Não achou após o TTL: o cliente pode tentar de novo.

### JEV fora
- **Sintoma:** classificação só pelo LLM de fallback; mais perguntas de esclarecimento; mais transferências por baixa confiança.
- **Onde olhar:** WARNING `jev_indisponivel erro=<Tipo>` (a cada turno afetado), métrica `sofia_metrica metrica=jev_indisponivel total=N`, ERROR `jev_indisponivel_alerta taxa=…` (acima de 5 % na janela de 15 min); na inicialização, `jev_sem_chave` = `TYPESAFE_API_KEY` ausente. No log `classificacao`, `jev=False` e `origem=llm`.
- **Causa:** TypeSafe fora, chave/limite, rede do cérebro. **O atendimento não para** (fail-soft); o que cai é a qualidade da classificação.
- **Fazer:** status/limite do TypeSafe, `TYPESAFE_API_KEY`; se for prolongado, avisar a equipe que a triagem está degradada.

### 403 no feed de eventos
- **Sintoma:** eventos que o webhook perdeu não são recuperados (a conversa "some" do reconciliador).
- **Onde olhar:** ERROR `feed /events recusado (403): a chave não tem o escopo events:read` (uma vez por episódio), alerta `reconciliador_sem_escopo`, `/health` → `reconciliador.feed.estado = "sem_escopo"` e `motivos: ["feed_sem_escopo"]`. Se for outra falha repetida: alerta `reconciliador_feed_falhando`, `estado = "falhando"`.
- **Fazer:** incluir `events:read` na chave do Infodesk (o gateway também checa os escopos no boot: `chave_sem_escopo`). O INFO `feed /events voltou a responder` fecha o episódio.

### Taxonomia inválida
- **Sintoma:** tickets recusados com `referencia_indisponivel`, "categoria sem mapa", ou o boot cai.
- **Onde olhar:** alerta `taxonomia_invalida` (títulos: "Taxonomia não carregada no boot", "Taxonomia diverge do Infodesk", "Categoria sem mapa na taxonomia"); logs `taxonomia diverge do Infodesk` (campo `problema`, um por divergência) e `taxonomia PLACEHOLDER (P1)`; `/health` → `taxonomia.*` e `referencia.*`. Com `STRICT_TAXONOMIA=true` o boot falha com `taxonomia inválida` e o gateway não sobe.
- **Causa:** nome de mesa/departamento/prioridade no `config/taxonomia.yaml` que o Infodesk não tem (renomeado/removido) ou referência fora do ar no boot (nesse caso a taxonomia tenta de novo sob demanda a cada 30 s e `INVALID_REFERENCE` recarrega).
- **Fazer:** ajustar o YAML (ou o cadastro no Infodesk) e reiniciar; `placeholder: true` significa que a tabela inteira ainda é hipótese (P1) — não use em produção sem o export do Infodesk.

### Prazos do turno estourando
- **Sintoma:** o cliente recebe a mensagem de "instabilidade" ou é transferido; turnos lentos.
- **Onde olhar:** cérebro ERROR `chat_erro … classe=transitoria causa=cérebro não concluiu o turno dentro do prazo (BRAIN_TURNO_PRAZO)` (HTTP 503); gateway WARN `falha transitória do cérebro` (`tentativa`) e ERROR `sofia_falha`; alertas `taxa_de_falhas` (> 5 % dos turnos em 15 min, mín. 3) e `disjuntor_aberto` (3 falhas definitivas iguais em 5 min: o cérebro não é chamado por um tempo e os clientes vão direto ao atendente).
- **Causa:** LLM/RAG/Infodesk lentos; **prazos mal encadeados**. A regra é `DEBOUNCE_TETO` < `TURNO_TETO` e o prazo do cérebro (`BRAIN_TURNO_PRAZO`) menor que `BRAIN_TIMEOUT` do gateway, com folga para a coalescência da rajada (que consome o `TURNO_TETO` desde a 1ª mensagem). Valores atuais: `.env.example` de cada lado; o gateway recusa combinações inválidas no boot (`config inválida`).
- **Fazer:** olhar o trace do turno no Langfuse (latência por span) para achar o passo lento; só então mexer nos prazos. Chamadas de LLM **não** entram na validação de prazos do boot.

## 5. Alertas — referência rápida

| `tipo` | Origem | Significa |
|---|---|---|
| `falha_definitiva` | engine | falha que repetir não resolve (402, chave, bug): alerta na hora |
| `taxa_de_falhas` | engine | > 5 % dos turnos com falha em 15 min (mín. 3) |
| `disjuntor_aberto` | engine | o gateway parou de chamar o cérebro |
| `handoff_falhou` | engine | o Infodesk não aceitou o handoff |
| `handoff_nao_confirmado` | reconciliador | o handoff repetido continua falhando |
| `bot_sem_resposta_handoff` | reconciliador | conversa do bot sem resposta há mais que `BOT_TIMEOUT_HANDOFF` foi a um humano |
| `webhook_timestamp_fora_da_janela` | webhook | entrega autêntica fora da janela (replay ou relógio) |
| `organizacao_divergente` | webhook | `organizationId` do webhook ≠ `GET /me` |
| `evento_ilegivel` / `lookup_ilegivel` | webhook / engine | objeto presente mas não decodificável |
| `chave_sem_escopo` | boot / engine | a chave não conduz conversas ou falta escopo (inclui o lookup) |
| `reconciliador_sem_escopo` | reconciliador | 403 no `GET /events` (falta `events:read`) |
| `reconciliador_feed_falhando` | reconciliador | o feed falha há várias execuções seguidas |
| `ticket_pendente_falhou` / `ticket_pendente_nao_checado` | reconciliador | ticket pendente passou do TTL sem aparecer / sem como conferir |
| `taxonomia_invalida` | toolsapi / boot | divergência, categoria sem mapa ou referência fora no boot |
| `producao_aberta` | boot | o gateway subiu sem lista de testadores (`SOFIA_PRODUCAO_ABERTA=true`): todo cliente fala com a Sofia (seção 7) |

O mesmo `tipo` + conversa não repete dentro de 5 minutos. Destino: log `sofia_alerta` (sempre) e `ALERTA_WEBHOOK_URL` (opcional; `{"text": …}`, compatível com Slack/Discord/n8n). Título e texto são mascarados contra PII antes de sair.

## 6. Limites conhecidos (follow-ups)

- **`sofia_metrica` do gateway** (`engine/estado.go`) ainda escreve a string na mão; falta usar `alerta.EventoMetrica`.
- **`taxonomia.*` do `/health` é a foto do boot** (o pacote `taxonomia` não expõe estado vivo). Se o boot não carregou mas a referência foi lida depois, `carregada` passa a `true` com `inferida=true` e `valida=false`; as divergências dessa carga só aparecem no alerta `taxonomia_invalida`. Um `Taxonomia.Estado()` tornaria isso vivo.
- **`agent/nodes.py` loga os valores dos args das tools em INFO** (`agent_tool_calls`), contra o que o catálogo promete para `tool_exec`. Fora da zona: trocar por `sorted(args)`. Até lá, só a máscara do formatter protege (um nº de contrato curto, < 9 dígitos, passa por ela).
- **A métrica `jev_indisponivel` conta pelo log** do roteador (nome do evento), por isso exige `LOG_LEVEL` ≤ WARNING. Chamar `metricas.contar("jev_indisponivel")` direto no roteador tiraria essa dependência.
- **Sem servidor de métricas** (Prometheus): os contadores vivem em memória e no log `sofia_metrica`.

## 7. Lista de testadores (homologação em produção)

O Infodesk não tem ambiente de homologação e a flag **"Conduz o atendimento"** da chave vale para a **organização inteira** (`docs/infodesk/08` e `10` §4): ligada, toda conversa nova de WhatsApp individual nasce com `handledBy = EXTERNAL_BOT` e os atendentes **não são avisados** até o handoff. A lista de testadores deixa só um grupo de números falar com a Sofia enquanto isso.

**Como funciona.** Com `TESTADORES_TELEFONES` preenchida, uma `message.received` que passa no filtro de turno e vem de número **fora** da lista não vai ao cérebro: o gateway faz **handoff imediato** para `DEPARTAMENTO_HUMANO_PADRAO`, **sem enviar nenhuma mensagem ao cliente**, com a nota por código `Sofia → humano · motivo: fora do grupo de teste da Sofia`, e marca a conversa como `transferida`. Uma vez por episódio (rajada = um handoff só; as mensagens seguintes encontram a conversa muda). O handoff que falhar ganha a marca de "não confirmado" e o reconciliador o repete (seção 4). Fora do piloto **não** é falha: com o handoff ok, não gera ocorrência `SOF-…`, nem log ERROR `sofia_falha`, nem alerta (a trilha é o evento `transferencia` com `origem=fora_do_piloto` e o INFO `fora_do_piloto` com o telefone mascarado). Se o Infodesk recusar o handoff, valem os alertas `handoff_falhou`/`handoff_nao_confirmado` (um por conversa, a cada 5 min) e a repetição da seção 4. Qualquer outro caminho que transfira um número de fora (o vigia `BOT_TIMEOUT_HANDOFF`, por exemplo) vira essa mesma transferência silenciosa.

**Telefones.** E.164, separados por vírgula, comparados como o lookup do Infodesk: com ou sem `+55` e com ou sem o 9º dígito dos celulares são o mesmo número (`+5531990000000`, `31990000000` e `553190000000` casam). Com `+55` o número tem de estar completo (12 ou 13 dígitos) e zero de tronco (`031…`) é recusado: número truncado não pode virar o de outra pessoa. Evento sem telefone legível, com a lista ligada, conta como fora (na dúvida, humano). Item que não é telefone derruba o boot (`config inválida … TESTADORES_TELEFONES`).

**Como ligar**
1. Preencha `TESTADORES_TELEFONES=+5531990000000,+5511988887777` e `DEPARTAMENTO_HUMANO_PADRAO` (sem ele o handoff vai à distribuição automática) no `.env` do gateway e **reinicie o gateway**. O boot loga um WARN `TESTADORES_TELEFONES LIGADA com N número(s)…`; confira `GET /health` → `piloto: {"ativo": true, "numeros": N}`.
2. **Só depois** ligue "Conduz o atendimento" na chave `sofia-agente-ia` (Configurações gerais › Integrações).
3. Teste com um número da lista (a Sofia responde) e confirme com um número de fora que a conversa vai à fila com a nota, sem mensagem ao cliente.

**Como desligar** (abrir para todos): deixe `TESTADORES_TELEFONES` vazia **e defina `SOFIA_PRODUCAO_ABERTA=true`** (sem isso o boot falha com mensagem clara: a produção aberta é um opt-in explícito) e reinicie o gateway; `piloto.modo` vira `aberto`, o boot loga o WARN `PRODUÇÃO ABERTA…` e emite o alerta `producao_aberta`. Em dev (Infodesk local, o mock) o opt-in não é exigido. Para sair do modo bot externo de vez, desligue a flag "Conduz o atendimento" na chave: as conversas voltam ao fluxo normal do Desk (as já em curso com a Sofia seguem até o handoff/encerramento).

> **Risco:** com a flag **ligada** e a lista **vazia**, **todos os clientes** caem na Sofia, sem filtro, e os atendentes só são avisados no handoff. Nunca ligue a flag antes de preencher a lista (passo 1 antes do 2) e nunca esvazie a lista com a flag ligada sem querer abrir para todos.

**Limites.** A lista é lida no boot (mudar exige reiniciar). Conversa que já estava com a Sofia (estado `bot`) quando a lista foi ligada passa pelo mesmo filtro na próxima mensagem. O filtro está na engine (`processarLote`), então vale para o webhook e para os eventos recuperados pelo feed. Além disso:

- **Conversa reaberta** (mesmo `id`, volta ao bot externo depois de `finished` ou de nova sessão): cada episódio de um número de fora faz um novo handoff silencioso.
- **`DEPARTAMENTO_HUMANO_PADRAO` vazio** com a lista ligada não derruba o boot (só avisa): os clientes de fora vão para a distribuição automática do Infodesk, sem departamento. Preencha antes de ligar.

## 8. Fail-closed do boot (segredos e modo aberto)

O gateway **recusa subir** (`config inválida`, sem repetir o valor do segredo) quando:

| Situação | O que fazer |
|---|---|
| `INFODESK_WEBHOOK_SECRET` vazio | defina o `whsec_…` do webhook. Só em dev: `WEBHOOK_SEM_AUTENTICACAO=true` (o boot avisa que o webhook fica sem HMAC) |
| `INFODESK_WEBHOOK_SECRET` ou `TOOLS_API_TOKEN` com menos de 32 caracteres | gere um maior (`openssl rand -hex 32`). `TOOLS_API_TOKEN` vazio = API de tools desligada, não é erro |
| `TESTADORES_TELEFONES` vazia (ou só vírgulas) sem `SOFIA_PRODUCAO_ABERTA=true` | preencha a lista (homologação) ou abra de propósito com `SOFIA_PRODUCAO_ABERTA=true` |

**Modo dev** = `INFODESK_BASE_URL` com host loopback de verdade (`localhost`, `*.localhost` ou IP loopback `127.0.0.0/8`/`::1`: o mock-infodesk do harness e do E2E; `127.0.0.1.nip.io` ou `localhost.exemplo.com` não contam). `WEBHOOK_SEM_AUTENTICACAO=true` **não** liga o modo dev: só dispensa o segredo do webhook (com WARN), e o resto (tamanho dos segredos, opt-in de produção aberta, alerta `producao_aberta`) segue valendo. No modo dev valem segredos curtos e a lista vazia sem o opt-in, com WARN no boot. `scripts/start-all.sh` já passa `INFODESK_WEBHOOK_SECRET` e `TOOLS_API_TOKEN` (curtos: `segredo-mock`, `dev-token`) e aponta para o mock, então segue funcionando sem mudança.

## 9. Limites de abuso, proposta selada e token do cérebro (E4.5)

- **Chamado confirmado pelo gateway.** Depois do "sim", o gateway abre o chamado e, antes do handoff pós-ticket, manda ele mesmo ao cliente `Pronto, abri o chamado nº N para você. Um de nossos atendentes segue com o seu caso por aqui.` (texto fixo, `engine.MsgChamadoAberto`). A resposta do cérebro no mesmo turno é descartada pelo handoff (`resposta descartada: a conversa deixou de ser da Sofia`): é esperado, não é erro. Com `TRANSFERIR_APOS_TICKET=false` não há handoff e a resposta do cérebro segue normal.
- **`abrir` só executa proposta selada.** O `preparar` grava o selo (hash de conversa + título + descrição + categoria + prioridade) e o `abrir` só cria o ticket se o selo confere, tem até 15 min e o telefone está identificado; senão devolve `sem_proposta` e o cérebro volta ao `preparar`. O retry da MESMA chave depois de aberto não precisa de selo (idempotência).
- **Cadastro sem PII depois de concluir.** Ao concluir, a linha de `cadastros` perde nome e e-mail (só serviam ao POST); propostas de cadastro não executadas são apagadas pelo reconciliador 20 min depois (log `propostas de cadastro vencidas apagadas`). As tentativas de "empresa não encontrada" contam por telefone (janela de 24 h), não por conversa.
- **`LOTE_MAX_CHARS`** (4000): o texto coalescido de uma rajada é cortado nesse tamanho antes do cérebro (WARN `lote coalescido cortado no teto`). **`TURNOS_POR_HORA_MAX`** (60): turnos por telefone na última hora; acima, a conversa vai a um atendente (origem `limite_turnos`, WARN `limite de turnos por hora excedido`). O contador é em memória: um reinício o zera.
- **`X-Brain-Token`.** O gateway envia `TOOLS_API_TOKEN` no header `X-Brain-Token` em toda chamada ao cérebro (`/chat`, `/encerrar-episodio`). O cérebro que ainda não o exige o ignora; quando exigir, os dois lados precisam ter o mesmo `TOOLS_API_TOKEN`.
- **Logs de erro do Infodesk** saem como `http <status> <code> <rota>` / `rede <rota>` (`engine.DescreverErro`), sem a mensagem do servidor, que pode ecoar dado do cliente.

## 10. Horário de atendimento humano (E5.5)

O expediente das mesas do Desk é seg–sex 08:00–18:00 (docs/infodesk/10 §8); **que isso valha para o atendimento humano do WhatsApp, e a mensagem fora dele, seguem sem confirmação (P7)**. Por isso tudo é configurável:

| Variável | Default | Significa |
|---|---|---|
| `HORARIO_HUMANO` | `seg-sex 08:00-18:00` (em **modo dev** — Infodesk local —, a variável ausente = sempre dentro) | dias + faixas, várias entradas com `;`: `seg-sex 08:00-18:00; sab 08:00-12:00`. Várias faixas no dia: `seg-sex 08:00-12:00,14:00-18:00`. Dias: `seg ter qua qui sex sab dom` (intervalo `seg-sex`, lista `seg,qua`, volta `sex-seg`). Fim menor que o início atravessa a meia-noite (`22:00-06:00`; pertence ao dia em que começou). **Presente e vazia (`HORARIO_HUMANO=`) = sempre dentro do horário.** Formato inválido derruba o boot |
| `HORARIO_FUSO` | `America/Sao_Paulo` | fuso do horário e dos feriados (nome IANA; o binário embute o banco de fusos) |
| `FERIADOS` | vazio | datas `AAAA-MM-DD` (vírgula, `;` ou espaço); nenhuma faixa **começa** no feriado (uma faixa que atravessa a meia-noite e começou na véspera continua valendo na madrugada do feriado) |
| `FORA_DO_HORARIO_MODO` | `avisar` | `avisar` ou `ignorar` (qualquer outro valor derruba o boot) |

**O que muda fora do horário.** A Sofia segue atendendo e abrindo chamado (nada muda no cérebro). Só a **transferência** muda: o handoff acontece igual (a conversa cai na fila para a equipe ver no próximo expediente), com a nota `… · motivo: … · fora do horário`, e o log `transferencia` sai com `fora_do_horario=true`. Com `avisar` a mensagem ao cliente vira: *"Agora a nossa equipe de atendimento não está disponível: ela atende de segunda a sexta, das 08:00 às 18:00. Sua conversa fica registrada por aqui e a equipe retoma com você quando o atendimento voltar. 🙏"* — o horário vem da configuração (nenhuma hora inventada, nenhum prazo prometido; com feriados configurados acrescenta "(exceto feriados)"). O que a mensagem normal tinha de próprio é mantido na frente: o número do chamado aberto, "não consigo ver áudio, imagem ou arquivo", "não consegui confirmar o seu chamado". Com `ignorar` o aviso é o normal (nota e log seguem marcados). Transferência silenciosa (fora do piloto) continua sem mensagem.

**Atenção:** o default liga o horário de expediente — em produção, quem escrever pedindo atendente às 20h recebe o aviso de fora do horário. Para manter o comportamento anterior, `HORARIO_HUMANO=` (vazia). O `/health` mostra `horario_humano.dentro` agora. O horário é lido no boot (mudar exige reiniciar).

Detalhes: faixas repetidas ou sobrepostas no mesmo dia são fundidas (`08:00-12:00,10:00-14:00` = `08:00-14:00`); na mensagem, a faixa que atravessa a meia-noite sai "das 22:00 às 06:00 do dia seguinte", o fim `24:00` sai "até a meia-noite" e `00:00-24:00` sai "o dia todo". O aviso de fora do horário também sai quando a resposta do cérebro no mesmo turno já anunciou a transferência (frustração, baixa confiança, pedido do cliente) — ele não repete "vou te encaminhar". Quando a origem tem uma mensagem própria de duas frases (chamado aberto, "não consigo ver anexo", "não localizei a sua empresa", "não consegui registrar o chamado"), a **primeira frase** é mantida na frente. Depois do aviso a conversa fica `transferida` e muda: **não há segundo aviso** nas mensagens seguintes do cliente até um atendente assumir. O boot loga o horário efetivo (`horário humano`, com `dentro_agora`) e avisa se `HORARIO_HUMANO` está vazio em produção. **Atenção:** o Desk também tem "Abrir pré-ticket fora do expediente" ligado (07 N6); confirmar na homologação que o cliente não recebe duas mensagens de fora do expediente.
