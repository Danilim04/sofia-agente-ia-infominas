# 0004 — Identificação por telefone + CPF/contrato validado por código

- **Estado:** aceita (2026-10-19); **revisada em 2026-10-20** (seção "Revisão" abaixo; a decisão original segue válida como fallback)
- **Etapa:** planejamento (E2.1, E2.3, E3.1, E5.3)
- **Pendências relacionadas:** P2 (respondida em 2026-10-19), `docs/infodesk/07` N2, N3

## Revisão 2026-10-20 — API v1: `GET /contacts/lookup?phone=` existe

A rota que o ADR previa como P2 está documentada (`docs/infodesk/04`):
devolve `requesters[]` (pessoa + `clientId` + `client.name`), `clientIds`,
`openTickets` e a conversa ativa do número. Decisão acrescentada:

1. **Primeira tentativa é o lookup**, feito pelo **gateway** no primeiro
   turno do episódio (não é tool; o LLM não participa). Exatamente **um**
   solicitante → identificado: `contatos{origem: lookup, requester_id,
   cliente_id, nome_cadastro}` e `contato{forma: "telefone"}` no Contrato A.
2. Com `IDENT_EXIGIR_CPF=false` (default; **hipótese de plano**, `07` N3),
   telefone conhecido **dispensa** o CPF. Com `true`, o lookup só preenche
   `requester_id`/`cliente_id` e a tool `informar_identificacao` continua
   obrigatória — é o comportamento original deste ADR.
3. Lookup vazio, ambíguo (>1 solicitante; `IDENT_LOOKUP_AMBIGUO=pedir_cpf`)
   ou com erro → **fallback = decisão original** (CPF/contrato validado por
   código, hash+sal, TTL 24 h). Nunca bloqueia o atendimento.
4. `POST /requesters` (cadastrar quem o lookup não acha a partir do que o
   cliente disse) fica **fora da v1**: cria dado no Infodesk sem validação
   externa; vira tool nova com ADR se a Infominas pedir (E5.3).
5. `requester_id` do lookup alimenta `POST /conversations/:id/requester`
   antes de abrir o ticket e a **posse** em `consultar_status_chamado`.

Consequências: cliente cadastrado não digita CPF; o risco "CPF válido de
outra pessoa" só existe no fallback; `GET /clients?document=` **não** é usado
para confrontar CPF (busca empresa, não pessoa). O texto original segue.

---

## Contexto

A Infominas tem cadastro de clientes vinculado ao telefone dentro do
Infodesk, mas a API documentada não expõe busca de cliente (verificado em
`docs/infodesk/`). O ticket aceita `clientId`/`requesterId` opcionais e
`contactPhone`. Decisão do cliente: pedir CPF ou nº de contrato antes de abrir;
não identificado não abre ticket.

## Decisão

O telefone do webhook é a chave da sessão. Antes de `preparar_abertura_chamado`
a política exige `identificacao` no estado, obtida pela tool
`informar_identificacao(cpf|contrato)`: o LLM extrai, o **código valida**
(dígitos verificadores do CPF em `src/identity/cpf.py`; regex configurável
para contrato) e o gateway guarda por telefone com TTL 24 h (`contatos`:
hash+sal e valor mascarado — nunca o CPF em claro). O rodapé do ticket leva o
valor mascarado. Quando existir rota de busca (P2), o gateway resolve
`clientId` pelo telefone e `IDENT_EXIGIR_CPF=false` dispensa a pergunta.

## Consequências

- Cliente recorrente não repete o CPF por 24 h.
- Sem validação externa, um CPF válido de outra pessoa passa — risco aceito
  até P2; o ticket carrega o telefone real do WhatsApp como segunda evidência.
- LGPD: CPF só mascarado/hash em disco e logs; em claro apenas em memória no
  turno.

## Alternativas descartadas

- Abrir ticket só com telefone: contraria a decisão do cliente (resposta 10).
- Transferir para humano todo não identificado: elimina o valor do bot.
