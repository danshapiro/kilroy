---
date: 2026-04-24
status: open
audience: kilroy engine + workflow contributors
---

# Kilroy fixes — from `quick-launch` and `pr-review` agent feedback

Two agent sessions hit real bugs running `quick-launch` and `pr-review` against live work
(the pr-review session reviewed PRs across `danshapiro/freshell` and other repos; the
quick-launch session ran 10+ parallel research agents against `bitbucket/svg-compose`).
This doc captures every actionable fix from both, ranked by impact, with the workflow-level
mitigations that have already shipped called out so the engine work can be scoped cleanly.

The `pr-review` and `quick-launch` skills + workflow packages now live in
`gf-software-factory/{skills,workflows}` (the kilroy-side copies were removed in the same
pass that produced this doc). Engine-level fixes still belong in this repo.

---

## Blockers — must fix before broader rollout

These caused user-visible damage or silent success-on-failure in real runs.

### 1. Agents leak out of the worktree when prompts contain absolute paths

**What happened.** A user's prompt opened with "You are working on `/Users/matt/bitbucket/svg-compose`."
Ten parallel agents followed the literal path: they `cd`'d to the user's main source tree
and ran `git -C /Users/matt/bitbucket/svg-compose ...` and `Edit` against absolute paths,
clobbering the shared tree while their isolated worktrees sat untouched. Their final commits
on `attractor/run/...` branches contained only `.gitignore` updates. End result: 10 agents
racing on the user's actual repo.

**Why the docs didn't catch it.** The skill says "the engine auto-detects and creates an
isolated run branch + worktree, so the user's source tree is never touched." That's true
of `cwd`, not of the paths the LLM writes in `Bash`/`Edit` calls.

**Fix path, in order of impact.**

1. **Agent system-prompt prepend** (highest leverage, low effort): "You are running inside
   an isolated Kilroy worktree at `{path}`. All work must happen relative to `cwd`; do not
   `cd` elsewhere or pass `-C` to git with paths outside `cwd`." Owners: tmux agent
   harness in `internal/agents/...` (or wherever the system-prompt assembly lives).
2. **Bash-tool path guard** (heaviest, optional): wrap `Bash` so writes outside the worktree
   require `--confirm-out-of-tree-writes` or fail loudly. Heavy-handed but appropriate for
   fire-and-forget agents.
3. **SKILL.md sharp-edge note** (already shipped in `gf-software-factory/skills/quick-launch/SKILL.md`
   — see Step 0 preflight; consider also adding to `pr-review`).

### 2. Detached runs don't register in the run DB until terminal

**What happened.** `runs wait --latest --label`, `runs wait <id>`, `status --latest`, and
`runs list` all fail to find an active detached run — they only see completed ones. The
first version of the `pr-review` skill modeled `runs wait --label` on `quick-launch`'s docs;
it broke on the first live run.

**Workaround in shipped skills.** Both `pr-review` and (partially) `quick-launch` now
capture `logs_root=...` from the launch stdout and poll `final.json`. That's mechanical
and works, but it's a regression from the documented `runs wait` UX.

**Fix.** Write the run row to the DB at launch with `status=running` so `wait`/`show`/`list`
operate on active runs, not just terminated ones. Owners: `cmd/kilroy/attractor_run.go`
and the run-DB writer in `internal/runs/...`.

### 3. Reaching any terminal node = `status=success`

**What happened.** PR #303 v1 hit 4× transient `git fetch` failures in setup. The original
`pr-review` graph followed the unconditional `setup -> done` fallback edge, so the engine
reached the `done` node and reported `status=success` at the run level — with no review
produced. Every graph author hits this trap.

**Workflow-level mitigation (already shipped).** `gf-software-factory/workflows/pr-review/graph.dot`
now routes `setup -> fail_report` and `collect_diff -> fail_report` instead of `-> done`,
matching `build_test -> fail_report`. `write-fail-report.sh` infers the failed phase from
artifact presence and writes a phase-specific `review-report.md`.

**Engine fix (still wanted).** The trap is graph-author-shaped, not workflow-shaped.
Either:

- **(a)** track "any stage failed?" in the engine and surface a distinct terminal state
  (e.g. `status=completed_with_failures`) on `final.json`; or
- **(b)** require an explicit `condition=` on every inbound edge to a terminal node, and
  fail validation otherwise.

(b) catches the bug at validate time, before runs happen. (a) is a runtime safety net.
Both are useful; (b) is cheaper.

### 4. Agents don't inherit MCP servers from the parent Claude Code session

