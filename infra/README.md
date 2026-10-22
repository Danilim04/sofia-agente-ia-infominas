# infra/

**Sem infraestrutura da Infominas de propósito.** A infraestrutura dela é desconhecida
(pendência **P10**), então este repositório não descreve servidor, DNS nem provedor.

**Resíduo do Zapin (E4.6, remoção pendente):** `infra/terraform/` (VM + DNS) e
`infra/ansible/` (hardening da VM) continuam aqui, cada um com um README
"LEGADO DO ZAPIN — NÃO USAR" e listados em `docs/plano/legado-permitido.txt`. O `git rm`
foi negado pelo classificador de permissões na sessão da etapa; nada na Sofia depende deles
e o histórico do git guarda o conteúdo. Quem remover apaga também a linha da lista.

O que a stack precisa de qualquer host está em [`deploy/README.md`](../deploy/README.md)
(Docker, domínio HTTPS público, onde ficam os segredos, backup do Postgres).

## O que falta decidir (P10)

| Decisão | Por quê importa | Quem |
|---|---|---|
| Onde a stack roda (VM própria, nuvem, Kubernetes) | define se o `docker-compose.prod.yml` basta ou se vira outra forma de deploy | Infominas |
| Domínio e quem termina o HTTPS (proxy reverso, load balancer) | o Infodesk só entrega webhook em URL **HTTPS pública**; hoje nada no repo a expõe | Infominas |
| Onde moram os segredos (cofre, variáveis do CI, `.env` no host) | `INFODESK_API_KEY`, `INFODESK_WEBHOOK_SECRET`, `TOOLS_API_TOKEN`, `PGVECTOR_PASSWORD`, `OPENROUTER_API_KEY` não entram no git | Infominas |
| Como as imagens chegam ao host (build no host, registry) | o compose constrói local; com registry, preencher `IMAGE_REPO`/`IMAGE_TAG` | Infominas + Daniel |
| Backup e restauração do volume `pgvector-data` | é o estado de produção (conversas, tickets, cursor, índice do RAG) | quem opera |
| Quem opera e recebe os alertas (`ALERTA_WEBHOOK_URL`, log `sofia_alerta`) | sem destino, alerta só existe em log | Infominas |
| Ambiente de homologação com chave própria do Infodesk | hoje só existe produção (`docs/infodesk/07`, A1) | Infominas |

Quando P10 for respondida, o código de infraestrutura (IaC, pipeline de
entrega) entra aqui e em `.github/workflows/deploy.yml`, numa etapa própria.
