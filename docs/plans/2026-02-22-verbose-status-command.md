# Verbose `attractor status` Command

**Date:** 2026-02-22
**Status:** Proposed
**Problem:** `kilroy attractor status` prints a 5-line summary (state, node, event, pid,
last_event_at). When a pipeline loops through postmortem → implement retries, the operator
has to manually find and read `worktree/.ai/postmortem_latest.md` to understand why. The
stage trace (which nodes passed/failed, retry loops, edge conditions) is buried in
`progress.ndjson` and requires manual parsing.

## Goal

Add a `--verbose` / `-v` flag to the one-shot status command that enriches the output with:

1. **Stage trace** — ordered list of stage attempts with pass/fail and failure reasons
2. **Completed nodes and retry counts** — from `checkpoint.json`
3. **Final commit SHA** — from `final.json` (when run completes)
4. **Postmortem text** — from `worktree/.ai/postmortem_latest.md` (when pipeline loops)
5. **Review text** — from `worktree/.ai/review_final.md` (when semantic review completes)

All data sources already exist on disk. No new data collection is needed.

## Design

### Level of effort: Small

The `Snapshot` struct is the core data model for status output. Both `--json` and
key=value formatters already use it. Adding verbose fields to the struct and
populating them conditionally is straightforward.

## Data sources

All files live under `~/.local/state/kilroy/attractor/runs/<run_id>/`:

| File | Data | Used for |
|------|------|----------|
| `checkpoint.json` | `completed_nodes[]`, `node_retries{}` | Node list, retry counts |
| `final.json` | `final_git_commit_sha`, `cxdb_context_id` | Outcome details |
| `progress.ndjson` | `stage_attempt_end` events | Stage trace with pass/fail |
| `worktree/.ai/postmortem_latest.md` | Markdown | Why the pipeline looped |
| `worktree/.ai/review_final.md` | Markdown | Semantic review findings |
| `worktree/.ai/implementation_log.md` | Markdown | What was implemented |

## Changes

### 1. Expand `Snapshot` struct

**File:** `internal/attractor/runstate/types.go`

Add verbose-only fields:

```go
type StageAttempt struct {
    NodeID        string `json:"node_id"`
    Status        string `json:"status"`          // success, fail
    Attempt       int    `json:"attempt"`
    MaxAttempts   int    `json:"max_attempts"`
    FailureReason string `json:"failure_reason,omitempty"`
}

type EdgeTransition struct {
    From      string `json:"from"`
    To        string `json:"to"`
    Condition string `json:"condition,omitempty"`
}

type Snapshot struct {
    // ... existing fields unchanged ...

    // Verbose fields (populated only when requested via ApplyVerbose)
    FinalCommitSHA  string            `json:"final_commit_sha,omitempty"`
    CXDBContextID   string            `json:"cxdb_context_id,omitempty"`
    CompletedNodes  []string          `json:"completed_nodes,omitempty"`
    RetryCounts     map[string]int    `json:"retry_counts,omitempty"`
    StageTrace      []StageAttempt    `json:"stage_trace,omitempty"`
    EdgeTrace       []EdgeTransition  `json:"edge_trace,omitempty"`
    PostmortemText  string            `json:"postmortem_text,omitempty"`
    ReviewText      string            `json:"review_text,omitempty"`
}
```

### 2. Add verbose loaders

**File:** `internal/attractor/runstate/snapshot.go`

New exported function `ApplyVerbose(s *Snapshot) error` that calls:

- **`applyCheckpointVerbose(s)`** — reads `checkpoint.json`:
  ```go
  type checkpointDoc struct {
      CompletedNodes []string       `json:"completed_nodes"`
      NodeRetries    map[string]int `json:"node_retries"`
  }
  ```
  Populates `s.CompletedNodes` and `s.RetryCounts`.

- **`applyFinalVerbose(s)`** — reads `final.json` for additional fields:
  ```go
  type finalVerboseDoc struct {
      FinalCommitSHA string `json:"final_git_commit_sha"`
      CXDBContextID  string `json:"cxdb_context_id"`
  }
  ```
  Populates `s.FinalCommitSHA` and `s.CXDBContextID`.

- **`applyStageTrace(s)`** — scans `progress.ndjson` line by line, collects
  `stage_attempt_end` events into `s.StageTrace` and `edge_selected` events into
  `s.EdgeTrace`. Uses existing `bufio.Scanner` pattern from `readLastProgressEvent`.

