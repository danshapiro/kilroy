#!/bin/sh
# Generate build-report.json summarizing the build-and-test workflow.
# Usage: generate-report.sh <status>
#   status: "success", "build_failed", or "test_failed"

set -e

STATUS="${1:-unknown}"
META_FILE=".kilroy/build-meta.json"
BUILD_RESULT=".kilroy/build-result.json"
TEST_RESULT=".kilroy/test-result.json"
REPORT_FILE="build-report.json"

# Read build system from meta
BUILD_SYSTEM="unknown"
if [ -f "$META_FILE" ]; then
  BUILD_SYSTEM=$(sed -n 's/.*"build_system": *"\(.*\)".*/\1/p' "$META_FILE")
fi

# Read build result if available
BUILD_STATUS="skipped"
BUILD_DURATION=0
BUILD_CMD=""
BUILD_SNIPPET=""
if [ -f "$BUILD_RESULT" ]; then
  BUILD_EXIT=$(sed -n 's/.*"exit_code": *\([0-9]*\).*/\1/p' "$BUILD_RESULT")
  BUILD_DURATION=$(sed -n 's/.*"duration_seconds": *\([0-9]*\).*/\1/p' "$BUILD_RESULT")
  BUILD_CMD=$(sed -n 's/.*"command": *"\(.*\)".*/\1/p' "$BUILD_RESULT")
  if [ "$BUILD_EXIT" = "0" ]; then
    BUILD_STATUS="pass"
  else
    BUILD_STATUS="fail"
  fi
fi

# Read test result if available
TEST_STATUS="skipped"
TEST_DURATION=0
TEST_CMD=""
if [ -f "$TEST_RESULT" ]; then
  TEST_EXIT=$(sed -n 's/.*"exit_code": *\([0-9]*\).*/\1/p' "$TEST_RESULT")
  TEST_DURATION=$(sed -n 's/.*"duration_seconds": *\([0-9]*\).*/\1/p' "$TEST_RESULT")
  TEST_CMD=$(sed -n 's/.*"command": *"\(.*\)".*/\1/p' "$TEST_RESULT")
  if [ "$TEST_EXIT" = "0" ]; then
    TEST_STATUS="pass"
  else
    TEST_STATUS="fail"
  fi
fi

TOTAL_DURATION=$((BUILD_DURATION + TEST_DURATION))
TIMESTAMP=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

# Build the JSON report using python3 for safe encoding
python3 -c "
import json, sys

report = {
    'status': '$STATUS',
    'timestamp': '$TIMESTAMP',
    'build_system': '$BUILD_SYSTEM',
    'build': {
        'status': '$BUILD_STATUS',
        'command': '$BUILD_CMD',
        'duration_seconds': $BUILD_DURATION,
    },
    'test': {
        'status': '$TEST_STATUS',
        'command': '$TEST_CMD',
        'duration_seconds': $TEST_DURATION,
    },
    'total_duration_seconds': $TOTAL_DURATION,
}

# Merge in output snippets from result files if available
for key, path in [('build', '$BUILD_RESULT'), ('test', '$TEST_RESULT')]:
    try:
        with open(path) as f:
            data = json.load(f)
            report[key]['output_snippet'] = data.get('output_snippet', '')
    except (FileNotFoundError, json.JSONDecodeError):
        pass

with open('$REPORT_FILE', 'w') as f:
    json.dump(report, f, indent=2)
print(json.dumps(report, indent=2))
"

echo ""
echo "Report written to $REPORT_FILE"
echo "Overall status: $STATUS"
