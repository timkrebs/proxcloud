# proxcloud-staging — the LXC that runs the single staging stack (ADR-0014).
# Unprivileged + nesting/keyctl so Docker runs inside the container. If Docker
# misbehaves on your kernel, set unprivileged=false (see deploy/README.md).

resource "proxmox_virtual_environment_container" "staging" {
  description  = "Proxcloud STAGING — single Docker compose stack. Managed by deploy/terraform."
  tags         = ["proxcloud", "staging"]
  node_name    = var.node
  vm_id        = var.vmid_staging
  unprivileged = true

  cpu {
    cores = var.staging_cores
  }

  memory {
    dedicated = var.staging_memory
    swap      = 512
  }

  disk {
    datastore_id = var.storage
    size         = var.staging_disk_gb
  }

  network_interface {
    name    = "eth0"
    bridge  = var.bridge
    vlan_id = var.vlan_id
  }

  operating_system {
    template_file_id = proxmox_download_file.debian_lxc_template.id
    type             = "debian"
  }

  initialization {
    hostname = "proxcloud-staging"

    ip_config {
      ipv4 {
        address = local.ipv4_staging
        gateway = local.gw_staging
      }
    }

    dns {
      domain  = var.domain
      servers = var.dns_servers
    }

    user_account {
      # Terraform provisions the LXC as root over SSH with these keys.
      keys = var.admin_ssh_public_keys
    }
  }

  features {
    nesting = true
  }

  start_on_boot = true

  lifecycle {
    ignore_changes = [initialization]
  }
}

# ── Provision: first-boot.sh (Docker + deploy user), lay down /opt/proxcloud
#    (common + staging), run bootstrap.sh. Runs as root (LXC). ──
resource "null_resource" "staging_provision" {
  count = var.run_provisioners ? 1 : 0

  depends_on = [proxmox_virtual_environment_container.staging]

  triggers = {
    ct_id        = proxmox_virtual_environment_container.staging.vm_id
    common_hash  = local.common_hash
    staging_hash = local.staging_hash
    host         = local.staging_host
    ci_deploy    = sha1(local.ci_deploy_staging)
  }

  connection {
    type  = "ssh"
    host  = local.staging_host
    user  = "root"
    agent = true
  }

  provisioner "remote-exec" {
    inline = ["mkdir -p /tmp/proxcloud-common /tmp/proxcloud-host"]
  }

  provisioner "file" {
    source      = "${path.module}/../host/common/"
    destination = "/tmp/proxcloud-common"
  }

  provisioner "file" {
    source      = "${path.module}/../host/staging/"
    destination = "/tmp/proxcloud-host"
  }

  provisioner "file" {
    content     = local.ci_deploy_staging
    destination = "/tmp/ci-deploy-key.pub"
  }

  provisioner "remote-exec" {
    inline = [
      "set -e",
      "chmod +x /tmp/proxcloud-common/bin/*.sh",
      "/tmp/proxcloud-common/bin/first-boot.sh",
      "mkdir -p /opt/proxcloud/bin",
      "cp -r /tmp/proxcloud-common/bin/. /opt/proxcloud/bin/",
      "cp -r /tmp/proxcloud-host/. /opt/proxcloud/",
      "cp /tmp/ci-deploy-key.pub /opt/proxcloud/ci-deploy-key.pub",
      "chmod +x /opt/proxcloud/bin/*.sh /opt/proxcloud/bootstrap.sh",
      "/opt/proxcloud/bootstrap.sh",
    ]
  }
}
