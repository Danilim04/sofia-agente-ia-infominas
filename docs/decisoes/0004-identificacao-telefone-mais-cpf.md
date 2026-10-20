# 0004 — Identificação por telefone + CPF/contrato validado por código

- **Estado:** aceita (2026-10-19)
- **Etapa:** planejamento (E2.3, E3.1)
- **Pendências relacionadas:** P2

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
