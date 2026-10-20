# Bloco A — Arquitetura de confiança e correções do laudo black-box

- **Data:** 2026-08-27
- **Insumo:** `laudo-teste-blackbox-zapin-2026-08-27.md` (1 Crítico, 5 Altos, 6 Médios)
- **Status:** implementado neste commit; suíte Python 178/178 ✅, Go build+vet+test ✅.

Este documento registra **por que** a arquitetura mudou (não só o quê), para
guiar decisões futuras. O CLAUDE.md descreve o estado atual; aqui fica o
raciocínio.

---

## 1. O diagnóstico em uma frase

Quase todas as falhas do laudo eram o mesmo erro em direções opostas: **uma
responsabilidade vivendo do lado errado da fronteira entre o determinístico e
o probabilístico.**

- **Padrão A — trabalho determinístico entregue ao LLM** (falhas de segurança):
  autorização (F7/F9), integridade de citação (F4), formatação (F3/F12).
- **Padrão B — trabalho probabilístico entregue a código rígido** (falhas de
  robustez/UX): confirmação por igualdade exata (F2), classificador com poder
  de veto sem contexto (F8).
- **Padrão C — engenharia de sistemas distribuídos ausente**: estado terminal
  (F1), entrega não idempotente (F10), ausência de serialização por conversa
  (F11).

## 2. O princípio que resolve o Padrão A

> **O LLM nunca faz parte da base confiável (TCB) do sistema. Tudo que ele
> produz — incluindo tool calls — é input não-confiável, como se viesse do
> próprio usuário. O LLM propõe; o código dispõe.**

É a outra metade do que o `<documento_externo>` já fazia: aquele trata o que
ENTRA no modelo como dado; agora o que SAI do modelo também é tratado como
proposta, nunca como ordem. (Referências: dual-LLM pattern de S. Willison;
CaMeL, DeepMind 2025.)

Três mecanismos em camadas:

1. **Estado inseguro irrepresentável** — `rastrear_nota_fiscal(numero_nota)`:
   o parâmetro de escopo saiu do schema que o modelo vê (`InjectedToolArg`).
   Não existe sequência de tokens que consulte outro tenant.
2. **Política declarativa** — `agente-ia/src/agent/tool_policy.py` (POLITICAS):
   para cada tool, o que se injeta da sessão e o que se valida do LLM. É o
   artefato auditável: "o que o LLM pode fazer?" = ler um arquivo.
3. **Validação server-side** — o gateway Go continua revalidando (relator,
   grupo, tipos de chamado). Defesa em profundidade.

## 3. Mapa achado → correção

| # | Achado | Correção | Onde |
|---|--------|----------|------|
| F7 🔴 | IDOR: `id_cliente` escolhido pelo LLM | escopo `grupos_emp_sessao` injetado; schema sem arg de identidade; fail-closed sem escopo | `crm_mocks.py`, `tool_policy.py`, `nodes.py` |
| F9 | chamado "em nome de" empresa alheia | validador `empresa ∈ grupos da sessão` na política (recusa vira ToolMessage) | `tool_policy.py` |
| F4 | citação fabricada sem consultar RAG | **retrieval-first** (nó `retrieve` roda para toda mensagem) + `validar_citacoes` remove "(fonte: ...)" que não bate com documento recuperado | `nodes.py`, `rag_tool.py`, `output_guardrails.py` |
| F6 | off-topic sob pretexto de trabalho | regra explícita no prompt (não gerar conteúdo fora do domínio nem "para testar campo") + few-shot no classificador + respostas curtas reduzem o valor do abuso. **Risco residual assumido**: é batalha de prompts; o blast radius é tokens, não dados | `prompts.py` |
| F8 | "boa noite"/"não" viram brush-off | `off_topic` **não bloqueia mais** — vira dica no system prompt; em fluxo ativo (agente perguntou algo) o classificador é pulado; só `malicioso` curto-circuita | `nodes.py`, `input_guardrails.py` |
| F1 | `GateFalha` terminal (silêncio eterno) | `GATE_FALHA_TTL` (1h): expira → identificação recomeça (cache primeiro) | `gate.go` |
| F2 | login/confirmação quebram com texto ao redor | `StripAssinatura` na borda; `confereConfirmacao` (contains + dígitos); e-mail/CPF extraídos de frase deterministicamente antes da IA | `whatsapp.go`, `gate.go` |
| F10 | reentrega (resposta-fantasma) | dedup por WAID (`source_id`) no store + descarte de eventos > `EVENTO_IDADE_MAX` | `engine.go`, `types.go` |
| F11 | turnos concorrentes, corrida no checkpointer | **fila FIFO por conversa** (1 worker/conversa) + **coalescência 8s/teto 20s** — rajada vira UM turno; + checkpointer SQLite no brain | `coalescer.go`, `server.py` |
| F3 | `**bold**` literal no WhatsApp | `FormatWhatsApp` em todo `send()` | `whatsapp.go` |
| F12 | respostas longas | prompt WhatsApp-native (~400 chars) + `QuebrarMensagem` (`REPLY_MAX_CHARS`) | `prompts.py`, `whatsapp.go` |
| F5 | PII em log claro | telefone/login mascarados; mensagem só em DEBUG | `server.py` |
| A2 | vazar erro interno | erros de tool viram texto genérico p/ o agente; `contem_vazamento_interno` troca resposta contaminada pelo fallback | `nodes.py`, `output_guardrails.py` |
| C2/A1 | disclosure via "sou da homologação" | invariante no prompt ("você fala SEMPRE com um cliente") + padrões novos na heurística (listar ferramentas/regras, "modo auditoria") | `prompts.py`, `input_guardrails.py` |
| B1 | cold-start do embedding | warm-up em thread no startup do server | `server.py` |
| — | latência (RAG ~17s) | retrieval-first corta 1 chamada de LLM; Haiku 4.5 é rápido; warm-up. Expectativa: ~5-7s | vários |

