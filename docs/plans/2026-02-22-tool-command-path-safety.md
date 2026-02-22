# Tool Command Hardcoded Path Detection

**Date:** 2026-02-22
**Status:** Proposed
**Problem:** A `tool_command` node with a hardcoded `cd /absolute/path &&` prefix silently
overrides the engine's worktree CWD, causing the command to run against the wrong checkout.
This caused `verify_clippy` to fail on a lab-bench run: the engine correctly set
`cmd.Dir = execCtx.WorktreeDir`, but the shell command itself started with
`cd /Users/.../lab-bench-poc &&`, which jumped to the main branch checkout where the new
crate didn't exist. Meanwhile `verify_build` and `verify_tests` (without `cd` prefixes)
ran correctly in the worktree.

The root cause was a DOT file generation bug (fixed in orchestration commit `6633e67`),
but the engine has no guard against this class of error.

## Goal

Detect `tool_command` strings that contain absolute paths conflicting with the worktree
directory and either warn or fail at preflight/validation time, before the run starts.

## Design

### Level of effort: Small

Two complementary checks:

1. **Preflight warning** — during `kilroy attractor run` preflight, scan all
   `tool_command` attributes for `cd /` patterns. If found, warn the operator.
2. **Validate subcommand** — `kilroy attractor validate --graph <file.dot>` should
   flag `tool_command` nodes containing hardcoded absolute `cd` paths as a lint warning.

Neither check should be a hard error — there may be legitimate uses of `cd` in tool
commands — but it should surface a visible warning.

## Data

The engine already parses tool_command in `ToolHandler.Execute()`:

```
File: internal/attractor/engine/handlers.go line 534
cmdStr := node.Attr("tool_command", "")
```

And sets the correct working directory at line 561:

```go
cmd.Dir = execCtx.WorktreeDir
```

The problem is that `bash -c "cd /other/path && cargo clippy"` ignores `cmd.Dir`
because the shell's `cd` takes precedence.

## Changes

### 1. Add path lint to graph validation

**File:** `internal/attractor/engine/validate.go` (or wherever `validate --graph` runs)

For each node with `shape=parallelogram` (tool handler), check if `tool_command`
matches the pattern `cd\s+/` (absolute cd). If so, emit a warning:

```
WARN: node "verify_clippy" tool_command contains "cd /..." which overrides
      the worktree working directory. Remove the cd prefix — the engine
      sets CWD to the worktree automatically.
```

### 2. Add runtime warning in ToolHandler.Execute

**File:** `internal/attractor/engine/handlers.go` ~line 534

After reading `cmdStr`, check for the pattern and log a warning before execution:

```go
cmdStr := node.Attr("tool_command", "")
if hasCDAbsPath(cmdStr) {
    log.Warnf("node %q tool_command contains 'cd /<path>' which overrides "+
        "worktree CWD (%s) — this is usually a DOT file bug", node.ID, execCtx.WorktreeDir)
}
```

The helper `hasCDAbsPath` can use a simple regex: `cd\s+/[^\s]`.

### 3. Add the warning to preflight report

**File:** wherever preflight validation runs (likely `internal/attractor/engine/preflight.go`
or similar)

During the preflight check that already validates the graph, add the same lint. Include
it in `preflight_report.json` as a warning (not a blocker).

## Files to modify

| File | Change |
|------|--------|
| `internal/attractor/engine/handlers.go` | Runtime warning when tool_command contains `cd /` |
| `internal/attractor/engine/validate.go` | Lint warning in `validate --graph` |
| Preflight validation file | Warning in preflight report |

## Testing

1. Create a test DOT file with `tool_command="cd /tmp && echo hello"` — verify warning
2. Create a test DOT file with `tool_command="cargo test"` — verify no warning
3. Verify `kilroy attractor validate --graph` surfaces the warning
4. `go test ./internal/attractor/engine/...`
