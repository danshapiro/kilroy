# Proposal: Post-Node Input Snapshot Update

## Problem

Agent-written `.ai/` files do not survive across node boundaries in certain
execution topologies, causing downstream nodes to receive empty or stale data.

### Observed Failure

During a Substack pipeline run (`01KJPDK649C65Y07TBX1041C73`), the following
sequence occurred:

1. `expand_spec` wrote `.ai/spec.md` (8142 chars) and `.ai/definition_of_done.md`
   (24239 chars) to the run worktree using the Write tool. Both writes succeeded.
2. The engine's checkpoint commit ran `git add -A && git commit`. Because the
   repo's `.gitignore` contains `.ai/`, git refused to track these files. The
   commit was empty.
3. Linear successor nodes (`check_dod`, `dod_fanout`, `consolidate_dod`) could
   still read the files from the shared worktree filesystem — no immediate
   failure.
4. When the pipeline reached the `work_pool` parallel node, the engine created
   per-branch worktrees from the git HEAD. Since `.ai/spec.md` and
   `.ai/definition_of_done.md` were never committed, they did not exist in the
   branch worktrees.
5. Input materialization hydrated branch worktrees from the input snapshot, but
   the snapshot was captured at run start — before `expand_spec` ran — so it
   contained a stale `.ai/spec.md` from a prior solitaire run (3698 bytes) and
   no `definition_of_done.md` at all.
6. Workers operated without the correct spec or DoD.

Additionally, `plan_work` explicitly deleted `.ai/plan_final.md` (1387 lines)
during its checkpoint commit by using `git add -f` for `work_queue.json`, which
triggered a `git add -A` that staged the deletion of the now-absent-from-index
file.

### Root Cause

The `.ai/` directory is gitignored at the repo root for good reason — it
accumulates per-run scratch files that should not pollute the main branch. But
the engine's checkpoint mechanism uses `git add -A && git commit`, which
respects `.gitignore`. This creates a gap: agents write files to `.ai/`, the
checkpoint doesn't capture them, and any execution path that creates a new
worktree from git (parallel branches, resume) loses the files.

### Why This Is Not a Git Problem

The obvious fix — force-including `.ai/` in run worktree git commits — treats
git as the inter-node file persistence layer. But the attractor spec
(attractor-spec.md) does not assign this role to git. The spec's checkpoint
(Section 5.3) is explicitly metadata: `current_node`, `completed_nodes`,
`node_retries`, and `context` (small key-value pairs). Git commits are an
implementation detail for tracking code changes in the worktree, not a
spec-level contract for persisting inter-node scratch data.

Patching git behavior (negating `.gitignore` in run worktrees, adding
`force_include` config, using `git add -f`) would work mechanically but would
conflate two concerns: code versioning (what git is for) and inter-node data
flow (what the spec's materialization and artifact systems are for).

## Spec Foundations

The attractor spec provides three inter-node data mechanisms:

### 1. Context (Section 5.1)

Small key-value store for routing decisions and checkpoint serialization.
Inappropriate for file-sized data (specs, plans, queues).

### 2. Artifact Store (Section 5.5)

Named, typed storage for large stage outputs. File-backed above 100KB threshold.
Stored in `{logs_root}/artifacts/`. Accessed programmatically via
`store()`/`retrieve()` API.

The Artifact Store is the spec's designated mechanism for large inter-node data.
Agents (Claude Code, Gemini CLI, Codex) could access it through engine-exposed
MCP tools, shell commands, or by writing temporary code that calls an API.
Access is not a fundamental barrier. See Alternative C for why the Artifact Store
is nonetheless not the right solution for this specific problem.

### 3. Input Materialization (Appendix C.1)

A first-class runtime reliability contract. When `inputs.materialize.enabled=true`:

- The engine hydrates required input files into the active run worktree before
  stage execution.
- `default_include` patterns (e.g. `".ai/*.md"`) specify which files to hydrate
  (best-effort: unmatched patterns do not fail the run).
- Run startup persists a canonical input snapshot under
  `logs_root/input_snapshot/`.
- Branch worktrees must be hydrated before branch stage execution.
- Resume must provide hydration parity with fresh runs, restoring inputs from
  persisted snapshot/manifest state.

Input materialization is the spec's mechanism for getting files to agents. The
input snapshot is its persistence layer. The materializer already knows how to
hydrate from the snapshot into worktrees — including parallel branch worktrees.

## Proposed Solution

### Core Change

After each node completes and before the checkpoint commit, the engine scans the
worktree for files matching the materialization include patterns
(`inputs.materialize.include` and `inputs.materialize.default_include`) and
copies any new or modified files into the input snapshot at
`logs_root/input_snapshot/files/`.

On the next node's startup, the existing materializer hydrates from the
(now-updated) snapshot into the worktree, exactly as it does today.

### Algorithm

```
FUNCTION update_input_snapshot(worktree_dir, snapshot_dir, include_patterns):
    FOR EACH pattern IN include_patterns:
        matched_files = glob(worktree_dir, pattern)
        FOR EACH file IN matched_files:
            IF file is non-empty:
                snapshot_path = snapshot_dir / relative_path(worktree_dir, file)
                IF NOT exists(snapshot_path) OR content_differs(file, snapshot_path):
                    copy(file, snapshot_path)
                    update inputs_manifest with new source mapping
```

### Call Site

In `engine.go`, between handler completion and the checkpoint commit
(approximately line 641):

```
// After handler returns outcome, before checkpoint:
update_input_snapshot(e.WorktreeDir, snapshot_dir, materializer_patterns)
sha, err := e.checkpoint(node.ID, out, completed, nodeRetries)
```

This ensures:
- The snapshot reflects the latest agent-written files before the checkpoint
  records the completed node.
- The next node's materialization (which runs before handler execution) finds
  current data.
- Parallel branch worktree hydration (which reads from the snapshot) gets
  current data.
- Resume (which recreates the worktree and re-materializes) gets current data.

### What Changes

| Component | Before | After |
|-----------|--------|-------|
| Input snapshot | Captured once at run start | Updated after each node |
| Materializer | Hydrates from stale snapshot | Hydrates from current snapshot |
| `.ai/` git tracking | Files lost on checkpoint | Irrelevant — snapshot is the persistence layer |
| Parallel branch hydration | Missing agent-written files | Gets current files from snapshot |
| Resume hydration | Missing agent-written files | Gets current files from snapshot |

### What Does Not Change

- **Agent behavior**: Agents keep writing files to the worktree filesystem using
  standard Read/Write tools. No prompt changes needed.
- **Git checkpoint commits**: Still run `git add -A && git commit` for code
  changes. `.gitignore` continues to exclude `.ai/` from git. This is correct —
  git tracks the project code, not the pipeline scratch space.
- **Materialization contract**: `default_include` remains best-effort.
  `include` remains fail-on-unmatched. No new config fields.
- **Snapshot format**: Same directory structure under
  `logs_root/input_snapshot/files/`. Same manifest format.
- **Existing runs**: Runs that don't use `.ai/` files (or where `.ai/` isn't
  gitignored) are unaffected. The snapshot update is additive.

