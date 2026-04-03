#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${1:-http://127.0.0.1:8317}"
API_KEY="${2:-123456}"
MODEL="${3:-gpt-5.4}"
PROMPT="${4:-Reply with OK and tell me the upstream model you used.}"

curl -sS "${BASE_URL%/}/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${API_KEY}" \
  -d "{
    \"model\": \"${MODEL}\",
    \"messages\": [
      {\"role\": \"user\", \"content\": $(printf '%s' "$PROMPT" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))')}
    ],
    \"stream\": false
  }"
echo
