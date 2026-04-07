#!/bin/sh
# Prepare .kilroy/ convention files from run inputs for agent consumption.
set -e

echo "Preparing input data..."

# Read topic from input env var (set by engine from --input).
TOPIC="${KILROY_INPUT_TOPIC:-unknown topic}"
STYLE="${KILROY_INPUT_STYLE:-thoughtful and concise}"

# Write INPUT.md for agents to read.
cat > .kilroy/INPUT.md <<EOF
# Topic

${TOPIC}

## Style Guidance

Write in a ${STYLE} style. Keep it brief — 2-3 short paragraphs.
EOF

# Ensure data directory exists for agent outputs.
mkdir -p .kilroy/data

# Write TASK.md with per-node output paths.
cat > .kilroy/TASK.md <<EOF
# Task Assignment

Each agent should write their perspective to a specific file:

- Claude: .kilroy/data/claude-perspective.md
- Codex: .kilroy/data/codex-perspective.md
- OpenCode: .kilroy/data/opencode-perspective.md

Read .kilroy/INPUT.md for the topic and style guidance.
EOF

echo "Topic: ${TOPIC}"
echo "Style: ${STYLE}"
echo "Input files written to .kilroy/"
