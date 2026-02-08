# Attractor Reliability Architecture Completion Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Make Attractor run/resume behavior architecturally correct and idiomatic by enforcing hard invariants across orchestration, provider execution, retries, restart policy, and terminal observability.

**Architecture:** Treat reliability as a first-class contract across `runtime`, `engine`, `provider adapters`, and CLI launcher surfaces. Enforce deterministic/transient classification at the same layer where retries and loop restarts are decided; centralize bootstrap state so run/resume code paths cannot diverge; and require terminal artifacts (`final.json`) on every fatal path. Persist enough structured state to make resume mathematically equivalent to uninterrupted execution.

**Tech Stack:** Go (`testing`, integration tests), existing Attractor engine/runtime test suite, CXDB test server harness, detached CLI subprocess tests, real-CXDB smoke validation.

---

## 1. Baseline Evidence (Current Incident)

Use these artifacts as canonical incident evidence for this plan:

- Run root: `/tmp/kilroy-dttf-real-cxdb-20260208T171236Z-postfix`
- Terminal status: `/tmp/kilroy-dttf-real-cxdb-20260208T171236Z-postfix/logs/final.json`
- Timeline and retry events: `/tmp/kilroy-dttf-real-cxdb-20260208T171236Z-postfix/logs/progress.ndjson`
- Loop-restart block signal: `/tmp/kilroy-dttf-real-cxdb-20260208T171236Z-postfix/logs/live.json`
- Parallel branch outcomes: `/tmp/kilroy-dttf-real-cxdb-20260208T171236Z-postfix/logs/par_tracer/parallel_results.json`
- Checkpoint state (restart/signature/branch context): `/tmp/kilroy-dttf-real-cxdb-20260208T171236Z-postfix/logs/checkpoint.json`
- Provider-specific failure evidence:
  - Anthropic: `/tmp/kilroy-dttf-real-cxdb-20260208T171236Z-postfix/logs/parallel/par_tracer/01-impl_tracer_a/impl_tracer_a/stderr.log`
  - Gemini: `/tmp/kilroy-dttf-real-cxdb-20260208T171236Z-postfix/logs/parallel/par_tracer/02-impl_tracer_b/impl_tracer_b/stderr.log`
  - Codex: `/tmp/kilroy-dttf-real-cxdb-20260208T171236Z-postfix/logs/parallel/par_tracer/03-impl_tracer_c/impl_tracer_c/stderr.log`

This incident confirms prior orchestration fixes work (branch-prefix, absolute state-root, terminal finalization, loop-restart blocking), while exposing remaining provider/preflight/retry-classification gaps.

---

## 2. Detailed Requirement Register (R001-R090)

Each requirement includes implementation intent, primary references, and verification expectation.

### 2.1 Runtime Contract And Artifact Correctness

- `R001` Define canonical JSON contracts for `status.json`, `checkpoint.json`, `progress.ndjson`, `final.json`.  
  Refs: `internal/attractor/runtime/status.go`, `internal/attractor/runtime/checkpoint.go`, `internal/attractor/runtime/final.go`.  
  Verify: schema-level tests for required fields and compatibility.

- `R002` Every failed outcome must include non-empty `failure_reason`.  
  Refs: `internal/attractor/runtime/status.go`, `internal/attractor/engine/engine.go`.  
  Verify: failure-path tests across run/resume.

- `R003` Every failure must carry normalized `failure_class` and `failure_signature` when retry/loop logic applies.  
  Refs: `internal/attractor/engine/loop_restart_policy.go`.  
  Verify: classification/signature unit tests.

- `R004` Define a closed failure-class enum (no unbounded strings).  
  Refs: `internal/attractor/engine/loop_restart_policy.go`.  
  Verify: compile-time constants + parser tests.

- `R005` Progress stream must be append-only, ordered, and replay-safe for timeline reconstruction.  
  Refs: `internal/attractor/engine/progress.go`.  
  Verify: order/idempotence tests.

