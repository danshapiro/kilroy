---
name: investigating-kilroy-runs
description: "To diagnose active, stuck, or failed Kilroy Attractor runs, inspect run artifacts (`manifest.json`, `live.json`, `checkpoint.json`, `final.json`, `progress.ndjson`), resolve run IDs/log roots, identify model/provider routing, and isolate failure causes. Includes CXDB operations for launching/probing CXDB, opening the CXDB UI, and querying run context turns. This skill is useful when investigating run status, debugging retries/failures, explaining model usage, or inspecting CXDB-backed event history."
---

# Investigating Kilroy Runs

To inspect a run quickly and produce a precise diagnosis, follow this workflow.

## Resolve Run Root

1. To resolve run root with highest confidence, start from an explicit `--logs-root` path when provided.
2. To infer run root when no path is provided, locate the newest run under `~/.local/state/kilroy/attractor/runs`.
3. To keep later commands consistent, treat that directory as `RUN_ROOT`.

```bash
RUNS="$HOME/.local/state/kilroy/attractor/runs"
RUN_ID="$(find "$RUNS" -mindepth 1 -maxdepth 1 -type d -printf '%T@ %f\n' | sort -nr | awk 'NR==1 {id=$2} END {print id}')"
RUN_ROOT="$RUNS/$RUN_ID"
echo "$RUN_ID"
```

For a quick check of the newest run without manually resolving `RUN_ROOT`, use:

```bash
./kilroy attractor status --latest --json
```

## CXDB: Launch, UI, and Query

To get CXDB connection details, start with `manifest.json`, then fall back to `run_config.json` and live artifacts when fields are missing:

```bash
CXDB_URL="$(jq -r '.cxdb.http_base_url // empty' "$RUN_ROOT/manifest.json")"
CONTEXT_ID="$(jq -r '.cxdb.context_id // empty' "$RUN_ROOT/manifest.json")"

if [ -z "$CXDB_URL" ] && [ -f "$RUN_ROOT/run_config.json" ]; then
  CXDB_URL="$(jq -r '.cxdb.http_base_url // empty' "$RUN_ROOT/run_config.json")"
fi
if [ -z "$CONTEXT_ID" ] && [ -f "$RUN_ROOT/live.json" ]; then
  CONTEXT_ID="$(jq -r '.context_id // empty' "$RUN_ROOT/live.json")"
fi
if [ -z "$CONTEXT_ID" ] && [ -f "$RUN_ROOT/checkpoint.json" ]; then
  CONTEXT_ID="$(jq -r '.context_id // empty' "$RUN_ROOT/checkpoint.json")"
fi

echo "cxdb_url=$CXDB_URL context_id=$CONTEXT_ID"
```

To make sure CXDB is available and to print the UI endpoint, run:

```bash
./scripts/start-cxdb.sh
UI_LINE="$(./scripts/start-cxdb-ui.sh)"
echo "$UI_LINE"   # prints: cxdb_ui=http://...
CXDB_UI="${UI_LINE#cxdb_ui=}"
```

Use the endpoint printed by `start-cxdb-ui.sh` (`cxdb_ui=...`) as the source of truth. To open the UI in a browser when needed, run:

```bash
KILROY_CXDB_OPEN_UI=1 ./scripts/start-cxdb-ui.sh
```

To follow run events directly from CXDB:

```bash
./kilroy attractor status --logs-root "$RUN_ROOT" --follow --cxdb
./kilroy attractor status --logs-root "$RUN_ROOT" --follow --cxdb --raw
```

To run direct HTTP queries for ad-hoc debugging, use:

```bash
# Health endpoint may be /healthz even when /health returns 404.
curl -fsS "$CXDB_URL/health" || curl -fsS "$CXDB_URL/healthz"
curl -fsS "$CXDB_URL/v1/contexts"
curl -fsS "$CXDB_URL/v1/contexts/$CONTEXT_ID"
curl -fsS "$CXDB_URL/v1/contexts/$CONTEXT_ID/turns?limit=20"
curl -fsS "$CXDB_URL/v1/contexts/$CONTEXT_ID/turns?view=typed&limit=20"
```

## Read Canonical Files

To build a reliable picture of run state:

Always inspect `graph.dot` first so status is interpreted in graph context.

Preflight-only runs (`--preflight` / `--test-run`) are expected to write `preflight_report.json` and skip execution artifacts (`manifest.json`, `checkpoint.json`, `final.json`, `worktree/`).

1. `manifest.json`: run identity, graph name, repo, worktree, `started_at`.
2. `live.json`: most recent event.
3. `checkpoint.json`: last completed node and failure context.
4. `final.json`: if present, run is finished (`success` or `fail`).
5. `progress.ndjson`: full event timeline.

