#!/bin/sh
# Creates the staging .env (mode 0600) with fresh random secrets, once.
# Usage: ./bootstrap.sh <admin-email>
set -eu
cd "$(dirname "$0")"
umask 077
if [ -f .env ]; then
  echo ".env exists; leaving it unchanged"
  exit 0
fi
admin_email=${1:?admin email required}
rand() { openssl rand -hex "$1"; }
cat > .env <<ENV
POSTGRES_PASSWORD=$(rand 24)
ADMIN_EMAIL=$admin_email
ADMIN_PASSWORD=$(rand 16)
JWT_SECRET=$(rand 32)
TOTP_ENCRYPTION_KEY=$(rand 32)
APP_IMAGE=xy2api-staging:unset
ENV
mkdir -p evidence fixtures
echo "created .env"
