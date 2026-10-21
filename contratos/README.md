# contratos/ — JSON de exemplo dos contratos Go ↔ Python

Fonte de verdade dos dois contratos que ligam o gateway Go (`backend/`) ao
cérebro Python (`agente-ia/`), na forma de **exemplos JSON carregados pelos
testes de contrato dos dois lados** (ADR 0006):

- **Contrato A** (`contrato-a/`): gateway → cérebro. `POST /chat`,
  `POST /encerrar-episodio`, `POST /classificar`. Lido por
  `backend/internal/brain` (Go) e `agente-ia/server.py` (Python).
- **Tools do Infodesk** (`tools-infodesk/`): cérebro → gateway.
  `POST /tools/infodesk/<área>/<ação>`. Lido por
  `backend/internal/toolsapi` (Go, E3.1) e `agente-ia/src/tools` (Python,
  E2.3).

A especificação de cada campo está em `docs/plano/01-ferramentas.md`
(seção A por tool; bloco "Contrato A — mudanças"). Este diretório só a
materializa em valores concretos; se os dois divergirem, o 01 está errado ou
o JSON está errado, e o PR corrige os dois.

## Convenção de nomes

```
contratos/
  contrato-a/
    chat-request.json                  # corpo de POST /chat
    chat-response.json                 # resposta de sucesso (com classificacao e acoes)
    chat-response-erro.json            # resposta com `erro` preenchido
    encerrar-episodio-request.json / -response.json
    classificar-request.json / -response.json
  tools-infodesk/<área>/<ação>/        # = POST /tools/infodesk/<área>/<ação>
    request.json                       # corpo que o Python envia
    ok.json                            # resposta {status: true, ...}
    erro-<motivo>.json                 # um por motivo da coluna "Erros" do 01
    _notas.md                          # explicações (JSON não aceita comentário)
  validar.py                           # validador (stdlib)
```

Endpoints cobertos (seção A do 01):

| Tool Python | Endpoint | Erros (`erro-<motivo>.json`) |
|---|---|---|
| `informar_identificacao` | `contato/identificar` | `campo_invalido`, `nao_identificado` |
| `preparar_abertura_chamado` | `ticket/preparar` | `campo_invalido`, `categoria_sem_mapa`, `chamado_aberto`, `nao_identificado`, `referencia_indisponivel` |
| `abrir_chamado` | `ticket/abrir` | `sem_proposta`, `em_andamento`, `infodesk_indisponivel`, `escopo_insuficiente` |
| `consultar_status_chamado` | `ticket/consultar` | `nao_encontrado`, `posse_nao_verificada`, `infodesk_indisponivel` |
| `listar_meus_chamados` | `ticket/listar` | `chamados_indisponiveis` (hipótese, ver abaixo) |
| `transferir_para_atendente` | `conversa/transferir` | `ja_transferida`, `infodesk_indisponivel` |

## Regra: erro de negócio é HTTP 200

Toda recusa que o LLM precisa explicar ao cliente volta com HTTP **200** e o
envelope:

```json
{"status": false, "erro": "<texto pt-BR para o LLM explicar>", "motivo": "<codigo>"}
```

`erro` é frase curta em pt-BR na voz da Sofia (sem jargão, sem texto cru do
Infodesk); `motivo` é o código que a docstring da tool ensina o LLM a tratar.
Alguns motivos carregam campos extras (`chamado_aberto` → `chamados_abertos`). HTTP 400/401/405 ficam só para erro de
protocolo (JSON inválido, token, método) e nunca chegam ao LLM como texto.

## Valores de exemplo (consistentes entre arquivos)

| Dado | Valor | Observação |
|---|---|---|
| `conversation_id` | `6f1c2a3e-9b4d-4e7a-8c21-5d3f0a9b7e10` | uuid fictício |
| `telefone` | `+5531999990001` | fictício, E.164 |
| CPF | `12345678909` → `***.***.789-**` | fictício com DV válido; em claro só em `contato/identificar/request.json` |
| ticket `numero` | `"48213"` | texto no contrato; o Infodesk devolve `number` inteiro (T1 respondido) e o gateway converte |
| `chave_idempotencia` | sha256 real dos campos de `ticket/abrir/request.json` | ADR 0005 |
| mesa / departamento | `Suporte Técnico` / `Suporte` | nomes placeholder da taxonomia (P1) |

## Como validar

```bash
python -I contratos/validar.py
```

Só stdlib. Checa: todo JSON parseável; cada endpoint com `request.json`,
`ok.json` e ≥ 1 `erro-*.json`; todo `status:false` com `erro` e `motivo`;
`motivo` igual ao nome do arquivo e presente na lista do 01 para aquele
endpoint (tabela `MOTIVOS` no script); chaves de `request.json` e `ok.json`
iguais ao esquema mínimo (`ESQUEMA`); objetos aninhados que os dois lados
leem (`proposta`); e os arquivos do Contrato A com as chaves do 01. Compara
**chaves**, não valores: `null` é aceito onde a pendência manda.

