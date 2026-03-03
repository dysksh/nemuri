#!/usr/bin/env bash
set -euo pipefail

: "${DISCORD_APP_ID:?DISCORD_APP_ID is not set}"
: "${DISCORD_BOT_TOKEN:?DISCORD_BOT_TOKEN is not set}"

curl -sfS -X POST \
  "https://discord.com/api/v10/applications/${DISCORD_APP_ID}/commands" \
  -H "Authorization: Bot ${DISCORD_BOT_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "agent",
    "description": "Submit a task to the agent",
    "options": [
      {
        "name": "prompt",
        "description": "What you want the agent to do",
        "type": 3,
        "required": true
      }
    ]
  }'

echo ""
echo "Slash command /agent registered successfully."