**What happened.** The user told their parent Claude session to verify work in Chrome
via `mcp__claude-in-chrome__*`. When that conversation launched 10 detached `quick-launch`
agents, those tools didn't exist in the children. The agents reached for `osascript`,
`screencapture`, `chrome --headless`, and `curl` via Bash to compensate — macOS-level
automation the user did not consent to. That's the "wild things happening with Chrome"
the user reported.

**Fix path.**

1. **Document prominently** (cheapest): add a sentence to `quick-launch` SKILL.md and
   `pr-review` SKILL.md: "Kilroy agents do not inherit the parent Claude Code session's
   MCP servers. Only Bash, Edit, Read are available unless you forward MCP via run config."
2. **Optional opt-in forwarding**: a `--forward-mcp <name>` flag (or `mcp_forward:` in
   `run.yaml`) that copies a parent server's config into the child agent. Not every parent
   server makes sense to forward; opt-in is the right default.

### 5. `install-skills.sh` doesn't rebuild the binary

**What happened.** The user re-ran `install-skills.sh` to relink the workflow symlink at
their checkout. The `kilroy` binary on PATH was 17 days stale (pre-`--tmux`). The first
sweep loop produced no launches because a `grep '^logs_root='` matched nothing on error
output that no longer included that line.

**Fix.** Either (a) the install script should run `go build ./cmd/kilroy` before linking,
or (b) it should `stat` the binary against `find . -name '*.go' -newer ...` and bail
loudly when stale. (a) is one extra line and handles every case. The kilroy-side
`scripts/install-skills.sh` does not currently build; the new factory-side
`gf-software-factory/scripts/install-kilroy-host.sh` does build.

---

## Important — fix before more graph authors arrive

### 6. `--help` is missing half the CLI surface

`kilroy attractor run --help` doesn't document `--tmux`, `--prompt-file`, `--label`,
`--workspace`, `--input`, `--package`, `--detach`. `kilroy attractor runs --help` lists
only `list` and `prune` — no `show`, no `wait`. Every flag the shipped skills use is
load-bearing but undiscoverable.

**Fix.** Regenerate help text from the actual flag parser in `cmd/kilroy/main.go`. If the
parser is hand-rolled (likely), align the printed usage with the parsed flags as a unit
test — drift here is the recurring class.

### 7. Progress stream has no terminal event

`progress.ndjson` emits `stage_attempt_*`, `edge_selected`, `input_materialization_*`,
`tmux_session_*`. Nothing for run completion. The canonical terminal signal today is the
appearance of `final.json`. That invariant isn't documented anywhere I could find.

**Fix.** Emit a `run_completed` / `run_failed` event as the final line of `progress.ndjson`,
plus document `final.json` as the public completion contract.

### 8. Worktree cleanup isn't tied to run completion

After a long working session, `git worktree list` showed 40+ orphaned worktrees from
attractor runs going back weeks — all physically gone from disk, stale git admin refs.
The user pruned manually with `git worktree prune`.

**Fix.** `git worktree remove` the run's worktree on successful terminal state. Keep it
on failure for forensics. Wire to whatever runs the existing per-run cleanup (or add it).

### 9. `result.md` contract is brittle under interruption

When the user `stop --force`'d 10 agents mid-flight, only 1 of 10 had written `result.md`.
Several had done substantive committed work but hadn't reached the "write the summary"
step. The `outputs=` contract surfaces this as missing `result.md` — a generic error that
obscures what actually happened.

**Fix options.**

- Auto-flush a partial `result.md` on graceful termination, with a `__stopped_early__: true`
  marker and the last assistant text block.
- Or: make `result.md` optional and let `runs show --print result.md` fall back to a
  generated one-line summary from the transcript.

### 10. `pr-review` workflow has no GitHub-review publish step

The workflow writes `review-report.md` to disk but never posts a GitHub review. Branch
protection requires an approved review, so the workflow's verdict is invisible to the
merge gate. We hit this on the first merge batch — all 5 PRs blocked by `REVIEW_REQUIRED`.

**Fix.** Add a node `post_review` after `holistic_review` that runs
`gh pr review <N> --approve|--request-changes --body "$(cat review-report.md)"`, gated by
the holistic decision token: `MERGE` → approve; `MERGE-FIX`/`FIX-MERGE` → request-changes
with checklist; `REJECT` → request-changes with rejection comment. Lives in the workflow,
not the engine. Owner: `gf-software-factory/workflows/pr-review/`.

### 11. `pr-review` setup uses HTTPS with no auth fallback

