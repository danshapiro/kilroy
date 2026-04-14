#!/bin/sh
# Stage a quick-launch task: write TASK.md from KILROY_INPUT_PROMPT, and
# (if provided) write CONTEXT.md from either KILROY_INPUT_CONTEXT_FILE (path
# to copy) or KILROY_INPUT_CONTEXT (inline string). Both land in .kilroy/ so
# the agent has a stable, predictable place to read them from.

set -e

: "${KILROY_INPUT_PROMPT:?KILROY_INPUT_PROMPT must be set (quick-launch requires --input with a prompt)}"

mkdir -p .kilroy

# Task description — required.
printf '%s\n' "$KILROY_INPUT_PROMPT" > .kilroy/TASK.md
echo "wrote .kilroy/TASK.md ($(wc -c < .kilroy/TASK.md) bytes)"

# Context — optional. A file path takes precedence over an inline string.
if [ -n "${KILROY_INPUT_CONTEXT_FILE:-}" ]; then
    if [ ! -r "$KILROY_INPUT_CONTEXT_FILE" ]; then
        echo "context_file not readable: $KILROY_INPUT_CONTEXT_FILE" >&2
        exit 1
    fi
    cp "$KILROY_INPUT_CONTEXT_FILE" .kilroy/CONTEXT.md
    echo "staged .kilroy/CONTEXT.md from $KILROY_INPUT_CONTEXT_FILE ($(wc -c < .kilroy/CONTEXT.md) bytes)"
elif [ -n "${KILROY_INPUT_CONTEXT:-}" ]; then
    printf '%s\n' "$KILROY_INPUT_CONTEXT" > .kilroy/CONTEXT.md
    echo "wrote .kilroy/CONTEXT.md from inline context ($(wc -c < .kilroy/CONTEXT.md) bytes)"
else
    echo "no context provided (optional)"
fi
