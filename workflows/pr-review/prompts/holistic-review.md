You are making a final assessment of a GitHub pull request. The code review is complete.

## Your inputs

Read these files:
- `.ai/pr-data/code-review-findings.md` — detailed code review from the previous step
- `.ai/pr-data/pr-view.txt` — PR description and discussion
- `.ai/pr-data/pr-meta.json` — structured metadata
- `.ai/pr-data/build-report.json` — build/test results
- `.ai/pr-data/pr-diff.patch` — the full diff (skim for overall scope)

## What to assess

1. **Net value**: Is this PR a net-positive change? Does it improve the codebase more than
   it costs in complexity, risk, or maintenance burden?
2. **Completeness**: Does the PR achieve what it claims? Are there missing pieces?
3. **Risk**: What could go wrong if merged? Migration risks, behavioral changes, edge cases?
4. **Decision**: Should this be merged, merged with fixes, or sent back?

## Output

Write `review-report.md` in the workspace root (NOT in .ai/pr-data/):

```markdown
# PR Review Report

**PR**: #<number> — <title>
**Repo**: <owner/repo>
**Author**: <author>

## Decision: [MERGE | MERGE-FIX | FIX-MERGE | REJECT]

- MERGE: Ship it as-is
- MERGE-FIX: Merge now, fix minor issues in a follow-up
- FIX-MERGE: Issues must be fixed before merging
- REJECT: Fundamental problems, needs rethinking

## Rationale
[3-5 sentences. What does the PR do, is it worth merging, why this decision.]

## Build & Test Results

| Check | Status |
|-------|--------|
| Build | pass/fail |
| Tests | pass/fail |

## Critical Issues
[From code review. Must be fixed. "None" if clean.]

## Warnings
[Non-blocking concerns. "None" if clean.]

## Positive Observations
[What the PR does well.]

## Next Actions
[Specific, ordered list of what should happen. Be directive but not prescriptive.]
```

Rules:
- Bias toward landing contributions. Prefer MERGE-FIX over FIX-MERGE for minor issues.
- Keep the report under 50 lines. No padding or filler.
- Every action item must be specific enough for someone unfamiliar with the PR to execute.
- If the code review found nothing concerning, say so briefly.
