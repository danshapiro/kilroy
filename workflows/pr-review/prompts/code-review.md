You are reviewing a GitHub pull request for code quality, bugs, and test coverage.

The PR has already been checked out, built, and tested. Build/test results and the
diff are available in `.ai/pr-data/`.

## Your inputs

Read these files first:
- `.ai/pr-data/pr-view.txt` — PR description and discussion
- `.ai/pr-data/pr-meta.json` — structured metadata (files, author, branches)
- `.ai/pr-data/pr-diff.patch` — the full diff
- `.ai/pr-data/changed-files.txt` — list of changed files
- `.ai/pr-data/build-report.json` — build/test results
- `.ai/pr-data/merge-result.txt` — merge status

## What to do

Read the actual source files that were changed. Read surrounding code for context.
For each changed file, assess:

1. **Correctness**: Does the code do what the PR claims? Are there logic errors, off-by-one
   bugs, race conditions, nil pointer risks?
2. **Security**: SQL injection, XSS, command injection, hardcoded secrets, unsafe deserialization?
3. **Error handling**: Are errors checked? Are failure modes handled gracefully?
4. **Test coverage**: Do tests exist for the changed code? Do they test the right things?
   Are edge cases covered? Do tests test real behavior (not mocked behavior)?
5. **Naming and clarity**: Are names descriptive? Is the code readable without comments?
6. **Scope**: Does every change serve the PR's stated purpose? Flag unrelated changes.

## Output

Write your findings to `.ai/pr-data/code-review-findings.md`:

```markdown
## Code Review Findings

### Critical Issues
[Issues that must be fixed before merge. Empty section if none.]

### Warnings
[Issues worth noting but not blocking. Empty section if none.]

### Positive Observations
[Things done well that should be preserved.]

### File-by-File Notes
[For each changed file with findings, list file:line and what you found.]
```

Be specific. Cite file paths and line numbers. Focus on substance, not style nitpicks.
If the code is clean, say so briefly — don't manufacture findings.