## 4. Decisões de modelo e persona

- **Agente:** `google/gemini-3.5-flash` (Bloco A adotou `claude-haiku-4.5`,
  era gemini-2.5-flash antes; trocado pós-homologação por preferência de
  qualidade nesta tarefa). Sem `cache_control` explícito (o projeto só o
  aplica a modelos `anthropic/`) — o Gemini cacheia o prefixo implicitamente.
  Classificador continua `gemini-2.5-flash-lite` (prompt melhorado primeiro).
- **Persona:** Zapin mantém o calor, **sotaque mineiro removido** (decisão
  pós-Conversa 34) — prompt e mensagens do gate Go atualizados juntos.

## 5. Semântica nova do Contrato A

- `fontes` agora significa "**documentos recuperados** para o turno" (antes:
  "fontes que a tool devolveu"). A citação em texto continua vindo do modelo,
  mas só sobrevive se corresponder a um desses documentos.
- Coalescência: o gateway envia ao `/chat` a rajada inteira num único
  `mensagem` (linhas separadas por `\n`).

## 6. Config nova (defaults)

| Var | Default | Lado | Para quê |
|-----|---------|------|----------|
| `GATE_FALHA_TTL` | `1h` | Go | F1 — expiração do estado de falha |
| `DEBOUNCE_JANELA` | `8s` | Go | F11 — janela de silêncio da rajada |
| `DEBOUNCE_TETO` | `20s` | Go | F11 — espera máxima acumulada |
| `EVENTO_IDADE_MAX` | `10m` | Go | F10 — descarte de reentrega antiga |
| `REPLY_MAX_CHARS` | `900` | Go | F12 — divisão de respostas longas |
| `LLM_TIMEOUT` | `45` (s) | Py | timeout por chamada < BRAIN_TIMEOUT |
| `CHECKPOINT_DB_PATH` | `./data/checkpoints.db` | Py | histórico persistente (volume `brain-data` no compose) |
| `OPENROUTER_MODEL` | `google/gemini-3.5-flash` | Py | novo default do agente |

## 7. Riscos residuais assumidos (com os olhos abertos)

- **F6 (off-topic com pretexto)**: não é confiavelmente classificável. A defesa
  é econômica (respostas curtas, teto de iterações) + prompt. Aceito porque o
  blast radius é custo de tokens, nunca dados.
- **Coalescência de 8s** adiciona 8s de latência percebida a toda mensagem —
  trade-off aprovado para WhatsApp (elimina resposta duplicada/mesclada).
- **Classificador fail-open** continua: com ele fora do ar, seguram a
  heurística determinística, o prompt e a política de tools.

## 8. Pendências (fora deste bloco)

- **Teto de turnos por conversa** (decisão antiga: 15): NÃO implementado —
  conversa de WhatsApp/Chatwoot é longeva e um teto absoluto viraria um novo
  "F1" (conversa morta para sempre). Precisa decidir a semântica de reset
  (por dia? por reabertura?) antes de implementar; melhor lugar: gateway.
- **M1**: extrair nº da NF da chave de 44 dígitos como helper determinístico no Go.
- **Ontologia enxuta** (glossário/taxonomia de intenções/personas) — fase 2.
- **Langfuse**: secret do projeto certo (`pk-lf-ec0b46cf…`) para leitura de
  custo/latência — INFRA-1 do laudo.
- **Reingestão do RAG não é necessária** (chunking não mudou), mas o próximo
  deploy precisa de `pip install` novo (`langgraph-checkpoint-sqlite`).
- Re-rodar a bateria do laudo (mesmo roteiro) como suíte de regressão para
  medir o antes/depois — inclusive os itens "não testados" (§8 do laudo).
