# Rogue Runs (Fast + Slow): Best-of-Both Fix Plan

This plan consolidates fixes from both rogue-fast and rogue-slow postmortems.
It includes both:
- spec-backed fixes aligned to `coding-agent-loop-spec`, `attractor-spec`, and `unified-llm-spec`;
- runtime-contract fixes that are currently implementation behavior and therefore require spec-delta docs.

## Spec coverage map (explicit)

- **Directly spec-backed today**
- Canonical stage status contract (`{logs_root}/{node_id}/status.json`).
- Edge routing semantics and condition evaluation.
- Parallel isolation/fan-in structure.
- Explicit provider/model selection principles (no hidden guessing).
- **Runtime contracts present in implementation but under-specified in docs**
- Stall watchdog semantics.
- Stage heartbeat/progress event semantics.
- Attempt ownership/lifecycle semantics.
- Top-level terminal artifact behavior (`final.json` in current implementation).
- Legacy status discovery fallbacks (worktree `status.json`, `.ai/status.json`).
- Failure classification metadata.
- **Net-new decisions that must be documented as spec deltas**
- Run-level cancellation precedence over branch processing.
- Traversal-level deterministic cycle-break policy.
- Strict pin/no-failover run-config policy semantics.
- Outcome-casing canonicalization rule.

## Core invariants (formal and testable)

- **Parent liveness invariant (runtime contract):** while any active fanout branch emits progress for the current run generation, parent watchdog idle time must reset.
  - Progress sources (explicit): `stage_attempt_start`, `stage_attempt_end`, `stage_progress`, `stage_heartbeat`, and branch completion events.
- **Attempt ownership invariant (runtime contract):** a stage attempt may read only status produced by the same `(run_id, node_id, attempt_id)` tuple.
- **Heartbeat lifecycle invariant (runtime contract):** no heartbeat may be emitted after `stage_attempt_end` for the same attempt tuple.
- **Cancellation convergence invariant (runtime contract):** once run-level cancellation is observed, no new stage attempts may start, and subgraph traversal must exit before selecting another edge.
- **Failure-causality invariant:** routing/check nodes must preserve upstream `failure_reason`; if classification is added, preserve both raw and classified forms.
- **Terminal artifact invariant (runtime contract):** all controllable terminal paths (success/fail/cancel/watchdog/internal fatal) must persist top-level terminal outcome artifacts.

## Spec alignment guardrails (idiomatic Attractor/Kilroy path)

- Preserve canonical stage status contract: `{logs_root}/{node_id}/status.json` is authoritative for routing.
- Keep edge-routing semantics unchanged: condition evaluation, retry-target fallback behavior, deterministic tie-breaks.
- Preserve parallel isolation and fan-in behavior: branch-local isolation and single winner integration.
- Keep provider behavior config-driven; avoid provider-specific hardcoding in engine logic.
- Keep condition matching semantics stable: route conditions still match raw outcome/context fields (do not silently change comparison semantics).
- Status outcome parsing policy: accept legacy case variants on read, normalize to lowercase internally, and emit canonical lowercase on write.

## P0 (must fix first)

- Make watchdog liveness fanout-aware. (runtime contract + spec-delta)
  - Done when: active child-branch events reset parent watchdog idle timer; no false `stall_watchdog_timeout` while branches are active.
- Add API `agent_loop` progress plumbing with explicit event mapping.
  - Done when: long-running API stages emit periodic attractor stage-progress events derived from agent-loop milestones (tool start/delta/end + assistant progress boundaries), not just final completion.
- Stop CLI heartbeat leaks with attempt scoping. (runtime contract + spec-delta)
  - Done when: zero heartbeats are observed after attempt end for the same `(node_id, attempt_id)`.
- Add run-level cancellation guards in subgraph execution with explicit policy interaction. (spec-delta)
  - Policy: run-level cancel always preempts branch execution regardless of branch `error_policy`; `error_policy` still governs branch-local failure handling while run is live.
  - Done when: after cancellation signal, no new branch stage attempts are started and traversal exits without selecting another edge.
- Add deterministic subgraph cycle breaker (implementation parity), without changing DOT routing semantics. (spec-delta)
  - Done when: repeated deterministic failure signatures in subgraph path abort at configured threshold and emit explicit cycle-break event.
- Preserve failure causality through routing nodes.
  - Done when: upstream raw `failure_reason` survives check/conditional traversal and terminal outcomes.
- Harden status ingestion with explicit precedence, ownership checks, and diagnostics. (runtime contract + spec-delta)
  - Precedence rule:
  - Read canonical `{logs_root}/{node_id}/status.json` first.
  - If canonical is absent, probe legacy fallbacks (`{worktree}/status.json`, then `{worktree}/.ai/status.json`) for compatibility with legacy prompts that write status in worktree paths.
  - Accept fallback only if ownership matches current stage/attempt (when ownership fields are present).
  - Never overwrite an existing canonical status with fallback data.
  - If fallback accepted, copy atomically to canonical path with provenance marker.
  - Done when: status path selection is deterministic and fully traceable in logs.
- Guarantee top-level terminalization on all controllable paths. (runtime contract + spec-delta)
  - Done when: terminal artifact exists for success/fail/watchdog cancel/context cancel/internal fatal exits.
  - Note: uncatchable hard kill (`SIGKILL`) remains best-effort.

## P1 (high-value hardening)