PR #303 v1 failed 4× with `remote: Internal Server Error` (HTTP 500) on `git fetch upstream
pull/N/head`. When the workspace is an existing clone, `setup-pr.sh` inherits whatever URL
is on the upstream remote — plain HTTPS, no credential helper. Flaky under GitHub pressure.

**Fix.** Use `gh pr checkout <N> --repo <owner/repo>` (handles auth via `gh`) or add an
explicit retry-with-SSH-URL fallback. Workflow-level fix; lives in
`gf-software-factory/workflows/pr-review/scripts/setup-pr.sh`.

### 12. Cross-repo PRs aren't handled

PR #297 was a fork PR from `Glowforge/freshell`. Nothing in the graph detects "is this
same-repo?" — a follow-up fix agent tried to push and hit HTTP 403.

**Fix.** `setup-pr.sh` should record `headRepositoryOwner` in `pr-meta.json`, and
`fail_report` should surface a distinct "cross-repo, push blocked" case so downstream
agents/humans know not to try. Workflow-level fix.

---

## Quality of life

### 13. `KILROY_PREDECESSOR_NODE` env for failure handlers

Today `write-fail-report.sh` infers the failed phase from filesystem state (`pr-meta.json`
exists? `build-report.json` exists?). That's a workaround for `fail_report` not knowing
its predecessor node. Adding `KILROY_PREDECESSOR_NODE` (and maybe `KILROY_PREDECESSOR_OUTCOME`)
to the env when a node fires off a non-primary edge would let failure handlers be
deterministic instead of probing.

### 14. `runs show --worktree-path` accessor

Today you `runs show <id>` and grep for `worktree_dir`. A scripting-friendly
`runs show --print-worktree-path` would clean up the bash patterns in skills.

### 15. Concurrency cap

10 parallel `attractor run`s worked but generated a lot of simultaneous LLM usage.
A `--max-concurrent N` flag (or a config setting) would help users self-pace without
writing their own throttle.

### 16. Long worktree paths

`/Users/matt/.local/state/kilroy/attractor/runs/01K.../worktree/` is a mouthful.
A `runs cd <id>` helper or a documented shell-function template ("here, source this
into your zshrc") would smooth post-mortem inspection.

### 17. `progress.ndjson` vs `agent_output.jsonl` documentation gap

Progress shows lifecycle events (start, input-materialization, tmux-session-start,
terminal); the actual tool-call log is in `agent/agent_output.jsonl`. Several minutes
were wasted reading the wrong file when debugging. A short paragraph in
`docs/runs-layout.md` (or wherever) would prevent that.

### 18. `Write` tool for agents

Agents have `Edit` + `Bash` heredoc only — no `Write` tool. Functionally fine, but
unconventional vs Claude Code's standard tool set. Worth either (a) adding `Write`,
or (b) noting in skill docs so prompt authors don't expect it.

---

## Things that worked — protect these

The pr-review session reported the following, with no kilroy-side failures across 8
parallel runs and 3 sequential batches:

- **`--prompt-file`** — writing prose to a file beat JSON-escaping multi-paragraph
  prompts. Keep it.
- **Labeling + `--latest --label task=X` lookup** — made multi-run orchestration
  tractable across 10+ concurrent runs.
- **`runs wait --timeout`** behavior — streams status to stderr, sane exit codes.
  (Note: the active-run lookup gap from item #2 is orthogonal.)
- **`stop --force`** across many concurrent runs — reliable, no orphaned tmux sessions.
- **CLI backend via Claude subscription** — no API keys, no rate-limit surprises.
- **`agent_output.jsonl`** — complete transcript saved a post-mortem investigation.
- **Git-worktree-per-run isolation** — the right design when it's actually used (see
  item #1 for when it isn't).
- **Auto-detect provider CLIs + auto-build default config when no `run.yaml` is supplied** —
  removed a lot of setup friction for the skill author.

---

## Suggested rollout order

If a single contributor picks this up:

1. **Item #1** (worktree isolation prompt prepend) and **#5** (install-skills builds binary).
   Both are one-line patches with high leverage. Do them first.
2. **Item #2** (DB-write at launch) and **#6** (help-text alignment). These unlock the
   documented UX.
3. **Item #7** (`run_completed` event) and **#8** (worktree cleanup). Polish that
   compounds across every future skill.
4. **Item #3** (engine-side terminal-state guard). Pick (b) — validator-time. Less
   runtime surface area to verify.
5. **Items #10–#12** (workflow-level pr-review fixes). These live in
   `gf-software-factory/workflows/pr-review/` now.
6. **Item #4** (MCP forwarding). Nice but optional.
7. **Items #13–#18**. Quality of life as time allows.
