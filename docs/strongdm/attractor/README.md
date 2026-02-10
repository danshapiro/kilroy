# Attractor 

This repository contains nlspecs to build your own version of Attractor to create your own software factory.

Although bringing your own agentic loop and unified LLM SDK is not required to build your own Attractor, we highly recommend controlling the stack so you have a strong foundation.

## Specs

- [Attractor Specification](./attractor-spec.md)
- [Coding Agent Loop Specification](./coding-agent-loop-spec.md)
- [Unified LLM Client Specification](./unified-llm-spec.md)
- [Provider Plug-in Migration](./provider-plugin-migration.md)
- [Reliability Troubleshooting Runbook](./reliability-troubleshooting.md)

## Runbook Notes

- Canonical `status.json` contract:
  - `status` values `fail` and `retry` must include a non-empty `failure_reason`.
  - Legacy worktree payloads that only provide `outcome` + `details` are normalized by the runtime decoder, but emitters should still write canonical `failure_reason` directly.
- OpenAI codex CLI invocation:
  - Default args use `codex exec --json --sandbox workspace-write ...`.
  - Deprecated `--ask-for-approval` is intentionally not used.
  - Attractor isolates Codex runtime state per stage (`env_mode=isolated`, `env_scope=codex`, stage-local `state_root`).
  - Sensitive Codex state roots (`codex-home*`, `.codex/auth.json`, `.codex/config.toml`) are excluded from `stage.tgz` and `run.tgz`.
  - Idle watchdog enforces process-group cleanup for stalled Codex CLI stages.
- Codex schema behavior:
  - Structured output schema requires `final` and `summary`, but allows additional properties for CLI compatibility.
  - If codex rejects schema validation (`invalid_json_schema`-class errors), Attractor retries once without `--output-schema` and records fallback metadata in stage artifacts.
  - If codex returns unknown structured keys on schema-enabled output, Attractor emits a loud warning, writes `structured_output_unknown_keys.json`, retries once without `--output-schema`, and records fallback metadata in `cli_invocation.json`.
  - If codex emits known state-db discrepancy signatures, Attractor retries once with a fresh isolated state root and records state-db fallback metadata.
- Loop safety:
  - Use `loop_restart=true` on retry-loop edges that jump back to earlier stages.
  - Set graph-level `max_restarts` to bound cycle count and prevent unbounded runs.
  - `loop_restart` now requires `failure_class=transient_infra`; deterministic failures emit `loop_restart_blocked` and terminate.
  - Loop restarts reset stage retry budgets per iteration (`retry_budget_reset=true` in loop-restart progress events).
- Failure-class semantics:
  - Provider CLI stage failures emit normalized `failure_class` and `failure_signature` metadata at source.
  - Stage retry gating consumes `failure_class` and blocks deterministic classified failures (`stage_retry_blocked` event).
  - Unclassified fail/retry outcomes retain legacy stage retry behavior for backward compatibility.
- Provider preflight:
  - Runs after catalog/provider-model validation and before CXDB health/bootstrap.
  - Always writes `<logs_root>/preflight_report.json` (pass/warn/fail checks and summary).
  - `KILROY_PREFLIGHT_STRICT_CAPABILITIES=1` turns capability-probe failures into hard preflight failures.
  - `KILROY_PREFLIGHT_CAPABILITY_PROBES=off` disables capability probing and keeps binary-presence checks only.
  - API prompt probes retry transient failures by default (timeout `30000ms`, retries `2`, backoff `500ms` to `5000ms`).
  - Tune with `KILROY_PREFLIGHT_API_PROMPT_PROBE_TIMEOUT_MS`, `KILROY_PREFLIGHT_API_PROMPT_PROBE_RETRIES`, `KILROY_PREFLIGHT_API_PROMPT_PROBE_BASE_DELAY_MS`, `KILROY_PREFLIGHT_API_PROMPT_PROBE_MAX_DELAY_MS`.
- Provider plug-ins (runtime metadata):
  - Providers are resolved through runtime metadata (protocol family + backend + failover), not hard-coded provider switches.
  - Built-ins: `openai`, `anthropic`, `google`, `kimi`, `zai`.
  - Built-in aliases: `gemini`/`google_ai_studio` -> `google`, `moonshot`/`moonshotai` -> `kimi`, `z-ai`/`z.ai` -> `zai`.
  - `kimi` and `zai` are API-only in this release (`kimi` uses `anthropic_messages`; `zai` uses `openai_chat_completions`).
- Real vs test-shim execution:
  - `llm.cli_profile` defaults to `real` and rejects `KILROY_CODEX_PATH`, `KILROY_CLAUDE_PATH`, `KILROY_GEMINI_PATH` overrides.
  - Test-shim mode requires both `llm.cli_profile: test_shim` and per-provider `executable` config.
  - Operators must pass `--allow-test-shim` for test-shim runs; without it, run preflight fails fast.
  - Real run command:
    - `unset KILROY_CODEX_PATH KILROY_CLAUDE_PATH KILROY_GEMINI_PATH && ./kilroy attractor run --graph <graph.dot> --config <run.yaml>`
  - Test-shim run command:
    - `./kilroy attractor run --graph <graph.dot> --config <run.yaml> --allow-test-shim`
  - Optional model override:
    - `--force-model <provider=model>` (repeatable) forces a provider model and bypasses provider/model catalog membership checks for that provider.
    - Supported canonical providers are `openai`, `anthropic`, `google`, `kimi`, `zai` (aliases accepted and canonicalized).
- Fan-in all-fail behavior:
  - When all parallel branches are `status=fail`, fan-in emits `failure_class` + `failure_signature` on the aggregate fail outcome.
  - Deterministic precedence is fail-closed: any deterministic/unknown branch class makes aggregate deterministic.
  - For `parallel.fan_in` fail/retry outcomes, routing precedence is: matching conditional edge, then retry-target chain, then terminal fail. Unconditional edges are not used for fail/retry fan-in outcomes.
  - Current caveat: `status=retry` branches are still considered winner candidates by heuristic selection; all-fail aggregation only runs when every branch is `status=fail`.
- Detached runs:
  - Launch long-running jobs with `./kilroy attractor run --detach --graph <graph.dot> --config <run.yaml> --run-id <run_id> --logs-root <logs_root>`.
  - Detached launch writes `<logs_root>/run.pid` and appends launcher/child output to `<logs_root>/run.out`.
- Monitoring:
  - Snapshot current run state with `./kilroy attractor status --logs-root <logs_root>` (or `--json`).
  - Stop a run with graceful termination then optional force kill: `./kilroy attractor stop --logs-root <logs_root> [--grace-ms <ms>] [--force]`.
  - Stream progress with `tail -f <logs_root>/progress.ndjson`.
  - Inspect terminal status with `cat <logs_root>/final.json`.
  - For detached runs, check launcher output fields: `detached=true`, `logs_root=...`, `pid_file=...`.
- Restart artifacts:
  - Base logs root remains the canonical root for run-level artifacts.
  - Each restart iteration writes to `<logs_root>/restart-<n>/...`.
  - Terminal `final.json` is persisted at both current restart root and base logs root.
