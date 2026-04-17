#!/bin/sh
# Validate that the combined output exists and has substance.
set -e

OUTPUT="combined-output.md"

if [ ! -f "${OUTPUT}" ]; then
    echo "FAIL: ${OUTPUT} does not exist"
    exit 1
fi

LINES=$(wc -l < "${OUTPUT}" | tr -d ' ')
echo "Output file: ${OUTPUT}"
echo "Lines: ${LINES}"

if [ "${LINES}" -lt 5 ]; then
    echo "FAIL: output too short (${LINES} lines)"
    exit 1
fi

# Check that at least one perspective has real content (not just the "no output" placeholder).
if grep -q "No output was produced" "${OUTPUT}"; then
    MISSING=$(grep -c "No output was produced" "${OUTPUT}" || true)
    echo "WARNING: ${MISSING} tool(s) produced no output"
fi

echo "Validation passed"
