# ==============================================================================
# Variáveis do root — os VALORES vivem todos em terraform.tfvars.
# Para mudar qualquer coisa da infra, edite o terraform.tfvars, não os .tf.
# ==============================================================================

variable "bot_server" {
  description = "VM do bot. ATENÇÃO: a VM já existe (adotada via import block em main.tf) — estes valores precisam bater com a realidade, senão o plan propõe replace (bloqueado por prevent_destroy). server_type/location/image forçam REPLACE."
  type = object({
    name        = string
    server_type = string
    image       = string
    location    = string
    labels      = optional(map(string), {})
  })
}

variable "ssh_key_name" {
  description = "Nome da chave SSH JÁ cadastrada no projeto Hetzner (data source — o cadastro pertence à infra do omni-route)."
  type        = string
}

variable "firewall_name" {
  description = "Nome do firewall de borda JÁ existente no projeto Hetzner (data source — só 22/80/443 + ICMP entram)."
  type        = string
}

variable "base_domain" {
  description = "Zona DNS gerenciada na Cloudflare (só usada quando bot_hosts não está vazio)."
  type        = string
}

variable "bot_hosts" {
  description = "Hosts DNS do bot (host → descrição). VAZIO = DNS desligado (estado atual: o bot só é alcançado por dentro da rede docker do servidor). Preencher aqui + descomentar os labels Traefik no docker-compose.prod.yml quando for expor."
  type        = map(string)
  default     = {}
}
