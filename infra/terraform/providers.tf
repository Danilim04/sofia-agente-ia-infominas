# Tokens via ambiente (nunca em arquivo):
#   HCLOUD_TOKEN          — token do MESMO projeto Hetzner onde a VM vive
#                           (a VM foi criada pelo provisionamento do omni-route)
#   CLOUDFLARE_API_TOKEN  — token com Zone:DNS:Edit (só necessário com DNS ativo)
#
# Provider Cloudflare pinado na major 4 (recurso cloudflare_record). Na major 5
# os recursos mudam de nome (cloudflare_dns_record).
terraform {
  required_version = ">= 1.10"

  required_providers {
    hcloud = {
      source  = "hetznercloud/hcloud"
      version = "~> 1.50"
    }
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = "~> 4.52"
    }
  }
}

provider "hcloud" {}

provider "cloudflare" {}