- `R006` Every stage must persist invocation + outcome artifacts (`cli_invocation.json`, `status.json`, stdout/stderr, timing).  
  Refs: `internal/attractor/engine/codergen_router.go`, `internal/attractor/engine/handlers.go`.  
  Verify: stage artifact existence tests.

- `R007` Fatal run/resume exits must always persist terminal `final.json`.  
  Refs: `internal/attractor/engine/engine.go`, `internal/attractor/engine/resume.go`, `internal/attractor/runtime/final.go`.  
  Verify: fatal-path tests.

- `R008` Terminal `final.json` must include `status`, `run_id`, `final_git_commit_sha`, `failure_reason`, timestamp, CXDB IDs (when available).  
  Refs: `internal/attractor/runtime/final.go`, `internal/attractor/engine/cxdb_events.go`.  
  Verify: integration assertions.

- `R009` `live.json` must always reflect the last terminal-significant event.  
  Refs: `internal/attractor/engine/engine.go`.  
  Verify: event sequencing tests.

- `R010` `run.out` must contain concise terminal reason lines suitable for operators.  
  Refs: `cmd/kilroy/run_detach.go`, `internal/attractor/engine/engine.go`.  
  Verify: detached run integration test.

### 2.2 Bootstrap, Run/Resume Invariants, And Path Canonicalization

- `R011` Use a single engine bootstrap path for run/resume option hydration.  
  Refs: `internal/attractor/engine/engine_bootstrap.go`, `internal/attractor/engine/run_with_config.go`, `internal/attractor/engine/resume.go`.  
  Verify: parity tests between run and resume.

- `R012` Persisted `logs_root`, `worktree`, `state_root`, `base_logs_root` must always be absolute.  
  Refs: `internal/attractor/engine/resume.go`, `internal/attractor/engine/codergen_router.go`.  
  Verify: absolute-path tests.

- `R013` Resume must reject relative persisted path state.  
  Refs: `internal/attractor/engine/resume.go`.  
  Verify: negative tests.

- `R014` Resume from `restart-N` directory must recover correct base logs root.  
  Refs: `internal/attractor/engine/resume.go`, `internal/attractor/engine/resume_from_restart_dir_test.go`.  
  Verify: restart-dir resume tests.

- `R015` Restart bookkeeping (`base_logs_root`, `restart_count`) must be persisted and restored from checkpoint.  
  Refs: `internal/attractor/engine/engine.go`, `internal/attractor/engine/resume.go`.  
  Verify: checkpoint round-trip tests.

- `R016` Restart failure signatures must be checkpoint-persisted/restored across resume.  
  Refs: `internal/attractor/engine/engine.go`, `internal/attractor/engine/resume.go`.  
  Verify: signature continuity tests.

- `R017` Resume must preserve branch prefix invariants exactly as run mode.  
  Refs: `internal/attractor/engine/resume.go`, `internal/attractor/engine/branch_names.go`.  
  Verify: resume parallel branch tests.

- `R018` Resume must fail closed when critical bootstrap invariants cannot be reconstructed.  
  Refs: `internal/attractor/engine/resume.go`.  
  Verify: bootstrap failure tests + fallback finalization.

### 2.3 Git Branch/Ref Correctness

- `R019` All run branch names must be built via canonical helper, never ad hoc concatenation.  
  Refs: `internal/attractor/engine/branch_names.go`, `internal/attractor/engine/engine.go`.  
  Verify: helper unit tests.

- `R020` All parallel branch names must be built via canonical helper and include configured prefix.  
  Refs: `internal/attractor/engine/parallel_handlers.go`, `internal/attractor/engine/branch_names.go`.  
  Verify: parallel branch prefix tests.

- `R021` `run_branch_prefix` must be normalized (trim, remove leading/trailing slash ambiguity).  
  Refs: `internal/attractor/engine/branch_names.go`, `internal/attractor/engine/config.go`.  
  Verify: normalization tests.

