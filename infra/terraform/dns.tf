# ==============================================================================
# DNS na Cloudflare — DESLIGADO por enquanto (bot_hosts = {} no tfvars): o bot
# não é exposto na internet; o Chatwoot fala com ele por dentro da rede docker
# do servidor.
#
# Para ativar no futuro: preencha bot_hosts no terraform.tfvars (ex.:
# { "bot" = "gateway do bot (webhook Chatwoot)" }), exporte CLOUDFLARE_API_TOKEN
# e rode plan/apply. Registros proxied (nuvem laranja) — o TLS de origem fica a
# cargo do Traefik já existente no servidor (labels comentados no
# docker-compose.prod.yml).
# ==============================================================================

data "cloudflare_zone" "main" {
  count = length(var.bot_hosts) > 0 ? 1 : 0

  name = var.base_domain
}

resource "cloudflare_record" "bot" {
  for_each = var.bot_hosts

  zone_id = data.cloudflare_zone.main[0].id
  name    = each.key
  type    = "A"
  content = hcloud_server.bot.ipv4_address
  proxied = true
  ttl     = 1 # obrigatório em registro proxied
  comment = "azapfy-bot — ${each.value} (Terraform)"
}
