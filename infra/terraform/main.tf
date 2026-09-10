# ==============================================================================
# VM do bot — a VM JÁ EXISTE (id 159386755, criada pelo provisionamento de
# tenant do omni-route, hostname tenant-azapfy). Este root a ADOTA via import
# block: o primeiro `terraform apply` importa em vez de criar; depois disso o
# import vira no-op.
#
# Chave SSH e firewall pertencem à infra do omni-route no MESMO projeto
# Hetzner — entram como data source para não acoplar states (mesmo padrão do
# root tenant/ de lá). CUIDADO: um deprovision do tenant "azapfy" no
# omni-route destrói esta VM por fora deste state.
# ==============================================================================

import {
  to = hcloud_server.bot
  id = "159386755"
}

data "hcloud_ssh_key" "deploy" {
  name = var.ssh_key_name
}

data "hcloud_firewall" "edge" {
  name = var.firewall_name
}

resource "hcloud_server" "bot" {
  name         = var.bot_server.name
  server_type  = var.bot_server.server_type
  image        = var.bot_server.image
  location     = var.bot_server.location
  ssh_keys     = [data.hcloud_ssh_key.deploy.id]
  firewall_ids = [data.hcloud_firewall.edge.id]
  labels       = var.bot_server.labels

  # prevent_destroy: mudança que exija replace (server_type/location/image)
  # faz o plan FALHAR em vez de destruir — decisão consciente e manual.
  # ignore_changes: image some do catálogo com o tempo; ssh_keys/user_data só
  # importam na criação (authorized_keys é gerenciado pelo Ansible); labels e
  # firewall ficam com o omni-route, dono original da VM.
  lifecycle {
    prevent_destroy = true
    ignore_changes  = [image, ssh_keys, user_data, firewall_ids, labels]
  }
}