```bash
sed -n '1,200p' "$RUN_ROOT/graph.dot"
sed -n '1,200p' "$RUN_ROOT/manifest.json"
sed -n '1,200p' "$RUN_ROOT/live.json"
[ -f "$RUN_ROOT/checkpoint.json" ] && sed -n '1,200p' "$RUN_ROOT/checkpoint.json"
[ -f "$RUN_ROOT/final.json" ] && sed -n '1,200p' "$RUN_ROOT/final.json"
tail -n 80 "$RUN_ROOT/progress.ndjson"
```

## Determine Current State

To classify run state:

- Running: `final.json` missing and `live.json`/`progress.ndjson` still changing.
- Finished: `final.json` present.
- Likely stalled: no `progress.ndjson` updates for longer than configured stall timeout.
- `attractor status` can show terminal `fail` while `progress.ndjson` still advances in overlap conditions; timestamp comparison resolves this ambiguity.

To quickly validate terminal state vs liveness, use:

```bash
ls -la "$RUN_ROOT/final.json"
tail -n 1 "$RUN_ROOT/progress.ndjson"
```

## Discover Event Schema First

To avoid filtering for non-existent event keys, discover the active event schema before building event-specific queries:

```bash
jq -r '.event? // empty' "$RUN_ROOT/progress.ndjson" | sort | uniq -c | sort -nr
```

To inspect fields for a specific event type, run:

```bash
jq -c 'select(.event=="stage_attempt_end")' "$RUN_ROOT/progress.ndjson" | head -n 5
```

## Noise-to-Signal Query Patterns (Observed)

These patterns come from real investigation mistakes where the first query produced noisy output and the replacement query produced useful signal.

1. Raw tail was dominated by heartbeats.

```bash
# noisy
tail -n 80 "$RUN_ROOT/progress.ndjson"

# higher-signal
jq -rc 'select(.event!="branch_heartbeat") | {ts,event,node_id,status,branch_key,branch_event,branch_status,branch_failure_reason}' \
  "$RUN_ROOT/progress.ndjson" | tail -n 80
```

2. Event frequency counts gave little "what is happening now" signal.

```bash
# broad but low immediate diagnostic value
jq -r '.event? // empty' "$RUN_ROOT/progress.ndjson" | sort | uniq -c | sort -nr

# better for current state
jq -rc 'select(.event!="branch_heartbeat") | {ts,event,node_id,status,branch_key,branch_event,branch_status,branch_failure_reason}' \
  "$RUN_ROOT/progress.ndjson" | tail -n 40
```

3. Broad history scans mixed unrelated commits into run analysis.

```bash
# noisy across repo history
git rev-list HEAD | head -n 200

# scoped to this run's commits
git log --oneline --grep "$RUN_ID" -n 80
```

4. Full commit stats pulled in `target/` and artifact churn.

```bash
# noisy if commit touched build outputs
git show --stat <commit>

# source-focused
git show --stat <commit> -- demo/rogue/rogue-wasm/src
```

5. Progress checks can fail on pipe/SIGPIPE mechanics instead of run state.

```bash
# brittle in strict pipefail shells
set -euo pipefail
jq -rc 'select(.event!="branch_heartbeat")' "$RUN_ROOT/progress.ndjson" | head -n 20

# safer wrapper for quick probes
set -euo pipefail
jq -rc 'select(.event!="branch_heartbeat")' "$RUN_ROOT/progress.ndjson" | head -n 20 || true
```

6. "Is code still improving?" was unclear without a last-meaningful-change anchor.

```bash
# activity-only view
git log --oneline --grep "$RUN_ID" -n 30

# source progress since last meaningful implementation commit
git diff --stat <last_meaningful_commit>..HEAD -- demo/rogue/rogue-wasm/src
```

## Resolve Terminal-vs-Live Conflicts

To handle cases where `final.json` exists but `progress.ndjson` still changes:

1. If `final.json` exists and `progress.ndjson` is newer, this is a possible overlapping-resume state rather than immediate data corruption.
2. Compare file mtimes and latest event timestamps.
3. Confirm whether a `run`/`resume` process is currently active.

```bash
stat -c '%n %y' "$RUN_ROOT/final.json" "$RUN_ROOT/live.json" "$RUN_ROOT/progress.ndjson" 2>/dev/null
tail -n 5 "$RUN_ROOT/progress.ndjson"
```

## Check Process Liveness

To determine whether the run is truly active at the OS level:

```bash
pgrep -af 'kilroy attractor (run|resume)'
[ -f "$RUN_ROOT/run.pid" ] && cat "$RUN_ROOT/run.pid"
[ -f "$RUN_ROOT/run.pid" ] && ps -fp "$(cat "$RUN_ROOT/run.pid")"
ps -ef | rg -i 'kilroy attractor (run|resume)' | rg -v rg
```

