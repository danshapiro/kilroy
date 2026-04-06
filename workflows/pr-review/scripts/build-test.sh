#!/bin/sh
# Detect build system and run build + tests.
# Exits 0 on success, non-zero if build or tests fail.

set -e

SCRATCH=".ai/pr-data"
mkdir -p "$SCRATCH"

# Detect build system
BUILD_CMD=""
TEST_CMD=""

if [ -f "go.mod" ]; then
    BUILD_CMD="go build ./..."
    TEST_CMD="go test ./..."
elif [ -f "Cargo.toml" ]; then
    BUILD_CMD="cargo build"
    TEST_CMD="cargo test"
elif [ -f "package.json" ]; then
    if [ -f "yarn.lock" ]; then
        BUILD_CMD="yarn install && yarn build"
        TEST_CMD="yarn test"
    else
        BUILD_CMD="npm install && npm run build"
        TEST_CMD="npm test"
    fi
elif [ -f "Makefile" ] || [ -f "makefile" ]; then
    BUILD_CMD="make"
    TEST_CMD="make test"
elif [ -f "CMakeLists.txt" ]; then
    BUILD_CMD="cmake -B build && cmake --build build"
    TEST_CMD="cd build && ctest"
fi

if [ -z "$BUILD_CMD" ]; then
    echo "No recognized build system found"
    echo '{"build": "skip", "test": "skip", "reason": "no build system detected"}' > "$SCRATCH/build-report.json"
    # Not a failure — some repos don't have a build step
    exit 0
fi

echo "=== Detected build system ==="
echo "  Build: $BUILD_CMD"
echo "  Test:  $TEST_CMD"

# Run build
echo ""
echo "=== Build ==="
BUILD_STATUS="pass"
if eval "$BUILD_CMD" 2>"$SCRATCH/build-stderr.txt"; then
    echo "Build: PASS"
else
    BUILD_STATUS="fail"
    echo "Build: FAIL"
    cat "$SCRATCH/build-stderr.txt"
fi

# Run tests (only if build passed)
TEST_STATUS="skip"
if [ "$BUILD_STATUS" = "pass" ] && [ -n "$TEST_CMD" ]; then
    echo ""
    echo "=== Tests ==="
    if eval "$TEST_CMD" 2>&1 | tee "$SCRATCH/test-output.txt"; then
        TEST_STATUS="pass"
        echo "Tests: PASS"
    else
        TEST_STATUS="fail"
        echo "Tests: FAIL"
    fi
fi

# Write structured report
cat > "$SCRATCH/build-report.json" <<EOF
{"build": "$BUILD_STATUS", "test": "$TEST_STATUS", "build_cmd": "$BUILD_CMD", "test_cmd": "$TEST_CMD"}
EOF

# Exit non-zero if anything failed
if [ "$BUILD_STATUS" = "fail" ] || [ "$TEST_STATUS" = "fail" ]; then
    exit 1
fi

exit 0
