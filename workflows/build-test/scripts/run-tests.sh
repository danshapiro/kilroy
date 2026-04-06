#!/bin/sh
# Run the test command detected by detect-build-system.sh.
# Writes test timing and output to .kilroy/test-result.json.

set -e

META_FILE=".kilroy/build-meta.json"
RESULT_FILE=".kilroy/test-result.json"

if [ ! -f "$META_FILE" ]; then
  echo "ERROR: $META_FILE not found. Run detect-build-system.sh first." >&2
  exit 1
fi

# Parse test command from meta (portable — no jq dependency)
TEST_CMD=$(sed -n 's/.*"test_command": *"\(.*\)".*/\1/p' "$META_FILE")

if [ -z "$TEST_CMD" ]; then
  echo "ERROR: No test command found in $META_FILE" >&2
  exit 1
fi

echo "Running tests: $TEST_CMD"

START_TIME=$(date +%s)
TEST_OUTPUT=""
TEST_EXIT=0

TEST_OUTPUT=$(eval "$TEST_CMD" 2>&1) || TEST_EXIT=$?
END_TIME=$(date +%s)
DURATION=$((END_TIME - START_TIME))

# Truncate output for the report (keep last 80 lines)
SNIPPET=$(echo "$TEST_OUTPUT" | tail -80)

cat > "$RESULT_FILE" <<ENDJSON
{
  "step": "test",
  "command": "$TEST_CMD",
  "exit_code": $TEST_EXIT,
  "duration_seconds": $DURATION,
  "output_snippet": $(printf '%s' "$SNIPPET" | python3 -c 'import sys,json; print(json.dumps(sys.stdin.read()))' 2>/dev/null || echo '"(output encoding failed)"')
}
ENDJSON

echo "$TEST_OUTPUT"

if [ $TEST_EXIT -ne 0 ]; then
  echo "Tests FAILED (exit code $TEST_EXIT)"
  exit 1
fi

echo "Tests passed in ${DURATION}s"