- `R022` Every computed ref must be validated before git operations.  
  Refs: `internal/attractor/gitutil/git.go`, `internal/attractor/engine/parallel_handlers.go`.  
  Verify: invalid ref rejection tests.

- `R023` No branch may ever resolve to `/parallel/...` or empty-prefix malformed refs.  
  Refs: `internal/attractor/engine/resume_parallel_branch_prefix_test.go`.  
  Verify: direct regression test.

### 2.4 Graph And Config Validation

- `R024` Run must validate graph schema and edge attributes before execution.  
  Refs: `internal/attractor/validate/validate.go`, `internal/attractor/engine/run_with_config.go`.  
  Verify: validation integration tests.

- `R025` Validate `loop_restart`, `max_restarts`, signature limits at bootstrap.  
  Refs: `internal/attractor/engine/loop_restart_policy.go`.  
  Verify: invalid-config tests.

- `R026` Validate provider/backend map completeness for providers referenced in graph.  
  Refs: `internal/attractor/engine/run_with_config.go`.  
  Verify: preflight config tests.

- `R027` Validate model catalog path/policy and loadability before stage execution.  
  Refs: `internal/attractor/modeldb/litellm_resolve.go`, `internal/attractor/engine/run_with_config.go`.  
  Verify: catalog load tests.

### 2.5 Provider/Model Preflight And Adapter Contracts

- `R028` Validate provider-model compatibility against pinned catalog before run starts.  
  Refs: `internal/attractor/engine/run_with_config.go`, `internal/attractor/engine/provider_preflight_test.go`.  
  Verify: provider preflight tests.

- `R029` Add provider binary capability preflight (binary exists, minimum CLI contract).  
  Refs: `internal/attractor/engine/codergen_router.go`.  
  Verify: capability preflight tests with fake PATH.

- `R030` Validate provider/model availability for actually used provider/model pairs using deterministic sources first (pinned catalog/runtime mapping); optional online probes must be best-effort and class-aware.  
  Refs: `internal/attractor/engine/run_with_config.go`, provider adapters under `internal/llm/providers/*`, and CLI adapters under `internal/attractor/engine/codergen_router.go`.  
  Verify: preflight tests for deterministic mismatch plus transient probe outage.

- `R031` Persist preflight report artifact in logs root.  
  Refs: `internal/attractor/engine/run_with_config.go`.  
  Verify: report artifact test.

- `R032` Abort run on preflight failure with class-aware outcomes: deterministic for config/contract/model-unavailable violations; transient-infra for probe transport/network/timeouts.  
  Refs: `internal/attractor/engine/run_with_config.go`, `internal/attractor/engine/loop_restart_policy.go`.  
  Verify: class-aware preflight failure tests.

- `R033` Standardize provider adapter error envelope (code/class/message/retryability).  
  Refs: `internal/attractor/engine/codergen_router.go`, provider adapters.  
  Verify: adapter contract tests.

- `R034` Map Anthropic CLI contract mismatch errors to deterministic provider-contract failures.  
  Evidence: `.../parallel/.../01-impl_tracer_a/.../stderr.log`.  
  Verify: classifier tests.

- `R035` Map Gemini `ModelNotFoundError` to deterministic provider-model-unavailable failure class.  
  Evidence: `.../parallel/.../02-impl_tracer_b/.../stderr.log`.  
  Verify: classifier tests.

- `R036` Map transport/network/timeout failures to transient-infra class at the adapter boundary; string matching is fallback-only for legacy unclassified errors.  
  Refs: `internal/attractor/engine/codergen_router.go`, `internal/attractor/engine/loop_restart_policy.go`.  
  Verify: precedence tests (explicit class wins, fallback only when class missing).

- `R037` Preserve Codex state-db discrepancy fallback metadata and retry state-root traceability.  
  Evidence: `.../03-impl_tracer_c/.../cli_invocation.json`.  
  Verify: fallback metadata tests.

- `R038` Provider adapter retries must be bounded and class-aware.  
  Refs: `internal/attractor/engine/codergen_router.go`.  
  Verify: retry bound tests.

