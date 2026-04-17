#!/bin/sh
# Combine agent perspectives into a single output file.
set -e

echo "Combining perspectives..."

OUTPUT="combined-output.md"

cat > "${OUTPUT}" <<'HEADER'
# Multi-Tool Perspectives

Three different AI coding tools were given the same topic and asked to write their perspective.

HEADER

for tool in claude codex opencode; do
    FILE=".kilroy/data/${tool}-perspective.md"
    if [ -f "${FILE}" ]; then
        echo "## ${tool} perspective" >> "${OUTPUT}"
        echo "" >> "${OUTPUT}"
        cat "${FILE}" >> "${OUTPUT}"
        echo "" >> "${OUTPUT}"
        echo "---" >> "${OUTPUT}"
        echo "" >> "${OUTPUT}"
        echo "  Found: ${FILE}"
    else
        echo "  Missing: ${FILE} (skipping)"
        echo "## ${tool} perspective" >> "${OUTPUT}"
        echo "" >> "${OUTPUT}"
        echo "*No output was produced by ${tool}.*" >> "${OUTPUT}"
        echo "" >> "${OUTPUT}"
    fi
done

LINES=$(wc -l < "${OUTPUT}" | tr -d ' ')
echo "Combined output: ${OUTPUT} (${LINES} lines)"