- Separate cancellation/stall classifications from deterministic provider/API failure classes.
  - Done when: terminal artifacts and telemetry distinguish cancel/timeouts from deterministic API failures, and provider/API classes map explicitly to unified-llm error taxonomy categories (`RateLimitError`, `ServerError`, `TimeoutError`, `AuthError`, terminal provider errors).
- Normalize failure signatures for cycle-break decisions without mutating route-visible raw reasons.
  - Done when: engine stores both `failure_reason_raw` and normalized signature key; condition expressions remain compatible.
- Enforce strict model/fallback policy from run config. (spec-delta)
  - Done when: pinned provider/model/no-failover configs block implicit fallback and emit explicit policy-violation diagnostics.
- Improve provider/tool adaptation (especially `apply_patch` contract handling in openai-family API profiles).
  - Done when: adapter behavior is deterministic and contract violations are surfaced as actionable errors; implementation notes explicitly reference coding-agent-loop tool contract expectations.
- Add parent rollup telemetry for branch health.
  - Done when: operators can see branch-level liveness/failure summaries from parent stream without drilling into branch directories.

## P2 (validation and prevention)

- Fanout watchdog false-timeout regression.
  - Level: integration.
  - Matrix: `error_policy=fail_fast` and `error_policy=continue`.
  - Assertion: no watchdog timeout fires while any branch emits accepted liveness events.
- Stale heartbeat leak regression.
  - Level: unit + integration.
  - Assertion: event stream contains no `stage_heartbeat` after matching `stage_attempt_end` for identical attempt tuple.
- Subgraph cancellation convergence regression.
  - Level: integration.
  - Assertion: after cancellation marker, no new stage attempt starts and traversal exits cleanly.
- Subgraph deterministic cycle-break regression.
  - Level: integration.
  - Assertion: repeating deterministic signature reaches threshold and exits with cycle-break reason.
- Status ingestion precedence/ownership regression (`status.json` vs `.ai/status.json`).
  - Level: unit + integration.
  - Assertion: canonical status wins; fallback is accepted only when canonical missing and ownership checks pass.
- Failure propagation through check/conditional nodes regression.
  - Level: integration.
  - Assertion: upstream raw `failure_reason` is preserved through routing and terminal output.
- Terminal artifact persistence regression for all controllable terminal paths.
  - Level: integration/e2e.
  - Assertion: terminal artifact exists with expected status/reason fields on each controllable terminal path.
- Model pin/no-failover enforcement regression.
  - Level: integration.
  - Assertion: pinned no-failover config never routes to alternate provider/model.
- True-positive watchdog timeout regression (no top-level and no branch activity).
  - Level: integration.
  - Assertion: timeout still fires when no accepted liveness events occur.

## Required observability

- Branch-to-parent liveness events/counters with run generation and branch identifiers.
- Attempt identifiers on all lifecycle events: `stage_attempt_start`, `stage_attempt_end`, `stage_heartbeat`, `stage_progress`.
- Status-ingestion decision events: searched paths, selected source, parse outcome, ownership validation result, canonical copy outcome.
- Subgraph cancellation-exit event including elapsed convergence time and stop node.
- Deterministic cycle-break event including signature, count, and threshold.
- Terminalization event including final status, reason/class, and artifact path.

## Spec delta proposals required (document separately)

These items are currently implementation concepts and should be codified in spec docs to avoid drift:

- Deterministic traversal-level cycle-break semantics for subgraph/main loop parity.
- Top-level terminal artifact contract (`final.json` or equivalent) and required fields.
- Optional failure classification taxonomy (if `failure_class` is retained).
- Legacy `.ai/status.json` compatibility contract and deprecation path.
- Explicit run-config failover policy semantics when provider/model is pinned.
- Outcome casing canonicalization rule (resolve uppercase/lowercase inconsistency in attractor docs).
- Watchdog semantics: liveness event set, idle timeout behavior, and parent/branch aggregation rules.
- Attempt lifecycle semantics: required attempt identifiers and heartbeat validity window.
- Cancellation semantics for parallel/subgraph execution, including interaction with `error_policy`.

## Suggested implementation order (risk-aware)

- If rogue-trigger frequency is non-trivial, ship minimal behavioral hotfixes first:
  - subgraph cancellation guards,
  - heartbeat lifecycle scoping,
  - fanout-aware watchdog liveness.
- Then land observability for stronger diagnosis and proof.
- Then land status-ingestion hardening + failure-causality preservation.
- Then classification/signature/tool-adaptation hardening.
- Then complete full regression matrix and release gates.

## Primary touchpoints

- `internal/attractor/engine/parallel_handlers.go`
- `internal/attractor/engine/subgraph.go`
- `internal/attractor/engine/engine.go`
- `internal/attractor/engine/codergen_router.go`
- `internal/attractor/engine/handlers.go`
- `internal/attractor/runtime/status.go`
- `internal/attractor/engine/engine_stall_watchdog_test.go`
- `internal/attractor/engine/parallel_guardrails_test.go`
- `internal/attractor/engine/parallel_test.go`
- `internal/attractor/engine/codergen_heartbeat_test.go`
- `internal/attractor/runtime/status_test.go`

## Release gates

- No stale-heartbeat events after attempt completion in canaries.
- No fanout false-timeouts in canaries with active branches.
- Deterministic loops terminate within configured thresholds.
- Cancellation convergence SLO is met.
- Every controllably terminated run persists terminal artifact with correct status/reason.
- No regressions in canonical status/routing semantics.
- No implicit provider/model fallback when config forbids it.
