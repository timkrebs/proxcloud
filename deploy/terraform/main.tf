# Proxcloud delivery infra (WS4). This is the ONE piece of infra managed OUTSIDE
# Proxcloud itself — Proxcloud cannot deploy the guests it runs on (bootstrap
# note, release-engineer.md). It provisions two guests on pve01:
#   - proxcloud-staging : LXC, single compose stack (staging.<domain>)
#   - proxcloud-prod    : VM,  blue/green compose behind Caddy (ADR-0015)
#
# The Proxmox API TOKEN is NOT declared as a Terraform variable so it can never
# land in state-adjacent .tfvars. Supply it at apply time:
#   export PROXMOX_VE_API_TOKEN='proxcloud@pve!tf=xxxxxxxx-xxxx-....'
# (Optionally also PROXMOX_VE_ENDPOINT / PROXMOX_VE_INSECURE instead of the vars.)

provider "proxmox" {
  endpoint = var.proxmox_endpoint
  insecure = var.proxmox_tls_insecure

  # api_token intentionally unset here — read from PROXMOX_VE_API_TOKEN at apply.

  ssh {
    agent    = var.proxmox_ssh_agent
    username = var.proxmox_ssh_username
  }
}

locals {
  # IPv4 config string bpg expects: "dhcp" or a CIDR.
  ipv4_staging = var.ip_mode == "static" ? var.staging_static_ipv4_cidr : "dhcp"
  ipv4_prod    = var.ip_mode == "static" ? var.prod_static_ipv4_cidr : "dhcp"

  gw_staging = var.ip_mode == "static" ? var.staging_gateway : null
  gw_prod    = var.ip_mode == "static" ? var.prod_gateway : null

  # Provisioner SSH targets: explicit override → else the DNS name.
  staging_host = var.staging_provision_host != "" ? var.staging_provision_host : "proxcloud-staging.${var.domain}"
  prod_host    = var.prod_provision_host != "" ? var.prod_provision_host : "proxcloud-prod.${var.domain}"

  # Re-run provisioners when the on-guest scaffolding changes.
  common_hash  = sha1(join("", [for f in sort(tolist(fileset("${path.module}/../host/common", "**"))) : filesha1("${path.module}/../host/common/${f}")]))
  prod_hash    = sha1(join("", [for f in sort(tolist(fileset("${path.module}/../host/prod", "**"))) : filesha1("${path.module}/../host/prod/${f}")]))
  staging_hash = sha1(join("", [for f in sort(tolist(fileset("${path.module}/../host/staging", "**"))) : filesha1("${path.module}/../host/staging/${f}")]))
}

# ── VM cloud image (imported as the prod root disk) ──────────────────────────
resource "proxmox_download_file" "ubuntu_cloud_image" {
  content_type = "iso"
  datastore_id = var.image_datastore
  node_name    = var.node
  url          = var.ubuntu_cloud_image_url
  file_name    = "proxcloud-ubuntu-24.04.img"
  overwrite    = false
}

# ── LXC template (staging) ───────────────────────────────────────────────────
resource "proxmox_download_file" "debian_lxc_template" {
  content_type = "vztmpl"
  datastore_id = var.image_datastore
  node_name    = var.node
  url          = var.lxc_template_url
  overwrite    = false
}

# ── Prod VM cloud-init user-data (minimal: admin user + guest-agent so the
#    Terraform provisioners can connect; Docker/deploy-user come from
#    first-boot.sh run by the provisioner). Uploaded as a snippet (bpg does this
#    over SSH — hence the provider ssh block). ──
resource "proxmox_virtual_environment_file" "prod_cloud_init" {
  content_type = "snippets"
  datastore_id = var.snippet_datastore
  node_name    = var.node

  source_raw {
    file_name = "proxcloud-prod-cloud-init.yaml"
    data = templatefile("${path.module}/templates/cloud-init.yaml.tftpl", {
      hostname       = "proxcloud-prod"
      admin_username = var.provision_ssh_username
      admin_ssh_keys = var.admin_ssh_public_keys
    })
  }
}