Hoje o validador roda **só manualmente** (e nesta documentação). O job
`contratos` do CI ainda é um placeholder (`TODO(E1.6)` em `.github/`, fora da
zona desta etapa): a E2.3/E3.1 liga nele `python -I contratos/validar.py` e os
testes de contrato dos dois lados (`go test -run Contrato`, `pytest -m contrato`).

## Como adicionar ou alterar um contrato

1. Altere primeiro a linha da tool em `docs/plano/01-ferramentas.md` (ou o
   bloco do Contrato A). Sem linha no 01, não há contrato.
2. Crie/edite os JSON aqui (`request.json`, `ok.json`, um `erro-<motivo>.json`
   por motivo) e a `_notas.md` se algum campo precisar de explicação.
3. Atualize `validar.py` (`MOTIVOS`, `ESQUEMA`, `ANINHADO`, `ANINHADO_LISTA`, `EXTRAS_ERRO` ou
   `CONTRATO_A_ESQUEMA`) e rode-o.
4. No **mesmo PR**, ajuste o handler Go (`backend/internal/toolsapi` ou
   `internal/brain`) e a tool Python (`src/tools`, `tool_policy.py`) com seus
   testes de contrato. Um PR que muda só um lado não passa na revisão
   (checklist do `PULL_REQUEST_TEMPLATE.md`: "`contratos/` atualizado nos dois
   lados").
5. Se a mudança for de decisão (não só de campo), ADR novo em `docs/decisoes/`.

## Pendências que deixam campos como `null`

Nada aqui "resolve" um item de `docs/infodesk/07-pontos-ambiguos.md`; o
contrato mantém a chave com `null` e o código trata como hipótese
configurável.

| Campo | Arquivos | Pendência |
|---|---|---|
| `atualizado_em` (`null`) e `status_ticket` (`"desconhecido"`) | `ticket/consultar/ok.json`, `ticket/listar/ok.json` | **T1** — respondido em 2026-10-19 (`docs/infodesk/07`): `status` é objeto por mesa e `status_ticket` leva `status.name` literal; `"desconhecido"` e `null` ficam como valor de exemplo/fallback |
| `prioridade.id` | `ticket/preparar/ok.json`, `ticket/abrir/*.json` | **T3** — respondido em 2026-10-19: UUID resolvido por mesa (`GET /desks/:id`); `null` quando a mesa não tem o nome (o `priorityId` é omitido no POST) |
| `cliente_id`, `nome_cadastro` | `contato/identificar/ok.json`, `contrato-a/chat-request.json` | **T4 / P2** — respondido em 2026-10-19: `GET /contacts/lookup?phone=` preenche quando o telefone foi reconhecido; `null` no fallback por CPF/contrato. `contato.forma` ganhou o valor `"telefone"` (lookup) ao lado de `"cpf"` e `"contrato"` — mesma chave, valor novo |
| posse (`erro-posse_nao_verificada`) | `ticket/consultar` | **P3 / T5** — respondido em 2026-10-19: posse por código (tabela local, `conversationId`, telefone do ticket, `requester_id`); sem prova, nenhum campo do ticket sai |
| `identificacao` = `{forma, valor_mascarado}` | `ticket/preparar/request.json`, `ticket/consultar/request.json` | o 01 escreve o injetado como `{cpf\|contrato}`; o contrato manda a visão **mascarada** (regra 5 do CLAUDE.md) e o gateway resolve pela tabela `contatos` via `telefone` — hipótese, `TODO(E2.3)` confirmar e atualizar as linhas A2/A4 do 01 |
| `erro-chamados_indisponiveis` | `ticket/listar` | confirmado na E3.1 (a linha A5 do 01 o lista): as duas partes da lista (Infodesk e tabela local) falharam |
| `observacao` | `ticket/listar/ok.json` | texto fixo do gateway: `abertos no Infodesk + abertos por aqui` (ver `_notas.md`) |
| `classificar-*` | `contrato-a/` | o 01 não fixa o JSON de `POST /classificar`; formato é hipótese para a E2.2 (`TODO(E2.2)`) |
| `esclarecimento` | `contrato-a/classificar-response.json` | pergunta de esclarecimento gerada quando a categoria fica entre os limiares (02); `null` quando não há |

Quando um item for respondido pela Infominas: atualizar `docs/infodesk/`,
trocar o `null` pelo valor real nos JSON, ajustar `validar.py` se mudar chave,
e os dois lados no mesmo PR.
