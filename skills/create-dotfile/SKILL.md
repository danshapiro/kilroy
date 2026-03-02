---
name: create-dotfile
description: Use when authoring or repairing Kilroy Attractor DOT graphs from requirements, with template-first topology, routing guardrails, and validator-clean output.
---

# Create Dotfile

## Scope

This skill owns DOT graph authoring and repair for Attractor pipelines.

In scope:
- Turning requirements/spec/DoD into a runnable `.dot` graph.
- Defining topology, node prompts, routing, model assignments, and validation behavior.
- Enforcing DOT-specific guardrails and validator compatibility.

Out of scope:
- Run config (`run.yaml` / `run.json`) authoring and backend policy details. Use `create-runfile` for that.

## Overview

Core principle:
- Prefer validated template topology over ad-hoc graph design.
- Compose prompt text from project evidence; do not copy stale boilerplate.
- Optimize for reliable execution and recoverability, not novelty.

Default topology source:
- `skills/create-dotfile/reference_template.dot`

Model defaults source:
- `skills/create-dotfile/preferences.yaml`

## Workflow

0. Fetch the current model list (required before writing any model_stylesheet).

Run:
    kilroy attractor modeldb suggest

Capture the output. Use ONLY the model IDs listed in the output. Do not use
model IDs from memory — they go stale. If the command is unavailable, default
to: `claude-sonnet-4.6` (anthropic), `gemini-3-flash-preview` (google),
`gpt-4.1` (openai).

1. Determine mode and hard constraints.
- If non-interactive/programmatic, do not ask follow-up questions.
- Extract explicit constraints (`no fanout`, model/provider requirements, deliverable paths).

2. Gather repo evidence.
- Read the authoritative spec/DoD sources if provided.
- Use repo docs and files to resolve ambiguity before making assumptions.

3. Choose topology from template first.
- Start from `reference_template.dot` for node shapes, routing, and loop structure.
- If user says `no fanout` or `single path`, remove fan-out/fan-in branch families.
- Fan-in semantics are shape-dependent:
  - `shape=tripleoctagon` (`parallel.fan_in`) uses `FanInHandler` winner selection/fast-forward semantics.
  - Converging branches into a `shape=box` node is a **manual merge handoff**: branch worktrees are passed to the LLM in that box node, and the prompt must instruct the node to manually inspect and merge branch outputs (including git-based workflows such as `git diff`, commit inspection, and merge/cherry-pick by branch `head_sha` when appropriate).
- **graph-level retry_target**: Set graph-level retry_target to the earliest node that
  preserves already-completed work on re-entry. For pipelines with an analysis or planning
  phase, point to plan_work or debate_consolidate so re-entry can reuse completed design
  docs and implementation files. Pointing retry_target at a specific implement_* node
  skips all sibling workers' output.

### Implementation Decomposition

- If the task involves implementing or porting a codebase estimated to exceed ~1,000 lines of new code, decompose the `implement` node into per-module fan-out nodes (e.g. `implement_core`, `implement_api`, `implement_data_layer`) with a `merge_implementation` synthesis node. Each module node targets a bounded deliverable (~200–500 lines). A single `implement` node for large codebases produces stub implementations that pass structural checks but deliver no functional behavior.
- Use parallel fan-out (multiple `implement_X` → `merge_implementation`) or sequential chain as appropriate. Each `implement_X` node writes to `.ai/runs/$KILROY_RUN_ID/module_X_impl.md` and commits the code. `merge_implementation` synthesizes integration points and resolves conflicts.
- Threshold: >1,000 estimated lines of new code → decompose. The cost of extra nodes is much lower than a stub implementation.

**Analyze-before-implement (required when porting or reading existing source):**
When the task involves translating, porting, or extending an existing codebase, insert a
dedicated analysis cluster BEFORE any implement_* nodes:

```
analyze_fanout (shape=component) → analyze_<module>×N (shape=box, auto_status=true)
  → merge_analysis (shape=box, auto_status=true) → [implement cluster]
```

