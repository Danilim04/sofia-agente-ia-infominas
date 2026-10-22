# 0008 — Revisão de segurança da v1

- **Estado:** aceita (2026-10-22)
- **Etapa:** E4.5 (correções em E4.5a gateway, E4.5b cérebro, E4.5c esteira; resíduos em E4.6)
- **Pendências relacionadas:** P10 (infra), A1 (homologação), regra 5 do `CLAUDE.md`

## Contexto

A E4.5 pede uma revisão de segurança da `main` antes da homologação, com o
checklist OWASP LLM Top 10 da skill `revisar-pr`. A revisão foi feita sobre a
`main` em `87102ad` (já com o cadastro de solicitante da E5.3) por um agente
`revisor` somente leitura, sem rede: leitura do código ao lado dos testes,
`go test ./...`, `go test -tags e2e ./internal/e2e/...`, `pytest`, e sondas
locais contra o schema das tools, o guardrail de saída, a transcrição enviada
ao JEV e o checkpointer.

O que está **correto e com evidência**: nenhum argumento de sessão no schema
do LLM e todas as 8 tools pela política; duas fases checadas por código;
idempotência de ticket (insert antes do POST, um único ponto de criação, sem
retry, reconciliação por listagem, tool calls paralelas cobertas); HMAC sobre
`timestamp.corpo` em tempo constante, janela de 5 min, dedup por id de evento,
`organizationId` conferido; erro do Infodesk, do LLM e do JEV nunca cru ao
cliente nem ao prompt; dado externo embrulhado; `/tools/infodesk/*` com token
em tempo constante e posse de ticket por código; nenhum segredo real no git.

## Decisão

### Bloqueantes para homologação (corrigir antes de qualquer cliente real)

| Id | Achado | Correção |
|---|---|---|
| B1 | Depois do "sim", o chamado é criado e a conversa já é transferida dentro da tool; a resposta do cérebro com o número é descartada porque o bot não conduz mais. O cliente nunca recebe o número | E4.5a: o gateway envia uma mensagem determinística com o número, na persona, antes do handoff; o E2E passa a exigir isso (`E2E_ESTRITO=1`) |
| B2 | O guardrail de saída aceita afirmações de ação concluída fora do padrão literal ("chamado nº N foi aberto", "transferi você", "cadastro concluído") e qualquer tool bem-sucedida no turno libera qualquer afirmação | E4.5b: afirmação ancorada no tipo de tool que a sustenta; número de chamado citado tem de constar no resultado de tool do turno |

### Importantes (antes do piloto ampliado)

| Id | Achado | Correção |
|---|---|---|
| I1 | Checkpoint do cérebro guarda telefone, nome e os dados digitados no cadastro em claro (só o CPF era mascarado) | E4.5b: telefone e nome fora do estado persistido; máscara por chave para nome, e-mail e empresa, e por texto para e-mail e telefone |
| I2 | Gateway guarda nome e e-mail de cadastro para sempre | E4.5a: zerar nome e e-mail ao concluir; expirar propostas vencidas |
| I3 | Nome completo digitado no cadastro vai em claro ao JEV, ao fallback LLM e ao Langfuse | E4.5b: o nome do cadastro entra na lista de nomes conhecidos da máscara |
| I4 | Segredo do webhook vazio desliga o HMAC só com um aviso | E4.5a: fail-closed; exceção só com `WEBHOOK_SEM_AUTENTICACAO=true` de dev |
| I5 | Os checks obrigatórios `contratos` e `e2e` ficam verdes sem rodar nada; `.env.example` na allowlist do gitleaks | E4.5c: jobs rodam os testes de verdade; brecha do gitleaks fechada |
| I6 | Lista de testadores vazia põe todos os clientes na Sofia, em silêncio | E4.5a: modo aberto só com `SOFIA_PRODUCAO_ABERTA=true`; modo no `/health` e alerta no boot |
| I7 | `/chat` do cérebro não exige autenticação; quem alcançar a porta interna personifica qualquer telefone | E4.5a envia `X-Brain-Token`; E4.5b exige, fail-closed |

### Menores (endurecimento)

Erro cru em log WARN onde o Infodesk pode ecoar nome (M1), nome de perfil do
WhatsApp sem limpeza no prompt e sem limite de tamanho (M2), dependências sem
lock e ferramentas de CI sem versão fixa (M3), sem teto de lote nem limite de
turnos por telefone (M4), tentativas de cadastro contadas por conversa (M5),
retry do `/chat` que pode exigir nova confirmação (M6), sessão permissiva em
quatro endpoints de tool (M7), `abrir` sem conferir a proposta gravada no
`preparar` (M8), resíduos do Zapin em `deploy/` e `infra/` (M9). M1, M4, M5,
M7 e M8 vão na E4.5a; M2 na E4.5b; M3 na E4.5c; M9 na E4.6; M6 fica como
risco aceito (o sentido da falha é seguro: não abre chamado).

### Decisões de projeto tomadas nesta revisão

1. **Telefone em claro no gateway é aceito.** Ele é a chave de negócio da
   conversa, da posse de ticket e da lista de testadores. A proteção é do
   ambiente: volume do banco cifrado, acesso restrito ao host e backup
   protegido. Nome e e-mail não ficam (I2).
2. **O número do chamado chega ao cliente por mensagem fixa do gateway**, não
   pelo texto do LLM (B1). O texto do LLM sobre a abertura deixa de ser a
   fonte da confirmação.
3. **Fail-closed por padrão** em toda autenticação entre componentes (I4,
   I7): segredo ausente impede o boot, salvo flag explícita de dev.

## Consequências

- A homologação só começa com B1, B2, I4, I5 e I6 na `main`.
- O `deploy/README.md` e o caderno de decisões passam a exigir volume cifrado
  e acesso restrito para o banco do gateway (decisão 1).
- O que a revisão não verificou fica para o smoke em produção (E5.6):
  comportamento real do Infodesk em 4xx de `POST /requesters`, spans reais do
  Langfuse, Postgres do gateway com `PG_TEST_URL`, proxy reverso expondo só
  `/webhook`, e o que acontece com conversas do bot quando o gateway está fora
  do ar.

## Alternativas descartadas

- Adiar o handoff pós-ticket para depois da resposta do cérebro (B1): depende
  do LLM escrever o número certo e do turno não falhar; a mensagem fixa não.
- Manter o telefone fora do disco do gateway: exigiria outra chave de
  correlação com o Infodesk e quebraria o lookup e a lista de testadores.
