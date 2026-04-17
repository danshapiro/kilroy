# File Conventions, Git Diff Tracking, and Run Observability

Date: 2026-04-07

## Goal

Establish standard file conventions for inter-node data exchange, add git diff
tracking per node execution, and expose run artifacts and diffs via the REST API
for UI consumption and agent discoverability.

## 1. File Convention Setup

**What:** At run start, the engine creates a `.kilroy/` directory in the workspace
and writes standard files that nodes can discover and use. This is convention-managed —
the engine writes a few files, nodes follow the convention voluntarily.

**Standard files:**

| File | Written by | Purpose |
|------|-----------|---------|
| `.kilroy/INPUT.md` | Engine, at run start | Structured input data formatted as readable markdown |
| `.kilroy/CONTEXT.md` | Engine, updated per-node | Accumulated context: prior nodes, outcomes, key outputs |
| `.kilroy/TASK.md` | Engine, per-node | Current node's task (from prompt + context) |
| `.kilroy/OUTPUT.md` | Node (convention) | What this node produced — single primary output |
| `.kilroy/FEEDBACK.md` | Engine, on retry | Why the previous attempt failed, contract violations |
| `.kilroy/data/` | Nodes (convention) | Structured data files for inter-node exchange |

**Gitignore:** The engine (or git hook) auto-adds `.kilroy/` to the workspace's
`.gitignore` if not already present. These are runtime scratch files, not source.

**Environment:** `$KILROY_DATA_DIR` points to the `.kilroy/` directory so scripts
and agents know where to look without hardcoding paths.

**Done when:**
- Engine creates `.kilroy/` in workspace at run start
- `INPUT.md` written from `--input` data (key-value pairs as markdown)
- `CONTEXT.md` written/updated before each node with accumulated run context
- `TASK.md` written before each node from the node's prompt
- `FEEDBACK.md` written on retry with failure reason and contract violation details
- `$KILROY_DATA_DIR` environment variable set for all nodes
- `.kilroy/` auto-added to `.gitignore` in git-managed workspaces
- Build, test, run a real graph, verify files are created and readable

## 2. Per-Node Output Contract Enforcement

**What:** Nodes can optionally declare output files. After execution, the engine
checks if declared outputs exist. If missing, the node's outcome is downgraded
and retry kicks in with feedback.

**Node attribute:** `outputs="analysis.json,summary.md"` (comma-separated, optional).

**Behavior:**
- If `outputs` attribute is not present: no checking (backward compatible)
- If present: after node execution, check each file exists relative to workspace
- If any missing: write details to `.kilroy/FEEDBACK.md`, set outcome to fail with
  `failure_reason: "output contract: missing analysis.json"`, retry if retries remain
- If all present: record in node execution metadata for downstream visibility

**Done when:**
- Node attribute `outputs` is parsed (comma-separated file list)
- Engine checks output files after node handler returns success
- Missing outputs downgrade the outcome and trigger retry with FEEDBACK.md
- Existing tests pass, new test graph exercises the contract

## 3. Git Diff Tracking Per Node

**What:** Record before/after git SHAs for each node execution. The git hook
(L2) writes to a new `node_diffs` table. This enables "what changed during
this node" queries.

**New migration (`003_node_diffs.sql`):**
```sql
CREATE TABLE IF NOT EXISTS node_diffs (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id        TEXT NOT NULL REFERENCES runs(run_id) ON DELETE CASCADE,
    node_id       TEXT NOT NULL,
    attempt       INTEGER NOT NULL DEFAULT 1,
    before_sha    TEXT NOT NULL,
    after_sha     TEXT NOT NULL,
    files_changed INTEGER,
    insertions    INTEGER,
    deletions     INTEGER,
    recorded_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX IF NOT EXISTS idx_node_diffs_run ON node_diffs(run_id);
CREATE INDEX IF NOT EXISTS idx_node_diffs_node ON node_diffs(run_id, node_id);
```

**Layer separation:** The L0 engine never touches this table. The L2 git hook
records SHAs after each per-node commit. The server reads from it to serve diffs.

**Done when:**
- Migration file created and auto-applied
- Git hook records before_sha (pre-execution HEAD) and after_sha (post-commit HEAD)
  for each node that produces changes
- Diffstat (files_changed, insertions, deletions) computed and stored
- RunDB has read methods for querying node diffs
- Build, test, run a graph in a git repo, verify diffs recorded

## 4. Diff API Endpoint

**What:** `GET /runs/{id}/nodes/{nodeId}/diff` returns the diff for a node execution,
including file list and full unified diff content.

**Query params:** `?attempt=N` (default: latest attempt)

**Response:**
```json
{
  "node_id": "implement",
  "attempt": 1,
  "before_sha": "abc123",
  "after_sha": "def456",
  "summary": {
    "files_changed": 3,
    "insertions": 47,
    "deletions": 12
  },
  "files": [
    {
      "path": "internal/server/handler.go",
      "status": "modified",
      "insertions": 30,
      "deletions": 8
    }
  ],
  "diff": "diff --git a/internal/server/handler.go ..."
}
```

**Implementation:** Query `node_diffs` table for SHAs, then run
`git diff before_sha..after_sha` against the repo path from the run record.
If the repo is no longer accessible, return the summary from the DB without
the full diff. If no diff data exists (no git hook), return 404 with a clear message.

**Done when:**
- Endpoint registered and implemented
- Returns summary from DB + full diff from git
- Handles missing repo gracefully (summary only)
- Handles no-git runs (404 with message)
- Test with a real graph run in a git repo

## 5. File Browser API Endpoints

**What:** Browse and download files from a run's log directory and workspace.

**Endpoints:**
- `GET /runs/{id}/files/` — list files in logs_root
- `GET /runs/{id}/files/{path}` — download file from logs_root
- `GET /runs/{id}/workspace/` — list files in worktree_dir
- `GET /runs/{id}/workspace/{path}` — download file from worktree_dir

**Constraints:**
- Path traversal blocked (no `..`)
- Scoped to the run's directories only
- Directory listings return JSON with name, size, is_dir, modified_at
- File downloads return content with appropriate content-type
- Workspace endpoint returns 404 if worktree has been cleaned up

**Done when:**
- Both endpoint pairs implemented
- Path traversal protection tested
- Directory listing and file download work
- Returns appropriate errors for cleaned-up worktrees
- Test by browsing a real run's files via curl

## Implementation Order

1 → 2 → 3 → 4 → 5 (sequential, each builds on the previous)

## Test Strategy

- Build after every change: `go build ./cmd/kilroy/`
- Run `go test ./...` after each task
- Run real graphs against the actual `./kilroy` binary:
  - Tool-only graph in a git repo (verify .kilroy/ files, git diffs)
  - Tool-only graph without git (verify .kilroy/ files, no diffs)
  - Graph with output contract (verify enforcement and FEEDBACK.md)
  - Browse files and diffs via curl against the running server
