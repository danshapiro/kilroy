# Kilroy Attractor Metaspec

## 0. What This Document Is (Definition of “Metaspec”)

This file is a **metaspec**: a project-specific specification that:

- **Points to upstream specs** (Attractor + Coding Agent Loop + Unified LLM) as the source of most behavior.
- **Pins local decisions** (language, storage, git workflow, debug policy, supported providers/backends).
- **Fills gaps and resolves ambiguities** where the upstream docs are intentionally flexible or internally inconsistent.

In other words: the upstream docs describe *a family* of valid Attractor implementations; this metaspec defines *the* Attractor implementation we are building in this repository.

Normative language: **MUST / SHOULD / MAY** are used intentionally.

## 1. Scope

We are implementing StrongDM’s Attractor in Go inside this repo, with these top-level properties:

- **Local-first, trusted machine** assumptions.
- **CLI + API neutral** LLM execution: the same graph can run against provider CLIs or provider APIs.
- **Everything is recorded** (debug-by-default) and persisted to **CXDB** as an unbounded local store.
- **git is required** and provides the authoritative checkpointing story for code changes.

## 2. Sources of Truth and Precedence

**Primary specs (in-repo):**

1. `docs/strongdm/attractor/attractor-spec.md` (graph DSL + engine semantics)
2. `docs/strongdm/attractor/coding-agent-loop-spec.md` (tool-using coding agent loop)
3. `docs/strongdm/attractor/unified-llm-spec.md` (provider-neutral LLM client types)

**CXDB (external, referenced):**

- Factory overview: `https://factory.strongdm.ai/products/cxdb.md`
- CXDB repo docs: `https://github.com/strongdm/cxdb` (HTTP API, protocol, type registry)

**Precedence rules:**

- If this metaspec conflicts with the three in-repo specs, **this metaspec wins**.
- If the three in-repo specs conflict with each other, resolve by:
  - `attractor-spec.md` wins for engine/graph semantics.
  - `coding-agent-loop-spec.md` wins for tool loop behavior.
  - `unified-llm-spec.md` wins for LLM wire types and cross-provider normalization.

## 3. Fixed Decisions (Non-Negotiable Defaults for Kilroy)

### 3.1 Language and Runtime

- Implementation language: **Go**.

### 3.2 Providers and Backends (v1)

- Provider plug-in runtime built-ins in v1: **OpenAI**, **Anthropic**, **Google (Gemini)**, **Kimi**, **ZAI**.
- Backends supported in v1:
  - `api`: call provider APIs through protocol-family adapters (provider SDKs and/or direct HTTP, depending on protocol).
  - `cli`: spawn provider CLIs as subprocesses and ingest their JSON/JSONL outputs.

API-only in v1:

- `kimi` and `zai` are API-only providers.
- Default `kimi` built-in protocol family: `anthropic_messages` (Kimi Coding endpoint).
- Default `zai` built-in protocol family: `openai_chat_completions`.

**No implicit backend defaults:**

- If a node resolves to provider `P` and the run config does not specify `backend(P)`, the run **MUST fail fast** with a configuration error.

### 3.3 Model Metadata Source (OpenRouter Model Info)

- Model metadata (model IDs, context windows, pricing estimates, capability flags) MUST come from OpenRouter `/api/v1/models`.
- This catalog is **metadata-only**. It MUST NOT be used as the call path to providers.

#### 3.3.1 Catalog Pinning and Updates

- The repository SHOULD include a pinned snapshot of OpenRouter model info (checked into git).
- Runs MUST be reproducible. To ensure repeatability while still benefiting from new models and pricing updates, Kilroy uses a **per-run snapshot**:
  - On run start, the engine MUST materialize the effective catalog to `{logs_root}/modeldb/openrouter_models.json`.
  - Resume MUST use the run’s snapshotted catalog (not whatever happens to be on disk now).
  - If the effective catalog differs from the repo-pinned snapshot, this MUST be recorded as a warning (not a fatal error).

**Update policy (v1):**

- Default: `on_run_start` (best compromise)
  - Try to fetch the latest catalog from upstream into the per-run snapshot file.
  - If fetch fails, fall back to the pinned snapshot and continue, emitting a warning.
- Optional: `pinned`
  - Do not fetch. Copy the pinned snapshot into the per-run snapshot file.

The CLI MAY provide a command to refresh the repo-pinned snapshot out-of-band.

