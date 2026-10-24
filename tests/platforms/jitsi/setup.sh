#!/usr/bin/env bash
# Creates .env from env.example (with fresh passwords) if it does not exist,
# and the configuration directories the containers mount.
set -euo pipefail
cd "$(dirname "$0")"

if [ ! -f .env ]; then
    sed -e "s#^JICOFO_AUTH_PASSWORD=.*#JICOFO_AUTH_PASSWORD=$(openssl rand -hex 16)#" \
        -e "s#^JVB_AUTH_PASSWORD=.*#JVB_AUTH_PASSWORD=$(openssl rand -hex 16)#" \
        env.example > .env
    echo "created .env"
fi

CONFIG=$(sed -n 's/^CONFIG=//p' .env)
CONFIG=${CONFIG/#\~/$HOME}
mkdir -p "$CONFIG"/{web,prosody/config,prosody/prosody-plugins-custom,jicofo,jvb}
mkdir -p "$CONFIG"/storage/{prosody,web}
chmod 777 "$CONFIG"/storage/{prosody,web}
echo "config in $CONFIG"
