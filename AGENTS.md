# Repository Guidelines

## What Kilroy Is
Kilroy is a local-first Go CLI for running software-factory pipelines in a Git repository. There is a skill to convert English requirements into DOT graphs. Then it validates graph semantics and executes stages with checkpoint commits and a run history backed by `cxdb`. Foundational specs that are in docs/strongdm/attractor.

Use Kilroy in this order: build the binary, generate or write a graph, validate it, then run it with a config file. Typical flow: `go build -o ./kilroy ./cmd/kilroy`, `./kilroy attractor ingest -o pipeline.dot "<requirements>"`, `./kilroy attractor validate --graph pipeline.dot`, then `./kilroy attractor run --graph pipeline.dot --config run.yaml`.

## What you're doing here
If you can see this message, then you are not here to use Kilroy - you're here to improve Kilroy. If Kilroy fails to build a project:
- Don't fix the project
- Don't fix the dotfile
- Don't fix the system so it works for *this* project
Use the knowledge you've gained from the failure to make the system more robust for *every* project. 
Of course, user instructions may override this, or any other section.

# Think like a user
Think about a blank slate agent that is trying to properly create a dotfile using the dotfile skill and then run it with the attractor. What mistakes would it make? What ergonomics would steer it away? How can you make that robust for every possible project the attractor could work on, not just this one? How can you do that without asking it to know the impossible, like how hard a problem is or how long something might take? 

## Project Structure & Module Organization
- `cmd/kilroy/`: CLI entrypoint and subcommands for `attractor` commands (`run`, `resume`, `status`, `stop`, `validate`, `ingest`).
- `internal/attractor/`: core engine/runtime, graph validation, config loading, and model metadata handling.
- `internal/agent/`, `internal/cxdb/`, `internal/llmclient/`: coding-agent loop, CXDB integration, and provider client/env wiring.
- `scripts/`: operational helpers (`e2e.sh`, `e2e-guardrail-matrix.sh`, `start-cxdb.sh`, `run_benchmarks.sh`).
- `demo/`, `docs/`, `skills/`: sample graphs, architecture/spec references, and ingestion skills.

## Build, Test, and Development Commands
- `go build -o ./kilroy ./cmd/kilroy`: build the local CLI binary.
- `go test ./...`: run the full Go test suite.
- `./scripts/e2e.sh`: smoke check (tests, build, and graph validation).
- `./scripts/e2e-guardrail-matrix.sh`: run targeted engine guardrail regression tests.
- `./kilroy attractor validate --graph <file.dot>`: validate graph structure/semantics before execution.

## Kilroy Agent Rules

### Production Safety (Strict)

NEVER start a production run except precisely as the user requested, and only after an explicit user request for that production run. Production runs are expensive.
Any routing decision (provider, model, reasoning depth, or API vs CLI) has cost implications and must be explicitly approved by the user.

For production runs (`llm.cli_profile=real`), execute only the exact command the user explicitly approved.
Do not change flags, env, config, paths, `--run-id`, `--detach`, or add overrides like `--force-model` unless explicitly approved.
If the run fails, stop immediately, report the error, and wait for explicit approval of a new exact command.

### Running Attractor

#### Launch Modes: Production vs Test

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

#### Binary Freshness

- Before running `./kilroy attractor run`, ensure `./kilroy` is built from current repo `HEAD`.
- If stale-build detection triggers, rebuild with `go build -o ./kilroy ./cmd/kilroy` and rerun.
- Use `--confirm-stale-build` only when intentionally running a stale binary.

#### Long Runs (Detached)

For long `attractor run`/`resume` jobs, launch detached so the parent shell/session ending does not kill Kilroy:

```bash
RUN_ROOT=/path/to/run_root
setsid -f bash -lc 'cd /home/user/code/kilroy-wt-state-isolation-watchdog && ./kilroy attractor resume --logs-root "$RUN_ROOT/logs" >> "$RUN_ROOT/resume.out" 2>&1'
```

### Checking Run Status

Runs live under `~/.local/state/kilroy/attractor/runs/<run_id>/`. Key files:

- `final.json` — exists only when the run finished; `status` is `success` or `fail`.
- `checkpoint.json` — last completed node, retry counts, `failure_reason` (if any).
- `live.json` — most recent engine event (retries, errors, current node).
- `progress.ndjson` — full event log (stage starts/ends, edge selections, LLM retries).
- `manifest.json` — run metadata (goal, graph, repo, base SHA).

### PR Review Process

For PRs we want to accept: check out the PR branch into a worktree, review, add fix-up commits, then non-squash merge — this preserves contributor credit while maintaining code quality.

## Coding Style & Naming Conventions
- Follow idiomatic Go and run formatter before commit: `gofmt -w ./cmd ./internal`.
- Keep packages domain-focused (for example `engine`, `validate`, `modeldb`) and avoid cross-package leakage.
- Use lowercase package names, `CamelCase` for exported symbols, and colocated `*_test.go` files.
- Prefer explicit config over implicit behavior; this codebase favors deterministic runtime contracts.

## Testing Guidelines
- Add tests next to the changed code (`internal/.../*_test.go`).
- Use table-driven tests for validation, parsing, and routing logic.
- For engine/runtime changes, run targeted package tests plus `go test ./...`.
- Include regression coverage for bug fixes, not just happy-path assertions.

## Commit & Pull Request Guidelines
- Follow commit patterns seen in history: `area: summary` or `type(scope): summary` (for example `engine/runtime: ...`, `docs(plan): ...`, `feat(modeldb): ...`).
- Keep commits narrow and include docs/tests when behavior or contracts change.
- PRs should include: intent, key files touched, commands run for validation, and config/runtime impact.
- For CLI behavior changes, include example invocations and representative output.
