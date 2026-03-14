#!/usr/bin/env bash
set -euo pipefail

: "${DISCORD_APP_ID:?DISCORD_APP_ID is not set}"
: "${DISCORD_BOT_TOKEN:?DISCORD_BOT_TOKEN is not set}"

INTERACTIONS_URL=$(terraform -chdir=terraform/envs/dev output -raw interactions_url)

echo "Setting Interactions Endpoint URL: $INTERACTIONS_URL"

RESPONSE_FILE=$(mktemp)
trap 'rm -f "$RESPONSE_FILE"' EXIT

HTTP_CODE=$(curl -s -o "$RESPONSE_FILE" -w "%{http_code}" -X PATCH \
  "https://discord.com/api/v10/applications/${DISCORD_APP_ID}" \
  -H "Authorization: Bot ${DISCORD_BOT_TOKEN}" \
  -H "Content-Type: application/json" \
  -d "{\"interactions_endpoint_url\": \"${INTERACTIONS_URL}\"}")

if [ "$HTTP_CODE" -ge 200 ] && [ "$HTTP_CODE" -lt 300 ]; then
  echo "Interactions Endpoint URL registered successfully."
else
  echo "Failed to register endpoint (HTTP $HTTP_CODE):" >&2
  cat "$RESPONSE_FILE" >&2
  echo "" >&2
  exit 1
fi