Each analyze_<module> node reads specific source files and writes a compact design doc
to `.ai/runs/$KILROY_RUN_ID/design_<module>.md` (under 300 lines — spec only, no implementation code).
merge_analysis verifies all design docs exist and are cross-module consistent.
Only after merge_analysis succeeds does the pipeline proceed to implement_* nodes.
See `skills/create-dotfile/reference_template.dot` OPTIONAL comments for stub examples.

**auto_status=true assignment rules:**
- MUST: synthesis nodes — consolidate_*, merge_*, debate_*, review_consensus, postmortem
- SHOULD: all implement_*, analyze_* nodes in fan-outs — their primary
  completion signal is the deliverable file; auto_status removes redundant success writes
- NEVER on shape=diamond routing nodes or shape=parallelogram tool nodes

4. Set model/provider resolution in `model_stylesheet`.
- Ensure every `shape=box` node resolves provider + model via attrs or stylesheet.
- Keep backend choice (`cli` vs `api`) out of DOT; backend belongs in run config.
- `model_stylesheet` declarations **MUST** use semicolons to separate property-value pairs within each selector block. Omitting semicolons causes silent parsing failures where nodes resolve to no provider.
  - Correct: `* { llm_model: gemini-2.0-flash; llm_provider: google; }`
  - Wrong: `* { llm_model: gemini-2.0-flash llm_provider: google }` — space-separated declarations silently fail.
- After writing `model_stylesheet`: verify each `{}` block uses only semicolon-terminated declarations; no two property names appear adjacent without a semicolon separator.
- **Anthropic model IDs use dot-separated version numbers**: `claude-opus-4.6`, `claude-sonnet-4.6`, `claude-haiku-4.5`. Never use dashes in the version suffix — `claude-opus-4-6` is wrong and will cause a validation ERROR. The three current canonical Anthropic IDs are: `claude-opus-4.6`, `claude-sonnet-4.6`, `claude-haiku-4.5`.

## Model Constraint Contract (Required)

- Treat explicit user model/provider directives as hard constraints.
- For explicit fan-out mappings, keep branch-to-model assignments one-to-one; do not reorder branches or merge assignments.
- Canonicalize provider aliases for DOT keys: `gemini`/`google_ai_studio` -> `google`, `z-ai`/`z.ai` -> `zai`, `moonshot`/`moonshotai` -> `kimi`, `minimax-ai` -> `minimax`.
- Resolve explicit model IDs against local evidence in this order:
1. exact user-provided ID (if already canonical),
2. `internal/attractor/modeldb/pinned/openrouter_models.json`,
3. `internal/attractor/modeldb/manual_models.yaml` (if present),
4. `skills/shared/model_fallbacks.yaml` (backup only when other sources fail).
- Never silently downgrade or substitute an explicit model request with a different major/minor family (example: requested `glm-5` must not become `glm-4.5`).
- If exact canonical resolution is unavailable, preserve the user-requested model literal in `llm_model` (normalize whitespace only) instead of guessing a nearby model.
- Apply known alias normalization from the fallback file before deciding unresolved status (for example: `glm-5.0` -> `glm-5` for provider `zai`).
- Explicit user model/provider directives override `skills/create-dotfile/preferences.yaml` defaults.

5. Compose node prompts and handoffs.
- Every `shape=box` prompt must include both `$KILROY_STAGE_STATUS_PATH` and `$KILROY_STAGE_STATUS_FALLBACK_PATH`.
- IMPORTANT: `auto_status=true` suppresses writing `status=success` on normal completion. It does NOT remove the requirement to include `$KILROY_STAGE_STATUS_PATH` and `$KILROY_STAGE_STATUS_FALLBACK_PATH` in the prompt — those paths are still required for failure-case writes. For auto_status nodes, phrase it as:
  ```
  "Write to $KILROY_STAGE_STATUS_PATH (fallback: $KILROY_STAGE_STATUS_FALLBACK_PATH)
   ONLY on failure: {"status":"fail","failure_reason":"...","failure_class":"..."}"
  ```
  Omitting the paths from auto_status node prompts causes `status_contract_in_prompt` warnings.
