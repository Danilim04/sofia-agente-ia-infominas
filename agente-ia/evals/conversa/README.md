# Evals de conversa (determinísticos)

Cada `*.yaml` desta pasta é UM comportamento da Sofia provado contra o **grafo
real** (`build_graph` com `get_default_tools()`): o LLM é scriptado, o JEV é o
`FakeJEV`, e o gateway é um `httpx.post` falso que devolve os JSON de
`contratos/tools-infodesk/`. Sem rede, sem chave, sem `.env`. O nome do arquivo é o
comportamento (`esse-nao-confirma-abertura.yaml`); todo cenário traz também o
**negativo** (o que NÃO pode acontecer).

> O LLM é scriptado de propósito: estes cenários não medem a qualidade da conversa
> (isso é o juiz LLM, E4.4) — medem o que é **código**: política das tools, duas
> fases, roteamento da classificação, guardrails, escopo da sessão, persona fixa.
> Um cenário "passa" quando o código segura mesmo um LLM que erra (ex.: tenta abrir
> sem confirmação, obedece a injeção do RAG, passa o telefone de outra pessoa).

## Rodar

```bash
cd agente-ia
python -m pytest -m evals_pr evals/            # o que bloqueia o merge (job evals-pr do CI)
python -m evals.conversa                       # tabela + falhas, exit 1 se algo quebrar
python -m evals.conversa --relatorio evals/relatorios/conversa.md   # + relatório em Markdown
python -m evals.conversa --sem-executar        # só valida os YAMLs
```

`pytest` roda um teste por cenário (`test_cenario_executa[conversa-0NN]`) e mais os
de higiene (`test_cenarios.py`: ≥ 30 cenários, os comportamentos do plano, todo turno
com espera de comportamento) e do runner (`test_extensoes.py`: o runner reprova
cenário errado e resposta fora da persona).

## Formato

O de `TEMPLATE.yaml` (validado por `evals/formatos.py`) mais as **extensões** de
`extensoes.py`, que ficam só aqui:

| Onde | Chave | Para quê |
|---|---|---|
| cenário | `relogio` | "agora" (ISO 8601 com fuso): validade da proposta e período do dia |
| turno | `avanca_minutos` | o relógio anda antes do turno (proposta vencida) |
| turno | `rag: [{texto, source?, secao?}]` | chunks do retriever neste turno (ausente = base vazia) |
| turno | `gateway: {caminho: arquivo}` | resposta do gateway neste turno: `erro-*` do contrato, `http_500` ou `timeout` |
| passo do `llm` | `paralelo: [{tool, args}, …]` | várias tool calls na MESMA mensagem do LLM |
| `espera` | `prompt_contem` / `prompt_nao_contem` | trechos do system prompt que o LLM recebeu |
| `espera` | `llm_chamadas` | quantas vezes o LLM foi chamado (0 = barrado antes) |
| `espera` | `gateway_json: {endpoint, campos}` | o gateway recebeu esses campos (ex.: `telefone` da sessão) |
| `espera` | `gateway_nao_recebeu`, `gateway_vezes` | endpoints que não podem ser chamados / quantas vezes |
| `espera` | `estado`, `estado_nao_contem` | subconjunto do estado do grafo; PII que não pode estar nele |

Os atalhos de `sessao.contato` (`telefone`, `cpf`, `contrato`, `null`) simulam o
`contato` que o gateway manda no Contrato A. `chama` conta só tools que **executaram**
(recusa da política não conta); `nao_chama` idem.

## Persona em todo cenário

Toda bolha da resposta final de todo turno passa por `runner.verificar_persona`: no
máximo 1 emoji, sem "prezado/sua solicitação", sem prometer prazo ("em até N", "prazo
de"), sem CAPS. Vale também para as respostas fixas do código (`RESPOSTA_OFF_TOPIC`,
`RESPOSTA_ERRO_INTERNO`, `RESPOSTA_ACAO_SEM_FERRAMENTA`).

## `xfail`: bug de produto, não "teste difícil"

Um cenário que descreve o comportamento **correto** que o código ainda não tem leva
`xfail: "motivo, citando o arquivo em src/"`. É `strict`: quando o bug for corrigido o
cenário passa, o teste falha e o marcador tem de sair. Hoje:
`abrir-incerto-frase-natural-nao-e-trocada` (guardrail de ação sem ferramenta) e
`abrir-paralelo-posta-uma-vez` (tool calls paralelas).

## Fora daqui

- `pendentes/`: cenários parafraseados do histórico real, aguardando validação humana;
  o runner não os carrega.
- Juiz LLM com rubrica (LLM real): `evals/conversa/juiz/` (E4.4, noturno).
- Transferência por mídia/áudio, fora do horário, vigia: decididas no gateway Go
  (`backend/internal/engine`); aqui só o que o cérebro faz com o texto que chega.
