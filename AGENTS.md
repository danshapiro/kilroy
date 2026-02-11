## Long Runs (Detached)

For long `attractor run`/`resume` jobs, launch detached so the parent shell/session ending does not kill Kilroy:

```bash
RUN_ROOT=/path/to/run_root
setsid -f bash -lc 'cd /home/user/code/kilroy-wt-state-isolation-watchdog && ./kilroy attractor resume --logs-root "$RUN_ROOT/logs" >> "$RUN_ROOT/resume.out" 2>&1'
```

## Launch Modes: Production vs Test

Use explicit run configs and flags so the mode is unambiguous:

- **Production run (real providers, real cost):**
  - `llm.cli_profile` must be `real`
  - Do **not** use `--allow-test-shim`
  - Example:

```bash
./kilroy attractor run --detach --graph <graph.dot> --config <run_config_real.json> --run-id <run_id> --logs-root <logs_root>
```

- **Test run (fake/shim providers):**
  - `llm.cli_profile` must be `test_shim`
  - Provider executable overrides are expected in config
  - `--allow-test-shim` is required
  - Example:

```bash
./kilroy attractor run --detach --graph <graph.dot> --config <run_config_test_shim.json> --allow-test-shim --run-id <run_id> --logs-root <logs_root>
```

## Binary Freshness

- Before running `./kilroy attractor run`, ensure `./kilroy` is built from current repo `HEAD`.
- If stale-build detection triggers, rebuild with `go build -o ./kilroy ./cmd/kilroy` and rerun.
- Use `--confirm-stale-build` only when intentionally running a stale binary.

## Checking Run Status

Runs live under `~/.local/state/kilroy/attractor/runs/<run_id>/`. Key files:

- `final.json` — exists only when the run finished; `status` is `success` or `fail`.
- `checkpoint.json` — last completed node, retry counts, `failure_reason` (if any).
- `live.json` — most recent engine event (retries, errors, current node).
- `progress.ndjson` — full event log (stage starts/ends, edge selections, LLM retries).
- `manifest.json` — run metadata (goal, graph, repo, base SHA).

## PR Review Process

For PRs we want to accept: check out the PR branch into a worktree, review, add fix-up commits, then non-squash merge — this preserves contributor credit while maintaining code quality.

## Production Authorization Rule (Strict)

NEVER start a production run except precisely as the user requested, and only after an explicit user request for that production run. Production runs are expensive.
Any routing decision (provider, model, reasoning depth, or API vs CLI) has cost implications and must be explicitly approved by the user.

## Production Runs: Exact Command Only

- For production runs (`llm.cli_profile=real`), execute only the exact command the user explicitly approved.
- Do not change flags, env, config, paths, `--run-id`, `--detach`, or add overrides like `--force-model` unless explicitly approved.
- If the run fails, stop immediately, report the error, and wait for explicit approval of a new exact command.
