#!/bin/sh
# Detect the build system in the current workspace and write discovery to .kilroy/build-meta.json.

set -e

META_DIR=".kilroy"
META_FILE="$META_DIR/build-meta.json"
mkdir -p "$META_DIR"

BUILD_SYSTEM="unknown"
BUILD_CMD=""
TEST_CMD=""

if [ -f "go.mod" ]; then
  BUILD_SYSTEM="go"
  BUILD_CMD="go build ./..."
  TEST_CMD="go test ./..."
elif [ -f "Cargo.toml" ]; then
  BUILD_SYSTEM="cargo"
  BUILD_CMD="cargo build"
  TEST_CMD="cargo test"
elif [ -f "package.json" ]; then
  BUILD_SYSTEM="npm"
  # Check for build script in package.json
  if grep -q '"build"' package.json 2>/dev/null; then
    BUILD_CMD="npm run build"
  else
    BUILD_CMD="echo 'no build script defined; skipping'"
  fi
  if grep -q '"test"' package.json 2>/dev/null; then
    TEST_CMD="npm test"
  else
    TEST_CMD="echo 'no test script defined; skipping'"
  fi
elif [ -f "Makefile" ] || [ -f "makefile" ]; then
  BUILD_SYSTEM="make"
  BUILD_CMD="make"
  if grep -q '^test:' Makefile 2>/dev/null || grep -q '^test:' makefile 2>/dev/null; then
    TEST_CMD="make test"
  else
    TEST_CMD="echo 'no test target; skipping'"
  fi
elif [ -f "CMakeLists.txt" ]; then
  BUILD_SYSTEM="cmake"
  BUILD_CMD="cmake --build ."
  TEST_CMD="ctest"
elif [ -f "pom.xml" ]; then
  BUILD_SYSTEM="maven"
  BUILD_CMD="mvn compile"
  TEST_CMD="mvn test"
elif [ -f "build.gradle" ] || [ -f "build.gradle.kts" ]; then
  BUILD_SYSTEM="gradle"
  BUILD_CMD="./gradlew build"
  TEST_CMD="./gradlew test"
fi

# Allow input overrides
if [ -n "$KILROY_INPUT_BUILD_COMMAND" ]; then
  BUILD_CMD="$KILROY_INPUT_BUILD_COMMAND"
  echo "Using build command override: $BUILD_CMD"
fi
if [ -n "$KILROY_INPUT_TEST_COMMAND" ]; then
  TEST_CMD="$KILROY_INPUT_TEST_COMMAND"
  echo "Using test command override: $TEST_CMD"
fi

cat > "$META_FILE" <<EOF
{
  "build_system": "$BUILD_SYSTEM",
  "build_command": "$BUILD_CMD",
  "test_command": "$TEST_CMD"
}
EOF

echo "Detected build system: $BUILD_SYSTEM"
echo "Build command: $BUILD_CMD"
echo "Test command: $TEST_CMD"
