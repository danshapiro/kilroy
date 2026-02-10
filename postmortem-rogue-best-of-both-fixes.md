# Rogue Runs (Fast + Slow): Consolidated Fix Plan

This document merges rogue-fast and rogue-slow findings.
It intentionally separates:
- spec-backed fixes (already defined by current specs);
- runtime-contract fixes (implemented behavior not fully specified);
- required spec deltas (new contracts that must be documented).

Source analyses:
- `postmortem-rogue-fast-part-1.md`
- `postmortem-rogue-fast-part-2.md`
- `postmortem-rogue-slow-part-1.md`
- `postmortem-rogue-slow-part-2.md`

## Spec Coverage Map

- **Spec-backed now**
- Canonical stage status file path/contract: `{logs_root}/{node_id}/status.json` (artifact contract; routing remains in-memory by handler outcome). Reference: attractor spec Section 5.6 + Appendix C.
- DOT routing/conditions/retry semantics. Reference: attractor spec Sections 3.3, 3.5, 3.7, 10.
- Parallel branch isolation + join/error policy model. Reference: attractor spec Section 4.8.
- Provider/model explicitness principles from unified-llm and attractor model selection. Reference: unified-llm Section 2.2; attractor Sections 2.6 and 8.
- **Runtime-contract in code but under-specified**
- Stall watchdog liveness aggregation.
- Attempt lifecycle identifiers and heartbeat validity windows.
- Terminal run artifact (`final.json`) behavior.
- Legacy status fallback probes from worktree locations.
- Failure classification/signature metadata.
- **Spec deltas required**
- Run-level cancellation precedence across parallel/subgraph flows.
- Traversal-level deterministic cycle-break behavior.
- Strict pin/no-failover run-config semantics.
- Canonical outcome casing rule across attractor docs.
- Runtime event taxonomy contract for progress/liveness.

## Proposed Runtime Invariants (Implementation Contract)

- Parent liveness invariant: if any active branch emits accepted liveness events, parent watchdog idle timer resets.
- Attempt ownership invariant: an attempt may consume status only from the same `(run_id, node_id, attempt_id)` scope.
- Heartbeat lifecycle invariant: no heartbeat for an attempt after its attempt-end marker.
- Cancellation invariant: once run-level cancel is observed, no new stage attempts start; traversal exits before selecting another edge.
- Failure-causality invariant: raw `failure_reason` survives check/conditional routing and terminal output.
- Terminal artifact invariant: all controllable terminal paths persist top-level terminal outcome artifact(s).

## Runtime Data Model Definitions (Must Be Explicit Before Coding)

- These are runtime-contract definitions (not currently specified in attractor/coding-agent-loop/unified-llm specs).
- `run_generation`: monotonic integer incremented on each loop-restart generation of a run; included on liveness events to avoid stale-branch attribution.
- `attempt_id`: deterministic identifier for a stage attempt, format `branch_id:node_id:attempt_ordinal`, where `attempt_ordinal` is 1-indexed and `1` means the initial execution (retries are `2+`).
- `terminal_artifact`: top-level `final.json` at run logs root, schema aligned with `internal/attractor/runtime/final.go`:
  - `timestamp`
  - `status` (`success|fail`)
  - `run_id`
  - `final_git_commit_sha`
  - `failure_reason` (optional)
  - `cxdb_context_id` (optional extension field)
  - `cxdb_head_turn_id` (optional extension field)
- Terminal status mapping (run-level, intentionally binary):
  - run `success` only when pipeline reaches terminal success state with goal-gate constraints satisfied (stage-level acceptance includes `SUCCESS` and `PARTIAL_SUCCESS`)
  - run `fail` for terminal failures (including exhausted retries, watchdog/cancel/internal fatal paths, or unsatisfied goal-gate completion)
- `cycle_break_threshold`: sourced from runtime graph attribute `loop_restart_signature_limit` (implemented in engine today, not in attractor-spec); default value inherits code constant (`defaultLoopRestartSignatureLimit`) rather than a hardcoded doc value.
  - This graph attribute is runtime-contract behavior today and must be documented as a spec delta.
  - This mechanism is distinct from coding-agent-loop per-session loop detection (`loop_detection_window`) in coding-agent-loop Section 2.10.

## Runtime Event Contract (Used By This Plan, Then Codified)

The events below are treated as runtime implementation contracts for this fix plan and must later be codified in spec deltas.

Proposed liveness event set for watchdog:
- `stage_attempt_start`
- `stage_attempt_end`
- `stage_heartbeat`
- `branch_complete`
- `api_tool_call_start`
- `api_tool_call_end`
- `api_assistant_text_delta`

## Spec Alignment Guardrails