### 3.4 Debugging and Persistence

- Debug/trace capture is **enabled by default**.
- **All artifacts** are persisted in **CXDB**:
  - raw provider CLI JSON/JSONL
  - session/restore JSON (when available)
  - request/response transcripts (as available)
  - tool call logs, command stdout/stderr, file diffs/patches
  - stage directories (or their contents) and checkpoints

### 3.5 git Checkpointing (Required)

- `git` MUST be present; if `git` is not available or the working directory is not a git repo, the run MUST fail fast.
- The run uses a dedicated **per-run branch** and MUST **commit after each node** completes (whether or not code changed).
- CLI backend execution MUST occur in an isolated **git worktree** so CLIs can edit files directly.

### 3.6 CXDB Trajectory Model (Required)

- Each Attractor run maps to **one CXDB context** (one trajectory head).
- Branching/retries in the pipeline map to **CXDB context forks** (Turn DAG branching), not separate unrelated logs.

## 4. User-Facing Contract (Inputs and Outputs)

### 4.1 Inputs

An Attractor run is defined by:

- A DOT graph file (or DOT source string), per `attractor-spec.md`.
- A run configuration (file and/or CLI flags) that specifies at minimum:
  - which provider backend to use (`api` vs `cli`) for each provider used by the graph
  - how to locate CXDB (local URL/addr; and optionally auto-start settings)
  - run working mode (in-place vs worktree) is **always worktree** for `cli` backend; see Section 5

Graphs remain backend-agnostic; backend choice is configuration, not embedded in the DOT by default.

### 4.2 Outputs

Each run produces:

- A **git branch** `attractor/run/<run_id>` with a linear commit history containing:
  - one checkpoint commit per executed node
  - commit message format: `attractor(<run_id>): <node_id> (<status>)`
- A **run directory** on disk at `{logs_root}` matching `attractor-spec.md` (stage dirs, `checkpoint.json`, etc.).
- A **CXDB context** containing:
  - typed turns for all engine events + outcomes
  - blobs for all artifacts, including raw CLI streams and stage files
  - a final head turn that corresponds to `com.kilroy.attractor.RunCompleted` or `com.kilroy.attractor.RunFailed`
- A machine-readable “final outcome” summary written to `{logs_root}/final.json` (Kilroy-specific addition):
  - final pipeline status
  - final git commit SHA
  - CXDB `context_id` and head `turn_id`

### 4.3 Run Configuration (File Schema)

Kilroy supports a single run config file (YAML or JSON). The schema below is normative; fields not listed are ignored.

```yaml
version: 1

repo:
  path: /abs/path/to/git/repo

cxdb:
  # Required in v1. Must point at a CXDB instance that Attractor can write to.
  # If not reachable, the run fails fast.
  binary_addr: 127.0.0.1:9009
  http_base_url: http://127.0.0.1:9010

llm:
  # Backend selection is REQUIRED. No implicit defaults.
  providers:
    openai:
      backend: api   # api|cli
    anthropic:
      backend: cli   # api|cli
    google:
      backend: api   # api|cli
    kimi:
      backend: api
      api:
        protocol: anthropic_messages
        api_key_env: KIMI_API_KEY
        base_url: https://api.kimi.com/coding
        path: /v1/messages
        profile_family: openai
    zai:
      backend: api
      api:
        protocol: openai_chat_completions
        api_key_env: ZAI_API_KEY
        base_url: https://api.z.ai
        path: /api/coding/paas/v4/chat/completions
        profile_family: openai

modeldb:
  # Local path to the pinned OpenRouter model info JSON.
  # May be updated out-of-band; runtime uses whatever is on disk.
  openrouter_model_info_path: /abs/path/to/openrouter_models.json
  # v1 compromise: prefer fresh metadata but keep runs repeatable.
  openrouter_model_info_update_policy: on_run_start   # pinned|on_run_start
  openrouter_model_info_url: https://openrouter.ai/api/v1/models
  openrouter_model_info_fetch_timeout_ms: 5000

# Deprecated compatibility aliases (`litellm_catalog_*`) are accepted for one release.

git:
  require_clean: true
  run_branch_prefix: attractor/run
  commit_per_node: true
```

### 4.4 Exit Codes

