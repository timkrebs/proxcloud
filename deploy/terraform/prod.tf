# proxcloud-prod — the VM that runs the blue/green stack (ADR-0015). A VM, not
# an LXC, because it runs the platform users depend on and needs full kernel
# isolation for Docker.

resource "proxmox_virtual_environment_vm" "prod" {
  name        = "proxcloud-prod"
  description = "Proxcloud PROD — blue/green Docker compose behind Caddy (ADR-0015). Managed by deploy/terraform."
  tags        = ["proxcloud", "prod"]
  node_name   = var.node
  vm_id       = var.vmid_prod

  # The cloud image must exist on the node before the disk can import from it.
  depends_on = [proxmox_download_file.ubuntu_cloud_image]

  agent {
    enabled = true
  }

  cpu {
    cores = var.prod_cores
    type  = "host"
  }

  memory {
    dedicated = var.prod_memory
  }

  disk {
    datastore_id = var.storage
    interface    = "scsi0"
    size         = var.prod_disk_gb
    # import_from removed temporarily — we write the image in by hand below
  }

  network_device {
    bridge  = var.bridge
    vlan_id = var.vlan_id
  }

  operating_system {
    type = "l26"
  }

  initialization {
    datastore_id = var.storage

    ip_config {
      ipv4 {
        address = local.ipv4_prod
        gateway = local.gw_prod
      }
    }

    dns {
      domain  = var.domain
      servers = var.dns_servers
    }

    user_data_file_id = proxmox_virtual_environment_file.prod_cloud_init.id
  }

  on_boot = true

  lifecycle {
    # cloud-init user-data is applied once at first boot; regenerating the
    # snippet later must not trigger a destroy/recreate of a live prod VM.
    ignore_changes = [initialization]
  }
}

# ── Provision: install Docker + the deploy user (first-boot.sh), lay down the
#    /opt/proxcloud scaffolding (common + prod), and run bootstrap.sh. ──
resource "null_resource" "prod_provision" {
  count = var.run_provisioners ? 1 : 0

  depends_on = [proxmox_virtual_environment_vm.prod]

  triggers = {
    vm_id       = proxmox_virtual_environment_vm.prod.vm_id
    common_hash = local.common_hash
    prod_hash   = local.prod_hash
    host        = local.prod_host
    ci_deploy   = sha1(local.ci_deploy_prod)
  }

  connection {
    type  = "ssh"
    host  = local.prod_host
    user  = var.provision_ssh_username
    agent = true
  }

  provisioner "local-exec" {
    command = <<-EOT
      set -e
      TMP=$(mktemp -d)
      cp -r ${path.module}/../host/common $TMP/common
      cp -r ${path.module}/../host/prod   $TMP/prod
      printf '%s' '${local.ci_deploy_prod}' > $TMP/ci-deploy-key.pub
      tar czf - -C $TMP common prod ci-deploy-key.pub \
        | ssh -o StrictHostKeyChecking=accept-new ${var.provision_ssh_username}@${local.prod_host} 'cat > /tmp/proxcloud-host.tgz'
      rm -rf $TMP
    EOT
  }

  provisioner "remote-exec" {
    inline = [
      "set -e",
      "rm -rf /tmp/proxcloud && mkdir -p /tmp/proxcloud",
      "tar xzf /tmp/proxcloud-host.tgz -C /tmp/proxcloud",
      "chmod +x /tmp/proxcloud/common/bin/*.sh",
      "sudo /tmp/proxcloud/common/bin/first-boot.sh",
      "sudo mkdir -p /opt/proxcloud/bin",
      "sudo cp -r /tmp/proxcloud/common/bin/. /opt/proxcloud/bin/",
      "sudo cp -r /tmp/proxcloud/prod/. /opt/proxcloud/",
      "sudo cp /tmp/proxcloud/ci-deploy-key.pub /opt/proxcloud/ci-deploy-key.pub",
      "sudo chmod +x /opt/proxcloud/bin/*.sh /opt/proxcloud/bootstrap.sh",
      "sudo /opt/proxcloud/bootstrap.sh",
    ]
  }
}

