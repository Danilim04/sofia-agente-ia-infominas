output "bot_ipv4" {
  description = "IPv4 público da VM do bot (é o ansible_host do inventário Ansible)"
  value       = hcloud_server.bot.ipv4_address
}

output "bot_hosts" {
  description = "FQDNs do bot (vazio enquanto o DNS estiver desligado)"
  value       = [for host in keys(var.bot_hosts) : "${host}.${var.base_domain}"]
}
