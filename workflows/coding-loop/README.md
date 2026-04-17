# coding-loop

An iterative coding workflow that repeatedly chooses sub-tasks, implements them, reviews the results, and decides when the spec is complete.

## What it does

Runs up to 8 loops of:

1. **Task Chooser** — reads the spec + latest review, picks the highest-priority unimplemented sub-task, writes `.kilroy/task.md`.
2. **Implementer** — reads the task, writes code, commits.
3. **Reviewer** — diffs HEAD~1..HEAD against the spec, writes `.reviews/iter-NNN.md` and `.reviews/latest.md`, commits.
4. **Done Gate** — reads spec + latest review, writes `COMPLETE` or `CONTINUE` to `.kilroy/decision.md`.

When Done Gate writes `COMPLETE` (or `loop_max=8` is reached), the loop exits and a **Report** node writes `result.md`.

## How to launch

```bash
kilroy attractor run \
  --package workflows/coding-loop/ \
  --workspace /abs/path/to/target-repo \
  --input '{"spec":"/abs/path/to/spec.md"}'
```

- `--workspace` — the repo being coded against (must already exist; the caller handles `git init` / `go mod init` etc.)
- `--input spec` — absolute path to the spec/requirements file (read in-place; not copied into the repo)

## Input contract

| Key    | Required | Description |
|--------|----------|-------------|
| `spec` | yes      | Absolute path to the spec/requirements markdown file |

## Output contract

| File             | Description |
|------------------|-------------|
| `result.md`      | Summary: what was implemented, iterations run, final status |
| `.reviews/iter-NNN.md` | Per-iteration reviewer feedback (committed to repo) |
| `.reviews/latest.md`   | Rolling copy of the most recent review |

## Known limits

- `loop_max=8` — hard ceiling; if the done-gate never writes `COMPLETE` after 8 iterations, the run fails.
- Chooser and done-gate use `claude-haiku-4.5` (cheap, API-based). Implementer and reviewer use `claude-sonnet-4.6` (Claude Code CLI via tmux, or API).
- Spec is NOT committed to the target repo — it is read in-place via the `spec` input path.
- No pre-flight scaffolding — the caller must initialize the repo before launching.
