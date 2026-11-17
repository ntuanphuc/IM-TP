#!/usr/bin/env bash
set -euo pipefail

cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."

if [[ -e .env || -L .env ]]; then
  printf '%s\n' '.env already exists. Edit it in place.' >&2
  exit 1
fi

if ! command -v openssl >/dev/null 2>&1; then
  printf '%s\n' 'Install openssl first: sudo apt install openssl' >&2
  exit 1
fi

secrets=(POSTGRES_PASSWORD REDIS_PASSWORD JWT_SECRET MINIO_SECRET_KEY LOCAL_ADMIN_PASSWORD)
config=$(tr -d '\r' < .env.example)
for key in "${secrets[@]}"; do
  value=$(openssl rand -hex 32)
  config=$(printf '%s\n' "$config" | sed "s/^${key}=$/${key}=${value}/")
done

umask 077
set -o noclobber
printf '%s\n' "$config" > .env
printf '%s\n' 'Created .env. Read LOCAL_ADMIN_PASSWORD from that file to sign in.'
