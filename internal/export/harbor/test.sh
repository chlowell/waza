#!/bin/sh

RESPONSE_FILE="/app/response.md"

if [ ! -f "$RESPONSE_FILE" ]; then
    echo "No response file found at $RESPONSE_FILE"
    echo 0 > /logs/verifier/reward.txt
    exit 0
fi

# Make the agent's response available for inspection after the run
cp "$RESPONSE_FILE" /logs/artifacts

waza grade /waza/eval.yaml \
    --task "{task}" \
    --output "$RESPONSE_FILE" \
    --context-dir /waza/fixtures \
    --workspace /app \
    --reward-file /logs/verifier/reward.txt \
    --reward-format txt

# fail the run if waza grade errored
if [ $? -ne 0 ]; then
    echo 0 > /logs/verifier/reward.txt
fi