- Require explicit success/fail/retry behavior. For fail/retry, `failure_reason` and `failure_class` are **required** — not optional. Missing `failure_class` defaults to `deterministic`, which is tracked by the cycle breaker. Silently omitting it makes the breaker behavior unpredictable.
- For any node whose failure prose may vary between retries (filenames, line numbers, counts, AC lists), also set `failure_signature` to a stable short key: `"failure_signature":"compile_errors"`. This prevents the same root cause from appearing as distinct signatures and defeating the cycle breaker. See **Cycle Detection Contract** section below.
- Keep `.ai/runs/$KILROY_RUN_ID/*` producer/consumer paths exact; no filename drift.
- Scratch artifacts are run-scoped only: use `.ai/runs/$KILROY_RUN_ID/...` everywhere. Root `.ai` is not implicitly ingested.
- **`max_tokens` (output token cap, default 32768):** Every provider adapter defaults to 32768 output
  tokens per response. This is a *per-response generation cap* — completely separate from the model's
  input context window. For nodes that write large files (full source modules, long docs), leave this
  at the default or increase it. For classification-only or short-answer nodes, reducing it is fine.
  Set it explicitly when you want deterministic behavior regardless of provider defaults:
  ```dot
  implement [shape=box, max_agent_turns=300, max_tokens=32768, prompt="..."]
  ```
  **Failing to account for `max_tokens`** on code-generating nodes is the most common silent failure
  mode: the model generates a large write_file call, hits the cap, Gemini/Anthropic return an empty
  or truncated response, and Kilroy interprets the session as cleanly ended (`auto_status=true`
  writes `{"status":"success"}`), producing an infinite do-nothing loop.
- `shape=parallelogram` nodes must use `tool_command`.
- For compiled or packaged deliverables (executables, libraries, modules, services, containers, bundles): the verification node MUST validate the expected runtime behavior or interface contract — not just file existence or a successful build exit code.
- Add a domain-specific runtime validation node when needed (for example `verify_runtime`, `verify_api_contract`, `verify_cli_behavior`, `verify_ui_smoke`). Use checks that prove the deliverable actually works for the intended use case.
- A stub artifact can compile and still be functionally empty; require contract-level verification (exports, endpoints, CLI behavior, or observable outputs) to catch this.
- For browser/UI verification, prefer explicit verify node names (for example `verify_browser`, `verify_e2e`) and runner commands (`playwright test`, `cypress run`, `npm run e2e`) so intent is unambiguous.
- If browser verification intent is wrapped/ambiguous (for example custom shell wrapper), set `collect_browser_artifacts=true` on that verify node.
- The verify_fidelity prompt MUST enumerate acceptance criteria by numbered ID (AC1, AC2, ...), map each to the specific output file(s) that implement it:
  ```
  AC1: src/auth.py   — verify token issuance and expiry behaviour
  AC2: src/storage.py — verify data persistence across restart
  ```
  Use these IDs in the failure_signature field so postmortem can reference failing ACs precisely and the next verify pass can re-check only those criteria.
- **Verify nodes must call runtime-authored scripts, not hardcode tool invocations.** Any `shape=parallelogram` verify node whose pass/fail depends on decisions made by implementation nodes — package manager, build system, directory layout, language runtime — MUST use `tool_command="sh scripts/validate-{stage}.sh"` rather than inline tool calls. The implementation node responsible for project scaffolding MUST write `scripts/validate-build.sh`, `scripts/validate-fmt.sh`, and `scripts/validate-test.sh` as committed deliverables. Scripts MUST open with `#!/bin/sh` and MUST NOT assume any runtime beyond what `check_toolchain` confirmed. Rationale: ingest-time `tool_command` strings embed assumptions about structure the implementation loop has not yet made; when the loop's choices diverge — different directory names, package managers, or build systems — the verify node fails deterministically and no postmortem routing can repair it.
- **Test evidence contract (required when DoD defines integration scenarios):** `scripts/validate-test.sh` MUST write deterministic evidence artifacts under `.ai/runs/$KILROY_RUN_ID/test-evidence/latest/` and produce `.ai/runs/$KILROY_RUN_ID/test-evidence/latest/manifest.json` mapping each `IT-*` scenario ID to artifact paths/types and pass/fail status. UI scenarios require screenshot evidence; non-UI scenarios require text/structured evidence. Do not require a specific test framework — require artifact outcomes.
- **Failure-path evidence durability:** `scripts/validate-test.sh` MUST emit best-effort evidence and a manifest entry even when tests fail (for example command exits non-zero). Missing evidence must be explicit in manifest fields so postmortem can diagnose incomplete capture as a finding.
- **Artifact verification gate:** ensure `verify_artifacts` checks that manifest scenario IDs match the DoD integration scenarios and that each scenario satisfies required artifact types (for example screenshots for UI scenarios) before semantic review proceeds.