### 2.6 Retry Semantics And Loop-Restart Policy

- `R039` Stage retry policy must be class-aware (default: transient only).  
  Refs: `internal/attractor/engine/engine.go`.  
  Verify: stage retry gating tests.

- `R040` Deterministic stage failures should short-circuit retries unless explicitly configured.  
  Refs: `internal/attractor/engine/engine.go`.  
  Verify: deterministic retry short-circuit tests.

- `R041` Loop restart only allowed for `failure_class=transient_infra`.  
  Refs: `internal/attractor/engine/loop_restart_policy.go`, `internal/attractor/engine/engine.go`.  
  Verify: loop restart blocked tests.

- `R042` Loop restart must emit explicit block event when class is deterministic.  
  Refs: `internal/attractor/engine/engine.go`.  
  Verify: progress event tests.

- `R043` Restart failure signature normalization must be deterministic and stable.  
  Refs: `internal/attractor/engine/loop_restart_policy.go`.  
  Verify: signature normalization tests.

- `R044` Deterministic-failure circuit breaker must abort early on repeated signature threshold.  
  Refs: `internal/attractor/engine/engine.go`, `internal/attractor/engine/loop_restart_policy.go`.  
  Verify: circuit-breaker tests.

- `R045` Signature threshold must be configurable and validated.  
  Refs: `internal/attractor/engine/loop_restart_policy.go`.  
  Verify: policy bound tests.

- `R046` Circuit breaker state must survive resume and restart boundaries.  
  Refs: `internal/attractor/engine/resume.go`, `internal/attractor/engine/resume_loop_restart_state_test.go`.  
  Verify: resume state tests.

- `R047` `max_restarts` must be enforced before creating new restart directories.  
  Refs: `internal/attractor/engine/engine.go`.  
  Verify: restart-limit tests.

- `R048` No deterministic loop may produce `restart-*` storm.  
  Evidence baseline: absence of restart dirs in `.../logs`.  
  Verify: e2e guardrail matrix.

### 2.7 Parallel/Join Reliability

- `R049` Parallel fanout must isolate per-branch worktree + state root.  
  Refs: `internal/attractor/engine/parallel_handlers.go`.  
  Verify: branch artifact isolation tests.

- `R050` Parallel result records must include branch ref, logs path, worktree path, outcome class/signature, CXDB IDs.  
  Refs: `internal/attractor/engine/parallel_handlers.go`, `.../par_tracer/parallel_results.json`.  
  Verify: parallel result schema tests.

- `R051` Join node must classify aggregate outcome from branch outcomes.  
  Refs: `internal/attractor/engine/handlers.go` (join logic).  
  Verify: join classification tests.

- `R052` Join retry must be class-aware; no blind retries when all branches are deterministic failures.  
  Refs: `internal/attractor/engine/engine.go`.  
  Verify: join retry policy tests.

- `R053` Parallel failure evidence must remain inspectable without decompressing archives.  
  Refs: branch logs under `logs/parallel/...`.  
  Verify: artifact presence tests.

### 2.8 Detached Lifecycle And CLI Ergonomics

- `R054` Detached execution (`--detach`) must be first-class and documented.  
  Refs: `cmd/kilroy/main.go`, `cmd/kilroy/run_detach.go`, `docs/strongdm/attractor/README.md`.  
  Verify: CLI tests + docs check.

- `R055` Detached launcher must detach process group (`setsid`) and release child handle.  
  Refs: `cmd/kilroy/run_detach.go`.  
  Verify: process lifecycle tests.

- `R056` Detached launcher must persist `run.pid` and append to `run.out`.  
  Refs: `cmd/kilroy/run_detach.go`.  
  Verify: detached pid/log tests.

- `R057` Detached launcher output must be machine-parseable (`detached=true`, `logs_root`, `pid_file`).  
  Refs: `cmd/kilroy/main.go`.  
  Verify: CLI integration tests.

