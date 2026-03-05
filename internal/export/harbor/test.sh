#!/bin/sh

RESULTS="/logs/artifacts/waza-results.json"
REWARD = "/logs/verifier/reward.txt"

if [ ! -f "$RESULTS" ]; then
    echo "No results found at $RESULTS"
    echo 0 > /logs/verifier/reward.txt
    exit 0
fi

# Extract success_rate from the JSON (e.g. "success_rate": 1)
RATE=$(grep -o '"success_rate":[^,}]*' "$RESULTS" | head -1 | sed 's/"success_rate":\s*//')
if [ "$RATE" = "1" ]; then
    echo 1 > "$REWARD"
else
    echo 0 > "$REWARD"
fi