- Keep `{logs_root}/{node_id}/status.json` as the canonical stage artifact path; do not change core routing to depend on filesystem reads during normal handler flow.
- Do not change DOT condition semantics or retry/fallback routing behavior.
- Preserve join policy semantics (`wait_all`, `k_of_n`, `first_success`, `quorum`) and error policy semantics (`fail_fast`, `continue`, `ignore`) while run is live.
- Run-level cancellation precedence rule (runtime policy pending spec delta): cancellation always stops further work regardless of branch `error_policy`; `error_policy` only governs branch-local failure handling before cancellation.
- Resolve attractor status-case inconsistency explicitly: Section 5.2 enum style vs Section 10.3 lowercase outcomes.
  - This plan does not change condition evaluator behavior.
  - Any casing-policy change to condition matching is treated as a separate spec delta and migration.
  - Spec delta: codify lowercase as canonical wire/storage form.

## P0 (Immediate)

- Harden status ingestion first (precondition for reliable liveness/routing).
  - Precedence:
  - canonical `{logs_root}/{node_id}/status.json`
  - fallback `{worktree}/status.json` (legacy compatibility)
  - fallback `{worktree}/.ai/status.json` (legacy compatibility)
  - Rules:
  - fallback accepted only when canonical missing
  - ownership check must pass when ownership fields are present
  - canonical file is never overwritten by lower-precedence source
  - accepted fallback is atomically copied to canonical path with provenance metadata
  - canonical status writes must be temp-file + atomic rename to avoid partial-read races
  - Done when: deterministic source selection is observable in logs/events.
- Make watchdog liveness fanout-aware.
  - Done when: no false `stall_watchdog_timeout` while any active branch emits accepted liveness events.
  - Coverage must include join policies: `wait_all`, `k_of_n`, `first_success`, `quorum`.
  - Policy expectations per join mode:
  - `wait_all`: any active branch liveness resets parent watchdog.
  - `k_of_n` and `quorum`: liveness from undecided branches resets parent watchdog until threshold is reached and branch set is finalized.
  - `first_success`: once winner selected and cancellation dispatched, only winner/join-path liveness counts; canceled-branch liveness is ignored after cancellation acknowledgment.
- Stop heartbeat leaks with attempt scoping.
  - Done when: zero `stage_heartbeat` after matching attempt-end for identical `(node_id, attempt_id, run_generation)`.
- Add cancellation guards in subgraph/parallel traversal.
  - Done when: after run cancel, no new attempts start and traversal exits without selecting another edge.
  - Clarification: `error_policy=ignore` never suppresses run-level cancellation.
- Extend deterministic cycle-break handling to subgraph path.
  - Done when: deterministic signature repetition triggers breaker using configured `loop_restart_signature_limit`.
- Preserve failure causality through routing.
  - Done when: terminal outputs retain upstream raw reason; normalized signature is separate metadata.
- Guarantee terminal artifact persistence for all controllable terminal paths.
  - Done when: `final.json` is persisted on success/fail/cancel/watchdog/internal fatal paths (best effort excludes uncatchable `SIGKILL`).
  - Write contract: `final.json` persistence uses temp-file + atomic rename for consistency with status artifact durability expectations.
  - Current code gap to fix explicitly: `runtime/final.go` currently writes via direct `os.WriteFile`; migrate to atomic temp+rename write path.

## P1 (Hardening)

- Align engine failure classification with unified-llm taxonomy.
  - Required mapping coverage:
  - `AuthenticationError`
  - `AccessDeniedError`
  - `NotFoundError`
  - `InvalidRequestError`
  - `RateLimitError`
  - `ServerError`
  - `ContentFilterError`
  - `ContextLengthError`
  - `QuotaExceededError`
  - `RequestTimeoutError`
  - `AbortError`
  - `NetworkError`
  - `StreamError`
  - `ConfigurationError`
  - `InvalidToolCallError` (explicitly mapped or explicitly out-of-scope)
  - `NoObjectGeneratedError` (explicitly mapped or explicitly out-of-scope)
  - Done when: terminal classing distinguishes cancel/stall/runtime faults from provider deterministic/transient categories using full mapping.
  - Existing behavior baseline to preserve: `ContentFilterError` remains non-retryable/deterministic unless a future spec-delta policy explicitly changes it.
- Normalize failure signatures only for breaker decisions.
  - Done when: raw reason remains route-visible; normalized key used only by breaker/telemetry.
- Enforce pinned model/provider no-failover policy from run config.
  - Done when: policy violations fail loudly; engine never silently falls back when config forbids it.
  - Cross-reference: this behavior remains a spec-delta item until codified.
- Tighten provider tool adaptation (`apply_patch` and API/CLI contract differences).
  - Done when: contract violations are deterministic and diagnosable.
- Add parent rollup branch telemetry.
  - Done when: parent stream shows branch health summaries without per-branch log spelunking.

## P2 (Tests: Unit + Integration + E2E)