- `R058` Detached tests must wait for child PID exit to avoid tempdir cleanup race.  
  Refs: `cmd/kilroy/main_detach_test.go`.  
  Verify: flake-resistant test runs.

### 2.9 Finalization, CXDB, And Terminal Consistency

- `R059` On fatal outcomes, terminal finalization must happen exactly once semantically (idempotent writes allowed).  
  Refs: `internal/attractor/engine/engine.go`, `internal/attractor/runtime/final.go`.  
  Verify: idempotence tests.

- `R060` When restart root differs, mirror terminal `final.json` to base logs root.  
  Refs: `internal/attractor/engine/engine.go`, `internal/attractor/engine/resume.go`.  
  Verify: restart-root finalization tests.

- `R061` Fatal pre-engine bootstrap resume failures must still write fallback finalization.  
  Refs: `internal/attractor/engine/resume.go`.  
  Verify: resume bootstrap fatal tests.

- `R062` CXDB terminal turn IDs must be persisted into final outcome when available.  
  Refs: `internal/attractor/engine/cxdb_events.go`, `internal/attractor/runtime/final.go`.  
  Verify: CXDB integration tests.

### 2.10 Archival And Sensitive State Hygiene

- `R063` Stage/run archives must exclude sensitive state roots (`codex-home*`, `.codex/auth.json`, `.codex/config.toml`).  
  Refs: `internal/attractor/engine/archive.go`.  
  Verify: archive content tests.

- `R064` Archive exclusion rules must be stable across restart and parallel subroots.  
  Refs: `internal/attractor/engine/archive.go`.  
  Verify: restart/parallel archive tests.

### 2.11 Observability And Operator UX

- `R065` Progress events must include retry sleep, attempt index, node ID, class where applicable.  
  Refs: `internal/attractor/engine/progress.go`, `engine.go`.  
  Verify: event contract tests.

- `R066` Loop-restart events must include `node_id`, `target_node`, class, and reason.  
  Refs: `internal/attractor/engine/engine.go`.  
  Verify: event payload tests.

- `R067` Operator docs must define detached launch and monitoring commands (`tail progress`, `cat final`).  
  Refs: `docs/strongdm/attractor/README.md`.  
  Verify: docs review gate.

- `R068` Docs must define restart/circuit-break semantics and deterministic blocking behavior.  
  Refs: `docs/strongdm/attractor/README.md`.  
  Verify: docs review gate.

### 2.12 Test Architecture And CI Gates

- `R069` Unit tests for failure classification/signature normalization are mandatory.  
  Refs: `internal/attractor/engine/loop_restart_guardrails_test.go`.  
  Verify: targeted unit tests.

- `R070` Unit tests for branch name builders/prefix sanitization are mandatory.  
  Refs: `internal/attractor/engine/branch_names_test.go`.  
  Verify: unit tests.

- `R071` Integration tests for provider preflight failure/pass behavior are mandatory.  
  Refs: `internal/attractor/engine/provider_preflight_test.go`, `run_with_config_integration_test.go`.  
  Verify: integration tests.

- `R072` Integration tests for deterministic loop block + circuit breaker are mandatory.  
  Refs: `internal/attractor/engine/loop_restart_test.go`, `loop_restart_guardrails_test.go`.  
  Verify: integration tests.

- `R073` Integration tests for fatal finalization on run/resume fatal paths are mandatory.  
  Refs: `internal/attractor/engine/loop_restart_test.go`, `resume_from_restart_dir_test.go`.  
  Verify: integration tests.

- `R074` Detached CLI lifecycle tests are mandatory.  
  Refs: `cmd/kilroy/main_detach_test.go`.  
  Verify: cmd test suite.

- `R075` Guardrail matrix script must run in CI/local gating.  
  Refs: `scripts/e2e-guardrail-matrix.sh`.  
  Verify: script passes in CI.

