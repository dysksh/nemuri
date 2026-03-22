#!/usr/bin/env bash
set -euo pipefail

SNAPSHOT_NAME="${1:-nemuri-v1}"
SNAPSHOT_DIR="eval/fixtures/snapshots/${SNAPSHOT_NAME}"

if [ -d "$SNAPSHOT_DIR" ]; then
  echo "Snapshot already exists: $SNAPSHOT_DIR"
  echo "Remove it first if you want to recreate: rm -rf $SNAPSHOT_DIR"
  exit 1
fi

echo "Creating snapshot '${SNAPSHOT_NAME}' from current repository..."

mkdir -p "$SNAPSHOT_DIR"

# Copy source files tracked by git (excludes .git, build artifacts, etc.)
git ls-files -z | while IFS= read -r -d '' file; do
  # Skip eval directory itself, .env, and other non-source files
  case "$file" in
    eval/*|.env|*.tfstate|*.tfstate.*|terraform.tfvars|backend.conf) continue ;;
  esac

  dir=$(dirname "$file")
  mkdir -p "${SNAPSHOT_DIR}/${dir}"
  cp "$file" "${SNAPSHOT_DIR}/${file}"
done

# Count files
FILE_COUNT=$(find "$SNAPSHOT_DIR" -type f | wc -l)
echo "Snapshot created: ${SNAPSHOT_DIR} (${FILE_COUNT} files)"