6. Enforce routing guardrails.
- Do not bypass actionable outcomes with unconditional pass-through edges.
- For nodes with conditional edges, include one unconditional fallback edge.
- Use only supported condition operators: `=`, `!=`, `&&`.
- Use `loop_restart=true` only for `context.failure_class=transient_infra`.
- The `postmortem` node **MUST** have at least three condition-keyed outbound edges covering distinct outcome classes (e.g. `impl_repair`, `needs_replan`, `needs_toolchain` or equivalents for the task domain) **before** the unconditional fallback. A `postmortem` with only one unconditional edge is invalid — it prevents recovery classification from routing differently and collapses all failure modes into a single path.
- The unconditional fallback from `postmortem` MUST come last among its outbound edges.
- **Postmortem progress detection (required):** The `postmortem` prompt MUST compare the current failing AC set against the previous iteration's failing AC set (stored in context key `last_failing_acs`). If the sets are identical — zero progress — the postmortem MUST route `needs_replan`, not `impl_repair`. The default `impl_repair` applies ONLY on the first occurrence of a failure. Identical repeated failures are a signal that the implementation approach is wrong, not that another repair pass will help. Add `loop_restart_persist_keys="last_failing_acs"` to the graph attrs so this key survives loop restarts.
- **Implement pre-exit verification (required on repair passes):** The `implement` prompt MUST require that on any repair pass (when `.ai/runs/$KILROY_RUN_ID/postmortem_latest.md` or `.ai/runs/$KILROY_RUN_ID/review_consensus.md` exists), the node runs `./scripts/validate-build.sh` and re-reads each targeted file to confirm the fix is present before exiting. Silent exit (auto-success) on a repair pass without self-verification is the primary cause of no-progress cycles.
- **Postmortem evidence usage (required):** The `postmortem` prompt MUST read `.ai/runs/$KILROY_RUN_ID/test-evidence/latest/manifest.json` when present. For each failed or suspicious `IT-*` scenario, it MUST read listed artifacts that can materially improve diagnosis and cite artifact file paths in `.ai/runs/$KILROY_RUN_ID/postmortem_latest.md`. It may skip an artifact only with an explicit reason (missing/unreadable/not produced), which becomes a finding.

## Cycle Detection Contract (Required)

The engine tracks a `map[signature]int` where **signature = `nodeID|failureClass|normalizedReason`**. When the same (node, class, reason) tuple appears `loop_restart_signature_limit` times (default 3), the run aborts with "deterministic failure cycle detected." Understanding this mechanism is required to design pipelines that terminate correctly without false-positive aborts.

**What counts toward the limit:**
- Only `status=fail` or `status=retry` outcomes enter the tracker. Custom non-fail statuses (`more_work`, `impl_repair`, `needs_revision`, etc.) do **not** — see the non-fail loop hole below.
- Only `failure_class=deterministic` and `failure_class=structural` are tracked. `transient_infra` is excluded (it routes through the `loop_restart` circuit breaker instead). Any unrecognized or missing `failure_class` defaults to `deterministic` and is tracked.
- **Signatures never reset on success.** If `implement` succeeds 10 times but `verify_build` fails with the same signature 5 times across those cycles, the breaker fires on the 5th hit. Routing through postmortem does not reset the map either — postmortem's own outcome is non-fail (`impl_repair` etc.) and is invisible to the tracker.