- `R076` CI gate must include `cmd/kilroy`, `internal/attractor/engine`, `internal/attractor/runtime`, `internal/llm/providers/...`, and guardrail matrix script.  
  Refs: `.github/workflows/attractor-reliability.yml` (or equivalent repo CI entrypoint).  
  Verify: CI pipeline updates.

### 2.13 Provider-Specific Hardening

- `R077` Anthropic adapter invocation must honor provider CLI contract for stream-json mode.  
  Evidence: `...01-impl_tracer_a.../stderr.log`.  
  Verify: adapter command composition tests.

- `R078` Gemini adapter model identifier mapping must match provider runtime IDs.  
  Evidence: `...02-impl_tracer_b.../stderr.log`.  
  Verify: mapping tests + probe checks.

- `R079` Codex idle-timeout handling must classify timeout as transient and provide actionable reason.  
  Evidence: `...03-impl_tracer_c.../status.json`.  
  Verify: timeout classification tests.

- `R080` Provider adapters must emit deterministic vs transient class explicitly; fallback string classification may be used only for legacy unclassified adapter errors and must not override explicit class.  
  Refs: `internal/attractor/engine/codergen_router.go`.  
  Verify: adapter output contract tests and explicit-vs-fallback precedence tests.

### 2.14 Repository Hygiene

- `R081` `.gitignore` must not accidentally ignore source directories (`cmd/kilroy/*`) while still ignoring the root `kilroy` binary artifact.  
  Evidence: current `.gitignore` has `kilroy`; enforce this as root-binary-only policy and prevent wildcard regressions.  
  Verify: `git check-ignore` assertions (`kilroy` ignored, `cmd/kilroy/*` not ignored).

- `R082` Run-generated `restart-*` directories must not pollute tracked workspace state.  
  Refs: run root policy and test tooling.  
  Verify: workspace cleanliness checks.

- `R083` Default run roots should be outside repo or under ignored state directories.  
  Refs: `cmd/kilroy/run_detach.go`.  
  Verify: default-path tests.

### 2.15 Acceptance Criteria For This Incident Class

- `R084` A deterministic product failure must produce one terminal fail with no restart storm.  
  Evidence: current run shows `loop_restart_blocked`, zero restart dirs.  
  Verify: e2e deterministic scenario.

- `R085` No invalid git refs may occur in parallel branches during run/resume.  
  Evidence: branch names in `parallel_results.json`.  
  Verify: branch-prefix regression tests.

- `R086` Final artifact set must be complete and operator-readable at run end.  
  Evidence: `final.json`, `progress.ndjson`, `run.out`, per-node status.  
  Verify: artifact completeness test.

- `R087` Root cause must be obvious from terminal reason and stage logs without replay/debug tooling.  
  Evidence: `final.json.failure_reason`, stage status files.  
  Verify: observability acceptance check.

- `R088` Resume from checkpoint must preserve retry/restart semantics and finalization behavior.  
  Refs: `resume_*` tests.  
  Verify: resume regression suite.

- `R089` Provider deterministic failures should fail early at preflight when possible.  
  Refs: preflight and provider probe implementation.  
  Verify: preflight deterministic tests.

- `R090` Post-fix architecture must remain idiomatic: minimal duplicated logic, strict invariant boundaries, explicit policy gates, and test-enforced contracts.  
  Refs: engine bootstrap + policy modules + tests.  
  Verify: code review + test map coverage.

---

## 3. Execution Plan (Task Batches)

### Task 1: Runtime Contract Hardening And Schema Tests

**Requirements:** `R001-R010`, `R059-R062`, `R065-R066`  
**Files:**
- Modify: `internal/attractor/runtime/status.go`
- Modify: `internal/attractor/runtime/checkpoint.go`
- Modify: `internal/attractor/runtime/final.go`
- Modify: `internal/attractor/engine/progress.go`
- Create/Modify tests: `internal/attractor/runtime/*_test.go`, `internal/attractor/engine/status_json_legacy_details_test.go`, `internal/attractor/engine/status_json_test.go`

