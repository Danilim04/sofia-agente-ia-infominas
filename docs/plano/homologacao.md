# Homologação da Sofia (E5.6)

Roteiro para testar a Sofia com pessoas, em duas partes: primeiro na máquina
de dev, contra o Infodesk falso, depois em produção com a lista de
testadores. Pré-requisito: as decisões de [`caderno-de-decisoes.md`](caderno-de-decisoes.md),
blocos A e C.

## Parte 1 — Teste local com os casos reais

Objetivo: a equipe conversa com a Sofia como cliente e confere as respostas,
sem tocar no Infodesk real.

1. Garanta `OPENROUTER_API_KEY` no `agente-ia/.env` (e, se tiver,
   `TYPESAFE_API_KEY`).
2. Suba a stack: `scripts/start-all.sh --chainlit` (detalhes em
   [`ambiente-dev.md`](ambiente-dev.md)).
3. Abra a UI do mock em <http://localhost:9000>: escreva como cliente de
   WhatsApp e veja, do lado do Infodesk, o ticket, a nota interna e o handoff.
   O Chainlit em <http://localhost:8000> mostra a classificação e as ações de
   cada turno.
4. Use os casos reais como roteiro. Cada arquivo em
   `agente-ia/evals/conversa/real-*.yaml` traz as mensagens do cliente e o
   comportamento esperado em `descricao`. Os de `pendentes/` dependem da
   taxonomia real (A6) e entram depois dela.

| Caso | O que conferir |
|---|---|
| `real-internet-caiu-empresa` (pendente de A6) | reclamação → proposta → "sim" → ticket com o número na mensagem → handoff |
| `real-nao-identificado-informa-cpf` (pendente de A6) | telefone desconhecido → cadastro em duas fases → ticket |
| `real-status-com-numero` | consulta o status sem abrir outro chamado |
| `real-status-sem-numero` | usa a lista de chamados do cliente |
| `real-pede-atendente` | transfere sem insistir |
| `real-cliente-manda-audio` | áudio não vira chamado; pede texto |
| `real-fora-de-escopo` | redireciona, sem tool |
| `real-injecao-de-prompt` | resposta padrão, sem obedecer |
| `real-pede-senha-de-terceiro` | não atende e termina em atendente |

Registre cada resposta estranha com o telefone de teste, o horário e o texto.
O log do cérebro e do gateway fica em `scripts/logs/`. O guia
[`operacao.md`](operacao.md) diz o que olhar em cada tipo de falha.

## Parte 2 — Piloto em produção com a lista de testadores

### Configuração

1. Deploy conforme [`deploy/README.md`](../../deploy/README.md), com:
   - `INFODESK_BASE_URL=https://infodesk.infominas.net/v1`
   - `TESTADORES_TELEFONES=` com os números do piloto (A1)
   - `SOFIA_PRODUCAO_ABERTA=false`
   - `DEPARTAMENTO_HUMANO_PADRAO`, `HORARIO_HUMANO` e os valores do bloco B
   - `STRICT_TAXONOMIA=true` depois do PR da taxonomia real
2. `deploy/healthcheck.sh https://<domínio>`: todos os portões verdes, e o
   `/health` do gateway mostra o modo `piloto` com a contagem de números.
3. No Desk, cadastre a URL do webhook e os eventos e use **Enviar teste**: o
   `ping` tem de voltar 200.
4. Só então ligue **Conduz o atendimento** na chave da Sofia.

### Smoke: 8 cenários com um número da lista

| # | Cenário | Esperado |
|---|---|---|
| 1 | "a internet caiu aqui na empresa", depois "sim" | ticket na mesa Abertura de Ticket, número enviado ao cliente, handoff com nota interna |
| 2 | "quero falar com um atendente" | aviso e handoff para o departamento padrão |
| 3 | áudio | pede texto; nenhum ticket |
| 4 | "qual o status do chamado <número do cenário 1>?" | status do chamado, sem abrir outro |
| 5 | número de teste sem cadastro: descreve o problema e informa nome e empresa | cadastro confirmado em duas fases, depois o ticket |
| 6 | atendente assume a conversa pelo inbox | a Sofia para de responder |
| 7 | mensagem fora do horário humano | a Sofia atende; o aviso de transferência diz o horário da equipe |
| 8 | um número **fora** da lista manda mensagem | nenhuma resposta da Sofia; a conversa cai na fila com a nota "fora do grupo de teste" |

### Checklist de aceite

- [ ] Os 8 cenários passaram, cada um com o horário e o número do chamado anotados.
- [ ] Nenhum alerta `sofia_alerta` sem explicação no período.
- [ ] Nenhum CPF, telefone ou nome em claro nos logs INFO (amostra de 20 linhas).
- [ ] O reconciliador rodou uma hora sem turno duplicado nem perdido (E5.4).
- [ ] Os alertas chegam no canal do responsável.
- [ ] Assinatura do responsável pela Infominas: ______________ Data: ________

### Como desligar na hora

Desligue **Conduz o atendimento** na chave da Sofia no Desk. As conversas
novas voltam ao fluxo normal da equipe na mesma hora. Depois, investigue com
calma pelos logs e pelo `/health`.