## Why Not Alternatives

### Alternative A: Force-include `.ai/` in git commits

Write `!.ai/` to `.git/info/exclude` in run worktrees, or add a
`git.force_include` config field, or use `git add -f .ai/` in
`AddAllWithExcludes`.

**Problems:**
- Conflates code versioning with inter-node data flow.
- Every checkpoint commit would include `.ai/` diffs, inflating git history in
  the run branch with scratch file churn.
- Requires choosing where to intervene (`.git/info/exclude`? `AddAllWithExcludes`?
  run config?) — all are equally unprincipled because the spec doesn't assign
  this role to git.
- Fragile interaction with `.gitignore` changes: if the repo restructures its
  ignore rules, the force-include might break or over-include.

### Alternative B: Instruct agents to `git add -f`

Have DOT prompts tell agents to force-add `.ai/` files after writing.

**Problems:**
- Depends on agent compliance — LLM agents are not reliable executors of
  git-internal commands.
- `plan_work` demonstrated the hazard: it used `git add -f` for one file, which
  triggered a broader staging that deleted another file.
- Moves engine-level persistence responsibility into prompt text, violating
  separation of concerns.

### Alternative C: Expose the Artifact Store to agents

Give agents access to the Artifact Store via MCP tools, shell commands, or a
filesystem bridge. Producing nodes explicitly store artifacts; consuming nodes
explicitly retrieve them.

This is feasible — agents are coding agents that can call APIs, write temp code,
or use engine-provided tools. The Artifact Store is the spec's designated
mechanism for large inter-node data, so this aligns with the spec's intent.

**Why it's not the right solution for this problem:**

- **Redundant declaration.** The operator already declares which files matter
  for inter-node flow via materialization patterns (`default_include:
  [".ai/*.md"]`). Requiring agents to separately declare the same intent by
  calling `store_artifact` creates two sources of truth for the same contract.
  When they disagree — agent writes a file but forgets to store it, or stores
  an artifact that doesn't match the materialization pattern — the system fails
  silently.
- **Agent compliance is unreliable.** LLM agents forget steps, especially
  ancillary ones that aren't core to their task. An agent whose job is "write
  the spec" will reliably write `.ai/spec.md`. It will less reliably also call
  `store_artifact(".ai/spec.md", "spec")` afterward. The observed `plan_work`
  failure — where `git add -f` for one file triggered unintended side effects —
  demonstrates that agents managing persistence mechanics produces fragile
  outcomes.
- **Persistence policy is an operator concern, not an agent concern.** The
  operator configures materialization patterns to define what persists. The
  engine should enforce that contract. Pushing persistence responsibility into
  agent prompts moves an engine-level concern into prompt text, where it
  competes for attention with the agent's actual task.
