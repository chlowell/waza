#!/bin/sh

RESULTS="/logs/artifacts/waza-results.json"

# If the waza agent didn't produce results, fail
if [ ! -f "$RESULTS" ]; then
    echo "No waza results found at $RESULTS"
    echo 0 > /logs/verifier/reward.txt
    exit 0
fi

waza grade /waza/eval.yaml \
    --task "{task}" \
    --results "$RESULTS" \
    --workspace /app \
    --reward-file /logs/verifier/reward.txt \
    --reward-format txt \
    -v 2>&1 | tee /logs/verifier/grade-output.txt

if [ $? -ne 0 ]; then
    echo 0 > /logs/verifier/reward.txt
fi
