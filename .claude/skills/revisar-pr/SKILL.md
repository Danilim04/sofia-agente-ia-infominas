---
name: revisar-pr
description: Checklist do revisor de PR deste repositório (camada 3 da esteira) — escopo/zona, contratos, segurança OWASP LLM, idempotência, determinismo, testes, persona, docs — e como combinar com /code-review e /security-review. Carregue ao revisar qualquer PR ou diff, antes de aprovar ou pedir mudanças.
---

# Revisar um PR

Você é a última barreira antes do humano. As camadas 1 e 2
(`docs/plano/04-esteira-qualidade.md`) já rodaram no CI: **não** repita lint;
procure o que máquina não pega.

## Procedimento

1. Leia o PR: título com `[E<n>.<m>]`, zona declarada, pendências tocadas. Abra
   a etapa em `docs/plano/03-etapas.md` e compare o **critério de pronto** com
   o que o PR prova.
2. `git diff --stat main...HEAD`: algum arquivo fora da zona? → pedir
   justificativa ou split.
3. Rode `/code-review` (bugs de correção) e, se o diff toca `tool_policy.py`,
   `toolsapi`, `webhook`, `infodesk`, `security/` ou prompts, `/security-review`.
4. Percorra o checklist abaixo lendo o diff **com os testes ao lado**.
5. Escreva o comentário no formato do fim desta skill. Aprovação sua não
   substitui a humana (D13).

## Checklist

**Escopo e contratos**
- [ ] Só a etapa; nada "aproveitando" para mexer em outra coisa.
- [ ] Mudou JSON em `contratos/` ou Contrato A? Os dois lados e os testes de contrato mudaram no mesmo PR?
- [ ] `docs/plano/01` ou `02` precisam refletir a mudança? ADR se a decisão mudou?

**Segurança (OWASP LLM Top 10, herdado do Zapin)**
- [ ] Nenhum arg de sessão/escopo no schema do LLM (`telefone`, `conversation_id`, `classificacao`, `proposta`, `identificacao`, mesa, departamento, prioridade).
- [ ] Tool nova tem `POLITICAS`; validador para todo arg que referencia número/enum/escopo.
- [ ] Efeito colateral em duas fases; `exigir` checa confirmação **por código**.
- [ ] Conteúdo externo (RAG, respostas do gateway) entra como DADO (`<documento_externo>`/JSON), nunca como instrução.
- [ ] Erro do Infodesk/LLM/JEV não chega ao cliente nem ao prompt cru (A2).
- [ ] CPF, telefone, nome: mascarados em log INFO, hash em disco, nunca no estado enviado ao JEV/Langfuse em claro.
- [ ] HMAC em tempo constante sobre o raw body; segredos só por env.
- [ ] `output_guardrail` continua barrando "abri o chamado"/"transferi" sem tool executada.

**Idempotência e falhas**
- [ ] Retry só em operação idempotente; `POST /tickets` nunca repetido; chave de idempotência presente.
- [ ] Falha classificada (transitória × definitiva) e o caminho de transferência é o único (`transferencia.go`).
- [ ] Timeouts: JEV 2 s, Infodesk 15 s, cérebro `BRAIN_TURNO_PRAZO` < `TURNO_TETO` < `BRAIN_TIMEOUT`.

**Determinismo**
- [ ] Fila, prioridade, confirmação, posse de ticket, validação de CPF: código ou tabela, não LLM.
- [ ] Limiar/pergunta do JEV mudou? Há resultado de eval no PR?

**Testes**
- [ ] Cada recusa de política e cada `motivo` de erro tem teste.
- [ ] Mudança em `store.Store` cobre SQLite e Postgres.
- [ ] Fakes compartilhados (`tests/fakes.py`, `fakeInfodesk`) em vez de cópias.
- [ ] `legado-permitido.txt` só encolheu.

**Persona e produto**
- [ ] Textos novos: Sofia, pt-BR, "você", ≤ 1 emoji, sem prometer prazo, sem causa-raiz por suposição; iguais nos dois lados (prompt e gateway).
- [ ] Mensagens ao cliente em falha dizem o que acontece a seguir (atendente, chamado), sem jargão.

## Formato do comentário

```
## Revisão [E<n>.<m>] — <aprovar | mudanças necessárias>

**Bloqueantes** (impedem merge)
- arquivo:linha — problema → o que fazer

**Importantes** (corrigir neste PR salvo justificativa)
- ...

**Sugestões**
- ...

**Verificado**: critério de pronto ✔/✘ · zona ✔/✘ · contratos ✔/n.a. · segurança ✔ · evals ✔/n.a.
```
