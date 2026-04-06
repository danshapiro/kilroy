#!/bin/sh
# Collect the PR diff and file list for review agents.
# Reads: KILROY_INPUT_PR_REPO, KILROY_INPUT_PR_NUMBER. Requires: gh.

set -e

: "${KILROY_INPUT_PR_REPO:?KILROY_INPUT_PR_REPO must be set (owner/repo)}"
: "${KILROY_INPUT_PR_NUMBER:?KILROY_INPUT_PR_NUMBER must be set}"

PR_REPO="$KILROY_INPUT_PR_REPO"
PR_NUMBER="$KILROY_INPUT_PR_NUMBER"
SCRATCH=".ai/pr-data"
mkdir -p "$SCRATCH"

echo "=== Collecting PR diff ==="

# Get the diff
gh pr diff "$PR_NUMBER" --repo "$PR_REPO" > "$SCRATCH/pr-diff.patch"
DIFF_LINES=$(wc -l < "$SCRATCH/pr-diff.patch" | tr -d ' ')
echo "Diff: ${DIFF_LINES} lines"

# Get changed file list
gh pr view "$PR_NUMBER" --repo "$PR_REPO" --json files \
  | python3 -c "
import json, sys
data = json.load(sys.stdin)
for f in data.get('files', []):
    print(f['path'])
" > "$SCRATCH/changed-files.txt"

FILE_COUNT=$(wc -l < "$SCRATCH/changed-files.txt" | tr -d ' ')
echo "Changed files: ${FILE_COUNT}"
cat "$SCRATCH/changed-files.txt"