**Steps:**
1. Write failing schema/contract tests for missing required fields and malformed finalization payloads.
2. Implement strict field requirements and normalization.
3. Add progress/live/run.out payload tests for terminal events.
4. Run targeted runtime + engine tests.
5. Commit.

### Task 2: Run/Resume Bootstrap Unification And Path Invariants

**Requirements:** `R011-R018`, `R060-R061`, `R088`  
**Files:**
- Modify: `internal/attractor/engine/engine_bootstrap.go`
- Modify: `internal/attractor/engine/run_with_config.go`
- Modify: `internal/attractor/engine/resume.go`
- Tests: `internal/attractor/engine/resume_test.go`, `internal/attractor/engine/resume_from_restart_dir_test.go`, `internal/attractor/engine/resume_loop_restart_state_test.go`

**Steps:**
1. Add failing tests for relative paths, restart-dir resume, and bootstrap parity.
2. Enforce absolute path validation and checkpoint restoration invariants.
3. Ensure fallback finalization on bootstrap failures.
4. Run resume-focused test suite.
5. Commit.

### Task 3: Branch/Ref Canonicalization And Validation

**Requirements:** `R019-R023`, `R085`  
**Files:**
- Modify: `internal/attractor/engine/branch_names.go`
- Modify: `internal/attractor/engine/parallel_handlers.go`
- Modify: `internal/attractor/engine/engine.go`
- Tests: `internal/attractor/engine/branch_names_test.go`, `internal/attractor/engine/resume_parallel_branch_prefix_test.go`

**Steps:**
1. Write failing tests for malformed prefixes and invalid parallel refs.
2. Route all branch name generation through centralized builders.
3. Add git-ref validation before git operations.
4. Run branch/ref regression tests.
5. Commit.

### Task 4: Graph/Config Preflight Expansion

**Requirements:** `R024-R027`, `R031-R032`  
**Files:**
- Modify: `internal/attractor/engine/run_with_config.go`
- Modify: `internal/attractor/engine/config.go`
- Tests: `internal/attractor/engine/config_test.go`, `internal/attractor/engine/provider_preflight_test.go`, `internal/attractor/engine/run_with_config_integration_test.go`

**Steps:**
1. Add failing tests for invalid loop params and missing provider/backends.
2. Implement strict preflight and persist preflight report artifact.
3. Ensure class-aware preflight classification (deterministic misconfig vs transient probe outage).
4. Run run-with-config tests.
5. Commit.

### Task 5: Provider Adapter Contract Hardening

**Requirements:** `R028-R038`, `R077-R080`, `R089`  
**Files:**
- Modify: `internal/attractor/engine/codergen_router.go`
- Modify: `internal/attractor/engine/codergen_process_test.go`
- Modify: `internal/attractor/engine/codergen_cli_invocation_test.go`
- Add/modify provider-specific tests under `internal/llm/providers/*` as needed

**Steps:**
1. Add failing tests for Anthropic contract mismatch mapping, Gemini model-not-found mapping, Codex timeout/state-db fallback classification.
2. Implement adapter error envelope and class mapping contract with explicit-class precedence over fallback string heuristics.
3. Add capability/model-availability preflight hooks and tests.
4. Run adapter and provider tests.
5. Commit.

### Task 6: Retry Policy Class-Gating

**Requirements:** `R039-R040`  
**Files:**
- Modify: `internal/attractor/engine/engine.go`
- Tests: `internal/attractor/engine/retry_policy_test.go`, `internal/attractor/engine/retry_on_retry_status_test.go`, new class-aware retry tests

**Steps:**
1. Write failing tests showing deterministic stage failures currently over-retry.
2. Implement class-gated stage retry decisions.
3. Emit policy decisions to progress stream.
4. Run retry-focused tests.
5. Commit.

### Task 7: Loop-Restart And Circuit-Breaker Completion

**Requirements:** `R041-R048`, `R066`  
**Files:**
- Modify: `internal/attractor/engine/loop_restart_policy.go`
- Modify: `internal/attractor/engine/engine.go`
- Tests: `internal/attractor/engine/loop_restart_guardrails_test.go`, `internal/attractor/engine/loop_restart_test.go`

