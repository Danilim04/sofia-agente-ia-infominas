# 01 — Autenticação e escopos

Fonte: PDF, seção "1. Autenticação" + escopos citados ao longo das seções 3–4 e
erros 401/403 da seção 6.

## Chave de API

- A integração usa **uma chave de API por parceiro/uso** (formato observado nos
  exemplos: `ik_live_xxxxxxxxxxxxxxxxxxxxxxxx`).
- A chave deve ir em **todas** as chamadas, de **uma** das duas formas
  (equivalentes):

```http
Authorization: Bearer ik_live_xxxxxxxxxxxxxxxxxxxxxxxx
```

ou

```http
X-Api-Key: ik_live_xxxxxxxxxxxxxxxxxxxxxxxx
```

Recomendação de implementação (nossa, não do PDF): usar `Authorization: Bearer`
por ser o padrão HTTP; manter o nome do header configurável só se a Infominas
pedir.

## Escopos

A chave carrega escopos; cada rota exige um. Os escopos citados no PDF:

| Escopo | Rotas que exigem |
|---|---|
| `tickets:write` | `POST /tickets`, `POST /tickets/:number/reply` |
| `tickets:read` | `GET /tickets/:number` |
| `conversations:read` | `GET /conversations`, `GET /conversations/:id` |
| `conversations:write` | `POST /conversations/:id/messages`, `/assign`, `/department`, `/finish` |
| (não informado) | `GET /desks`, `GET /users`, `GET /departments` — **NÃO DOCUMENTADO** se exigem escopo |

Chamada com escopo insuficiente → `403` com corpo
`{"statusCode":403,"message":"Escopo insuficiente. Necessário: tickets:write."}`.

## Configuração da chave (lado Infominas)

Na chave também se configuram `webhookUrl`, `webhookEvents` e o `webhookSecret`
usado na assinatura HMAC (ver [05-webhooks.md](05-webhooks.md)). O PDF não diz
**quem** configura (painel do Infodesk? pedido à Infominas?) nem se há rota de
auto-serviço — ver 07.

## Erros relacionados

| Código | Significado |
|---|---|
| 401 | Chave ausente, inválida ou revogada |
| 403 | Escopo insuficiente para a operação |

## Implicações para o nosso lado

- A chave é **segredo de produção**: vive em variável de ambiente/secret
  (`INFODESK_API_KEY`), nunca em código, log ou prompt. Mascarar nos logs como o
  Zapin faz com telefone/login (F5/LGPD).
- Pedir à Infominas uma chave com **apenas** os escopos necessários ao agente
  (princípio do menor privilégio): `tickets:write`, `tickets:read`,
  `conversations:read`, `conversations:write`. Reavaliar `conversations:write`
  se o agente nunca for encerrar conversa.
- Pedir **chave separada para homologação** (se existir ambiente) — ver 07.
