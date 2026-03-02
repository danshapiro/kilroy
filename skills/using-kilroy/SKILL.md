---
name: using-kilroy
description: "Operate Kilroy Attractor pipelines end-to-end: ingest English requirements into DOT graphs, validate graph semantics, run and resume pipelines with run config files, configure provider backends (cli/api), and debug runs from logs_root artifacts and checkpoints."
---

# Using Kilroy

Kilroy is a local-first Attractor runner:

1. Generate a DOT pipeline from English requirements.
2. Validate DOT structure + semantics.
3. Run in an isolated git worktree with checkpoint commits.
4. Resume interrupted runs from logs, CXDB, or run branch.

## Command Surface

Use these exact command forms:

```text
kilroy attractor run [--preflight|--test-run] [--detach] [--allow-test-shim] [--confirm-stale-build] [--no-cxdb] [--force-model <provider=model>] --graph <file.dot> --config <run.yaml> [--run-id <id>] [--logs-root <dir>]
kilroy attractor resume --logs-root <dir>
kilroy attractor resume --cxdb <http_base_url> --context-id <id>
kilroy attractor resume --run-branch <attractor/run/...> [--repo <path>]
kilroy attractor status [--logs-root <dir> | --latest] [--json] [--follow|-f] [--cxdb] [--raw] [--watch] [--interval <sec>]
kilroy attractor stop --logs-root <dir> [--grace-ms <ms>] [--force]
kilroy attractor validate --graph <file.dot>
kilroy attractor ingest [--output <file.dot>] [--model <model>] [--skill <skill.md>] [--repo <path>] [--max-turns <n>] [--no-validate] <requirements>
kilroy attractor serve [--addr <host:port>]
```

## Workflow

1. Run ingest:

```bash
kilroy attractor ingest -o pipeline.dot "Build a Go CLI link checker"
```

2. Validate:

```bash
kilroy attractor validate --graph pipeline.dot
```

3. Create run config (`run.yaml` or `run.json`).

4. Run:

```bash
kilroy attractor run --graph pipeline.dot --config run.yaml
```

Optional preflight-only check (validates all preflights, no stage execution):

```bash
kilroy attractor run --graph pipeline.dot --config run.yaml --preflight
```

5. If interrupted, resume from the most convenient source:

```bash
kilroy attractor resume --logs-root <path>
```

6. For long runs, launch detached so work continues after shell/session exits:

```bash
./kilroy attractor run --detach --graph pipeline.dot --config run.yaml --run-id <run_id> --logs-root <logs_root>
```

7. Observe run health and preflight behavior:

```bash
./kilroy attractor status --logs-root <logs_root>
cat <logs_root>/preflight_report.json
tail -f <logs_root>/progress.ndjson
```

8. Intervene when a run is stuck or needs termination:

```bash
./kilroy attractor stop --logs-root <logs_root> --grace-ms 30000 --force
```

## Ingest Details

- Uses Claude CLI (`KILROY_CLAUDE_PATH` override, default executable `claude`).
- Default model: `claude-sonnet-4-5`.
- Default repo: current working directory.
- Default skill path auto-detection: `<repo>/skills/create-dotfile/SKILL.md`, then binary-relative fallbacks (for example `<kilroy-prefix>/share/kilroy/skills/create-dotfile/SKILL.md`) and Go module-cache roots from binary build metadata.
- If no skill file exists, ingest fails fast.
- `--max-turns` defaults to 15 when omitted.
- Validation runs by default; use `--no-validate` to skip.

## Validate Semantics

`attractor validate` runs parse + transforms + validators and fails on error-severity diagnostics.

Key checks:

- Exactly one start node and one exit node.
- Start has no incoming edges; exit has no outgoing edges.
- All nodes reachable from start.
- Edge conditions parse correctly.
- `llm_provider` required for codergen nodes (`shape=box`).
- `model_stylesheet` is optional, but if present must parse.

## Run Config (`version: 1`)

Required fields:

- `repo.path`
- `cxdb.binary_addr`
- `cxdb.http_base_url`
- `modeldb.openrouter_model_info_path`

Defaults:

- `git.run_branch_prefix`: `attractor/run`
- `modeldb.openrouter_model_info_update_policy`: `on_run_start`
- `modeldb.openrouter_model_info_url`: `https://openrouter.ai/api/v1/models`
- `modeldb.openrouter_model_info_fetch_timeout_ms`: `5000`

Minimal example:

```yaml
version: 1

repo:
  path: /absolute/path/to/repo

cxdb:
  binary_addr: 127.0.0.1:9009
  http_base_url: http://127.0.0.1:9010

llm:
  providers:
    openai:
      backend: cli
    anthropic:
      backend: api
    google:
      backend: api

modeldb:
  openrouter_model_info_path: /absolute/path/to/openrouter_models.json
  openrouter_model_info_update_policy: on_run_start
  openrouter_model_info_url: https://openrouter.ai/api/v1/models
  openrouter_model_info_fetch_timeout_ms: 5000

git:
  require_clean: true
  run_branch_prefix: attractor/run
  commit_per_node: true
```

Notes:

- Provider keys accept `openai`, `anthropic`, `google` (`gemini` alias maps to `google`), `kimi`, `zai`, `cerebras`, and `minimax`.
- If a graph node uses provider `P`, `llm.providers.P.backend` must be set (`api` or `cli`).
- `backend: cli` is currently supported for `openai`, `anthropic`, and `google` (including the `gemini` alias).
- In v1 behavior, runs require a clean repo and checkpoint each node.
- Prefer first-class run config policy knobs over env tuning:
  - `runtime_policy` for stage timeout, stall watchdog, and retry cap.
  - `preflight.prompt_probes` for prompt-probe mode/transports/policy.

## Provider Backends

CLI backend mappings:

- `openai` -> `codex exec --json --sandbox workspace-write -m <model> -C <worktree>`
- `anthropic` -> `claude -p --dangerously-skip-permissions --output-format stream-json --verbose --model <model> "<prompt>"`
- `google` -> `gemini -p --output-format stream-json --yolo --model <model> "<prompt>"`

CLI executable overrides:

- `KILROY_CODEX_PATH`
- `KILROY_CLAUDE_PATH`
- `KILROY_GEMINI_PATH`

API backend credentials:

- OpenAI: `OPENAI_API_KEY` (`OPENAI_BASE_URL` optional)
- Anthropic: `ANTHROPIC_API_KEY` (`ANTHROPIC_BASE_URL` optional)
- Google: `GEMINI_API_KEY` or `GOOGLE_API_KEY` (`GEMINI_BASE_URL` optional)
- Kimi: `KIMI_API_KEY`
- Z.ai: `ZAI_API_KEY`
- Cerebras: `CEREBRAS_API_KEY`
- MiniMax: `MINIMAX_API_KEY`

API protocol/base URL/path overrides are configured in `llm.providers.<provider>.api` in run config.

## Run Output and Exit Codes

`run` and `resume` print:

- `run_id`
- `logs_root`
- `worktree`
- `run_branch`
- `final_commit`

Exit codes:

- `0`: final status `success` (or validation success)
- `1`: command failure, validation failure, or non-success final status

## Artifacts

Run-level (`{logs_root}`) commonly includes:

- `graph.dot`
- `manifest.json`
- `checkpoint.json`
- `final.json`
- `run_config.json`
- `modeldb/openrouter_models.json`
- `run.tgz`
- `worktree/`

Stage-level (`{logs_root}/{node_id}`) commonly includes:

- `prompt.md`
- `response.md`
- `status.json`
- `stage.tgz`
- `stdout.log`, `stderr.log`
- `events.ndjson`, `events.json`
- `cli_invocation.json`, `cli_timing.json`
- `api_request.json`, `api_response.json`
- `output_schema.json`, `output.json`
- `tool_invocation.json`, `tool_timing.json`
- `diff.patch`

Exact files depend on handler/backend type.

Browser verification notes:
- Browser verify nodes emit `tool_browser_artifacts` events in `{logs_root}/progress.ndjson`.
- Collected browser files are stored in `{logs_root}/{node_id}/browser_artifacts/`; on retries, prior copies are preserved in `{logs_root}/{node_id}/attempt_N/browser_artifacts/`.

## Status Contract for Codergen Nodes

For `shape=box` nodes:

- `llm_provider` and `llm_model` must resolve.
- If backend returns no explicit outcome, Kilroy expects a `status.json` signal.
- `status.json` may be written in worktree root; Kilroy copies it into stage directory.
- If `auto_status=true`, missing `status.json` becomes success; otherwise stage fails.

Canonical `status.json` shape:

```json
{
  "status": "success",
  "preferred_label": "",
  "suggested_next_ids": [],
  "context_updates": {},
  "notes": "",
  "failure_reason": ""
}
```

Valid statuses: `success`, `partial_success`, `retry`, `fail`, `skipped`.

## Resume Behavior

