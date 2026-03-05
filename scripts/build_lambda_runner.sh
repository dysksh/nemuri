#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
DIST_DIR="$PROJECT_ROOT/dist"

mkdir -p "$DIST_DIR"

echo "Building lambda-runner..."
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
  go build -o "$DIST_DIR/bootstrap" "$PROJECT_ROOT/cmd/lambda-runner"

echo "Creating ZIP..."
(cd "$DIST_DIR" && zip -j lambda-runner.zip bootstrap)
rm "$DIST_DIR/bootstrap"

echo "Done: $DIST_DIR/lambda-runner.zip"
