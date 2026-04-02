#!/bin/sh
# Checkout a PR branch into the current worktree via raw git, merge base branch.

set -e

: "${PR_REPO:?PR_REPO must be set (owner/repo format)}"
: "${PR_NUMBER:?PR_NUMBER must be set}"

SCRATCH=".ai/pr-data"
mkdir -p "$SCRATCH"

echo "=== Setting up PR #${PR_NUMBER} from ${PR_REPO} ==="

# Preserve workflow scripts before branch switch (PR branch won't have them)
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
for f in "$SCRIPT_DIR"/*.sh; do
    cp "$f" "$SCRATCH/" 2>/dev/null || true
done

# Fetch PR metadata via gh (read-only, no state changes)
gh pr view "$PR_NUMBER" --repo "$PR_REPO" \
  --json title,body,author,labels,files,baseRefName,headRefName,additions,deletions,changedFiles \
  > "$SCRATCH/pr-meta.json"
gh pr view "$PR_NUMBER" --repo "$PR_REPO" > "$SCRATCH/pr-view.txt"
gh pr diff "$PR_NUMBER" --repo "$PR_REPO" > "$SCRATCH/pr-diff.patch"

# Extract metadata
BASE_BRANCH=$(python3 -c "import json; print(json.load(open('$SCRATCH/pr-meta.json'))['baseRefName'])")
HEAD_BRANCH=$(python3 -c "import json; print(json.load(open('$SCRATCH/pr-meta.json'))['headRefName'])")
TITLE=$(python3 -c "import json; print(json.load(open('$SCRATCH/pr-meta.json'))['title'])")

# Add upstream remote for the PR's repo
REMOTE_URL="https://github.com/${PR_REPO}.git"
if ! git remote get-url upstream >/dev/null 2>&1; then
    git remote add upstream "$REMOTE_URL"
fi

# Fetch PR ref and base branch via raw git
echo "Fetching PR #${PR_NUMBER} head and base (${BASE_BRANCH})..."
git fetch upstream "pull/${PR_NUMBER}/head" --quiet
git fetch upstream "$BASE_BRANCH" --quiet

# Checkout PR at FETCH_HEAD on a unique branch name for this run
# Unique name avoids git worktree branch-lock conflicts with parallel runs
RUN_TAG=$(echo "${KILROY_RUN_ID:-$$}" | tail -c 9)
LOCAL_BRANCH="pr-review/${PR_NUMBER}-${RUN_TAG}"
git checkout -b "$LOCAL_BRANCH" FETCH_HEAD --quiet

PR_SHA=$(git rev-parse HEAD)
echo "PR branch HEAD: $PR_SHA (local: $LOCAL_BRANCH)"

# Merge base branch into PR branch (no rebase — preserve history)
echo "Merging upstream/${BASE_BRANCH} into PR branch..."
if git merge "upstream/${BASE_BRANCH}" --no-edit 2>"$SCRATCH/merge-stderr.txt"; then
    echo "MERGE_STATUS=clean" > "$SCRATCH/merge-result.txt"
    echo "Merge: clean"
else
    echo "MERGE_STATUS=conflict" > "$SCRATCH/merge-result.txt"
    git diff --name-only --diff-filter=U > "$SCRATCH/conflict-files.txt" 2>/dev/null || true
    git merge --abort || true
    echo "Merge: CONFLICTS (see $SCRATCH/conflict-files.txt)"
fi

# Write setup summary
FILES=$(python3 -c "import json; print(json.load(open('$SCRATCH/pr-meta.json'))['changedFiles'])")
ADDS=$(python3 -c "import json; print(json.load(open('$SCRATCH/pr-meta.json'))['additions'])")
DELS=$(python3 -c "import json; print(json.load(open('$SCRATCH/pr-meta.json'))['deletions'])")

echo ""
echo "PR #${PR_NUMBER}: ${TITLE}"
echo "  Author: $(python3 -c "import json; print(json.load(open('$SCRATCH/pr-meta.json'))['author']['login'])")"
echo "  Branch: ${HEAD_BRANCH} -> ${BASE_BRANCH}"
echo "  Files changed: ${FILES}  (+${ADDS} / -${DELS})"
echo "  Diff: $(wc -l < "$SCRATCH/pr-diff.patch" | tr -d ' ') lines"