**The `failure_signature` field stabilizes the reason component:**
By default, `failure_reason` is normalized (lowercase, hex→`<hex>`, digits→`<n>`, comma-spaces collapsed) to form the reason component of the signature. For any node that emits variable failure prose, set an explicit stable key:
```json
{
  "status": "fail",
  "failure_reason": "cargo build returned 47 errors in src/lib.rs line 203...",
  "failure_signature": "compile_errors",
  "failure_class": "deterministic_agent_bug"
}
```
The `failure_signature` value replaces `failure_reason` as the reason component. This collapses all "compile error" variants into one counter regardless of error count, line numbers, or message wording.

Set `failure_signature` on:
- All verify/check nodes whose failure enumerates file paths, AC IDs, or counts.
- Merge/synthesis nodes whose failure lists missing files (use `"missing_files"` or `"missing_design_docs"`).
- Any node where the same root cause may appear with varying wording across retries.
- Any node where you want to intentionally distinguish two different failure modes: use separate stable keys like `"compile_errors"` vs `"missing_wasm_exports"`.

**The non-fail `loop_restart` hole:**
`loop_restart=true` edges carrying a custom non-fail status (e.g. `outcome=more_work`) bypass the engine's signature cycle breaker entirely. The only protections are the LLM-side pass counter and `max_restarts` (default 50). For every non-fail `loop_restart` cycle, the prompting node MUST:
1. Maintain a pass counter in a scratch file (e.g. `.ai/runs/$KILROY_RUN_ID/work_pass.txt`)
2. Increment it on each execution
3. Emit `status=fail, failure_class=deterministic_agent_bug` when the counter exceeds N (recommended: 5–10)

This converts the stall into a `fail` that the signature cycle breaker can then accumulate and trip on. This is the only engine-enforced backstop for `more_work`-style loops.

**Setting `loop_restart_signature_limit`:**
The default is 3. For pipelines with a multi-pass repair loop (implement → verify → postmortem → implement), 3 may abort legitimate repair attempts. Recommended values:
- Simple linear pipelines: default (3)
- Pipelines with 1–2 repair passes expected: 4–5
- Pipelines with postmortem → plan_work → implement repair cycles: 5
- Always set explicitly in graph attrs so the intent is visible:
  ```
  graph [loop_restart_signature_limit=5, loop_restart_persist_keys="last_failing_acs", ...]
  ```

7. Preserve authoritative text contracts.
- If user explicitly provides goal/spec/DoD text, keep it verbatim (DOT-escape only).
- `expand_spec` must include the full user input verbatim in a delimited block.

8. Validate and repair before emit.
- Verify no unresolved placeholders (`DEFAULT_MODEL`, etc.).
- Run syntax + semantic validation loops, applying minimal fixes until clean.
- A PostToolUse hook (`skills/create-dotfile/hooks/validate-dot.sh`) runs automatically
  after every Write, Edit, or MultiEdit to a `.dot` file. It calls
  `kilroy attractor validate --graph` and, if issues are found, signals Claude Code
  via exit 2 + stderr so the feedback is injected into your context. If feedback
  appears, repair the reported issues immediately and re-write the file. No manual
  validate invocation is needed during ingest sessions.
- The hook requires `kilroy` in PATH. The `KILROY_CLAUDE_PATH` environment variable
  can override the binary location (full path or directory containing `kilroy`).

## Non-Negotiable Guardrails

