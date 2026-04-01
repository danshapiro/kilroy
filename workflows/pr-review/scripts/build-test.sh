#!/bin/sh
# Run build and tests, capture results. Failure is a finding, not a blocker.

SCRATCH=".ai/pr-data"
mkdir -p "$SCRATCH"

echo "=== Build ==="
if go build ./... 2>"$SCRATCH/build-stderr.txt"; then
    echo "BUILD=pass" > "$SCRATCH/build-result.txt"
    echo "Build: PASS"
else
    echo "BUILD=fail" > "$SCRATCH/build-result.txt"
    echo "Build: FAIL (see $SCRATCH/build-stderr.txt)"
    cat "$SCRATCH/build-stderr.txt"
fi

echo ""
echo "=== Tests ==="
if go test ./... 2>&1 | tee "$SCRATCH/test-output.txt"; then
    echo "TESTS=pass" > "$SCRATCH/test-result.txt"
    echo "Tests: PASS"
else
    echo "TESTS=fail" > "$SCRATCH/test-result.txt"
    echo "Tests: FAIL (see $SCRATCH/test-output.txt)"
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
fi

# Always exit 0 — build/test failure is a finding, not a graph failure
exit 0
