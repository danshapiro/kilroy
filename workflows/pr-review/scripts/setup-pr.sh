#!/bin/sh
# Checkout a PR branch into the current worktree and merge main.

set -e

: "${PR_REPO:?PR_REPO must be set (owner/repo format)}"
: "${PR_NUMBER:?PR_NUMBER must be set}"

SCRATCH=".ai/pr-data"
mkdir -p "$SCRATCH"

echo "=== Setting up PR #${PR_NUMBER} from ${PR_REPO} ==="

# Add the PR's repo as a remote if needed
REMOTE_URL="https://github.com/${PR_REPO}.git"
if ! git remote get-url pr-origin >/dev/null 2>&1; then
    git remote add pr-origin "$REMOTE_URL"
fi

# Fetch PR metadata via gh
gh pr view "$PR_NUMBER" --repo "$PR_REPO" \
  --json title,body,author,labels,files,baseRefName,headRefName,additions,deletions,changedFiles \
  > "$SCRATCH/pr-meta.json"
gh pr view "$PR_NUMBER" --repo "$PR_REPO" > "$SCRATCH/pr-view.txt"
gh pr diff "$PR_NUMBER" --repo "$PR_REPO" > "$SCRATCH/pr-diff.patch"

# Checkout the PR branch
gh pr checkout "$PR_NUMBER" --repo "$PR_REPO" --force

# Record the PR branch state
PR_SHA=$(git rev-parse HEAD)
echo "PR branch HEAD: $PR_SHA"

# Merge main into the PR branch (no rebase — preserve history)
BASE_BRANCH=$(cat "$SCRATCH/pr-meta.json" | python3 -c "import sys,json; print(json.load(sys.stdin)['baseRefName'])")
echo "Merging ${BASE_BRANCH} into PR branch..."
git fetch pr-origin "$BASE_BRANCH"
if git merge "pr-origin/${BASE_BRANCH}" --no-edit 2>"$SCRATCH/merge-stderr.txt"; then
    echo "MERGE_STATUS=clean" > "$SCRATCH/merge-result.txt"
    echo "Merge: clean"
else
    echo "MERGE_STATUS=conflict" > "$SCRATCH/merge-result.txt"
    git diff --name-only --diff-filter=U > "$SCRATCH/conflict-files.txt" 2>/dev/null || true
    git merge --abort || true
    echo "Merge: CONFLICTS detected (see $SCRATCH/conflict-files.txt)"
fi

# Stats
TITLE=$(python3 -c "import json; print(json.load(open('$SCRATCH/pr-meta.json'))['title'])")
FILES=$(python3 -c "import json; print(json.load(open('$SCRATCH/pr-meta.json'))['changedFiles'])")
ADDS=$(python3 -c "import json; print(json.load(open('$SCRATCH/pr-meta.json'))['additions'])")
DELS=$(python3 -c "import json; print(json.load(open('$SCRATCH/pr-meta.json'))['deletions'])")

echo ""
echo "PR #${PR_NUMBER}: ${TITLE}"
echo "  Files changed: ${FILES}  (+${ADDS} / -${DELS})"
echo "  Diff: $(wc -l < "$SCRATCH/pr-diff.patch" | tr -d ' ') lines"
echo "  Data: $SCRATCH/"
