#!/usr/bin/env bash
# Idempotent base provisioning, run once by Terraform after the guest boots
# (VM: over SSH as the sudo admin user; LXC: over SSH as root). Installs Docker
# CE + the compose plugin, the locked key-only `deploy` user (docker group, no
# password), and unattended-upgrades. Safe to re-run.
set -euo pipefail

DEPLOY_USER=deploy

# Re-exec under sudo if we are not already root (VM path; LXC runs as root).
if [ "$(id -u)" -ne 0 ]; then
  exec sudo -n "$0" "$@"
fi

export DEBIAN_FRONTEND=noninteractive
apt-get update -y
apt-get install -y ca-certificates curl jq gnupg openssl unattended-upgrades

# Docker CE + compose plugin via the official convenience script (idempotent:
# the script is a no-op when docker is already installed).
if ! command -v docker >/dev/null 2>&1; then
  curl -fsSL https://get.docker.com | sh
fi
systemctl enable --now docker

# The deploy user: home + real shell (needed so sshd can exec the forced
# command), docker group (compose runs as this user), password locked. Its
# authorized_keys (the forced-command CI key) is written later by bootstrap.sh.
if ! id -u "$DEPLOY_USER" >/dev/null 2>&1; then
  useradd --create-home --shell /bin/bash "$DEPLOY_USER"
fi
usermod -aG docker "$DEPLOY_USER"
passwd -l "$DEPLOY_USER" || true

dpkg-reconfigure -f noninteractive unattended-upgrades || true

echo "first-boot: complete"
