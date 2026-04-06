#!/bin/sh
# Checkout a PR branch, fetch metadata, merge base branch into it.
# Reads: KILROY_INPUT_PR_REPO, KILROY_INPUT_PR_NUMBER. Requires: gh, git.

set -e

: "${KILROY_INPUT_PR_REPO:?KILROY_INPUT_PR_REPO must be set (owner/repo)}"
: "${KILROY_INPUT_PR_NUMBER:?KILROY_INPUT_PR_NUMBER must be set}"

PR_REPO="$KILROY_INPUT_PR_REPO"
PR_NUMBER="$KILROY_INPUT_PR_NUMBER"
SCRATCH=".ai/pr-data"
mkdir -p "$SCRATCH"

echo "=== Setting up PR #${PR_NUMBER} from ${PR_REPO} ==="

# Fetch PR metadata via gh
gh pr view "$PR_NUMBER" --repo "$PR_REPO" \
  --json title,body,author,labels,files,baseRefName,headRefName,additions,deletions,changedFiles \
  > "$SCRATCH/pr-meta.json"
gh pr view "$PR_NUMBER" --repo "$PR_REPO" > "$SCRATCH/pr-view.txt"

# Extract metadata
BASE_BRANCH=$(python3 -c "import json; print(json.load(open('$SCRATCH/pr-meta.json'))['baseRefName'])")
HEAD_BRANCH=$(python3 -c "import json; print(json.load(open('$SCRATCH/pr-meta.json'))['headRefName'])")
TITLE=$(python3 -c "import json; print(json.load(open('$SCRATCH/pr-meta.json'))['title'])")

# Clone the repo if we're in an empty workspace
if [ ! -d ".git" ]; then
    echo "No git repo found — cloning ${PR_REPO}..."
    git clone "https://github.com/${PR_REPO}.git" _repo
    # Move contents up (clone into subdir, then move to workspace root)
    mv _repo/.git .
    mv _repo/* . 2>/dev/null || true
    mv _repo/.* . 2>/dev/null || true
    rmdir _repo 2>/dev/null || true
fi

# Add upstream remote for the PR's repo
REMOTE_URL="https://github.com/${PR_REPO}.git"
if ! git remote get-url upstream >/dev/null 2>&1; then
    git remote add upstream "$REMOTE_URL"
fi

# Fetch PR ref and base branch
echo "Fetching PR #${PR_NUMBER} head and base (${BASE_BRANCH})..."
git fetch upstream "pull/${PR_NUMBER}/head" --quiet
git fetch upstream "$BASE_BRANCH" --quiet

# Checkout PR at FETCH_HEAD
RUN_TAG=$(echo "${KILROY_RUN_ID:-$$}" | tail -c 9)
LOCAL_BRANCH="pr-review/${PR_NUMBER}-${RUN_TAG}"
git checkout -b "$LOCAL_BRANCH" FETCH_HEAD --quiet

PR_SHA=$(git rev-parse HEAD)
echo "PR branch HEAD: $PR_SHA (local: $LOCAL_BRANCH)"

# Merge base branch into PR branch
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

# Summary
FILES=$(python3 -c "import json; print(json.load(open('$SCRATCH/pr-meta.json'))['changedFiles'])")
ADDS=$(python3 -c "import json; print(json.load(open('$SCRATCH/pr-meta.json'))['additions'])")
DELS=$(python3 -c "import json; print(json.load(open('$SCRATCH/pr-meta.json'))['deletions'])")
AUTHOR=$(python3 -c "import json; print(json.load(open('$SCRATCH/pr-meta.json'))['author']['login'])")

echo ""
echo "PR #${PR_NUMBER}: ${TITLE}"
echo "  Author: ${AUTHOR}"
echo "  Branch: ${HEAD_BRANCH} -> ${BASE_BRANCH}"
echo "  Files changed: ${FILES}  (+${ADDS} / -${DELS})"
