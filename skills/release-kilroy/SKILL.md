---
name: release-kilroy
description: Use when preparing a Kilroy release — writing release notes, tagging, and publishing via goreleaser on GitHub.
---

# Releasing Kilroy

## When to Use

Invoke this skill before cutting a release. Never release without explicit user request.

## Sanity Check

Before anything: if something seems off (discontinuous version jump like 0.5→2.0, failing tests, broken code), stop and confirm with the user.

## Writing Release Notes

Release notes are **user-facing, not code-facing**. Write from the perspective of someone running Kilroy pipelines, not someone reading the git log.

### Structure

Two sections, in this order:

**"New things you can do"** — Features that let users do something they couldn't before. Each item: what it is + why you'd care. Priority-ordered (most impactful first).

**"Things that got better"** — Improvements to existing functionality. Same format: what changed + why it matters. Priority-ordered.

### Principles

- **Feature, benefit.** Not "add fan-out parallel handler to engine" but "Run pipeline stages in parallel — fan-out nodes execute concurrently and fan-in collects results."
- **User verbs, not code verbs.** Not "refactor edge selection to use cond evaluator" but "Conditional routing is more reliable — edge conditions are evaluated consistently."
- **Skip internal-only changes.** Test refactors, doc updates, reverts-then-re-lands — users don't care.
- **Priority order within each section.** The things that are most exciting, the things that change daily usage most, then the rest.
- **Deep-dive the changelog in preparation.** Read every commit since the last tag. Skim the files affected so you can tell if there may be changes beyond the commit note, and if so, read the diffs and source code. Group by user-visible impact. Many commits collapse into one release note line. Some commits (chore, test, docs) produce no release note at all.
- **Read the code, not just the commit messages.** Commit messages are often terse or misleading. When a commit touches engine behavior, CLI flags, provider support, or graph semantics, read the actual diff to understand what changed from the user's perspective.

### Deriving Release Notes

```bash
# 1. Get the full commit list
git log v<PREV>..HEAD --oneline --no-merges

# 2. Get the diffstat for scope
git diff v<PREV>..HEAD --stat | tail -5

# 3. For commits that touch user-facing code, read the diffs
git show <hash> --stat   # what files changed?
git show <hash>          # read the diff if unclear from commit message

# 4. Walk through commits and ask: "What can the user now DO differently?"
```

For the **first release** (no previous tag), derive notes from the full project history or a user-specified starting commit.

### Example

```markdown
## What's New

### New things you can do

- **Run pipeline stages in parallel** — Fan-out nodes execute concurrently with
  deterministic fan-in collection. Halves wall-clock time for independent stages.
- **Resume from CXDB** — Interrupted runs can resume from the execution database,
  not just local logs. Works across machines.

### Things that got better

- **Conditional routing is more reliable** — Edge conditions evaluate consistently
  even when upstream stages produce unexpected output shapes.
- **Stale binary detection** — Kilroy warns before running if the binary doesn't
  match HEAD, preventing silent behavior mismatches.
```

## Version Number

Kilroy uses semver. The version is injected at build time by goreleaser from the git tag. The file `internal/version/version.go` has `var Version = "dev"` as the default for local builds — do NOT manually edit this for releases.

Decide the bump with the user, but offer a recommendation:

- **Patch** (0.x.Y): Bug fixes, minor improvements
- **Minor** (0.X.0): New capabilities, new providers, meaningful new features
- **Major** (X.0.0): Breaking changes to CLI flags, config format, or graph semantics

## Update the README

**This is the last step before mechanical release, and requires user approval.**

Review `README.md` against the current state of the product. Read the relevant source code to verify claims — don't trust the README or your memory alone.

For each section:

- Is it still accurate? Read the implementing code if unsure. Update or remove if something changed or was removed.
- Is there a new capability from this release that belongs in the README? Propose adding it.
- Are any sections stale enough to warrant cleanup? Recommend changes to keep it tight.

Present the proposed README changes to the user for approval before proceeding to release steps.

## Release Steps

All work happens on a release branch in a worktree — main is untouched until the final atomic fast-forward.

### 1. Create the release branch

```bash
# From the main repo
git worktree add .worktrees/release-vX.Y.Z -b release/vX.Y.Z main
cd .worktrees/release-vX.Y.Z
go mod download
```

### 2. Run the full test suite

```bash
# In the worktree
go test ./...
```

All tests must pass. If any fail, fix them on the release branch before proceeding.

### 3. Run graph validation

```bash
# Validate all demo pipelines still parse and pass validation
for f in demo/**/*.dot; do
  ./kilroy attractor validate --graph "$f" || exit 1
done
```

### 4. Run E2E and ergonomics checks

```bash
scripts/e2e.sh
scripts/check-ergonomics-docs.sh
scripts/check-using-kilroy-skill.sh
```

### 5. Prepare the release (on the release branch)

All of these are committed to the release branch:

1. **Write release notes** to `RELEASE_NOTES.md` in the repo root (following the guidelines above). The GitHub Actions workflow passes `--release-notes=RELEASE_NOTES.md` to goreleaser, which publishes it as the GitHub release body. This file is committed (not gitignored) so it is present at the tagged commit.
2. **Update README** with any approved changes (version is injected by goreleaser from the tag — no file to bump)
3. **Commit** with message like `release: vX.Y.Z`

### 6. Fast-forward main

```bash
# Back in the main repo working directory
git merge --ff-only release/vX.Y.Z
```

If `--ff-only` fails, go back to the worktree and rebase onto main until it can fast-forward.

### 7. Tag and publish

```bash
git push origin main
git tag -a vX.Y.Z -m "vX.Y.Z"
git push origin vX.Y.Z
# GoReleaser takes over from here via GitHub Actions:
#   - Runs go test ./...
#   - Builds cross-platform binaries (linux/darwin/windows x amd64/arm64)
#   - Creates GitHub release with archives and checksums
#   - Updates Homebrew formula in Formula/kilroy.rb
```

### 8. Verify the release

1. Watch GitHub Actions: https://github.com/danshapiro/kilroy/actions
2. Confirm the GitHub release has 6 platform archives + checksums.txt
3. Confirm the release notes appear on the GitHub release page
4. Test Homebrew install:
   ```bash
   brew tap danshapiro/kilroy
   brew install kilroy
   kilroy --version  # should print the new version
   ```

### 9. Clean up

```bash
git worktree remove .worktrees/release-vX.Y.Z
git branch -d release/vX.Y.Z
```

## Safety

- All release prep happens on a branch in a worktree, so main is never modified until the atomic fast-forward
- Commit RELEASE_NOTES.md and any README changes before tagging so the tag points to the right commit
- Build the binary from the release branch and verify `go test ./...` passes before merging
- If any step fails, stop and assess, then make recommendations to the user, rather than pushing forward