- Exit code `0`: pipeline completed with final status `success`.
- Exit code `1`: pipeline failed (final status `fail`) or could not start (config/validation error).
- Exit code `2`: pipeline cancelled (future; not required in v1).

## 5. Workspace and git Model

### 5.0 Run ID and logs_root

- `run_id` MUST be a globally unique, filesystem-safe identifier. Recommended: **ULID**.
- Default `{logs_root}` SHOULD be outside the git repo to avoid contaminating the working tree:
  - `${XDG_STATE_HOME:-$HOME/.local/state}/kilroy/attractor/runs/<run_id>`
- `{logs_root}` MUST be recorded in CXDB (so a run can be “found” from CXDB even if the CLI output is lost).

### 5.1 Cleanliness and Safety

- By default, the run MUST refuse to start if the repo has uncommitted changes.
- An explicit override flag (e.g., `--allow-dirty`) MAY be implemented later; it is not required for v1.

### 5.2 Branch and Worktree Layout

Given the user launches a run from repo `R` at starting commit `BASE_SHA`:

1. Create run branch: `attractor/run/<run_id>` at `BASE_SHA`.
2. Create a worktree directory under `{logs_root}/worktree` (or equivalent) that checks out `attractor/run/<run_id>`.
3. All tool execution and all CLI backend execution uses that worktree as the working directory.

### 5.3 Node Checkpoint Commits

After each node completes:

- The engine MUST create a git commit on `attractor/run/<run_id>` capturing the worktree state.
- The commit MUST happen even if there are no file changes (empty commit) to preserve a uniform “one node = one commit” checkpoint invariant.
- The checkpoint commit SHA MUST be recorded in:
  - `{logs_root}/checkpoint.json` (Kilroy extension)
  - CXDB as a typed event turn

### 5.4 Resume Semantics (git + CXDB)

Resume MUST be possible from:

- `{logs_root}/checkpoint.json` (filesystem checkpoint)
- CXDB context head (trajectory state)
- git branch commit chain (code state)

Normative behavior:

- On resume, the engine MUST reset the worktree to the last checkpoint commit SHA.
- The next node to execute is derived from the checkpoint’s `current_node` per `attractor-spec.md`.
- If the previous hop depended on `full` fidelity, the first resumed node MUST downgrade to `summary:high` unless the backend proves it can restore the exact session/thread state.

## 6. Outcome and Status Canonicalization (Spec Gap Fix)

The upstream `attractor-spec.md` uses both uppercase pseudo-enums (e.g. `SUCCESS`) and lowercase strings (e.g. `success`) in different places. Kilroy MUST canonicalize to the condition-language semantics.

### 6.1 Canonical Outcome Status Values (Serialized)

All serialized outcomes (in `status.json`, checkpoints, CXDB events) MUST use:

- `success`
- `partial_success`
- `retry`
- `fail`
- `skipped` (rare; generally avoid in v1)

### 6.2 Internal Representation

Implementation MAY use an internal enum, but MUST map to/from the canonical lowercase strings.

### 6.3 Context Variable `outcome`

The context key used by edge conditions (`outcome`) MUST resolve to the canonical lowercase status string.

### 6.4 `status.json` Contract (Filesystem)

Every node execution MUST produce `{logs_root}/{node_id}/status.json` containing an `Outcome` serialized as JSON:

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

Rules:

- `status` MUST be one of the canonical values in Section 6.1.
- `failure_reason` MUST be non-empty when `status` is `fail` or `retry`.
- Handlers MAY omit optional fields, but the implementation MUST treat omitted optional fields as empty/zero values.
- The engine is authoritative for updating built-in routing context keys:
  - `context["outcome"] = outcome.status`
  - `context["preferred_label"] = outcome.preferred_label`

## 7. LLM Execution: API Backend vs CLI Backend

### 7.1 Provider Selection

Provider selection is resolved per node using `attractor-spec.md`’s stylesheet + attributes (`llm_provider`, `llm_model`, `reasoning_effort`).

Kilroy rule:

- If `llm_provider` is missing after stylesheet resolution, the run MUST fail (no provider auto-detection).

### 7.2 API Backend (Protocol Adapters)

When `backend(provider)=api`:

- Calls MUST use protocol-family adapters that implement `unified-llm-spec.md` interfaces.
- The Unified LLM interface is the host contract; provider-specific features flow through `provider_options` (per `unified-llm-spec.md`).

### 7.3 CLI Backend (Subprocess Agents)

