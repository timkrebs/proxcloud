output "staging_container_id" {
  description = "Staging LXC VMID."
  value       = proxmox_virtual_environment_container.staging.vm_id
}

output "prod_vm_id" {
  description = "Prod VM VMID."
  value       = proxmox_virtual_environment_vm.prod.vm_id
}

output "prod_vm_ipv4_addresses" {
  description = "IPv4 addresses the prod VM's guest agent reports (useful to learn the DHCP lease for prod_provision_host / DNS)."
  value       = proxmox_virtual_environment_vm.prod.ipv4_addresses
}

output "provision_hosts" {
  description = "Hosts the Terraform provisioners SSH to. With DHCP, set *_provision_host to the leased IP (or ensure DNS registration) before re-applying if the DNS names don't resolve."
  value = {
    staging = local.staging_host
    prod    = local.prod_host
  }
}

output "next_steps" {
  description = "One-time manual steps after apply."
  value       = <<-EOT
    1. On EACH guest: copy the env.example to /opt/proxcloud/.env and fill every
       CHANGEME (Proxmox token, SECRETS_KEY, DB password, FRONTEND_ORIGIN, TLS mode).
    2. Prod: choose the Caddy TLS mode in /opt/proxcloud/caddy/Caddyfile (default
       Mode A = plain HTTP behind cloudflared).
    3. Prod: run `sudo /opt/proxcloud/bootstrap.sh` then `/opt/proxcloud/bin/up-infra.sh`.
    4. Register the CI deploy key: set ci_deploy_public_key and re-apply, OR drop
       the pubkey at /opt/proxcloud/ci-deploy-key.pub and re-run bootstrap.sh.
    5. The self-hosted GitHub runner lives in its OWN LXC (not these guests) — see
       deploy/README.md "Self-hosted runner".
  EOT
}
