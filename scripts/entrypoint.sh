#!/usr/bin/env bash
set -euo pipefail

# Register current UID/GID in /etc/passwd and /etc/group if missing
# (needed when container runs as a host UID that has no image entry)
if ! whoami &>/dev/null; then
    echo "dev:x:$(id -u):$(id -g):Dev User:/home/dev:/bin/bash" >> /etc/passwd
fi
if ! getent group "$(id -g)" &>/dev/null; then
    echo "dev:x:$(id -g):" >> /etc/group
fi

# Docker ソケットへのアクセス権を付与（GID=0 の場合など非rootユーザーが使えないケースに対応）
if [ -S /var/run/docker.sock ]; then
    sudo chmod 666 /var/run/docker.sock
fi

MARKER="/tmp/.setup-done"

if [ ! -f "$MARKER" ]; then
  echo "=== Running initial setup ==="
  make setup
  make bootstrap
  touch "$MARKER"
fi

exec sleep infinity