When `backend(provider)=cli`:

- The node executes by spawning the configured provider CLI.
- The CLI runs in the run worktree (Section 5.2) so it can edit files directly.
- The engine MUST capture:
  - raw `stdout` and `stderr`
  - any JSON/JSONL event stream the CLI can emit
  - any session/restore files the CLI produces (copied into the stage directory)
- All captured artifacts MUST be persisted to CXDB.

**Tooling note (jj):**

- Captured JSON/JSONL MUST be stored in a `jj`-friendly shape:
  - raw streams as `events.ndjson` (one JSON object per line)
  - a materialized JSON array as `events.json` when feasible

#### 7.3.1 CLI Requirements (Integration + Observability)

For each provider CLI integration, the adapter MUST:

- Run non-interactively (no TTY dependency).
- Enable the most structured output mode available (JSON/JSONL event stream preferred).
- Persist “session restore” state when the CLI supports it (session IDs, resume flags, restore files).
- Capture enough information to replay the exact invocation:
  - executable path
  - argv
  - env allowlist used
  - working directory

If a CLI provides both a “final JSON output” and an “event stream”, Kilroy MUST capture both.

#### 7.3.2 CLI Adapters (Current Known Defaults)

This section documents *current known* headless interfaces for the three target CLIs, to guide adapter implementation. Adapters MUST remain configurable and SHOULD perform light capability detection (e.g., `--help` parsing) because CLIs evolve.

**OpenAI Codex CLI (`codex`)**

- Headless mode: `codex exec`
- Machine-readable events: `codex exec --json ...` emits newline-delimited JSON events to `stdout` (JSONL).
- Model override: `--model, -m`
- Working directory: `--cd, -C`
- Non-interactive execution:
  - `codex exec --json --sandbox workspace-write ...` is sufficient for non-interactive runs
  - `--skip-git-repo-check` is only required when running outside a trusted git repository
- Structured final output: `--output-schema <schema.json> -o <output.json>` writes final JSON output to a file.
- Resume: `codex exec resume [SESSION_ID]` or `codex exec resume --last [PROMPT]`

**Anthropic Claude Code (`claude`)**

- Headless mode: `claude --print` / `claude -p`
- Output formats: `--output-format text|json|stream-json`
- Resume: `--resume, -r <session_id>`
- Permissions/tools are CLI-controlled and MUST be set such that the run cannot block for interactive approvals (exact flags may vary by version).

**Google Gemini CLI (`gemini`)**

- Headless mode: `gemini --prompt ...` / `gemini -p ...`
- Output formats:
  - `--output-format json` for a single JSON object
  - `--output-format stream-json` for newline-delimited JSON events
- Model override: `--model, -m`
- Non-interactive approvals: `--yolo` and/or `--approval-mode auto_edit` (version-dependent)

### 7.4 Codergen Modes (Per-Node)

For nodes executed through the codergen handler:

- For `api` backend, Kilroy MUST support:
  - `one_shot`: a single LLM call, no tool loop
  - `agent_loop`: the full tool-using loop per `coding-agent-loop-spec.md`
- For `cli` backend, the effective mode is the CLI’s native agent behavior; `one_shot` is not required in v1.

Node selection mechanism:

- Add a Kilroy node attribute `codergen_mode` with values `one_shot|agent_loop`.
- Default for `api`: `agent_loop` (safer for coding tasks).

## 8. Observability Model (Events + CXDB Types)

Kilroy’s observability has two layers:

1. **Raw capture** of backend-native traces (CLI JSON, SDK request/response metadata).
2. A **normalized event stream** representing engine semantics (pipeline started, stage started, tool call, outcome, checkpoint commit, etc.).

### 8.1 CXDB as the Event Store

- Normalized events MUST be appended as CXDB turns, in order, under the run’s CXDB context.
- Large payloads (raw traces, transcripts, diffs, tarballs) SHOULD be stored as CXDB blobs and referenced by hash from turns.

### 8.2 Typed Turns and Registry

Kilroy MUST publish a CXDB type registry bundle that includes (at minimum) types for:

