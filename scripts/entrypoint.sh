#!/usr/bin/env bash
set -euo pipefail

MARKER="/tmp/.setup-done"

if [ ! -f "$MARKER" ]; then
  echo "=== Running initial setup ==="
  make setup
  make bootstrap
  touch "$MARKER"
fi

exec sleep infinity
