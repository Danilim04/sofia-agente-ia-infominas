---
name: cadastro-solicitante
description: Como a Sofia cadastra um solicitante que o lookup do telefone não achou — fluxo em duas fases (preparar → confirmar → cadastrar), validações por código, motivos de erro, onde cada peça vive (Python, Go, mock, contratos, evals) e o que segue sem decisão. Carregue ao mexer em preparar_cadastro_solicitante / cadastrar_solicitante, no endpoint /tools/infodesk/cadastro/*, em POST /requesters, no bloco "identificação" do prompt ou ao decidir IDENT_EXIGIR_CPF.
---

# Cadastro de solicitante pela Sofia

Decisão: ADR 0009 (2026-10-22, produto). Catálogo: `docs/plano/01-ferramentas.md` A8.
API do Infodesk: `docs/infodesk/04` (fluxo do PDF) e `10` §6 (idempotência).
Princípio: o LLM só repassa o que o cliente disse; **código** valida, escolhe o cliente e executa.

## Quando acontece

Telefone **sem** solicitante no `GET /contacts/lookup` (o engine não preenche `contato`) e cliente
ainda não identificado. Quem já tem cadastro **não** é cadastrado de novo: o gateway responde
`ja_cadastrado` e o caminho volta a ser CPF/contrato (`informar_identificacao`).

## Fluxo (três turnos)

1. **Coletar** (só conversa): nome completo, empresa, e-mail opcional — um dado por vez.
2. **`preparar_cadastro_solicitante(nome, empresa, email="")`** — dry-run. Política valida; gateway
   confere o lookup, busca `GET /clients?q=`, **escolhe o candidato por código** (único, ou nome
   idêntico ao digitado, sem acento/caixa) e grava a proposta em `cadastros`. Devolve
   `{nome, email, empresa{nome}, telefone_mascarado, chave}`; nunca `clientId` nem telefone completo.
   A Sofia mostra o resumo e pede "sim".
3. **`cadastrar_solicitante()`** — sem argumentos do LLM, no turno **seguinte**, só com confirmação
   explícita (`CONFIRMACOES_EXPLICITAS`; "esse" não vale). `POST /requesters {name, clientId, phone,
   email?, conversationId}` UMA vez. Depois: `contatos` (origem `cadastro`, `requester_id`,
   `cliente_id`) e estado `identificacao = {forma: "cadastro", valor_mascarado, requester_id}`.
   Daí `preparar_abertura_chamado` passa a precondição `exigir_identificado`.

## Validações (Python e Go repetem as mesmas)

| Campo | Regra | Onde |
|---|---|---|
| nome | ≥ 2 palavras, só letras/acentos e `'` `-` `.`, ≤ 120 | `src/identity/cadastro.py`, `toolsapi/cadastro.go` |
| e-mail | opcional; `^[^@\s]+@[^@\s]+\.[^@\s]+$`, ≤ 254 | idem |
| empresa | não vazia, uma linha, ≤ 120; é texto de busca, nunca id | idem |
| telefone | **sempre o da sessão** (injetado); o que o LLM mandar é sobrescrito | `tool_policy.POLITICAS` |
| cliente | escolhido pelo gateway entre os candidatos da busca; sem `clientId` não cadastra | `escolherCliente` |

## Motivos de erro (`contratos/tools-infodesk/cadastro/`)

`preparar`: `campo_invalido`, `ja_cadastrado`, `empresa_nao_encontrada`, `empresa_ambigua`
(`candidatos` = só nomes), `cadastro_transferido`, `escopo_insuficiente`, `infodesk_indisponivel`.
`cadastrar`: `sem_proposta`, `escopo_insuficiente`, `infodesk_indisponivel`.
Erro de negócio é HTTP 200 `{status:false, erro, motivo}`; o texto cru do Infodesk nunca chega ao LLM.

## Hipóteses configuráveis (sem decisão do produto, `TODO(decisão do produto)`)

| Variável | Default | O que faz |
|---|---|---|
| `CADASTRO_MAX_TENTATIVAS` | `2` | falhas de **empresa** (não achada/ambígua) por conversa antes de desistir; erro de campo não conta |
| `CADASTRO_SEM_CLIENTE` | `transferir` | `transferir` = handoff com nota por código + aviso fixo (`MsgCadastroSemEmpresa`); `oferecer` = só orienta |
| `IDENT_EXIGIR_CPF` | `false` | **segue ABERTO** (`docs/infodesk/07` N3): com `true` o cadastro não dispensa o CPF (contato fica sem `forma`) |

## Onde cada peça vive

| Peça | Arquivo |
|---|---|
| Tools + docstring (instrução ao LLM) | `agente-ia/src/tools/infodesk_tools.py` |
| Política (injeção, validação, confirmação, proposta lida do **histórico**) | `agente-ia/src/agent/tool_policy.py` |
| Efeito no estado | `agente-ia/src/tools/efeitos.py` (`_efeito_cadastro`) |
| Prompt (fluxo na persona) | `agente-ia/src/agent/prompts.py`, seção "Cadastro do cliente" |
| Handlers Go | `backend/internal/toolsapi/cadastro.go` |
| Cliente Infodesk | `backend/internal/infodesk/cadastro.go` (`Clientes`, `CriarSolicitante`) |
| Store (os dois backends) | `cadastros`, `cadastro_tentativas` em `backend/internal/store` |
| Contratos | `contratos/tools-infodesk/cadastro/{preparar,cadastrar}/` |
| Mock | `mock-infodesk/` (`GET /clients`, `POST /requesters` idempotente, `created`) |
| Evals | `agente-ia/evals/conversa/cadastro-*.yaml` (conversa-080…085) |

## Pegadinhas

- **A proposta não está no estado.** `state.py` não tem campo para ela: a política lê o último
  `ToolMessage` de `preparar_cadastro_solicitante` que o gateway aceitou (`_cadastro_preparado`).
  Recusa da política não consome nem desarma; `cadastrar` executado (ok ou falha) consome.
- **Validade de 15 min** é conferida no gateway (a mensagem do histórico não tem relógio).
- `contatos.forma` do gateway fica `telefone` (origem `cadastro`) para o engine e o `preparar`
  de ticket reconhecerem o telefone sem mudar o Contrato A; `identificacao.forma = "cadastro"` é só
  do estado do cérebro.
- `POST /requesters` é idempotente no Infodesk; o gateway mesmo assim guarda a chave local e não
  repete um cadastro `feito`. **Nunca** retry automático no cliente HTTP.
- Chave de API precisa do escopo `contacts:write` (a `sofia-agente-ia` tem `*`). Sem ele:
  `escopo_insuficiente` + alerta `configuracao_infodesk`.
- Dado do cliente é PII: nome e e-mail ficam em `cadastros` (SQLite/Postgres) e no `ToolMessage`;
  log INFO só leva ids, nunca nome, e-mail nem telefone.