- **The Artifact Store solves a different problem.** The Artifact Store is
  keyed by ID, typed, and programmatic — designed for structured data exchange
  between handlers. Inter-node scratch files (`.ai/spec.md`,
  `.ai/plan_final.md`) are filesystem paths that agents reference by name in
  prompts. The materialization system is already designed to bridge between
  filesystem paths and persistent storage. Routing these files through the
  Artifact Store instead adds indirection without adding capability.

The Artifact Store is the right mechanism for use cases the materialization
system doesn't cover: structured data, binary artifacts, cross-run sharing, or
explicit agent-to-agent message passing. Those are separate design surfaces
worth pursuing independently.

### Alternative D: Do nothing; agents share one worktree anyway

For linear execution, all nodes share the same worktree directory, so
filesystem writes persist naturally.

**Problems:**
- Breaks on parallel branches (the observed failure).
- Breaks on resume (worktree is recreated from git).
- Creates a silent correctness gap: linear pipelines work, parallel pipelines
  don't, and the failure mode is stale/missing data rather than a clear error.

## Status: Root Cause Confirmed

The primary root cause for run `01KJPDK649C65Y07TBX1041C73` is **stale binary
execution**, not snapshot update timing.

### Confirmed Causal Chain

1. The run was launched on `2026-03-02` from repo base SHA
   `45b3956c83dcd19340b50e971e1e03818848eb9b` (`manifest.json`).
2. At run startup, input materialization used `source_roots=["/home/user/code/kilroy"]`
   and `default_include=[".ai/*.md"]`, so it copied the repository-local
   `/home/user/code/kilroy/.ai/spec.md` into both:
   - `logs_root/input_snapshot/files/.ai/spec.md`
   - `logs_root/worktree/.ai/spec.md`
3. That repository-local `.ai/spec.md` is an old **Solitaire** spec (3698 bytes,
   SHA256 `1f94094f...`) left in the developer checkout and ignored by git
   (`.gitignore` includes `.ai/`), so it is not visible in commit history but is
   still visible to filesystem-based materialization.
4. The snapshot copy is byte-identical to the repo-local file (same size and hash),
   confirming direct provenance.
5. The `./kilroy` binary used on that machine was built from
   `cee6fe8e2b1771aa3304ec1cf8b3798003549835` (`go version -m ./kilroy`), which
   predates the self-copy truncation fix.
6. In that older binary, `copyInputFile` opened the target with
   `os.O_TRUNC` unconditionally and had no same-file guard.
7. Stage materialization copies from the worktree into the worktree target
   (`source == target` for matched `.ai/*.md` files). In the stale binary, this
   truncates the source file to zero before copy.
8. Result: `.ai/spec.md`, `.ai/definition_of_done.md`, and later
   `.ai/plan_final.md` repeatedly become 0-byte files at stage startup.

This exactly matches observed behavior:

- `expand_spec` writes non-empty files successfully.
- `check_dod` (next linear node) already reads 0-byte
  `.ai/definition_of_done.md`.
- Parallel workers inherit/process 0-byte `.ai` files.

### Why the Earlier Diagnosis Was Off

- The failure begins in linear execution before parallel fan-out.
- Workers seeing 0-byte files is a downstream consequence of repeated
  truncation, not the initial failure event.
- Snapshot staleness exists, but it is secondary in this incident.

### Secondary Design Issue (Still Real)

Even with the self-copy fix present, current materialization behavior can still
prefer stale snapshot content over newer worktree content for duplicate target
paths, because source roots are normalized/sorted and duplicate target
resolution is by source-path iteration order. That is a separate correctness
issue from the zero-byte truncation root cause above.

### Next Steps

1. Treat this incident as a stale-build execution failure first; rerun with a
   freshly built binary from current `HEAD`.
2. Record binary build metadata (`vcs.revision`) and whether
   `--confirm-stale-build` was used in run artifacts for future forensics.
3. Evaluate a separate fix for duplicate-target precedence in input
   materialization (worktree should be authoritative over snapshot for stage
   startup hydration).

---

## Scope and Risk

### Scope

- One new function (~30 lines) in the engine: scan worktree, copy matching files
  to snapshot.
- One call site: between handler completion and checkpoint commit.
- Update to `inputs_manifest.json` to reflect new snapshot entries.
- Tests: verify snapshot update after node completion, verify parallel branch
  hydration sees updated files, verify resume hydration sees updated files.

### Risk

- **Performance**: Glob scan + file copy after each node. For typical `.ai/`
  directories (< 20 files, < 1MB total), this is negligible compared to LLM
  execution time.
- **Snapshot growth**: The snapshot accumulates files across the run. For
  long-running pipelines with many nodes, the snapshot could grow. Mitigation:
  snapshot is in `logs_root/`, which is already expected to grow with run
  artifacts. No new cleanup burden.
- **Conflict resolution**: If two parallel branches both write to the same
  `.ai/` path, the snapshot update after fan-in merge would need to decide which
  version to keep. This is the same problem that git merge faces and should
  follow the same resolution: the fan-in merge node's worktree state is
  authoritative.