- Programmatic output is DOT only (`digraph ... }`), no markdown fences or sentinel text.
- `shape=diamond` nodes route outcomes only; do not attach execution prompts.
- Keep prerequisite/tool gates real: route success/failure explicitly.
- Add deterministic checks for explicit deliverable paths named in requirements.
- Include a stable `failure_signature` on any node whose `failure_reason` may vary between retries — not just verify stages. Merge nodes listing missing files, plan nodes, analysis nodes, and any node whose error prose contains counts or filenames all need a stable key. See **Cycle Detection Contract** for the full guidance.
- **Never** instruct any `shape=box` node to write `status: retry`. It is reserved by the attractor and triggers `deterministic_failure_cycle_check`, which downgrades to `fail` after N attempts. For iteration/revision loops, use a custom outcome: e.g. `{"status": "needs_revision"}` routed via `condition="outcome=needs_revision"` edge.
- **Never** instruct `review_consensus` (or any review/gate node) to write `status: fail` for a rejection verdict. Write a custom outcome instead: e.g. `{"status": "rejected"}`. `status: fail` triggers failure processing and blocks `goal_gate=true` re-execution. Route rejection via `condition="outcome=rejected"`.
- **Never** write `{"status":"success","outcome":"..."}` in a status JSON — the `outcome` key is silently ignored by the runtime decoder; only `status` drives edge condition matching. Custom routing always uses the `status` field: `{"status":"more_work"}` matches `condition="outcome=more_work"`, not `{"status":"success","outcome":"more_work"}`.
- **Never use DOT/Graphviz reserved keywords as node IDs**: `if`, `node`, `edge`, `graph`, `digraph`, `subgraph`, `strict`. These cause routing failures — the DOT parser interprets them as language keywords rather than node names.
- **Every `goal_gate=true` node must declare its own `retry_target`** pointing to the appropriate recovery node (typically `postmortem`). The graph-level `retry_target` is for transient node failures and is not an appropriate retry path for a failed review/gate consensus. Example: `review_consensus [auto_status=true, goal_gate=true, retry_target="postmortem"]`.
- **Never embed runtime assumptions in verify `tool_command` fields.** Package manager invocations (`npm`, `cargo`, `pip`, `go build`), directory paths (`server/`, `client/`, `backend/`), and build tool flags MUST NOT appear directly in a verify node's `tool_command`. The only permitted form is `tool_command="sh scripts/validate-{stage}.sh"`. Violation causes deterministic cycle failures that postmortem cannot route out of: the script is hardcoded at ingest time, the implementation loop makes different structural choices at runtime, and every retry re-runs the same broken command against the same wrong structure until the cycle limit aborts the run.
- **Every `sh scripts/validate-*.sh` call MUST include a `KILROY_VALIDATE_FAILURE` fallback.** When a validate script is missing, crashes before printing output, or returns non-zero without diagnostic text, postmortem receives no repair signal and cannot distinguish a missing script from a genuine build failure. The canonical `tool_command` form is:
  ```
  tool_command="sh scripts/validate-{stage}.sh || { echo 'KILROY_VALIDATE_FAILURE: validate-{stage}.sh missing or failed — postmortem must write scripts/validate-{stage}.sh'; exit 1; }"
  ```
  Within each `scripts/validate-{stage}.sh` authored by implementation nodes, include a POSIX sh failure trap as the first executable line:
  ```sh
  #!/bin/sh
  set -e
  trap 'echo "KILROY_VALIDATE_FAILURE: validate-{stage}.sh crashed at line $LINENO — postmortem must repair scripts/validate-{stage}.sh"' EXIT
  # ... stage-specific checks ...
  trap - EXIT
  ```
  The `KILROY_VALIDATE_FAILURE:` prefix is the linter-enforced token. The validator rule `validate_script_failure_contract` fires a warning when `sh scripts/validate-*.sh` appears in `tool_command` without this token.
- **Never treat `verify_artifacts` as optional when a DoD defines test evidence.** If `.ai/runs/$KILROY_RUN_ID/test-evidence/latest/manifest.json` is missing, scenario IDs are incomplete, or required artifact types are absent, route to failure/postmortem with a stable `failure_signature` (for example `missing_test_evidence`) instead of proceeding.

## References

- `docs/strongdm/attractor/ingestor-spec.md`
- `docs/strongdm/attractor/attractor-spec.md`
- `docs/strongdm/attractor/coding-agent-loop-spec.md`
- `skills/create-dotfile/reference_template.dot`
- `skills/create-dotfile/preferences.yaml`
- `skills/shared/model_fallbacks.yaml`
- `internal/attractor/modeldb/pinned/openrouter_models.json`
- `internal/attractor/modeldb/manual_models.yaml`