- Watchdog fanout liveness.
  - Unit: liveness aggregator accepts branch events and resets idle timer.
  - Integration: no false timeout with active branch signals.
  - Matrix: join policies (`wait_all`, `k_of_n`, `first_success`, `quorum`) and error policies (`fail_fast`, `continue`, `ignore`).
- Heartbeat lifecycle.
  - Unit: heartbeat emitter stops on attempt completion signal.
  - Integration: no post-attempt heartbeats in progress stream.
- Cancellation.
  - Unit: cancel gate prevents scheduling next stage.
  - Integration: no new attempts after cancel; traversal exits cleanly.
  - Matrix: includes `error_policy=ignore` to prove cancellation precedence.
  - Interaction case: watchdog must ignore post-cancel liveness events from canceled branches.
- Deterministic cycle breaker.
  - Unit: signature counting/limit logic.
  - Integration: subgraph breaker triggers at configured limit.
- Status ingestion.
  - Unit: precedence + ownership decision table.
  - Decision table minimum cases:
  - canonical present + valid -> choose canonical
  - canonical present + corrupt/unparseable -> fail deterministically with explicit ingestion diagnostic
  - canonical missing + worktree status valid/owned -> choose worktree status
  - canonical missing + worktree invalid + .ai valid/owned -> choose .ai status
  - canonical missing + fallback ownership mismatch -> reject fallback
  - canonical present + fallback present -> canonical wins
  - fallback accepted -> atomic canonical copy with provenance recorded
  - Integration: canonical/fallback behavior with actual files.
  - Assertions: selected source path, ownership verdict, canonical write behavior (temp+rename), and provenance metadata are all observable.
- Failure propagation.
  - Unit: check/conditional passes through raw reason/class metadata.
  - Integration: terminal artifact preserves causal reason.
- Terminal artifact persistence.
  - Integration/E2E: `final.json` exists with expected fields across controllable terminal paths.
- No-failover policy.
  - Unit: policy evaluator.
  - Integration: pinned no-failover config never switches provider/model.
- True-positive watchdog.
  - Unit: no-event path triggers timeout logic.
  - Integration: timeout still fires when top-level and branch liveness are absent.
  - E2E: detached run with intentionally silent branches times out within configured watchdog bounds.

## Required Observability

- Branch-to-parent liveness counters/events with `run_generation`.
- Attempt lifecycle events with `attempt_id`.
- Status ingestion decision events: searched paths, chosen source, parse result, ownership result, copy result.
- Cancellation exit events: node, reason, and exit point.
- Cycle-break events: signature, count, limit.
- Terminalization events: final status and artifact path.

## Spec Delta Backlog (Document in Specs)

- Runtime event taxonomy for attractor progress/liveness events and mapping to existing spec events.
- Watchdog semantics: accepted liveness set, aggregation rules, and stall decision contract.
- Attempt lifecycle semantics: attempt identity, ownership, and heartbeat validity.
- Cancellation semantics across parallel join/error policies.
- Traversal-level deterministic cycle-break contract.
- Terminal artifact (`final.json`) contract and required schema.
- Legacy fallback status contract (`worktree/status.json`, `.ai/status.json`) + deprecation plan.
- Explicit pinned-model failover policy contract.
- Canonical outcome casing contract.

## Implementation Order (Risk-Aware)

- Stage 1: status ingestion hardening + ownership checks.
- Stage 2: draft spec deltas for status/casing/cancel/watchdog/event contracts (before broad behavior rollout).
- Stage 3: watchdog fanout aggregation + heartbeat lifecycle fixes.
- Stage 4: cancellation guards + subgraph cycle-break parity.
- Stage 5: failure taxonomy mapping + no-failover policy enforcement.
- Stage 6: full test matrix and release gates.
- Stage 7: finalize and merge spec-delta docs to remove implementation/spec drift.

## Primary Touchpoints

Implementation:
- `internal/attractor/engine/handlers.go`
- `internal/attractor/runtime/status.go`
- `internal/attractor/engine/progress.go`
- `internal/attractor/engine/engine.go`
- `internal/attractor/engine/subgraph.go`
- `internal/attractor/engine/parallel_handlers.go`
- `internal/attractor/engine/codergen_router.go`
- `internal/attractor/runtime/final.go`

Tests:
- `internal/attractor/engine/engine_stall_watchdog_test.go`
- `internal/attractor/engine/parallel_guardrails_test.go`
- `internal/attractor/engine/parallel_test.go`
- `internal/attractor/engine/codergen_heartbeat_test.go`
- `internal/attractor/engine/progress_test.go`
- `internal/attractor/runtime/status_test.go`

## Release Gates

- No stale heartbeats after attempt completion in canaries.
- No fanout false-timeouts with active branch liveness.
- Deterministic cycle-breaker triggers at configured limits.
- No new attempts after run cancel.
- `final.json` persisted for all controllable terminal outcomes.
- No implicit fallback when pin/no-failover policy forbids it.
- No regression in canonical status/routing semantics.
