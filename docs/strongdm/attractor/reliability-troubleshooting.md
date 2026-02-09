# Attractor Reliability Troubleshooting

This runbook focuses on reliability validation for `parallel.fan_in` failure routing, deterministic retry gating, and provider CLI contract checks.

## Fresh DTTF Run From Scratch

```bash
RUN_ROOT="/tmp/kilroy-dttf-main-fresh-$(date -u +%Y%m%dT%H%M%SZ)"
RUN_ID="dttf-main-fresh-$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$RUN_ROOT"

git clone --no-local /path/to/kilroy/worktree "$RUN_ROOT/repo"

cat > "$RUN_ROOT/run_config.json" <<JSON
{
  "version": 1,
  "repo": { "path": "$RUN_ROOT/repo" },
  "cxdb": {
    "binary_addr": "127.0.0.1:9009",
    "http_base_url": "http://127.0.0.1:9010"
  },
  "llm": {
    "cli_profile": "real",
    "providers": {
      "anthropic": { "backend": "cli" },
      "google": { "backend": "cli" },
      "openai": { "backend": "cli" }
    }
  },
  "modeldb": {
    "openrouter_model_info_path": "$RUN_ROOT/openrouter_models.json",
    "openrouter_model_info_update_policy": "pinned",
    "openrouter_model_info_url": "https://openrouter.ai/api/v1/models",
    "openrouter_model_info_fetch_timeout_ms": 5000
  },
  "git": {
    "require_clean": true,
    "run_branch_prefix": "attractor/run",
    "commit_per_node": true
  }
}
JSON

./kilroy attractor run \
  --detach \
  --graph "$RUN_ROOT/repo/demo/dttf/dttf.dot" \
  --config "$RUN_ROOT/run_config.json" \
  --run-id "$RUN_ID" \
  --logs-root "$RUN_ROOT/logs"
```

Before real runs, clear shim overrides:

```bash
unset KILROY_CODEX_PATH KILROY_CLAUDE_PATH KILROY_GEMINI_PATH
```

## Explicit Test-Shim Run (Fake Provider CLIs)

Use this only for deterministic local tests with fake provider binaries.

```json
"llm": {
  "cli_profile": "test_shim",
  "providers": {
    "openai": {
      "backend": "cli",
      "executable": "/absolute/path/to/fake-codex"
    }
  }
}
```

```bash
./kilroy attractor run \
  --graph "$RUN_ROOT/repo/demo/dttf/dttf.dot" \
  --config "$RUN_ROOT/run_config_test_shim.json" \
  --allow-test-shim \
  --run-id "$RUN_ID-test-shim" \
  --logs-root "$RUN_ROOT/logs-test-shim"
```

Wait for terminal status:

```bash
while [ ! -f "$RUN_ROOT/logs/final.json" ]; do
  date -u +"%Y-%m-%dT%H:%M:%SZ"
  test -f "$RUN_ROOT/logs/live.json" && cat "$RUN_ROOT/logs/live.json"
  sleep 10
done
cat "$RUN_ROOT/logs/final.json"
```

## Fan-In Failure Routing Checks

Inspect all `join_tracer` outcomes and selected next hops:

```bash
jq -c 'select(.event=="stage_attempt_end" and .node_id=="join_tracer")
  | {ts,status,failure_reason}' "$RUN_ROOT/logs/progress.ndjson"

jq -c 'select(.event=="edge_selected" and .from_node=="join_tracer")
  | {ts,to_node,hop_source,condition}' "$RUN_ROOT/logs/progress.ndjson"
```

Expected:
- If a `join_tracer` attempt is `status=fail` with `failure_reason="all parallel branches failed"`, the next hop should be `retry_target` (for DTTF: `to_node=impl_setup`) unless a matching conditional fail edge exists.
- Unconditional `join_tracer -> verify_tracer` should only appear on successful join attempts.

## Deterministic No-Retry Checks

```bash
# Must be empty for join_tracer deterministic all-fail outcomes.
jq -c 'select(.event=="stage_retry_sleep" and .node_id=="join_tracer")' \
  "$RUN_ROOT/logs/progress.ndjson"

# Must show deterministic retry block for join_tracer fail attempts.
jq -c 'select(.event=="stage_retry_blocked" and .node_id=="join_tracer")
  | {ts,failure_class,failure_reason,max_retry}' \
  "$RUN_ROOT/logs/progress.ndjson"
```

Expected:
- No `stage_retry_sleep` entries for deterministic `join_tracer` failures.
- At least one `stage_retry_blocked` with `failure_class="deterministic"` when all parallel branches fail.

## Anthropic Stream-JSON Contract Checks

Runtime mismatch scan (exclude copied source trees under worktrees):

```bash
rg -n 'requires --verbose|provider cli preflight|stream-json contract' \
  "$RUN_ROOT/logs" -S -g '!**/worktree/**'
```

Expected:
- No runtime mismatch lines like `--output-format stream-json requires --verbose`.

Invocation artifact spot-check:

```bash
INV=$(find "$RUN_ROOT/logs/parallel/par_tracer" -path '*/impl_tracer_a/cli_invocation.json' | head -n1)
jq -r '.executable, (.argv|join(" "))' "$INV"
```

Expected:
- Anthropic argv includes `--output-format stream-json --verbose`.

## Terminal Outcome Check

```bash
jq -r '.status, (.failure_reason // "")' "$RUN_ROOT/logs/final.json"
```

Expected:
- `status` is present (`success` or `fail`).
- `failure_reason` is non-empty when `status=fail`.