- **`applyWorktreeArtifacts(s)`** — reads markdown files if they exist:
  - `worktree/.ai/postmortem_latest.md` → `s.PostmortemText`
  - `worktree/.ai/review_final.md` → `s.ReviewText`

  Missing files are silently skipped (not an error).

### 3. Wire the flag

**File:** `cmd/kilroy/attractor_status.go`

Add flag parsing in `runAttractorStatus()`:

```go
var verbose bool

// In the switch:
case "--verbose", "-v":
    verbose = true
```

Pass `verbose` to `printSnapshot`:

```go
return printSnapshot(logsRoot, stdout, stderr, asJSON, verbose)
```

### 4. Enhance `printSnapshot`

**File:** `cmd/kilroy/attractor_status_follow.go` (function at line 384)

After loading the snapshot, conditionally apply verbose data:

```go
func printSnapshot(logsRoot string, stdout io.Writer, stderr io.Writer, asJSON bool, verbose bool) int {
    snapshot, err := loadSnapshot(logsRoot)
    if err != nil {
        fmt.Fprintln(stderr, err)
        return 1
    }

    if verbose {
        if err := runstate.ApplyVerbose(snapshot); err != nil {
            fmt.Fprintln(stderr, err)
            return 1
        }
    }
    // ... existing output logic ...
```

For key=value output, append after the existing fields:

```
completed_nodes=start,expand_spec,check_expand_spec,implement,...
retry_counts=implement:0,postmortem:0
final_commit_sha=c3bc0fae31e65c8721a84eb88ff50084b948658f
cxdb_context_id=12

--- stage trace ---
  start                success  attempt 1/4
  expand_spec          success  attempt 1/4
  implement            success  attempt 1/4
  fix_fmt              fail     attempt 1/4  exit status 1
  verify_fmt           fail     attempt 1/4  exit status 1
  check_fmt            fail     attempt 1/4  exit status 1
    → postmortem       (outcome=fail && context.failure_class=deterministic)
  postmortem           success  attempt 1/4
    → implement        (retry)
  implement            success  attempt 1/4
  ...
  review_consensus     success  attempt 1/4
    → exit             (outcome=success)

--- postmortem (worktree/.ai/postmortem_latest.md) ---
# Postmortem: check_fmt Failure
...

--- review (worktree/.ai/review_final.md) ---
# Semantic Review
...
```

For `--json` output, the new struct fields serialize automatically — no additional
formatting code needed.

### 5. Update usage string

**File:** `cmd/kilroy/main.go` line 73

```go
// Before
"kilroy attractor status [--logs-root <dir> | --latest] [--json] [--follow|-f] [--cxdb] [--raw] [--watch] [--interval <sec>]"

// After
"kilroy attractor status [--logs-root <dir> | --latest] [--json] [-v|--verbose] [--follow|-f] [--cxdb] [--raw] [--watch] [--interval <sec>]"
```

### 6. Update `printSnapshot` call sites

The `printSnapshot` function is also called from `runWatchStatus` (line 346). Update
that call to pass `verbose` through as well — or pass `false` to keep watch mode
compact by default.

## Files to modify

| File | Change |
|------|--------|
| `internal/attractor/runstate/types.go` | Add `StageAttempt`, `EdgeTransition` types; add verbose fields to `Snapshot` |
| `internal/attractor/runstate/snapshot.go` | Add `ApplyVerbose()` and helper functions |
| `cmd/kilroy/attractor_status.go` | Parse `--verbose`/`-v` flag, pass to `printSnapshot` |
| `cmd/kilroy/attractor_status_follow.go` | Update `printSnapshot` signature, add verbose output formatting |
| `cmd/kilroy/main.go` | Update usage string |

## Testing

1. `go build -o kilroy ./cmd/kilroy/` — verify compilation
2. `./kilroy attractor status --latest --verbose` — verify stage trace and artifacts print
3. `./kilroy attractor status --latest --verbose --json | python3 -m json.tool` — verify JSON includes all verbose fields
4. `./kilroy attractor status --latest` (without `--verbose`) — verify existing output is unchanged
5. `go test ./internal/attractor/runstate/...` — verify any new unit tests pass
6. `go test ./...` — full test suite
