#!/bin/sh
# Run build, tests, and format checks. Captures results for the review agent.
# Always exits 0 — failures are findings, not blockers.

SCRATCH=".ai/pr-data"
mkdir -p "$SCRATCH"

echo "=== Build ==="
if go build ./... 2>"$SCRATCH/build-stderr.txt"; then
    echo "BUILD=pass" > "$SCRATCH/build-result.txt"
    echo "Build: PASS"
else
    echo "BUILD=fail" > "$SCRATCH/build-result.txt"
    echo "Build: FAIL"
    cat "$SCRATCH/build-stderr.txt"
fi

echo ""
echo "=== Tests ==="
go test ./... 2>&1 | tee "$SCRATCH/test-output.txt"
if [ "${PIPESTATUS[0]:-${pipestatus[1]:-1}}" -eq 0 ]; then
    echo "TESTS=pass" > "$SCRATCH/test-result.txt"
    echo "Tests: PASS"
else
    echo "TESTS=fail" > "$SCRATCH/test-result.txt"
    echo "Tests: FAIL"
fi

echo ""
echo "=== Format Check ==="
GOFMT_OUT=$(gofmt -l . 2>/dev/null || true)
if [ -z "$GOFMT_OUT" ]; then
    echo "GOFMT=pass" > "$SCRATCH/gofmt-result.txt"
    echo "gofmt: PASS"
else
    echo "GOFMT=fail" > "$SCRATCH/gofmt-result.txt"
    echo "$GOFMT_OUT" > "$SCRATCH/gofmt-files.txt"
    echo "gofmt: FAIL — $(echo "$GOFMT_OUT" | wc -l | tr -d ' ') files"
    cat "$SCRATCH/gofmt-files.txt"
fi

exit 0