If a `resume` process is already active for the same `--logs-root`, launching another `resume` is a possible source of mixed terminal/live state.

A live PID with unchanged tail events across repeated checks is a possible stale/hung process, not active run progress.

```bash
E1="$(tail -n 1 "$RUN_ROOT/progress.ndjson")"
sleep 3
E2="$(tail -n 1 "$RUN_ROOT/progress.ndjson")"
[ "$E1" = "$E2" ] && echo "no new events" || echo "events advancing"
```

## Relaunch Hygiene (Single Owner)

When relaunching, quiescing duplicate `resume` processes first and launching one detached `resume` reduces the chance of `stopped by signal terminated` outcomes.

```bash
ps -ef | rg -i "kilroy attractor resume --logs-root $RUN_ROOT" | rg -v rg
# If duplicates exist, stop extras before launching a single detached resume.
setsid -f bash -lc "cd /path/to/repo && ./kilroy attractor resume --logs-root '$RUN_ROOT' >> '$RUN_ROOT/resume.out' 2>&1"
```

## Debug Parallel Fan-In Stalls

To diagnose fan-in waits, inspect branch-local progress under `parallel/<join-node>/`:

```bash
find "$RUN_ROOT/parallel" -maxdepth 3 -type f -name 'progress.ndjson' | sort
```

To inspect branch outcomes and status-contract behavior:

```bash
rg -n 'status_contract|stage_attempt_end|stage_retry_blocked|deterministic_failure_cycle_check|subgraph_deterministic_failure_cycle_check' \
  "$RUN_ROOT"/parallel/*/*/progress.ndjson
```

To interpret heartbeat-only behavior:

- If events are mostly `branch_heartbeat` and `branch_idle_ms` rises monotonically, the branch is likely waiting/stalled rather than converging.
- If `branch_progress` continues with new `stage_attempt_*` events, the branch is still making progress.

```bash
tail -n 200 "$RUN_ROOT/progress.ndjson" | rg 'branch_heartbeat|branch_progress|branch_idle_ms'
```

## Check Loop and Cycle Guardrails

To distinguish policy stops from compute hangs, inspect guardrail events directly:

```bash
rg -n 'stuck_cycle_breaker|deterministic_failure_cycle_breaker|stage_retry_blocked|deterministic_failure_cycle_check' \
  "$RUN_ROOT/progress.ndjson"
```

Interpretation guidance:

- `stuck_cycle_breaker` with `visit_count`/`visit_limit` indicates a configured loop-visit stop.
- `*_cycle_breaker` indicates repeated deterministic failures reached the configured signature limit.

## Identify Models and Providers

To identify models/providers accurately, combine static and runtime evidence:

1. Static routing from graph `model_stylesheet` and node classes.
2. Runtime events from `progress.ndjson` (`llm_retry`, `llm_call_*`, provider/model fields).
3. Provider availability from `run_config.json`.

```bash
rg -n 'model_stylesheet|llm_model|llm_provider|class=' "$RUN_ROOT/graph.dot"
rg -n '"event":"llm_|"provider":"|"model":"' "$RUN_ROOT/progress.ndjson"
sed -n '1,220p' "$RUN_ROOT/run_config.json"
```

## Triage Common Failures

- To diagnose `missing status.json`, check whether the codergen node emitted the required status signal.
- To diagnose `llm retry` with `429`/rate-limit, check for provider quota or backoff pressure.
- To diagnose `deterministic_failure_cycle_check`, check for repeated deterministic failure at the same node.
- To diagnose toolchain/setup errors, inspect `setup_command_*` events and stage `stderr.log`.

```bash
rg -n 'missing status.json|llm_retry|deterministic_failure_cycle_check|setup_command_|failure_reason' "$RUN_ROOT/progress.ndjson"
```

## Postmortem Capture (Overlap/Termination)

Capture `final.json` timestamp, latest `progress.ndjson` timestamp, active `resume` PIDs/PPIDs, and termination events (`stopped by signal terminated`, `subgraph_canceled_exit`, `stage_attempt_end`).

```bash
stat -c '%y %n' "$RUN_ROOT/final.json" "$RUN_ROOT/live.json" "$RUN_ROOT/progress.ndjson" 2>/dev/null
ps -ef | rg -i "kilroy attractor resume --logs-root $RUN_ROOT" | rg -v rg
rg -n 'stopped by signal terminated|subgraph_canceled_exit|stage_attempt_end' "$RUN_ROOT/progress.ndjson" | tail -n 80
```

## Report Format

To present findings clearly, report in this order:

1. `run_id`, `run_root`, started time.
2. Current node/state and whether run is still active.
3. Models/providers configured and observed.
4. Top failure signals with exact file references.
5. One next action (continue waiting, resume, or fix specific failure cause).
