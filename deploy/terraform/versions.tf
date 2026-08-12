terraform {
  required_version = ">= 1.6.0"

  required_providers {
    # bpg/proxmox — the actively-maintained Proxmox VE provider. Pinned to the
    # 0.x minor; bump deliberately and re-run `terraform plan`.
    proxmox = {
      source  = "bpg/proxmox"
      version = "~> 0.66"
    }
    null = {
      source  = "hashicorp/null"
      version = "~> 3.2"
    }
  }
}
