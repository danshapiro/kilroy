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
kilroy attractor run --graph <file.dot> --config <run.yaml> [--run-id <id>] [--logs-root <dir>]
kilroy attractor resume --logs-root <dir>
kilroy attractor resume --cxdb <http_base_url> --context-id <id>
kilroy attractor resume --run-branch <attractor/run/...> [--repo <path>]
kilroy attractor validate --graph <file.dot>
kilroy attractor ingest [--output <file.dot>] [--model <model>] [--skill <skill.md>] [--repo <path>] [--no-validate] <requirements>
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

5. If interrupted, resume from the most convenient source:

```bash
kilroy attractor resume --logs-root <path>
```

## Ingest Details

- Uses Claude CLI (`KILROY_CLAUDE_PATH` override, default executable `claude`).
- Default model: `claude-sonnet-4-5`.
- Default repo: current working directory.
- Default skill path auto-detection: `<repo>/skills/english-to-dotfile/SKILL.md`.
- If no skill file exists, ingest fails fast.
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

- Provider keys accept `openai`, `anthropic`, `google` (`gemini` alias maps to `google`).
- If a graph node uses provider `P`, `llm.providers.P.backend` must be set (`api` or `cli`).
- In v1 behavior, runs require a clean repo and checkpoint each node.

## Provider Backends

CLI backend mappings:

- `openai` -> `codex exec --json --sandbox workspace-write -m <model> -C <worktree>`
- `anthropic` -> `claude -p --output-format stream-json --model <model>`
- `google` -> `gemini -p --output-format stream-json --yolo --model <model>`

CLI executable overrides:

- `KILROY_CODEX_PATH`
- `KILROY_CLAUDE_PATH`
- `KILROY_GEMINI_PATH`

API backend credentials:

- OpenAI: `OPENAI_API_KEY` (`OPENAI_BASE_URL` optional)
- Anthropic: `ANTHROPIC_API_KEY` (`ANTHROPIC_BASE_URL` optional)
- Google: `GEMINI_API_KEY` or `GOOGLE_API_KEY` (`GEMINI_BASE_URL` optional)

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
- English-to-dotfile skill: `skills/english-to-dotfile/SKILL.md`
