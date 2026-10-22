# 0009 — Cadastro de solicitante pela Sofia

- **Estado:** aceita (2026-10-22, decisão do produto); substitui o item 4 da "Revisão 2026-10-20" do ADR 0004
- **Etapa:** E5.3
- **Pendências relacionadas:** `docs/infodesk/07` N3 (cadastro respondido; `IDENT_EXIGIR_CPF` segue aberto), T4, T10

## Contexto

O ADR 0004 (revisão) deixou `POST /requesters` fora da v1 ("vira tool nova com ADR se a Infominas pedir").
O produto pediu: telefone que `GET /contacts/lookup` não acha é cadastrado pela Sofia, por uma sequência
de perguntas, antes de abrir o chamado (fluxo do PDF em `docs/infodesk/04`).

## Decisão

**Substitui o item 4 do ADR 0004:** o cadastro entra na v1 como duas tools em duas fases (A8 em
`docs/plano/01-ferramentas.md`; skill `cadastro-solicitante`):

1. `preparar_cadastro_solicitante(nome, empresa, email="")` — dry-run. O LLM entrega o que o cliente
   disse; o **código valida** (nome com ≥ 2 palavras só de letras; e-mail por regex, opcional; empresa
   não vazia), confere no lookup que o telefone ainda não tem cadastro e busca a empresa com
   `GET /clients?q=`. Quem escolhe o cliente é o **gateway**, entre os candidatos da própria busca (um só,
   ou nome idêntico ao digitado). O LLM nunca passa `clientId` nem telefone.
2. `cadastrar_solicitante()` — sem argumentos do LLM, no turno **seguinte**, com confirmação explícita
   checada por código (`CONFIRMACOES_EXPLICITAS`, validade de 15 min): `POST /requesters {name, clientId,
   phone, email?, conversationId}`. O telefone é o da sessão. O gateway só executa o que ele gravou no
   `preparar`.
3. Depois, `contatos` guarda `requester_id`/`cliente_id` (origem `cadastro`), o cérebro fica com
   `identificacao.forma = "cadastro"` e `exigir_identificado` passa (sujeito a `IDENT_EXIGIR_CPF`).

Escopo novo: **`contacts:write`** (a chave `sofia-agente-ia` tem `*`; sem ele a tool devolve
`escopo_insuficiente` e o gateway alerta). `POST /requesters` é idempotente por telefone/e-mail no mesmo
cliente (`docs/infodesk/10` §6); mesmo assim o gateway grava a chave local (`cadastros.chave`) e nunca
repete um cadastro concluído.

Hipóteses configuráveis (sem decisão do produto, `TODO(decisão do produto)`): `CADASTRO_SEM_CLIENTE`
(default `transferir`: empresa não achada ou ambígua após `CADASTRO_MAX_TENTATIVAS`=2 → handoff com nota
por código; `oferecer` só orienta) e `CADASTRO_MAX_TENTATIVAS`. **Nunca** cadastra sem `clientId`.

## Consequências

- Cliente novo abre chamado sem CPF; o nome/empresa são declarados, não verificados: risco aceito.
- O ticket herda `requesterId`/`clientId` da conversa (T11) e chega com vínculo.
- Nova superfície de escrita: coberta por política, testes de recusa e evals `cadastro-*`.
- Risco aceito, mitigado: a busca de empresa exige ≥ 4 letras/dígitos e só mostra nomes em `empresa_ambigua`; um número desconhecido ainda pode sondar a carteira (limite de tentativas por conversa). `TODO(decisão do produto)`.

## Alternativas descartadas

- LLM escolhendo o `clientId`: viola a regra 1. Cadastro sem cliente ou criando cliente: não decidido.
- Cadastro numa fase só: efeito colateral sem confirmação explícita (regra 3).
