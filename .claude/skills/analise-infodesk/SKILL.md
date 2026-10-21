---
name: analise-infodesk
description: Como medir, só com contagens e só com GET, como a Infominas usa o Infodesk hoje (tickets por mesa × canal, uso do catálogo, conversas por departamento) para responder as perguntas de roteamento da taxonomia da Sofia. Carregue ao decidir mesa/departamento/catálogo de uma categoria (E5.1), ao fechar itens de docs/infodesk/09 e 10 §8, ou ao reexecutar a análise com dados novos.
---

# Análise de uso do Infodesk (agregados, sem dado pessoal)

`scripts/analisar-infodesk.py` pagina `GET /tickets?status=all` e
`GET /conversations?tab=todas`, junto de `/desks`, `/desks/:id`, `/catalog`,
`/departments` e `/users`, e grava **só agregados**. Complementa
`scripts/descobrir-taxonomia.py` (que lê a *configuração*): este lê o *uso*.
Fatos da API: skill `infodesk-api` e `docs/infodesk/10`.

## Pré-requisitos

- `.env` na raiz (gitignored) com `INFODESK_BASE_URL` e `INFODESK_API_KEY`.
  **Nunca abra, leia, imprima nem copie esse arquivo**; só carregue no shell:
  `set -a && . ./.env && set +a`. Em worktree, o `.env` fica no checkout
  principal: `. "<raiz-do-repo>/.env"`.
- Python da venv (o `python` do PATH quebra no Windows):
  `./agente-ia/venv/Scripts/python.exe`. Stdlib pura, sem pip.
- A chave só precisa de leitura (`tickets:read`, `conversations:read`).

## Rodar

```bash
set -a && . ./.env && set +a
./agente-ia/venv/Scripts/python.exe scripts/analisar-infodesk.py --saida descoberta-infodesk/ --meses 12
# regenerar só o markdown, sem API: --de-arquivo descoberta-infodesk/analise.json
# teste rápido (números parciais): --limite-paginas 2
```

Saídas em `descoberta-infodesk/` (gitignored, **não vão para o PR**):
`analise.json` (agregados) e `analise.md` (12 tabelas). Janela: `--meses N`
meses completos antes do atual + o mês corrente. Se passar de ~20 mil
tickets, use `--meses 6` e diga isso na resposta.

## O que cada tabela do `analise.md` responde

| Tabela | Pergunta de roteamento |
|---|---|
| 1 tickets mesa × canal; 3 por mês | **Q1** chamado do WhatsApp cai em `Incidente`/`Solicitações`? **Q3** qual mesa de triagem recebe o que vem de chat |
| 2 mesas “Externo/Interno/Remoto” | **Q1** as cascas ficam de fora (esperado zero) |
| 6 Financeiro por canal; 11 departamento → mesa | **Q2** cobrança/boleto vai para `Financeiro` ou `Solicitações` |
| 5 tickets com `conversationId` | **Q3/Q4** o que nasce de conversa (o caminho da Sofia) e em que mesa |
| 9, 10, 11, 12 conversas e equipe | **Q4** departamento de chat por categoria e padrão de transferência |
| 7 áreas; 8 itens | **Q5** áreas/itens que aparecem em ticket de cliente (WHATSAPP/CHAT) e os que nunca aparecem. Itens marcados “fora do catálogo atual” são de uma versão anterior do catálogo (mesmos nomes, outros ids): servem para ver *assunto*, não *id* |
| 4 `catalogItemId` preenchido | **Q6** a prática da casa preenche o item de catálogo? |

Leia sempre com o **canal** em mente: `AGENT` (alertas/agente desktop) pode
dominar o volume e não representa cliente falando com a Sofia; a origem
“cliente” é `WHATSAPP` + `CHAT` (`WEB`/`EMAIL` são portal e e-mail).

## Privacidade (inegociável — `CLAUDE.md`, regra 5)

- O script reduz cada item a uma **lista branca** (`_ticket`, `_conversa`) e
  descarta o cru por página. Nunca persista nem imprima `requester`,
  `contactPhone`, `contact`, `title`, `description`, `body`,
  `lastMessagePreview`, nome de pessoa ou id de ticket/conversa. Só contagens,
  meses e nomes de mesa/área/item/departamento/canal, mais **quantos** membros
  cada departamento tem. **Não** grave `username` de atendente: na Infominas é
  o nome da pessoa.
- Se mexer no script, mantenha a lista branca: campo novo só entra se for
  agregável e não pessoal. Erros da API mostram só status e `code`.
- Depois de rodar, confira que as saídas não vazaram nada:
  `grep -nE '\+55|@|requester|contactPhone|lastMessagePreview|username' descoberta-infodesk/analise.*`
  (esperado: nenhuma linha).
- Nomes de **mesa** de cliente específico (ex.: “Nefroclinicas”) são nome de
  empresa, não de pessoa; podem aparecer.

## Limites

- Só `GET`. Rate limit 300 req/min por IP: pausa de 150 ms por chamada e
  espera com repetição em 429/5xx (respeita `Retry-After`). Cada página traz
  100 itens; ~1 s por página na prática. Estimativa: 10 mil tickets + 5 mil
  conversas ≈ 150 chamadas ≈ 1–3 min; 100 mil tickets ≈ 15–30 min (rode em
  background).
- Ordem das páginas não é por data de criação (observado em 2026-10-21): o
  script só para cedo se detectar ordem decrescente; senão lê tudo.
- Conversa → mesa usa `ticketNumber`/`conversationId` do ticket dentro da
  janela; conversa cujo ticket é anterior à janela entra como “mesa
  desconhecida”.
- O que a API não mostra (campos personalizados, `serviceGroupId` do item)
  continua só no painel (10, §2/§8).

## Registrar o resultado

Quando uma pergunta ficar respondida:

1. Escreva a resposta com os números, o intervalo e o **grau de confiança** em
   `descoberta-infodesk/respostas-roteamento.md` (local, gitignored).
2. Passe o **fato** (sem dado pessoal) para o repositório: em
   `docs/infodesk/09` (seção “O que a API devolveu…”) para uso de mesas e
   catálogo, e na linha correspondente de `docs/infodesk/10 §8` (marque
   `RESPONDIDO (data)` ou acrescente “Fato (análise de uso, data): …”). A
   decisão de negócio continua da Infominas: números sustentam a proposta, não
   a substituem.
3. Informe no PR a janela usada (`--meses`) e a data da coleta.