**Steps:**
1. Add failing tests for class mismatch, signature stability, threshold config, and pre-limit restart dir creation.
2. Implement policy and circuit breaker with resume persistence.
3. Verify event payload completeness.
4. Run loop-restart suite.
5. Commit.

### Task 8: Parallel/Join Classification And Retry Policy

**Requirements:** `R049-R053`  
**Files:**
- Modify: `internal/attractor/engine/parallel_handlers.go`
- Modify: `internal/attractor/engine/handlers.go`
- Modify: `internal/attractor/engine/engine.go`
- Tests: `internal/attractor/engine/parallel_guardrails_test.go`, `internal/attractor/engine/parallel_test.go`

**Steps:**
1. Add failing tests for join retries on deterministic all-branch-fail scenarios.
2. Implement aggregate class computation and retry gating.
3. Ensure parallel result schema carries class/signature metadata.
4. Run parallel/join tests.
5. Commit.

### Task 9: Detached Lifecycle And CLI Robustness

**Requirements:** `R054-R058`  
**Files:**
- Modify: `cmd/kilroy/main.go`
- Modify: `cmd/kilroy/run_detach.go`
- Modify: `cmd/kilroy/main_detach_test.go`

**Steps:**
1. Add failing tests for detached metadata contract and race-free process lifecycle.
2. Finalize detach behavior and output contract.
3. Stabilize tests around PID exit and artifact presence.
4. Run cmd test suite.
5. Commit.

### Task 10: Archive/Sensitive State Hygiene

**Requirements:** `R063-R064`  
**Files:**
- Modify: `internal/attractor/engine/archive.go`
- Tests: archive tests (new or extend existing)

**Steps:**
1. Add failing tests showing sensitive files leak into archives.
2. Implement exclusion consistency across stage/run/restart/parallel roots.
3. Run archive tests.
4. Commit.

### Task 11: Repository Hygiene And Defaults

**Requirements:** `R081-R083`  
**Files:**
- Modify: `.gitignore`
- Modify: `cmd/kilroy/run_detach.go`
- Add hygiene checks/tests where appropriate

**Steps:**
1. Add failing checks for accidental ignore of `cmd/kilroy/*` and workspace pollution.
2. Tighten ignore patterns and run-root defaults.
3. Run hygiene checks.
4. Commit.

### Task 12: End-To-End And CI Gate Completion

**Requirements:** `R067-R076`, `R084-R087`, `R090`  
**Files:**
- Modify: `scripts/e2e-guardrail-matrix.sh`
- Create/Modify: `.github/workflows/attractor-reliability.yml`
- Modify docs: `docs/strongdm/attractor/README.md`

**Steps:**
1. Expand guardrail matrix to include provider deterministic preflight scenarios.
2. Wire mandatory suites into CI at `.github/workflows/attractor-reliability.yml`.
3. Update runbook docs to match exact behavior.
4. Run full local gate.
5. Commit.

---

## 4. Verification Matrix (Required Green Gate)

Run all of the following before closing the branch:

1. `go test ./cmd/kilroy -count=1`
2. `go test ./internal/attractor/engine -count=1`
3. `go test ./internal/attractor/runtime -count=1`
4. `go test ./internal/llm/providers/... -count=1`
5. `bash scripts/e2e-guardrail-matrix.sh`
6. Real-CXDB deterministic-failure scenario must produce:
   - valid prefixed parallel branches,
   - zero restart storm,
   - explicit `loop_restart_blocked` event,
   - terminal `final.json` with deterministic reason and CXDB IDs.

---

## 5. Completion Definition

This plan is complete only when:

1. All `R001-R090` are implemented or explicitly waived in writing with rationale.
2. All required tests are green locally and in CI.
3. Real-CXDB validation reproduces expected deterministic-fast-fail behavior with complete artifacts.
4. Documentation matches implementation behavior exactly.
