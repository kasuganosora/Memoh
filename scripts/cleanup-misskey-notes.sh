#!/bin/bash
# Delete all bot notes from Misskey timeline (cleanup after spam bug).
# Respects rate limits: 1 request per second for notes/delete.
# Usage: bash scripts/cleanup-misskey-notes.sh

INSTANCE="https://maid.lat"
TOKEN="eMYejNTvYijdlo5mBLcPdrSbqbUqyqhJ"
BOT_USER_ID="al9dhx46xbg200n5"

DELETED=0
FAILED=0

echo "Fetching and deleting bot notes from Misskey..."

while true; do
  RESPONSE=$(curl -s -X POST "${INSTANCE}/api/notes/timeline" \
    -H "Content-Type: application/json" \
    -d "{\"i\":\"${TOKEN}\",\"limit\":100}")

  BOT_IDS=$(echo "$RESPONSE" | python3 -c "
import json, sys
data = json.load(sys.stdin)
if not isinstance(data, list):
    sys.exit(1)
for n in data:
    if n.get('user',{}).get('id') == '${BOT_USER_ID}':
        print(n['id'])
")

  COUNT=$(echo "$BOT_IDS" | grep -cE '^[a-z0-9]{10,}$' 2>/dev/null || echo 0)

  if [ "$COUNT" -eq 0 ] || [ -z "$BOT_IDS" ]; then
    break
  fi

  echo "  Found $COUNT bot notes, deleting (1/s due to rate limit)..."

  for ID in $BOT_IDS; do
    RESULT=$(curl -s -X POST "${INSTANCE}/api/notes/delete" \
      -H "Content-Type: application/json" \
      -d "{\"i\": \"${TOKEN}\", \"noteId\": \"${ID}\"}")

    if [ -z "$RESULT" ]; then
      DELETED=$((DELETED + 1))
      echo "    [${DELETED}] Deleted ${ID}"
    else
      FAILED=$((FAILED + 1))
      echo "    Failed: ${ID}"
      # If rate limited, wait longer
      if echo "$RESULT" | grep -q "RATE_LIMIT"; then
        sleep 5
        # Retry once
        RESULT2=$(curl -s -X POST "${INSTANCE}/api/notes/delete" \
          -H "Content-Type: application/json" \
          -d "{\"i\": \"${TOKEN}\", \"noteId\": \"${ID}\"}")
        if [ -z "$RESULT2" ]; then
          DELETED=$((DELETED + 1))
          FAILED=$((FAILED - 1))
          echo "    [${DELETED}] Retry OK: ${ID}"
        fi
      fi
    fi
    sleep 1.2
  done

  echo "  Batch done. Total deleted: $DELETED, failed: $FAILED"
  sleep 2
done

echo ""
echo "Done. Deleted: $DELETED, Failed: $FAILED"
