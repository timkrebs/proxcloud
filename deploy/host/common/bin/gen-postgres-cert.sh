#!/usr/bin/env bash
# Idempotent self-signed server cert for the Postgres container's TLS.
#
# The app connects with sslmode=require (see .env DATABASE_URL): the client
# ENCRYPTS but does not verify the CA, so a self-signed server cert is
# sufficient and needs no CA distribution. This exists because config.go fails
# closed when PROXCLOUD_ENV=production and DATABASE_URL is not TLS.
#
# Files MUST be owned by uid 70 (the alpine `postgres` user) mode 600, or
# Postgres refuses to start ("private key file has group or world access").
set -euo pipefail

dir="${1:?usage: gen-postgres-cert.sh <tls-dir>}"
crt="$dir/server.crt"
key="$dir/server.key"

mkdir -p "$dir"
if [ -s "$crt" ] && [ -s "$key" ]; then
  echo "postgres tls: cert already present in $dir"
else
  openssl req -x509 -nodes -newkey rsa:2048 -days 3650 \
    -subj "/CN=proxcloud-postgres" \
    -keyout "$key" -out "$crt"
  echo "postgres tls: generated self-signed cert in $dir"
fi

# uid/gid 70 = the `postgres` user inside postgres:16-alpine.
chown 70:70 "$crt" "$key"
chmod 600 "$key"
chmod 644 "$crt"