- `com.kilroy.attractor.RunStarted`
- `com.kilroy.attractor.RunCompleted`
- `com.kilroy.attractor.RunFailed`
- `com.kilroy.attractor.StageStarted`
- `com.kilroy.attractor.StageFinished` (includes canonical outcome status)
- `com.kilroy.attractor.ToolCall` / `ToolResult`
- `com.kilroy.attractor.Artifact` (content hash + logical name + MIME/type)
- `com.kilroy.attractor.GitCheckpoint` (commit SHA + node_id + status)
- `com.kilroy.attractor.CheckpointSaved` (filesystem checkpoint pointer + CXDB head)
- `com.kilroy.attractor.BackendTraceRef` (references raw CLI/SDK trace blobs)

The exact msgpack field tags are an implementation detail, but MUST follow CXDB rules:

- numeric tags, never reused
- versions increment monotonically

### 8.3 Artifact Persistence Mapping (Filesystem -> CXDB)

Kilroy persists artifacts twice:

1. **Filesystem** (human-friendly, per `attractor-spec.md` run directory layout)
2. **CXDB** (authoritative, unbounded, deduped)

Normative mapping rules:

- Each of these files MUST be stored as a CXDB blob and referenced by an `Artifact` turn:
  - `{logs_root}/checkpoint.json`
  - `{logs_root}/manifest.json` (if implemented)
  - `{logs_root}/final.json` (Kilroy-specific)
  - `{logs_root}/{node_id}/prompt.md`
  - `{logs_root}/{node_id}/response.md` (when applicable)
  - `{logs_root}/{node_id}/status.json`
- If a stage produces additional files (tool logs, CLI streams, session restore JSON), those MUST also be stored as blobs and referenced.

For convenience and portability, implementations SHOULD also store:

- a per-stage tarball `stage.tgz` (blob) of `{logs_root}/{node_id}/`
- a run tarball `run.tgz` (blob) of `{logs_root}/` (excluding `worktree/`)

## 9. Determinism and Ordering (Spec Gap Fixes)

### 9.1 Edge Selection Tie-Breaks

Kilroy MUST implement the `attractor-spec.md` edge selection algorithm and make tie-breaks stable:

1. Condition-matching edges (eligible `condition` evaluates `true`) win over unconditional edges.
2. Among eligible edges, higher `weight` wins.
3. If still tied, lexical order by `to_node` then by edge declaration order in the DOT source (stable parse order).

### 9.2 DOT Attribute Value Parsing

Kilroy MUST accept both quoted and unquoted values (already required by `attractor-spec.md` DoD). Where the DOT subset grammar and examples diverge, implement the DoD behavior.

### 9.3 Failure Routing Condition

Failure routing references `condition="outcome=fail"` (lowercase) per the condition language. The engine MUST use canonical lowercase statuses (Section 6).

## 10. Parallelism and Isolation

`attractor-spec.md` includes `parallel` and `fan_in`. Kilroy supports them with a deterministic, git-safe isolation model.

### 10.1 Parallel Branch Workspaces

- Each parallel branch MUST execute in an isolated git branch + worktree rooted at the parent’s current checkpoint commit.
- Each parallel branch MUST also fork the CXDB context so branch events form a true DAG.

### 10.2 Merging Results Back

Kilroy’s rule for code changes from parallel branches:

- If branches can modify code, the graph MUST include a `fan_in` node that selects a single “winner”.
- The `fan_in` node MUST fast-forward the main run branch to the winner branch head (ff-only); non-fast-forward merges are not supported in v1.
- Losing branches are retained as artifacts (their git head SHAs and CXDB context IDs are recorded) but are not merged.

## 11. CXDB Storage Policy (Local, Unbounded)

Because this system is trusted and local:

- There is no redaction requirement in v1.
- There is no retention/TTL requirement in v1.
- “Store everything” is the default behavior.

The implementation SHOULD still use CXDB’s blob CAS and avoid duplicating identical payloads.

## 12. Kilroy Definition of Done (Metaspec-Level)

An implementation satisfies this metaspec when:

- It passes the upstream `attractor-spec.md` DoD items relevant to parsing/execution/checkpoints.
- It can run a small graph end-to-end in a git repo and:
  - creates `attractor/run/<run_id>`
  - creates one commit per executed node
  - writes `{logs_root}` stage artifacts and a checkpoint
  - writes the normalized event stream to CXDB (one context per run)
  - persists raw backend traces and stage artifacts to CXDB blobs
- It supports both:
  - `api` backend for all three providers (SDK calls)
  - `cli` backend for all three providers (subprocess + JSON/JSONL capture)
