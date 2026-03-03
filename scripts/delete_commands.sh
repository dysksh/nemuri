#!/usr/bin/env bash
set -euo pipefail

: "${DISCORD_APP_ID:?DISCORD_APP_ID is not set}"
: "${DISCORD_BOT_TOKEN:?DISCORD_BOT_TOKEN is not set}"

echo "Fetching registered commands..."
commands=$(curl -sfS \
  "https://discord.com/api/v10/applications/${DISCORD_APP_ID}/commands" \
  -H "Authorization: Bot ${DISCORD_BOT_TOKEN}")

echo "$commands" | jq -r '.[] | "\(.id) \(.name)"'

read -rp "Enter command ID to delete (or 'all' to delete all): " input

if [ "$input" = "all" ]; then
  echo "$commands" | jq -r '.[].id' | while read -r id; do
    curl -sfS -X DELETE \
      "https://discord.com/api/v10/applications/${DISCORD_APP_ID}/commands/${id}" \
      -H "Authorization: Bot ${DISCORD_BOT_TOKEN}"
    echo "Deleted command: ${id}"
  done
else
  curl -sfS -X DELETE \
    "https://discord.com/api/v10/applications/${DISCORD_APP_ID}/commands/${input}" \
    -H "Authorization: Bot ${DISCORD_BOT_TOKEN}"
  echo "Deleted command: ${input}"
fi

echo "Done."
