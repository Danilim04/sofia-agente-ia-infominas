# LEGADO DO ZAPIN — NÃO USAR, remoção pendente

Terraform da VM e do DNS do Zapin. Não é usado pela Sofia: não faz parte do compose, do `deploy.yml` nem de
nenhum teste, e não cobre os segredos novos (`INFODESK_*`, `TOOLS_API_TOKEN`).
Foi herdado do Zapin e deve sair do repositório com `git rm -r infra/terraform`
(etapa E4.6; a remoção foi negada pelo classificador de permissões do Claude
Code e ficou para o orquestrador/Daniel). Enquanto existir, está listado em
`docs/plano/legado-permitido.txt`, que só encolhe. O que a Sofia precisa de
infraestrutura está em `deploy/README.md` e `infra/README.md`.
