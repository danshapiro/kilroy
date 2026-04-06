#!/bin/sh
# Run the build command detected by detect-build-system.sh.
# Writes build timing and output to .kilroy/build-result.json.

set -e

META_FILE=".kilroy/build-meta.json"
RESULT_FILE=".kilroy/build-result.json"

if [ ! -f "$META_FILE" ]; then
  echo "ERROR: $META_FILE not found. Run detect-build-system.sh first." >&2
  exit 1
fi

# Parse build command from meta (portable — no jq dependency)
BUILD_CMD=$(sed -n 's/.*"build_command": *"\(.*\)".*/\1/p' "$META_FILE")

if [ -z "$BUILD_CMD" ]; then
  echo "ERROR: No build command found in $META_FILE" >&2
  exit 1
fi

echo "Running build: $BUILD_CMD"

START_TIME=$(date +%s)
BUILD_OUTPUT=""
BUILD_EXIT=0

BUILD_OUTPUT=$(eval "$BUILD_CMD" 2>&1) || BUILD_EXIT=$?
END_TIME=$(date +%s)
DURATION=$((END_TIME - START_TIME))

# Truncate output for the report (keep last 80 lines)
SNIPPET=$(echo "$BUILD_OUTPUT" | tail -80)

cat > "$RESULT_FILE" <<ENDJSON
{
  "step": "build",
  "command": "$BUILD_CMD",
  "exit_code": $BUILD_EXIT,
  "duration_seconds": $DURATION,
  "output_snippet": $(printf '%s' "$SNIPPET" | python3 -c 'import sys,json; print(json.dumps(sys.stdin.read()))' 2>/dev/null || echo '"(output encoding failed)"')
}
ENDJSON

echo "$BUILD_OUTPUT"

if [ $BUILD_EXIT -ne 0 ]; then
  echo "Build FAILED (exit code $BUILD_EXIT)"
  exit 1
fi

echo "Build succeeded in ${DURATION}s"
