# All inputs have Tim-approved defaults so `terraform plan` works out of the box
# on pve01; override any of them in terraform.tfvars. NO secret has a default —
# the Proxmox API token is supplied at apply time via the PROXMOX_VE_API_TOKEN
# environment variable and never lives in a .tf/.tfvars file (see main.tf).

# ── Proxmox provider connection ──────────────────────────────────────────────
variable "proxmox_endpoint" {
  description = "Proxmox VE API endpoint (no /api2/json suffix)."
  type        = string
  default     = "https://pve01:8006"
}

variable "proxmox_tls_insecure" {
  description = "Skip TLS verification for the PVE API (homelab self-signed certs)."
  type        = bool
  default     = true
}

variable "proxmox_ssh_username" {
  description = "SSH user the provider uses on the PVE node for snippet uploads (bpg uploads cloud-init snippets over SSH)."
  type        = string
  default     = "root"
}

variable "proxmox_ssh_agent" {
  description = "Use the local SSH agent for the provider's node SSH connection."
  type        = bool
  default     = true
}

# ── Placement / storage / network ────────────────────────────────────────────
variable "node" {
  description = "Proxmox node name."
  type        = string
  default     = "pve01"
}

variable "storage" {
  description = "Datastore for guest root disks (VM + LXC)."
  type        = string
  default     = "local-lvm"
}

variable "image_datastore" {
  description = "Datastore that holds downloaded images (VM cloud image = content 'iso', LXC template = content 'vztmpl'). Usually 'local'."
  type        = string
  default     = "local"
}

variable "snippet_datastore" {
  description = "Datastore with 'snippets' content enabled, for the VM cloud-init user-data. Usually 'local'."
  type        = string
  default     = "local"
}

variable "bridge" {
  description = "Linux bridge for guest NICs."
  type        = string
  default     = "vmbr0"
}

variable "vlan_id" {
  description = "VLAN tag for guest NICs; null = untagged (default: no VLAN)."
  type        = number
  default     = null
}

variable "ip_mode" {
  description = "IP assignment: 'dhcp' (default) or 'static'. For 'static' set the *_static_ipv4_cidr / *_gateway vars."
  type        = string
  default     = "dhcp"
  validation {
    condition     = contains(["dhcp", "static"], var.ip_mode)
    error_message = "ip_mode must be 'dhcp' or 'static'."
  }
}

variable "dns_servers" {
  description = "DNS resolvers for the guests."
  type        = list(string)
  default     = ["1.1.1.1", "9.9.9.9"]
}

variable "domain" {
  description = "Lab search domain (staging.<domain>, proxcloud.<domain>)."
  type        = string
  default     = "proxcloud.lab"
}

# Static-IP inputs (only used when ip_mode = "static").
variable "staging_static_ipv4_cidr" {
  description = "Static IPv4 CIDR for staging when ip_mode='static' (e.g. 192.168.1.20/24)."
  type        = string
  default     = null
}
variable "staging_gateway" {
  description = "Default gateway for staging when ip_mode='static'."
  type        = string
  default     = null
}
variable "prod_static_ipv4_cidr" {
  description = "Static IPv4 CIDR for prod when ip_mode='static'."
  type        = string
  default     = null
}
variable "prod_gateway" {
  description = "Default gateway for prod when ip_mode='static'."
  type        = string
  default     = null
}

# ── Images / templates ───────────────────────────────────────────────────────
variable "ubuntu_cloud_image_url" {
  description = "URL of the Ubuntu cloud image imported as the prod VM disk."
  type        = string
  default     = "https://cloud-images.ubuntu.com/releases/24.04/release/ubuntu-24.04-server-cloudimg-amd64.img"
}

variable "lxc_template_url" {
  description = "URL of the Debian LXC template for staging (Proxmox CDN). Alternatively pre-download with `pveam` and point at the local vztmpl."
  type        = string
  default     = "http://download.proxmox.com/images/system/debian-12-standard_12.7-1_amd64.tar.zst"
}

# ── Guest sizing (Tim-approved defaults) ─────────────────────────────────────
variable "staging_cores" {
  type    = number
  default = 2
}
variable "staging_memory" {
  description = "Staging RAM in MiB."
  type        = number
  default     = 4096
}
variable "staging_disk_gb" {
  type    = number
  default = 32
}
variable "prod_cores" {
  type    = number
  default = 4
}
variable "prod_memory" {
  description = "Prod RAM in MiB."
  type        = number
  default     = 8192
}
variable "prod_disk_gb" {
  type    = number
  default = 64
}

variable "vmid_staging" {
  description = "VMID for the staging LXC (null = let Proxmox pick)."
  type        = number
  default     = 8001
}
variable "vmid_prod" {
  description = "VMID for the prod VM (null = let Proxmox pick)."
  type        = number
  default     = 8002
}

# ── SSH keys / provisioning ──────────────────────────────────────────────────
variable "admin_ssh_public_keys" {
  description = "Admin SSH public keys — installed for the sudo admin user (VM) / root (LXC). Terraform's provisioners connect with one of these. Set at least one."
  type        = list(string)
  default     = []
}

variable "provision_ssh_username" {
  description = "Sudo admin user created on the prod VM by cloud-init; Terraform provisioners connect as this user. (The LXC is provisioned as root.)"
  type        = string
  default     = "proxadmin"
}

variable "ci_deploy_public_key" {
  description = "The CI deploy PUBLIC key. bootstrap.sh installs it into the deploy user's authorized_keys with the forced-command wrapper. Public key = safe to keep here; the private half lives only in the GitHub environment secret."
  type        = string
  default     = ""
}

variable "unattended_upgrades" {
  description = "Install + enable unattended-upgrades on both guests."
  type        = bool
  default     = true
}

variable "run_provisioners" {
  description = "Run the file/remote-exec provisioners that install the /opt/proxcloud scaffolding + bootstrap. Set false to `apply` compute/network only, then provision by hand."
  type        = bool
  default     = true
}

# Provisioner SSH targets. With DHCP the leased IP is unknown until boot, so
# default to the DNS names (requires DHCP+DNS registration) — override with the
# actual IP if your lab does not register leases. With static IPs, set these to
# the *_static_ipv4_cidr host part.
variable "staging_provision_host" {
  description = "Host/IP Terraform provisioners SSH to for staging (default: DNS name)."
  type        = string
  default     = ""
}
variable "prod_provision_host" {
  description = "Host/IP Terraform provisioners SSH to for prod (default: DNS name)."
  type        = string
  default     = ""
}
