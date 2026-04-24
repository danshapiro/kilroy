# Run Directory Layout

Each Kilroy attractor run produces a self-contained directory under
`$XDG_STATE_HOME/kilroy/attractor/runs/<run_id>/` (defaulting to
`~/.local/state/kilroy/attractor/runs/<run_id>/`).

## Directory Structure

```
<run_id>/
├── manifest.json          # Run identity and configuration snapshot
├── graph.dot              # DOT source used for this run (preserved for replay/resume)
├── run_config.json        # Snapshotted RunConfig (if used)
├── run.pid                # PID of the process currently executing this run
│
├── checkpoint.json        # Last stable execution state (for resume)
│
├── progress.ndjson        # ← Append-only event stream (see §Lifecycle Events)
├── live.json              # Last progress event (overwritten on each event)
│
├── final.json             # ← Completion contract (see §Completion Contract)
├── failure_dossier.json   # Structured failure analysis (on failure)
│
├── run.log                # Human-readable structured log
├── run.tgz                # Archive of the entire run directory
│
├── <node_id>/             # Per-node artifacts
│   ├── status.json        # Stage outcome (status, failure_reason, ...)
│   ├── prompt.md          # Agent prompt (LLM nodes)
│   ├── response.md        # Agent response (LLM nodes)
│   ├── parallel_results.json  # Fan-out results (parallel split nodes)
│   └── ...                # Other stage-specific artifacts
│
├── outputs/               # Declared output artifacts collected after run
├── worktree/              # Git worktree (or plain working directory in no-git mode)
└── modeldb/               # Model catalog snapshot
```

## Completion Contract

### `final.json`

`final.json` is the **canonical, stable completion signal** for a run. It is
written atomically after all stage execution is complete. Callers that need to
wait for a run to finish should poll for the existence of `final.json`.

**Schema:**

```json
{
  "timestamp": "2026-04-24T10:00:00Z",
  "status": "success",
  "run_id": "01ABC123...",
  "final_git_commit_sha": "abc123...",
  "failure_reason": "",
  "cxdb_context_id": "...",
  "cxdb_head_turn_id": "..."
}
```

| Field | Values | Description |
|---|---|---|
| `status` | `success`, `fail`, `canceled` | Terminal run status |
| `run_id` | string | Unique run identifier |
| `failure_reason` | string (empty on success) | Human-readable reason for failure |
| `final_git_commit_sha` | string | Last git commit SHA (empty in no-git mode) |
| `timestamp` | ISO8601 | When the outcome was persisted |

## Lifecycle Events

`progress.ndjson` is an append-only newline-delimited JSON stream. Each line is
a self-contained JSON object. The file is written with O_APPEND so it is safe
to tail while a run is in progress.

### Common Event Fields

Every event in the stream has these envelope fields:

| Field | Description |
|---|---|
| `event` | Event type identifier (string) |
| `id` | Unique event ID (8-char hex) |
| `run_id` | Run identifier |
| `ts` | ISO8601 timestamp (UTC, nanosecond precision) |

### Stage Events

| Event | Description |
|---|---|
| `stage_attempt_start` | A stage (node) attempt is starting |
| `stage_attempt_success` | A stage attempt completed successfully |
| `stage_attempt_fail` | A stage attempt failed |
| `stage_retry_sleep` | Engine is sleeping before a retry |
| `stage_retry_blocked` | Retry was blocked (e.g. deterministic failure) |
| `stage_heartbeat` | Periodic liveness signal from a running stage |

### Routing Events

| Event | Description |
|---|---|
| `edge_selected` | An outgoing edge was chosen for traversal |
| `edge_condition_evaluated` | A conditional edge was evaluated |
| `no_matching_fail_edge_fallback` | No fail edge matched; trying retry_target |
| `status_ingestion_decision` | Status source (canonical/worktree/AI) was selected |

### Input Events

| Event | Description |
|---|---|
| `input_materialization_start` | Input materialization is beginning |
| `input_materialization_complete` | All inputs are ready |

### Session Events

| Event | Description |
|---|---|
| `tmux_session_start` | A tmux-based agent session is starting |
| `tmux_session_complete` | A tmux-based agent session completed |

### Infrastructure Events

| Event | Description |
|---|---|
| `stall_watchdog_timeout` | Run stalled (no progress) for the configured timeout |
| `git_push_start` | Pushing run branch to remote |
| `git_push_ok` | Git push succeeded |
| `git_push_failed` | Git push failed (run outcome unaffected) |

### Terminal Events

**These are the final events in `progress.ndjson`.** They are emitted after
`final.json` is fully written, so a consumer that observes a terminal event can
immediately open `final.json` and find it complete.

#### `run_completed` — successful run

```json
{
  "event": "run_completed",
  "status": "success",
  "run_id": "01ABC123...",
  "id": "a1b2c3d4",
  "ts": "2026-04-24T10:05:00.123456789Z"
}
```

#### `run_failed` — failed or canceled run

```json
{
  "event": "run_failed",
  "status": "fail",
  "run_id": "01ABC123...",
  "reason": "stage failed with no outgoing fail edge: ...",
  "id": "a1b2c3d4",
  "ts": "2026-04-24T10:05:00.123456789Z"
}
```

| `status` value | Meaning |
|---|---|
| `fail` | The run encountered a hard failure (e.g. stage error, no routing path) |
| `canceled` | The run was externally canceled via context cancellation |

The `reason` field is included on `run_failed` events when a failure reason is
available. It is omitted when the reason is empty.

### Ordering Guarantee

**`final.json` is always written before the terminal event is appended to
`progress.ndjson`.** This means:

1. A reader tailing `progress.ndjson` that sees `run_completed` or `run_failed`
   can immediately open `final.json` and find it present and complete.
2. Polling for `final.json` remains valid as the primary completion signal.
3. The terminal event in `progress.ndjson` is an additional, streaming-friendly
   signal that eliminates the need for file-existence polling.

### Example Stream

A minimal successful run (`start → exit`) produces a stream like:

```ndjson
{"event":"stage_attempt_start","node_id":"start","attempt":1,"run_id":"...","id":"...","ts":"..."}
{"event":"stage_attempt_success","node_id":"start","attempt":1,"run_id":"...","id":"...","ts":"..."}
{"event":"edge_selected","from_node":"start","to_node":"exit","run_id":"...","id":"...","ts":"..."}
{"event":"stage_attempt_start","node_id":"exit","attempt":1,"run_id":"...","id":"...","ts":"..."}
{"event":"stage_attempt_success","node_id":"exit","attempt":1,"run_id":"...","id":"...","ts":"..."}
{"event":"run_completed","status":"success","run_id":"...","id":"...","ts":"..."}
```

## Resume Layout

When a run is resumed after a checkpoint, new stages execute in the same
`<run_id>/` directory. If a `loop_restart` triggers a new attempt, it creates
a sibling directory:

```
<run_id>/
└── restart-1/
    ├── progress.ndjson   # Events for the restarted attempt
    ├── final.json        # Completion contract for the restarted attempt
    └── ...
```

Each restart directory has its own independent `progress.ndjson` with its own
terminal event.
