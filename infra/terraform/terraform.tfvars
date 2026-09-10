# ==============================================================================
# Valores da infra do bot — FONTE ÚNICA de configuração deste root.
# Mudou algo? Edite AQUI (e rode plan/apply), não os .tf.
# Nenhum segredo entra neste arquivo: tokens vão por variável de ambiente.
# ==============================================================================

# VM existente (Hetzner id 159386755 — adotada via import block do main.tf).
# Valores conferidos na VM real: hel1-dc2, Ubuntu 24.04, 2 vCPU / 4 GB / 40 GB
# (cx22). O primeiro `terraform plan` após o import deve mostrar ZERO mudanças
# — se propor replace, um destes valores está errado: corrija ANTES do apply.
bot_server = {
  name        = "tenant-azapfy"
  server_type = "cx22"
  image       = "ubuntu-24.04"
  location    = "hel1"
}

# Recursos do projeto Hetzner que pertencem à infra do omni-route (data source).
ssh_key_name  = "omni-route-deploy"
firewall_name = "omni-route-edge"

# DNS — desligado por enquanto (mapa vazio). Quando for expor o bot:
#   1. preencha aqui, ex.: bot_hosts = { "bot" = "gateway (webhook Chatwoot)" }
#   2. descomente os labels Traefik no docker-compose.prod.yml
#   3. exporte CLOUDFLARE_API_TOKEN e rode terraform apply
base_domain = "azapfy.com.br"
bot_hosts   = {}
