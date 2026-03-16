#!/bin/sh

RESULTS="/logs/artifacts/waza-results.json"
GRADE_JSON="/logs/verifier/grade.json"
GRADE_LOG="/logs/verifier/grade-output.txt"
REWARD_FILE="/logs/verifier/reward.txt"

# If the waza agent didn't produce results, fail
if [ ! -f "$RESULTS" ]; then
    echo "No waza results found at $RESULTS"
    echo 0 > "$REWARD_FILE"
    exit 0
fi

waza grade /waza/eval.yaml \
    --task "{task}" \
    --results "$RESULTS" \
    --workspace /app \
    -v > "$GRADE_JSON" 2> "$GRADE_LOG"

if [ $? -ne 0 ]; then
    echo 0 > "$REWARD_FILE"
    exit 0
fi

score=$(awk -F: '/"overall_score"/ {
    gsub(/[ ,]/, "", $2)
    print $2
    exit
}' "$GRADE_JSON")

if [ -z "$score" ]; then
    echo 0 > "$REWARD_FILE"
    exit 0
fi

printf '%s\n' "$score" > "$REWARD_FILE"