- `--logs-root`: direct and most reliable.
- `--cxdb --context-id`: recovers logs path from recent `RunStarted`/`CheckpointSaved` turns.
- `--run-branch`: derives run id from branch suffix and scans default runs directory for manifest match.

On resume, Kilroy:

- Loads `manifest.json`, `checkpoint.json`, and `graph.dot`.
- Recreates run branch/worktree at checkpoint commit.
- Requires clean repo before continuing.
- Uses the run's snapshotted model catalog from `logs_root/modeldb/openrouter_models.json`.

## Run-Config Immutability Guard

Once a user asks you to run or launch a Kilroy pipeline, the following files are **frozen** — do NOT modify them without explicit user permission:

- The graph file (`.dot`)
- The run config file (`run.yaml` / `run.json`)
- Any model configuration (catalog files, model IDs in the graph)
- The preferences file (`preferences.yaml`)

If preflight or launch fails, **diagnose and present options** — never silently fix the inputs. See "Preflight Failure Playbook" below.

This guard applies from the moment you begin building or executing a `kilroy attractor run` command until the user explicitly asks for changes. It does NOT apply during graph authoring/editing phases before a run is requested.

## Launch Intent Priority

When the user clearly instructs you to start/launch/run Kilroy, begin the run immediately. Do not ask extra "are you sure?" confirmation questions that delay execution.

Rationale: users often issue launch commands right before stepping away, and waiting for an unnecessary confirmation can waste hours.

Execution rule:

- If the requested run config is a production profile (for example `llm.cli_profile: real`) and the user clearly asked to start the run, start the production run.
- Prefer detached launch for long-running jobs unless the user explicitly requests foreground execution.
- Only stop to ask questions when required launch inputs are genuinely missing or contradictory (for example no graph path and no run config path).

## Preflight Failure Playbook

When preflight checks fail, follow this sequence:

1. **Read the preflight report**: `cat <logs_root>/preflight_report.json`
2. **Diagnose** each failure/warning and identify the root cause.
3. **Present options to the user** with your recommendation:

| Failure | Likely Cause | Options |
|---------|-------------|---------|
| Model not in catalog | Pinned catalog is stale; model is new | (a) Switch run.yaml to `on_run_start` to fetch live catalog (b) Manually update pinned catalog (c) User confirms model ID is wrong |
| CLI binary not found | Provider CLI not installed | (a) Install the CLI tool (b) Switch provider to `backend: api` (c) Use a different provider |
| API key missing | Env var not set | (a) Set the env var (b) Switch to CLI backend (c) Use a different provider |
| Prompt probe timeout | Provider is slow/down | (a) Increase `preflight.prompt_probes.timeout_ms` (b) Retry (c) Disable probes for this run |
| CLAUDECODE conflict | Running inside Claude Code session | (a) Engine strips it automatically (post-fix); rebuild if on old binary |
| Repo not clean | Uncommitted changes | (a) Commit changes (b) Stash changes (c) Set `git.require_clean: false` |

4. **Wait for user decision** before making any changes.
5. After user approves a fix, apply it and re-run.

**Never do any of the following without asking:**
- Downgrade a model ID (the model may be valid but absent from a stale catalog)
- Change the graph topology or node shapes
- Switch provider backends
- Modify prompt text

## CLAUDECODE Environment Variable

When Kilroy runs inside a Claude Code session, the `CLAUDECODE` env var is set. This causes the Claude CLI to refuse to launch (nested session protection). The engine strips `CLAUDECODE` from subprocess environments automatically (both preflight probes and codergen CLI invocations). If you encounter this error on an older binary, rebuild with `go build -o ./kilroy ./cmd/kilroy`.

## Frequent Failures

- `missing llm.providers.<provider>.backend`: add explicit backend in config.
- `missing llm_model on node`: set `llm_model` (or stylesheet model that resolves to it).
- `missing status.json (auto_status=false)`: write status file or set `auto_status=true`.
- `repo has uncommitted changes`: commit/stash before run or resume.
- `could not locate logs_root for run_branch`: use `--logs-root` or `--cxdb --context-id`.
- `resume: missing per-run model catalog snapshot`: ensure run logs are intact.

## Related Files

- Kilroy metaspec: `docs/strongdm/attractor/kilroy-metaspec.md`
- Attractor spec: `docs/strongdm/attractor/attractor-spec.md`
- Ingestor spec: `docs/strongdm/attractor/ingestor-spec.md`
- Test coverage map: `docs/strongdm/attractor/test-coverage-map.md`
- Create-dotfile skill: `skills/create-dotfile/SKILL.md`
