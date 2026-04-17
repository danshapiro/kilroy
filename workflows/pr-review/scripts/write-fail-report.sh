#!/bin/sh
# Generate review-report.md from build/test failure data.
# Called when build_test exits non-zero.

SCRATCH=".ai/pr-data"

# Read build report if it exists
BUILD_STATUS="unknown"
TEST_STATUS="unknown"
if [ -f "$SCRATCH/build-report.json" ]; then
    BUILD_STATUS=$(python3 -c "import json; print(json.load(open('$SCRATCH/build-report.json'))['build'])" 2>/dev/null || echo "unknown")
    TEST_STATUS=$(python3 -c "import json; print(json.load(open('$SCRATCH/build-report.json'))['test'])" 2>/dev/null || echo "unknown")
fi

# Read PR title
TITLE="(unknown)"
if [ -f "$SCRATCH/pr-meta.json" ]; then
    TITLE=$(python3 -c "import json; print(json.load(open('$SCRATCH/pr-meta.json'))['title'])" 2>/dev/null || echo "(unknown)")
fi

# Build the failure report
cat > review-report.md <<EOF
# PR Review Report

**PR**: #${KILROY_INPUT_PR_NUMBER} — ${TITLE}
**Repo**: ${KILROY_INPUT_PR_REPO}
**Result**: BUILD/TEST FAILURE — code review skipped

## Build & Test Results

| Check   | Status |
|---------|--------|
| Build   | ${BUILD_STATUS} |
| Tests   | ${TEST_STATUS} |

## Details

The PR failed build or test checks. Code review was skipped because the code
does not compile or pass tests in its current state.

EOF

# Append build stderr if available
if [ -f "$SCRATCH/build-stderr.txt" ] && [ -s "$SCRATCH/build-stderr.txt" ]; then
    echo "### Build Errors" >> review-report.md
    echo '```' >> review-report.md
    head -100 "$SCRATCH/build-stderr.txt" >> review-report.md
    echo '```' >> review-report.md
fi

# Append test output if available
if [ -f "$SCRATCH/test-output.txt" ] && [ -s "$SCRATCH/test-output.txt" ]; then
    echo "### Test Output" >> review-report.md
    echo '```' >> review-report.md
    tail -50 "$SCRATCH/test-output.txt" >> review-report.md
    echo '```' >> review-report.md
fi

echo "Wrote review-report.md (build/test failure)"
