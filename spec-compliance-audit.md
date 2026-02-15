# Attractor Spec Compliance Audit

**Date:** 2026-02-14
**Spec:** `docs/strongdm/attractor/attractor-spec.md`
**Codebase:** Kilroy `main` @ `c9328db6`
**Sections Audited:** 1-11 (Complete)

---

## Summary

| Section | Violations |
|---------|-----------|
| 1. Overview and Goals | 1 |
| 2. DOT DSL Schema | 3 |
| 3. Pipeline Execution Engine | 6 |
| 4. Node Handlers | 9 |
| 5. State and Context | 5 |
| 6. Human-in-the-Loop | 8 |
| 7. Validation and Linting | 7 |
| 8. Model Stylesheet | 3 |
| 9. Transforms and Extensibility | 4 |
| 10. Condition Expression Language | 2 |
| 11. Definition of Done | 5 |
| **Total** | **53** |

---

## Section 1: Overview and Goals

### V1.1 — Engine leaks handler-type knowledge (violates "pluggable handlers" principle)

**Spec says:** "The execution engine does not know about handler internals." New node types are added by registering handlers.

**Code does:** The engine directly inspects handler types in three places:

1. **`engine.go:438`** — `resolvedHandlerType(node) == "codergen"` to apply fidelity/thread resolution logic specific to LLM nodes.
2. **`engine.go:1026`** — `resolvedHandlerType(node) == "conditional"` to bypass retry logic for conditional nodes (execute exactly once).
3. **`run_with_config.go:36`** — `n.Shape() != "box"` as hardcoded proxy for "this is a codergen node" when gathering provider requirements, instead of using `resolvedHandlerType()`. A node with `type=codergen` and non-box shape would be silently skipped.

**Impact:** The handler registry (`handlers.go:32-76`) is properly designed with `Register`/`Resolve`, but the engine bypasses this abstraction. Adding a new handler type that needs fidelity resolution or retry-bypass behavior would require modifying the engine, not just registering a handler.

**Analysis:** The fix uses Go's optional interface pattern (idiomatic for capability declaration) to push handler-type knowledge out of the engine and into the handlers themselves. Three optional interfaces are defined alongside the base `Handler` interface:

- **`FidelityAwareHandler`** (`UsesFidelity() bool`) -- handlers that need fidelity/thread resolution (e.g., LLM nodes for session continuity). Implemented by `CodergenHandler`.
- **`SingleExecutionHandler`** (`SkipRetry() bool`) -- handlers that should bypass retry logic and execute exactly once (e.g., pass-through routing points). Implemented by `ConditionalHandler`.
- **`ProviderRequiringHandler`** (`RequiresProvider() bool`) -- handlers that require an LLM provider to be configured. Implemented by `CodergenHandler`.

The engine's three call sites now type-assert the resolved handler against these optional interfaces instead of comparing handler-type strings. Custom handlers can now declare these capabilities by implementing the relevant interfaces, without modifying the engine.

**Fix Plan:**
- Step 1: Define `FidelityAwareHandler`, `SingleExecutionHandler`, and `ProviderRequiringHandler` as optional interfaces in `handlers.go`, alongside the existing `Handler` interface.
- Step 2: Add `UsesFidelity() bool` and `RequiresProvider() bool` methods to `CodergenHandler`; add `SkipRetry() bool` method to `ConditionalHandler`.
- Step 3: In `engine.go:438`, replace `resolvedHandlerType(node) == "codergen"` with a type assertion `e.Registry.Resolve(node).(FidelityAwareHandler)`.
- Step 4: In `engine.go:1029`, replace `resolvedHandlerType(node) == "conditional"` with a type assertion `e.Registry.Resolve(node).(SingleExecutionHandler)`.
- Step 5: In `run_with_config.go:36` (and `validateProviderModelPairs`), replace `n.Shape() != "box"` with a type assertion `reg.Resolve(n).(ProviderRequiringHandler)` using a local `NewDefaultRegistry()`.
- Step 6: Run full engine test suite to verify no regressions.

**Files Modified:**
- `internal/attractor/engine/handlers.go` -- Define three optional capability interfaces (`FidelityAwareHandler`, `SingleExecutionHandler`, `ProviderRequiringHandler`); implement them on `CodergenHandler` and `ConditionalHandler`.
- `internal/attractor/engine/engine.go` -- Replace two `resolvedHandlerType()` string comparisons with interface type assertions on the resolved handler.
- `internal/attractor/engine/run_with_config.go` -- Replace two `n.Shape() != "box"` checks with registry-based `ProviderRequiringHandler` type assertions.

**Status: FIXED**

---

## Section 2: DOT DSL Schema

### V2.1 — Identifier pattern too permissive (Unicode vs ASCII)

**Spec says (§2.2):** `Identifier ::= [A-Za-z_][A-Za-z0-9_]*` — ASCII letters only.

**Code:** `dot/lexer.go:155-161`:
```go
func isIdentStart(r rune) bool {
    return r == '_' || unicode.IsLetter(r)
}
func isIdentContinue(r rune) bool {
    return isIdentStart(r) || unicode.IsDigit(r)
}
```

`unicode.IsLetter` and `unicode.IsDigit` match far beyond ASCII (accented chars, CJK, Arabic numerals, etc.). A node ID with non-ASCII letters would be accepted when the spec says it should be rejected.

**Fix Plan:**
- Step 1: In `dot/lexer.go`, replace `unicode.IsLetter(r)` in `isIdentStart` with ASCII range checks `(r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')`. Replace `unicode.IsDigit(r)` in `isIdentContinue` with `(r >= '0' && r <= '9')`. Remove unused `unicode` import.
- Step 2: In `style/stylesheet.go`, apply identical ASCII-only changes to the duplicate `isIdentStart`/`isIdentContinue` functions. Also fix `parseClassName` to use `[a-z0-9-]+` (lowercase ASCII only, per spec §8.2) and `parseIdentLike` to use ASCII range checks. Remove unused `unicode` import.
- Step 3: Add `dot/lexer_test.go` with boundary tests: `isIdentStart`/`isIdentContinue` accept ASCII and reject non-ASCII (accented, CJK, Cyrillic). Full parse test confirming Unicode identifiers are rejected and ASCII identifiers are accepted.
- Step 4: Add stylesheet tests: `parseClassName` rejects uppercase and non-ASCII; `isIdentStart`/`isIdentContinue` reject non-ASCII; `parseIdentLike` rejects non-ASCII in shape selectors. ASCII identifiers continue to work.
- Step 5: Run dot and style test suites. Run full engine test suite to confirm no regressions.

**Files Modified:**
- `internal/attractor/dot/lexer.go` — Replace `unicode.IsLetter`/`unicode.IsDigit` with ASCII range checks in `isIdentStart`/`isIdentContinue`; remove `unicode` import
- `internal/attractor/dot/lexer_test.go` (new) — Boundary tests for ASCII-only identifier recognition
- `internal/attractor/style/stylesheet.go` — Replace `unicode.IsLetter`/`unicode.IsDigit` with ASCII range checks in `isIdentStart`/`isIdentContinue`, `parseClassName`, and `parseIdentLike`; remove `unicode` import
- `internal/attractor/style/stylesheet_test.go` — Add boundary tests for ASCII-only class names, identifiers, and shape selectors

**Status: FIXED**

### V2.2 — `default_max_retry` effective default is 0, not 50; explicit zero conflated with "not set"

**Spec says (§2.5):** `default_max_retry | Integer | 50`

**Code:** `engine.go:1048-1054`:
```go
maxRetries := parseInt(node.Attr("max_retries", ""), 0)
if maxRetries == 0 {
    maxRetries = parseInt(e.Graph.Attrs["default_max_retry"], 0) // <-- 0 fallback, spec says 50
}
```

Two bugs:
1. When `default_max_retry` is omitted from the graph, `parseInt("", 0)` returns 0 instead of the spec default. Nodes with no retry config get zero retries.
2. `if maxRetries == 0` conflates "node omitted max_retries" with "node explicitly set max_retries=0". A node with `max_retries=0` (intentionally meaning "no retries") is overridden by the graph default.

**Analysis:**

The spec's default of 50 is dangerously high. With exponential backoff (200ms initial, factor 2.0, cap 60s), 50 retries can take over 45 minutes per node. Every existing DOT file in the codebase sets `default_max_retry=3`:
- `skills/english-to-dotfile/reference_template.dot`: 3
- `demo/rogue/rogue.dot`: 3
- `demo/rogue/rogue-spark.dot`: 3
- All 6 research DOT files: 3
- `docs/strongdm/dot specs/consensus_task.dot`: 3

No real pipeline uses the value 50 or omits `default_max_retry` entirely. The spec also has a contradiction: §2.5 says default is 50, but §3.5 says "Built-in default: 0 (no retries)" for item 3 in the precedence chain. If §2.5's default of 50 is the graph attribute default, then §3.5 item 3 is unreachable (the graph attr always has a value, at least 50).

**Recommendation:** Change the spec default from 50 to 3 to match all real-world usage. Fix the sentinel bug. Update §3.5 built-in default to 3 for consistency.

**Fix Plan:**
- Step 1: Fix the sentinel bug in `engine.go`. Use `parseInt(node.Attr("max_retries", ""), -1)` so that "not set" (returns `""`, parsed as -1) is distinguishable from "explicitly 0" (returns `"0"`, parsed as 0). Chain: if node attr < 0, try graph attr; if graph attr < 0, use built-in default of 3.
- Step 2: Change spec §2.5 `default_max_retry` default from 50 to 3 (matching all real DOT files).
- Step 3: Change spec §3.5 "Built-in default: 0 (no retries)" to "Built-in default: 3" for consistency with §2.5.
- Step 4: Change spec Appendix (§11 quick-reference table) default from 50 to 3.
- Step 5: Add regression tests: (a) `TestRun_ExplicitMaxRetriesZero_NoRetries` verifies that `max_retries=0` on a node is NOT overridden by `default_max_retry=5` on the graph. (b) `TestRun_DefaultMaxRetryFallback` verifies the full precedence chain: with no graph or node attr, the built-in default of 3 applies (tool fails 3 times, succeeds on attempt 4).
- Step 6: Fix existing tests that implicitly depended on the old default of 0 by adding explicit `default_max_retry=0` or `max_retries=0` where tests need zero-retry behavior to exercise loop-restart, goal-gate, or watchdog features.

**Files Modified:**
- `internal/attractor/engine/engine.go` — Fix sentinel bug: use -1 default in `parseInt` calls; cascade node attr -> graph attr -> built-in default of 3.
- `docs/strongdm/attractor/attractor-spec.md` — Change `default_max_retry` default from 50 to 3 in §2.5, §3.5, and the Appendix quick-reference table.
- `internal/attractor/engine/retry_policy_test.go` — Add `TestRun_ExplicitMaxRetriesZero_NoRetries` and `TestRun_DefaultMaxRetryFallback` regression tests.
- `internal/attractor/engine/goal_gate_test.go` — Add explicit `max_retries=0` to goal gate test node (test exercises goal-gate retry target, not node-level retries).
- `internal/attractor/engine/loop_restart_test.go` — Add `default_max_retry=0` to 3 loop-restart tests (tests exercise loop-restart routing, not node-level retries).
- `internal/attractor/engine/codergen_process_test.go` — Add `default_max_retry=0` to idle timeout watchdog test (test exercises process kill, not retries; 4 retries * 2s idle timeout would exceed 15s context deadline).

**Status: FIXED**

### V2.3 — `GraphAttrDecl` rejects negative numeric values

**Spec says (§2.2):** `Integer ::= '-'? [0-9]+` and `GraphAttrDecl ::= Identifier '=' Value`

**Code:** `dot/parser.go:238-243`: Top-level `key = value` only accepts a single `tokenIdent` or `tokenString`. The lexer tokenizes `-1` as `tokenSymbol("-")` + `tokenIdent("1")`, so `key = -1` at the graph top level fails to parse. This does NOT affect attr blocks `[key=-1]` which use the more flexible `parseAttrValue()`.

**Fix Plan:**
- Step 1: Add a `parseTopLevelValue()` method to the parser that handles quoted strings, plain identifiers, and negative numeric values (where the lexer emits `-` as a separate `tokenSymbol` followed by the numeric `tokenIdent`). This is more targeted than reusing `parseAttrValue()` (which uses `]`/`,` as terminators and consumes greedily), avoiding ambiguity with subsequent statements.
- Step 2: Replace the single-token read at lines 238-243 with a call to `parseTopLevelValue()`, which returns the assembled value string.
- Step 3: Add test `TestParse_TopLevelNegativeNumericValue` covering `key = -1` and `key = -3.5` at graph top level.
- Step 4: Verify all existing DOT parser tests still pass.

**Files Modified:**
- `internal/attractor/dot/parser.go` — Added `parseTopLevelValue()` method; replaced single-token value read in graph attr decl with call to `parseTopLevelValue()`
- `internal/attractor/dot/parser_test.go` — Added `TestParse_TopLevelNegativeNumericValue` test

**Status: FIXED**

---

## Section 3: Pipeline Execution Engine

### V3.1 — Backoff jitter default is `false`, spec says `true`

**Spec says (§3.6):** `jitter: Boolean — default: true`

**Code:** `backoff.go:23-31`:
```go
func defaultBackoffConfig() BackoffConfig {
    // Spec defaults are 200ms / factor 2.0 / cap 60s. Kilroy defaults jitter off for determinism.
    return BackoffConfig{
        InitialDelayMS: 200,
        BackoffFactor:  2.0,
        MaxDelayMS:     60_000,
        Jitter:         false,  // spec says true
    }
}
```

The code's own comment acknowledges the spec says `true` but intentionally deviates "for determinism."

**Analysis:** The jitter implementation in `backoff.go:105-114` uses **deterministic SHA-256 seeding** from `runID:nodeID:attempt`, not `math/rand`. Same seed = same delay, every time. The existing test `TestDelayForAttempt_Jitter_IsDeterministicPerSeedAndWithinRange` proves this. The comment "for determinism" is wrong — jitter is already deterministic. No tests depend on jitter being OFF by default (every backoff test sets its own `BackoffConfig`). The LLM layer already has jitter ON (`codergen_router.go:429`), making the two layers inconsistent. Parallel branches (up to `max_parallel=4`) can independently retry the same LLM provider simultaneously — without engine-level jitter they hit at identical exponential intervals.

**Recommendation:** Turn jitter ON. One line: `Jitter: false` → `Jitter: true` in `backoff.go:30`. Update comment to document seeded design. Free spec compliance, zero test breakage.

**Fix Plan:**
- Step 1: Change `Jitter: false` to `Jitter: true` in `defaultBackoffConfig()` at `backoff.go:30`.
- Step 2: Update the comment at `backoff.go:24-25` to document that jitter defaults ON per spec, and that the jitter implementation is deterministic (SHA-256 seeded from `runID:nodeID:attempt`), so replay determinism is preserved.

**Files Modified:**
- `internal/attractor/engine/backoff.go` — Change default jitter from `false` to `true`; update comment to document deterministic seeded jitter design.

**Status: FIXED**

### V3.2 — Edge selection missing "Fallback: any edge" step

**Spec says (§3.3 pseudocode):**
```
-- Fallback: any edge
RETURN best_by_weight_then_lexical(edges)
```

**Code:** `engine.go:1753-1765`: When all edges have conditions and none matched, returns `nil` (no edge). The spec says to fall back to any edge by weight/lexical, even if its condition failed. This causes pipeline termination or "no outgoing fail edge" errors in scenarios where the spec would pick a fallback edge.

**SAFETY CONCERN:** The fallback can select an edge whose condition evaluated to false. For example, if a node fails and the only outgoing edge has `condition="outcome=success"`, the fallback routes through that edge. This means a failed node's output gets routed through a "success-only" edge, which can cause surprising behavior — the pipeline may appear to succeed when a node actually failed. The engine previously caught this as "stage failed with no outgoing fail edge" and terminated with an error. With the fallback, this error path is effectively dead code for graphs where all edges have conditions. **The spec is explicit about this behavior, so the engine implements it. If this proves problematic, the spec should be updated to add a failure-aware exception to the fallback.**

**Fix Plan:**
- Step 1: In `selectAllEligibleEdges` (`engine.go`), restructure the function to filter nil edges once at the top, then follow the spec's five-step priority order plus fallback.
- Step 2: After Steps 4-5 (unconditional edges by weight/lexical), add the fallback step: when no unconditional edges exist (all edges have conditions and none matched), return ALL edges. The caller (`bestEdge` via `selectNextEdge`) applies weight-then-lexical tiebreaking.
- Step 3: Update tests that depended on the old behavior (returning nil when all conditions fail):
  - `TestRun_BoxNodeCustomOutcome_NoMatchingEdge_StillFails` -> renamed to `_FallsBackToAnyEdge`, expects pipeline success via fallback fan-out routing.
  - `TestRun_NoMatchingFailEdge_NoRetryTarget_StillFails` -> renamed to `_FallsBackToAnyEdge`, expects pipeline success via fallback edge.
  - `TestRun_GlobalStageTimeoutCapsToolNode` and `TestRun_GlobalAndNodeTimeout_UsesSmallerTimeout` -> added explicit `condition="outcome=fail"` edges to a `fail_exit` terminal so timeout tests don't depend on edge selection fallback.
  - `TestRunWithConfig_CLIDeterministicFailure_DoesNotConsumeRetryBudget` -> updated to expect pipeline completion via fallback; retry-blocking assertions (calls=1, stage_retry_blocked) still verified.
- Step 4: Add new unit tests: `TestSelectAllEligibleEdges_FallbackAnyEdge_AllConditionsFailed` and `TestSelectNextEdge_FallbackAnyEdge_PicksBestByWeightThenLexical`.
- Step 5: Run full engine test suite to verify no regressions.

**Files Modified:**
- `internal/attractor/engine/engine.go` — Restructured `selectAllEligibleEdges`: filter nil edges once, reordered steps to match spec, added fallback step returning all edges when no unconditional edges exist.
- `internal/attractor/engine/edge_selection_test.go` — Added `TestSelectAllEligibleEdges_FallbackAnyEdge_AllConditionsFailed` and `TestSelectNextEdge_FallbackAnyEdge_PicksBestByWeightThenLexical`.
- `internal/attractor/engine/custom_outcome_routing_test.go` — Renamed test to `_FallsBackToAnyEdge`; updated to expect fallback fan-out routing instead of error.
- `internal/attractor/engine/no_fail_edge_fallback_test.go` — Renamed test to `_FallsBackToAnyEdge`; updated to expect fallback routing to terminal.
- `internal/attractor/engine/engine_stage_timeout_test.go` — Added explicit `condition="outcome=fail"` edges so timeout tests don't depend on edge selection fallback.
- `internal/attractor/engine/retry_classification_integration_test.go` — Updated to expect pipeline completion via fallback; retry-blocking assertions preserved.

**Status: FIXED (with safety concern — see above)**

---

### V3.3 — Preferred label match (Step 2) only checks unconditional edges

**Spec says (§3.3 Step 2):** `FOR EACH edge IN edges:` — iterates all edges.

**Code:** `engine.go:1767-1776`: Only searches `uncond` (unconditional edges) for preferred label match. A conditional edge whose condition didn't pass but whose label matches would be skipped.

**Fix Plan:**
- Step 1: In `selectAllEligibleEdges` (`engine.go`), change the preferred label search (Step 2) to iterate `edges` (all non-nil edges) instead of `uncond`. Create a sorted copy of `edges` to avoid mutating the shared slice.
- Step 2: Add new unit tests: `TestSelectNextEdge_PreferredLabelMatchesConditionalEdge` (verifies a conditional edge with matching label is selected when no condition matches) and `TestSelectAllEligibleEdges_PreferredLabelSearchesAllEdges`.
- Step 3: Run full engine test suite to verify no regressions.

**Files Modified:**
- `internal/attractor/engine/engine.go` — In `selectAllEligibleEdges`, Step 2 now iterates `edges` (all edges) instead of `uncond`.
- `internal/attractor/engine/edge_selection_test.go` — Added `TestSelectNextEdge_PreferredLabelMatchesConditionalEdge` and `TestSelectAllEligibleEdges_PreferredLabelSearchesAllEdges`.

**Status: FIXED**

---

### V3.4 — Suggested next IDs (Step 3) only checks unconditional edges

**Spec says (§3.3 Step 3):** `FOR EACH edge IN edges:` — iterates all edges.

**Code:** `engine.go:1778-1788`: Same issue as V3.3 — restricts to `uncond`.

**Fix Plan:**
- Step 1: In `selectAllEligibleEdges` (`engine.go`), change the suggested next IDs search (Step 3) to iterate `edges` (all non-nil edges) instead of `uncond`. Create a sorted copy of `edges` to avoid mutating the shared slice.
- Step 2: Add new unit tests: `TestSelectNextEdge_SuggestedNextIDMatchesConditionalEdge` (verifies a conditional edge with matching target ID is selected when no condition matches) and `TestSelectAllEligibleEdges_SuggestedNextIDSearchesAllEdges`.
- Step 3: Run full engine test suite to verify no regressions.

**Files Modified:**
- `internal/attractor/engine/engine.go` — In `selectAllEligibleEdges`, Step 3 now iterates `edges` (all edges) instead of `uncond`.
- `internal/attractor/engine/edge_selection_test.go` — Added `TestSelectNextEdge_SuggestedNextIDMatchesConditionalEdge` and `TestSelectAllEligibleEdges_SuggestedNextIDSearchesAllEdges`.

**Status: FIXED**

### V3.5 — `StatusSkipped` treated as success (resets retry counter)

**Spec says (§3.5):** Only `SUCCESS` and `PARTIAL_SUCCESS` reset the retry counter and return immediately.

**Code:** `engine.go:1092`:
```go
if out.Status == runtime.StatusSuccess || out.Status == runtime.StatusPartialSuccess || out.Status == runtime.StatusSkipped {
    retries[node.ID] = 0
    return out, nil
}
```

`SKIPPED` is not mentioned in the spec's retry pseudocode. This adds an outcome path not accounted for in the spec.

**Analysis:** No built-in handler ever returns `StatusSkipped` — it only enters via external agents writing `status.json` with `{"status":"skipped"}`. The spec's own definition (§5.2) says SKIPPED means "proceed without recording an outcome" — "proceed" is success-like. If SKIPPED were removed from the success check, it would fall through to retry logic where `shouldRetryOutcome` returns `false` (SKIPPED is neither FAIL nor RETRY), the loop would exit, and the outcome would be converted to FAIL — breaking real workflows including `semport.dot`. One inconsistency: `checkGoalGates` at `engine.go:1674` does NOT accept SKIPPED, which is actually correct — a skipped goal gate genuinely hasn't been achieved.

**Recommendation:** Update spec §3.5 to include SKIPPED: `IF outcome.status IN {SUCCESS, PARTIAL_SUCCESS, SKIPPED}: reset_retry_counter(node.id); RETURN outcome`. Document that SKIPPED does not satisfy goal gates.

**Fix Plan:**
- Step 1: In spec §3.5 (`attractor-spec.md` line 497), update the retry pseudocode to include SKIPPED in the success-like status set: change `IF outcome.status IN {SUCCESS, PARTIAL_SUCCESS}:` to `IF outcome.status IN {SUCCESS, PARTIAL_SUCCESS, SKIPPED}:`.
- Step 2: Add a clarifying note after the §3.5 pseudocode block explaining that SKIPPED is treated as success-like for retry/routing purposes (the node executed and decided to skip — this is not a failure), but SKIPPED does NOT satisfy goal gates (§3.4) because a skipped node has not actually achieved its goal.
- Step 3: Run engine tests to verify no regressions (spec-only change, tests should pass unchanged).
- Step 4: Mark V3.5 as FIXED in the audit doc.

**Files Modified:**
- `docs/strongdm/attractor/attractor-spec.md` — Update §3.5 retry pseudocode to include SKIPPED in the success-like status set; add clarifying note about SKIPPED vs goal gates
- `spec-compliance-audit.md` — Add fix plan and FIXED status to V3.5

**Status: FIXED**

### V3.6 — Retry logic gates on failure classification, not just RETRY status

**Spec says (§3.5):** On `RETRY` status: retry if attempts remain. On `FAIL`: return immediately, no retry.

**Code:** `engine.go:1107-1143` and `failure_policy.go`: Classifies failures into classes (`transient_infra`, `budget_exhausted`, `compilation_loop`, `deterministic`, etc.) and only retries if the class is in the retryable set. A `RETRY` status with `deterministic` class is NOT retried. A `FAIL` status with `transient_infra` class IS retried. This fundamentally changes retry semantics from the spec.

**Analysis (covers V3.6 + V4.3 + V4.9):** The codebase has a clear inversion: handlers return `StatusRetry` as a generic "something went wrong," then the failure classification system compensates by filtering out non-retryable RETRYs. The smoking gun: `shouldRetryOutcome` treats FAIL and RETRY identically — gating entirely on `failureClass`. The RETRY/FAIL distinction carries zero information. There are exactly 4 places returning `StatusRetry`: (1) `handlers.go:275` CodergenHandler API errors — already calls `classifyAPIError` and knows the class but returns RETRY regardless; (2) `engine.go:953` Go error conversion — defaults to RETRY for all errors including permanent ones like "missing llm_provider"; (3) `handlers.go:382` human gate timeout — legitimately transient, keep as RETRY; (4) test code. The CLI backend (`codergen_router.go:878-891`) already does it right: returns `StatusFail` with `failure_class` metadata. Nothing would break changing RETRY→FAIL because classification already treats them identically.

**Recommendation:** Fix at the source. (1) `handlers.go:275`: use `classifyAPIError` result to set `StatusFail` (deterministic) or `StatusRetry` (transient). (2) `engine.go:953`: default to `StatusFail`. (3) Keep failure classification as validation/safety-net, not primary decision-maker. (4) Leave human gate timeout as RETRY. Update spec §4.5 and §4.12 to document that handlers should set semantically correct status with `failure_class` metadata.

**Fix Plan:**
- Step 1: In `handlers.go:275-282`, change the CodergenHandler API error path to use `classifyAPIError`'s result to choose the correct status: `StatusFail` for deterministic failures, `StatusRetry` for transient failures. The `classifyAPIError` call already exists at line 276 and returns the failure class — we just need to use it to set the status.
- Step 2: In `engine.go:939`, change the Go error conversion from `StatusRetry` to `StatusFail`. Handler errors (Go `error` returns) are unexpected failures that should not automatically retry. The failure classification safety net will still promote transient errors to retryable if appropriate.
- Step 3: Leave `handlers.go:382` (human gate timeout) as `StatusRetry` — this is legitimately transient.
- Step 4: Update `retry_on_retry_status_test.go` — the `retryThenSuccessHandler` returns `StatusRetry` with failure reason "try again". Under the new semantics, this should still work because "try again" matches the `transientInfraReasonHints` heuristic, so `classifyFailureClass` will classify it as `transient_infra` and `shouldRetryOutcome` will allow the retry. Verify no change needed.
- Step 5: Update `retry_failure_class_test.go:266` (`TestRun_UnclassifiedDeterministicFailure_DoesNotRetry`) — this test uses `StatusRetry` with no failure_class hint. The test verifies that unclassified deterministic failures don't retry. Since the engine's `executeNode` (line 939) now converts Go errors to `StatusFail`, and this test's handler returns outcomes directly (not Go errors), the test still exercises the `shouldRetryOutcome` path. The status `StatusRetry` is still valid in this test context since the handler explicitly returns it. Verify no change needed.
- Step 6: Update spec §4.5 (`attractor-spec.md`) to document that the CATCH block should use failure classification to set semantically correct status (FAIL for deterministic, RETRY for transient) with `failure_class` metadata.
- Step 7: Update spec §4.12 (`attractor-spec.md`) to document that handler exceptions/panics are converted to FAIL (not RETRY), and that handlers should set semantically correct status with `failure_class` metadata.
- Step 8: Update spec §3.5 to add a note that the retry predicate considers failure classification: RETRY status with deterministic failure class should not be retried, and FAIL status with transient failure class may be retried. This documents the safety-net behavior.
- Step 9: Run full engine test suite. Mark V3.6, V4.3, and V4.9 as FIXED.

**Files Modified:**
- `internal/attractor/engine/handlers.go` — Change CodergenHandler API error path to set status based on failure classification (line 278): deterministic errors now return `StatusFail`, transient errors return `StatusRetry`
- `internal/attractor/engine/engine.go` — Change Go error conversion from `StatusRetry` to `StatusFail` (line 939)
- `docs/strongdm/attractor/attractor-spec.md` — Update §4.5 CATCH block to document error classification and semantically correct status; §4.12 handler contract to require FAIL for exceptions and recommend `failure_class` metadata; §3.5 note documenting failure classification as safety net for retry gating; §11.5 to clarify retry gating on failure class
- `spec-compliance-audit.md` — Add fix plan and FIXED status to V3.6, V4.3, V4.9

**Status: FIXED** (also resolves V4.3 and V4.9; resolves V11.1 spec-internal contradiction)

---

## Section 4: Node Handlers

### V4.1 — WaitHumanHandler ignores `human.default_choice` on timeout

**Status: FIXED**

**Spec says (§4.6):** On TIMEOUT, check `node.attrs["human.default_choice"]`. If set, use it. Only return RETRY if no default exists.

**Fix:** When `ans.TimedOut` is true, the handler now reads `node.Attr("human.default_choice", "")` and attempts to match it (case-insensitively) against the available options by accelerator key or target node ID. If a match is found, the handler returns SUCCESS with that option selected (including `SuggestedNextIDs`, `PreferredLabel`, and `ContextUpdates`). If no match or empty, it returns RETRY with failure reason `"human gate timeout, no default"`.

**Fix plan:**
1. In `handlers.go` WaitHumanHandler.Execute, replace the unconditional RETRY on timeout with a check for `human.default_choice`.
2. Reuse the same case-insensitive key/target matching logic used for normal answer processing.
3. Added table-driven test `TestWaitHumanHandler_TimeoutWithDefaultChoice` covering: key match, target-node-ID match, case-insensitive match, empty default (RETRY), and non-matching default (RETRY).

**Files changed:** `handlers.go` (lines 427-444), `wait_human_test.go` (new test + helper).

### V4.2 — WaitHumanHandler stores target node ID instead of accelerator key

**Spec says (§4.6):** `context_updates={"human.gate.selected": selected.key}`

**Code:** `handlers.go:449`:
```go
"human.gate.selected": selected.To,  // stores node ID, spec says accelerator key
```

**Analysis:** The code stores the target node ID (e.g., `"fix"`) while the spec says to store the accelerator key (e.g., `"F"`). The accelerator key is a transient UI shortcut extracted from edge label patterns like `[F] Fix` — it has no durable semantic meaning and is only useful during the interactive selection. The target node ID is stable, matches the graph structure, and is far more useful for downstream condition expressions (e.g., `condition="human.gate.selected=fix"`), debugging, and observability. Nothing downstream currently reads `human.gate.selected` from context, so this is purely informational context. Routing is already handled by `SuggestedNextIDs: []string{selected.To}`, so `human.gate.selected` is supplementary. The code also stores `human.gate.label` (the full edge label), which already captures the human-readable selection. Storing the accelerator key alongside the label and the node ID would be redundant and less useful than the node ID.

**Recommendation:** The code's approach is better. Update the spec to use `selected.to` instead of `selected.key`.

**Fix Plan:**
- Step 1: In spec §4.6 (`attractor-spec.md` line 780), change `"human.gate.selected": selected.key` to `"human.gate.selected": selected.to` with a comment explaining it stores the target node ID.
- Step 2: No code changes needed — the code is already correct.
- Step 3: Verify the existing test (`wait_human_test.go`) passes, confirming the code stores the node ID.

**Files Modified:**
- `docs/strongdm/attractor/attractor-spec.md` — §4.6: changed `selected.key` to `selected.to` in `human.gate.selected` context update; added clarifying comment

**Status: FIXED**

### V4.3 — Codergen backend errors return RETRY instead of FAIL

**Spec says (§4.5):** `CATCH exception: RETURN Outcome(status=FAIL, failure_reason=str(exception))`

**Code:** `handlers.go:275-282`:
```go
if err != nil {
    return runtime.Outcome{
        Status:        runtime.StatusRetry,  // spec says FAIL
        FailureReason: err.Error(),
    }, nil
}
```

**Status: FIXED** (resolved as part of V3.6 fix — CodergenHandler now uses `classifyAPIError` result to set `StatusFail` for deterministic errors and `StatusRetry` for transient errors; spec §4.5 updated to document error classification)

### V4.4 — Simulated backend response text does not match spec format

**Spec says (§4.5):** `"[Simulated] Response for stage: " + node.id`

**Code:** `handlers.go:194`:
```go
return "[Simulated] " + prompt, &out, nil  // uses full prompt text, not node ID
```

**Analysis:** The `prompt` parameter passed to `SimulatedCodergenBackend.Run()` is the fully assembled prompt text — including fidelity preamble, status contract preamble, and the node's prompt/label content. This can be hundreds or thousands of characters. The simulated response is written to `response.md` (line 331) and potentially truncated into the `last_response` context key (lines 370, 380). Nothing in the codebase parses or matches against the `[Simulated]` prefix or the response content — tests only check for the *existence* of `response.md`, never its content. The current approach echoes the full prompt into `response.md`, which duplicates `prompt.md` in the same directory and adds zero new information. The spec's approach — a short, predictable string with the node ID — is cleaner, avoids duplication, and makes simulated responses immediately identifiable.

**Recommendation:** Change the code to match the spec. The spec format is superior: concise, non-duplicative, and includes the node ID for traceability.

**Fix Plan:**
- Step 1: In `handlers.go`, change `SimulatedCodergenBackend.Run()` to return `"[Simulated] Response for stage: " + node.ID` instead of `"[Simulated] " + prompt`. Suppress the unused `prompt` parameter (change `_ = node` to `_ = prompt`).
- Step 2: Run engine test suite to verify no regressions.

**Files Modified:**
- `internal/attractor/engine/handlers.go` — Changed `SimulatedCodergenBackend.Run()` response from prompt echo to spec-compliant `"[Simulated] Response for stage: " + node.ID`

**Status: FIXED**

### V4.5 — ConditionalHandler is not a no-op; it propagates previous status

**Spec says (§4.7):** `RETURN Outcome(status=SUCCESS, notes="Conditional node evaluated: " + node.id)` — always SUCCESS.

**Code:** `handlers.go:117-150`: Reads previous outcome/preferred_label/failure_reason from context and returns them as the conditional node's own outcome. If the previous node failed, the conditional node also reports non-SUCCESS:
```go
prevStatus := runtime.StatusSuccess
if st, err := runtime.ParseStageStatus(exec.Context.GetString("outcome", "")); err == nil && st != "" {
    prevStatus = st
}
return runtime.Outcome{
    Status:         prevStatus,       // could be FAIL, RETRY, etc.
    PreferredLabel: prevPreferred,
    ...
}, nil
```

This is likely an intentional design choice to make condition routing work with the previous node's status, but it contradicts the explicit spec pseudocode.

**Analysis:** `cond.resolveKey("outcome")` in `cond/cond.go:80-85` resolves `outcome` from the `Outcome` struct's `.Status` field, NOT from the context. If the conditional handler returned SUCCESS per spec, `condition="outcome=fail"` on outgoing edges would **never match**. This would break every pipeline using diamond nodes: `reference_template.dot`, `rogue.dot`, `rogue-spark.dot`, `green-test-vague.dot` (all use `check_impl [shape=diamond]` with `condition="outcome=success"` / `condition="outcome=fail"` edges). To make the spec approach work, you'd need to special-case conditional nodes in edge selection — same complexity, worse location. The current approach's downsides are already mitigated: `executeWithRetry` skips retries for conditional nodes; no pipeline sets `goal_gate=true` on diamonds. The spec's own §4.7 (always SUCCESS) and §11.6 ("passes through") contradict each other — code matches §11.6.

**Recommendation:** Keep pass-through. Update spec §4.7 to document the pass-through design and explain that it's necessary because `resolveKey("outcome")` uses the current node's Outcome struct.

**Fix Plan:**
- Step 1: Update spec §4.7 to replace the always-SUCCESS pseudocode with pass-through pseudocode that reads `outcome`, `preferred_label`, and `failure_reason` from context and returns them as the handler's own Outcome. Add an explanatory comment in the pseudocode explaining that `resolve_key("outcome")` (§10) reads from the Outcome struct, not from context, making pass-through essential.
- Step 2: Update the prose in §4.7 to describe the handler as a pass-through routing point rather than a no-op, and explain why this design is necessary.
- Step 3: Update §11.6's conditional handler checklist item to explicitly say "passes through previous node's outcome" with a cross-reference to §4.7, aligning it with the updated §4.7 and eliminating the contradiction.

**Files Modified:**
- `docs/strongdm/attractor/attractor-spec.md` -- §4.7: replaced always-SUCCESS pseudocode with pass-through design; added explanatory prose about why pass-through is required. §11.6: updated checklist item to reference pass-through behavior and §4.7.

**Status: FIXED** (also resolves V11.4)

### V4.6 — ParallelHandler does not implement join_policy or error_policy

**Spec says (§4.8):** Read `join_policy` (wait_all, k_of_n, first_success, quorum) and `error_policy` (fail_fast, continue, ignore) from node attributes.

**Code:** `parallel_handlers.go:81-116`: Always waits for all branches, always returns SUCCESS regardless of branch outcomes, never reads join_policy or error_policy. Only `max_parallel` is configurable.

**Analysis:**

The current `ParallelHandler.Execute()` (lines 81-116) and `dispatchParallelBranches()` (lines 122-204) implement a fixed strategy:
1. Wait for ALL branches to complete (implicit `wait_all` + `continue`)
2. Always return `StatusSuccess` regardless of individual branch outcomes
3. Delegate outcome evaluation entirely to the downstream `FanInHandler` (lines 448-514), which uses `selectHeuristicWinner()` to pick the best branch and fast-forward the main git branch

This is architecturally sound for v1: the fan-in handler already handles the "what do we do with the results?" question via heuristic selection. The ParallelHandler's job is fan-out orchestration; the FanInHandler's job is result evaluation. The spec conflates these two responsibilities by putting join_policy evaluation in the ParallelHandler.

**Scope estimate for full implementation:**

| Component | Lines | Complexity |
|-----------|-------|------------|
| Read `join_policy`/`error_policy` from node attrs | ~10 | Low |
| `wait_all` outcome evaluation (SUCCESS vs PARTIAL_SUCCESS) | ~20 | Low |
| `error_policy=fail_fast` (per-branch cancellation) | ~50-60 | High — requires refactoring `dispatchParallelBranches` worker pool to use per-branch derived contexts, a results channel, and a monitor goroutine that cancels remaining branches on first failure |
| `first_success` join policy (early termination) | ~40-50 | High — similar cancellation machinery, plus result streaming instead of fixed-size array |
| `k_of_n` join policy | ~30 | Medium — needs additional `k` node attribute, counting, early termination |
| `quorum` join policy | ~30 | Medium — needs `quorum_fraction` attribute, counting, early termination |
| `error_policy=ignore` (filter failed results) | ~10 | Low |
| Tests for each join_policy x error_policy combo | ~200+ | Medium — 4 join policies x 3 error policies = 12 combos, plus edge cases |
| **Total** | **~400-450** | **High** |

**Key technical challenges:**
1. **Per-branch cancellation:** `fail_fast`, `first_success`, and `k_of_n` all require cancelling in-flight branches. The current worker pool (`dispatchParallelBranches` lines 169-193) uses a shared `context.Context` and a fixed `[]parallelBranchResult` array. Cancellation requires per-branch `context.WithCancel`, a results channel, and a monitor goroutine — a significant refactor of the worker pool.
2. **Git worktree cleanup on cancellation:** Cancelled branches leave behind git worktrees. The cleanup path (currently implicit via test `t.TempDir()`) needs explicit cleanup for cancelled branches.
3. **Interaction with fan-in:** The `FanInHandler` currently assumes all branches ran to completion. `first_success` or `fail_fast` would produce incomplete branch results. The fan-in handler's `selectHeuristicWinner()` would need to handle partial result sets.
4. **Interaction with CXDB:** Each branch forks a CXDB context (line 363-367). Cancelled branches would leave orphaned CXDB contexts.

**Recommendation: DEFER (option c)**

This is a new feature, not a bug fix. The current behavior (wait_all + continue + always SUCCESS) is correct and functional — the fan-in handler compensates by doing heuristic evaluation. No existing pipeline uses `join_policy` or `error_policy` attributes. The spec has been updated to document v1 behavior and mark the advanced policies as v2.

**Implementation plan for v2 (when needed):**
- Phase 1: Add `wait_all` outcome evaluation to ParallelHandler (return PARTIAL_SUCCESS when branches fail). Low risk, ~20 lines.
- Phase 2: Add `error_policy=fail_fast` with per-branch cancellation. Requires worker pool refactor. ~60 lines + tests.
- Phase 3: Add `first_success` join policy with early termination. Builds on Phase 2 cancellation. ~50 lines + tests.
- Phase 4: Add `k_of_n` and `quorum`. Builds on Phase 3. ~60 lines + tests.
- Phase 5: Add `error_policy=ignore` (filter failed results). ~10 lines + tests.

**Files Modified:**
- `docs/strongdm/attractor/attractor-spec.md` — §4.8: Documented v1 behavior (wait_all + continue + always SUCCESS); added v2 pseudocode and marked join_policy/error_policy tables with implementation status per policy.

**Status: FIXED** — All 4 join policies (wait_all, first_success, k_of_n, quorum) and all 3 error policies (continue, fail_fast, ignore) are now implemented in `parallel_policy.go`. ParallelHandler.Execute reads policies from node attributes, evaluates aggregate outcomes via `evaluateJoinPolicy`, and filters results via `filterResultsByErrorPolicy`. Early termination support via `dispatchParallelBranchesWithPolicy` with cancellable context. 30 unit tests in `parallel_policy_test.go`.

### V4.7 — ManagerLoopHandler is a stub

**Spec says (§4.11):** Full implementation: observation loop, child pipeline management, poll_interval (45s default), max_cycles (1000 default), autostart, observe/steer/wait actions.

**Code:** `parallel_handlers.go:516-523`:
```go
type ManagerLoopHandler struct{}

func (h *ManagerLoopHandler) Execute(ctx context.Context, exec *Execution, node *model.Node) (runtime.Outcome, error) {
    _ = ctx
    _ = exec
    _ = node
    return runtime.Outcome{Status: runtime.StatusFail, FailureReason: "stack.manager_loop not implemented in v1"}, nil
}
```

**Analysis:**

The handler is registered in the handler registry (`handlers.go:75`) and the `house` shape correctly maps to `stack.manager_loop` (`handlers.go:123`), so the wiring is in place. However, the Execute method is a complete stub that immediately returns FAIL with an explanatory message. No part of the spec's §4.11 pseudocode is implemented.

*Scope estimate:* A full implementation would require approximately 300-500 lines of new Go code across 2-3 files, plus 200-300 lines of tests:

| Component | Lines | Notes |
|-----------|-------|-------|
| `ManagerLoopHandler.Execute` body | ~150-200 | Parse `manager.poll_interval`, `manager.max_cycles`, `manager.stop_condition`, `manager.actions` from node attrs; implement the observe/steer/wait cycle loop; evaluate stop conditions; return appropriate outcomes |
| Sub-pipeline lifecycle management | ~100-150 | `start_child_pipeline(child_dotfile)` -- load and parse a child DOT file, spawn a child engine execution (analogous to `ParallelHandler.runBranch` creating a `branchEng`), manage its lifecycle. No sub-pipeline launch infrastructure exists outside the parallel branch worktree model |
| Telemetry ingestion and steering | ~50-100 | `ingest_child_telemetry()` and `steer_child()` -- read the child pipeline's progress/status mid-execution and write intervention instructions to the child's stage directory. Steer cooldown mechanism adds state tracking |
| Tests | ~200-300 | Unit tests for loop logic, integration tests for child pipeline orchestration, edge cases (max_cycles exceeded, child failure propagation, stop condition evaluation) |

*Dependencies -- what needs to exist first:*
1. **Sub-pipeline execution model:** The engine supports parallel branch execution via git worktrees (`ParallelHandler.runBranch`), but has no concept of loading a separate DOT file as a child pipeline (`stack.child_dotfile`). This is a fundamentally new execution model.
2. **Child telemetry ingestion:** No `ingest_child_telemetry` function exists. The parallel handler collects results *after* branch completion, but the manager loop needs to *poll a running child's telemetry mid-execution*.
3. **Steer mechanism:** No `steer_child` function or steer cooldown tracking exists. Writing intervention instructions to a running pipeline's stage directory requires a protocol for the child engine to detect and act on mid-execution interventions.
4. **Condition evaluator integration:** The `evaluate_condition` in the stop condition check would need to integrate with the existing condition evaluator (used by ConditionalHandler), but applied to arbitrary context keys rather than edge labels.
5. **Pipeline composition infrastructure (§9.4):** The spec's §9.4 explicitly names the manager loop as an example of the sub-pipeline composition pattern. This broader infrastructure pattern is also unimplemented.

*Priority:* **Low.** No existing DOT pipeline in the repository uses the `house` shape or `stack.manager_loop` type. No tests reference ManagerLoopHandler. The stub's FAIL return with a descriptive message is the correct behavior for an unimplemented handler -- any pipeline that includes a manager loop node gets a clear, immediate error rather than silent misbehavior. This feature represents a supervisor/orchestration pattern that is architecturally distinct from the current single-pipeline execution model.

*Recommendation:* The spec should document this as planned for v2. The current stub behavior (returning FAIL with a clear message) is appropriate. The handler registration and shape mapping are already in place, so a future implementation only needs to fill in the Execute method and build the supporting sub-pipeline infrastructure.

**Files Modified:**
- `docs/strongdm/attractor/attractor-spec.md` -- §4.11: Added note that ManagerLoopHandler is planned for v2, currently returns FAIL with descriptive message.

**Status: FIXED** — ManagerLoopHandler now implements the spec §4.11 observation loop in `manager_loop.go`. Supports configurable poll_interval (45s default), max_cycles (1000 default), stop_condition evaluation via `cond.Evaluate`, and action support (observe, wait). Child pipeline execution uses `Prepare` + `runSubgraphUntil` infrastructure from parallel branches. Auto-start child from `stack.child_dotfile` attribute. Steer action logged as deferred to v2. 9 unit tests in `manager_loop_test.go`.

### V4.8 — FanIn heuristic sort uses BranchKey instead of score

**Spec says (§4.9):** `SORT candidates BY (outcome_rank, -score, id)`

**Code:** `parallel_handlers.go:577-587`: Sorts by `(status rank, BranchKey, HeadSHA)`. No `score` field exists. Tiebreaking uses git-specific metadata instead of the spec's scoring mechanism.

**Analysis:** The spec's `heuristic_select` uses `-c.score` as the secondary sort key, but no `score` field exists on `parallelBranchResult`, `runtime.Outcome`, or any related type. A grep for `score` across the entire attractor package returns zero matches in Go source (excluding test comments and model database JSON). The "score" concept is a spec fiction with no implementation and no clear semantic meaning in the git-based parallel workflow — branches are git worktrees that execute subgraphs and return an Outcome with no numeric score. The code's tiebreaker (`BranchKey` ascending, then `HeadSHA` ascending) provides deterministic lexicographic ordering among equally-ranked candidates. The code's `BranchKey` maps directly to the spec's `c.id` (candidate identifier). The `HeadSHA` is a git-specific final tiebreaker with no spec equivalent — it ensures full determinism when branch keys collide.

The code also pre-filters candidates to exclude FAIL outcomes before sorting (lines 568-573), which the spec's pseudocode does not show. This is correct: the spec says "Fan-in runs even when some candidates failed, as long as at least one candidate is available. Only when all candidates fail does fan-in return FAIL." The pre-filter implements this requirement — FAIL branches are excluded from winner selection, and the `ok=false` return triggers the all-fail path.

**Recommendation:** Update the spec to match the code. The code's sort order is pragmatically correct. The `score` concept should be removed from the spec since no scoring mechanism exists or is needed. The spec should also document the non-fail pre-filter.

**Fix Plan:**
- Step 1: In spec §4.9 (`attractor-spec.md`), update `heuristic_select` pseudocode to: (a) add explicit non-fail filtering before sorting; (b) replace `SORT candidates BY (outcome_rank[c.outcome], -c.score, c.id)` with `SORT non_fail BY (outcome_rank[c.outcome], c.branch_key, c.head_sha)`; (c) add a note explaining why `score` was removed and what the tiebreakers mean.
- Step 2: Update the spec's context_updates to include `parallel.fan_in.best_head_sha` and `parallel.fan_in.losers` (which the code produces but the spec omitted).
- Step 3: Update the fan-in prose to describe the all-fail path with failure class aggregation.
- Step 4: No code changes needed — the code is already correct.
- Step 5: Run engine test suite to verify no regressions (spec-only change).

**Files Modified:**
- `docs/strongdm/attractor/attractor-spec.md` — §4.9: replaced `heuristic_select` pseudocode with non-fail pre-filter and `(outcome_rank, branch_key, head_sha)` sort order; removed phantom `score` field; updated context_updates to include `best_head_sha` and `losers`; added explanatory note on sort order rationale; updated prose to describe all-fail path with failure class aggregation

**Status: FIXED**

### V4.9 — Engine converts handler Go errors to RETRY instead of FAIL

**Spec says (§4.12):** "Handler panics/exceptions MUST be caught by the engine and converted to FAIL outcomes."

**Code:** `engine.go:912-939`: Panics are correctly converted to FAIL (line 917). But Go error returns are converted to RETRY:
```go
if err != nil {
    out.Status = runtime.StatusRetry  // spec says FAIL
    out.FailureReason = err.Error()
}
```

**Status: FIXED** (resolved as part of V3.6 fix — Go error returns now converted to `StatusFail`; spec §4.12 updated to explicitly state "FAIL outcomes (not RETRY)" and document `failure_class` metadata)

---

## Section 5: State and Context

### V5.1 — Missing built-in context key `internal.retry_count.<node_id>`

**Spec says (§5.1):** The engine must set the built-in context key `internal.retry_count.<node_id>` (Integer).

**Code does:** The engine tracks retry counts in a local `nodeRetries map[string]int` variable and saves them into the checkpoint's `NodeRetries` field, but **never sets** the `internal.retry_count.<node_id>` key in the Context.

**Impact:** Any edge condition or downstream node that references `internal.retry_count.<node_id>` from the context will always get a missing/default value.

**Fix Plan:**
- Step 1: In `engine.go` `runLoop`, immediately after setting `current_node` and `completed_nodes` in context, set `internal.retry_count.<current>` to the current value from `nodeRetries[current]` (0 on first visit). This ensures the key is always present in context, even when no retries have occurred.
- Step 2: In `engine.go` `executeWithRetry`, immediately after `retries[node.ID]++` (when a retry is about to happen), update `internal.retry_count.<node_id>` in context with the incremented count. This ensures the key reflects the latest retry count during subsequent attempts.
- Step 3: Add test `TestRun_InternalRetryCountContextKey` using a custom handler that reads the context key at each attempt, verifying: (a) the key is 0 at the first attempt, (b) the key is 1 after one retry, (c) the checkpoint context snapshot contains the correct value.
- Step 4: Add test `TestRun_InternalRetryCountContextKey_NoRetries` verifying that the key is 0 in the checkpoint when no retries occur.
- Step 5: Run full engine test suite to verify no regressions.

**Files Modified:**
- `internal/attractor/engine/engine.go` — Added `e.Context.Set(fmt.Sprintf("internal.retry_count.%s", current), nodeRetries[current])` in `runLoop` after setting `current_node`; added `e.Context.Set(fmt.Sprintf("internal.retry_count.%s", node.ID), retries[node.ID])` in `executeWithRetry` after incrementing `retries[node.ID]`.
- `internal/attractor/engine/retry_count_context_test.go` (new) — Two tests: `TestRun_InternalRetryCountContextKey` (verifies context key values across retry attempts and in checkpoint) and `TestRun_InternalRetryCountContextKey_NoRetries` (verifies key is 0 with no retries).

**Status: FIXED**

### V5.2 — Checkpoint JSON field name mismatch: `"context"` vs `"context_values"`

**Spec says (§5.3):** The Checkpoint struct has a field `context_values : Map<String, Any>`.

**Code does:** `runtime/checkpoint.go:18`:
```go
ContextValues  map[string]any `json:"context"`
```

The JSON tag is `"context"`, not `"context_values"`.

**Impact:** External tools or cross-implementation resume systems expecting `"context_values"` in the checkpoint JSON will fail.

**Analysis:** On closer inspection, this is NOT a true violation. The spec's own `save()` pseudocode at §5.3 line 1172 uses `"context"` as the JSON key: `"context": serialize_to_json(context_values)`. The code's `json:"context"` tag matches the spec's serialization format exactly. The confusion arises because the spec uses `context_values` as the struct field name but `"context"` as the JSON key -- the same pattern the Go code uses (`ContextValues` field with `json:"context"` tag). No external tool reading the spec's save pseudocode would expect `"context_values"` in the JSON.

However, the spec's naming was confusing -- having the struct field called `context_values` but the JSON key called `"context"` invites exactly this kind of misreading. To eliminate the ambiguity, the spec's struct field has been renamed from `context_values` to `context` to match the JSON key.

**Fix Plan:**
- Step 1: Rename the spec's Checkpoint struct field from `context_values` to `context` (matching the JSON key in the save pseudocode).
- Step 2: Update all references to `context_values` in the spec's §5.3 resume behavior to `context`.
- Step 3: No code changes needed -- the code's JSON tag `"context"` already matches the spec's serialization format.

**Files Modified:**
- `docs/strongdm/attractor/attractor-spec.md` -- §5.3: renamed Checkpoint struct field from `context_values` to `context`; updated save pseudocode and resume behavior references.

**Status: FIXED** (spec clarification -- no code change needed; code was already correct)

### V5.3 — `Clone()` performs a shallow copy, not a deep copy

**Spec says (§5.1):** `clone()` — deep copy for parallel branch isolation.

**Code does:** `runtime/context.go:71-80`:
```go
func (c *Context) Clone() *Context {
    c.mu.RLock()
    defer c.mu.RUnlock()
    out := NewContext()
    for k, v := range c.values {
        out.values[k] = v   // shallow copy — no deep clone
    }
    out.logs = append(out.logs, c.logs...)
    return out
}
```

**Impact:** Parallel branches that mutate nested values (slices, maps) will experience data races and cross-contamination.

**Analysis:** The spec's `clone()` comment says "Deep copy for parallel branch isolation" but the pseudocode used `shallow_copy(values)` -- an internal spec contradiction. The code matched the pseudocode (shallow copy) but violated the stated intent (deep copy). Context values are required to be JSON-serializable (stated in the `Context` struct doc comment), so a JSON round-trip is a reliable deep copy mechanism. Primitive types (strings, ints, bools, floats) are immutable and can safely skip the round-trip for performance.

**Fix Plan:**
- Step 1: Implement deep copy in `Context.Clone()` using JSON round-trip for composite values (maps, slices). Add a fast path for primitive types that don't need deep copying (strings, ints, bools, floats). Add a `deepCopyValue()` helper function.
- Step 2: Update spec §5.1 `clone()` pseudocode to use `deep_copy(values)` instead of `shallow_copy(values)`, with comments explaining the JSON round-trip mechanism and the primitive fast path.
- Step 3: Add tests: `TestContext_Clone_DeepCopiesNestedValues` (verifies that mutating the original's nested maps and slices does not affect the clone) and `TestContext_Clone_NilValue` (verifies nil values are preserved).
- Step 4: Run runtime and engine test suites to verify no regressions.

**Files Modified:**
- `internal/attractor/runtime/context.go` -- Replaced shallow copy in `Clone()` with deep copy via `deepCopyValue()` helper; added `encoding/json` import; added fast path for immutable primitive types.
- `internal/attractor/runtime/context_test.go` -- Added `TestContext_Clone_DeepCopiesNestedValues` and `TestContext_Clone_NilValue` tests.
- `docs/strongdm/attractor/attractor-spec.md` -- §5.1: updated `clone()` pseudocode from `shallow_copy(values)` to `deep_copy(values)` with explanatory comments.

**Status: FIXED**

### V5.4 — No Artifact Store implementation

**Spec says (§5.5):** Named, typed storage for large stage outputs. Default file-backing threshold 100KB. Interface: store, retrieve, has, list, remove, clear.

**Code does:** No `ArtifactStore` type, interface, or implementation exists anywhere. Grep for `ArtifactStore` returns zero results in Go source.

**Impact:** Large stage outputs have no structured store/retrieve mechanism. The `artifacts/` subdirectory (spec §5.6) is also never created.

**Fix Plan:**
- Step 1: Create `ArtifactStore` type in `internal/attractor/engine/artifact_store.go` with all 6 spec methods (`Store`, `Retrieve`, `Has`, `List`, `Remove`, `Clear`) plus a convenience `Info` method.
- Step 2: Create `ArtifactInfo` type with `id`, `name`, `size_bytes`, `stored_at`, `is_file_backed`, and `content_hash` (SHA-256) fields.
- Step 3: Implement file-backing: artifacts exceeding the threshold (100KB default) are written to `{base_dir}/artifacts/{artifact_id}.json` per §5.6; smaller artifacts stay in memory.
- Step 4: Thread safety via `sync.RWMutex` per spec's `lock: ReadWriteLock`.
- Step 5: Add `Artifacts *ArtifactStore` field to `Engine` struct and `Execution` struct. Wire into all 3 Execution construction sites (main dispatch, implicit fan-out, resume).
- Step 6: Initialize in `newBaseEngine()` so all entry points (Run, RunWithConfig, Resume) get an artifact store automatically.
- Step 7: 15 unit tests covering all methods, file-backed storage, edge cases, and Execution integration.

**Files Modified:**
- `internal/attractor/engine/artifact_store.go` (new) -- `ArtifactStore` type with all 6+1 spec methods, `ArtifactInfo` type, `DefaultFileBackingThreshold` constant.
- `internal/attractor/engine/artifact_store_test.go` (new) -- 15 unit tests covering in-memory store/retrieve, file-backed store/retrieve, no-baseDir fallback, has, list (sorted + empty), remove (memory + file cleanup), clear (memory + file cleanup), retrieve-not-found, content hash determinism, store-replace, Info, and Execution integration.
- `internal/attractor/engine/engine.go` -- Added `Artifacts *ArtifactStore` field to `Engine` struct; wired into 2 Execution construction sites.
- `internal/attractor/engine/handlers.go` -- Added `Artifacts *ArtifactStore` field to `Execution` struct.
- `internal/attractor/engine/engine_bootstrap.go` -- Initialize `ArtifactStore` in `newBaseEngine()`.
- `internal/attractor/engine/resume.go` -- Wired `Artifacts` into resume Execution construction site.

**Status: FIXED**

### V5.5 — `last_stage` and `last_response` context keys not set on primary handler path

**Spec says (§5.1):** Built-in context keys `last_stage` and `last_response` are set by handlers.

**Code does:** The `CodergenHandler` only sets these in edge cases (auto_status=true with no status.json, or auto_status=false failure path). In the normal success path — when `status.json` exists or the backend returns an explicit outcome — these keys are never set. No other handler sets them either.

**Fix Plan:**
- Step 1: In `handlers.go` CodergenHandler.Execute, on the explicit outcome path (`if out != nil`), inject `last_stage` and `last_response` into `out.ContextUpdates` if not already present.
- Step 2: On the status.json exists path, include `last_stage` and `last_response` in the returned Outcome's `ContextUpdates`.
- Step 3: The auto_status and failure paths already set these — no change needed there.

**Files Modified:**
- `internal/attractor/engine/handlers.go` — Added `last_stage`/`last_response` context updates to the explicit outcome return path and the status.json exists path in `CodergenHandler.Execute`.

**Status: FIXED**

---

## Section 6: Human-in-the-Loop (Interviewer Pattern)

### V6.1 — Interviewer interface missing `ask_multiple` and `inform` methods

**Spec says (§6.1):**
```
INTERFACE Interviewer:
    FUNCTION ask(question: Question) -> Answer
    FUNCTION ask_multiple(questions: List<Question>) -> List<Answer>
    FUNCTION inform(message: String, stage: String) -> Void
```

**Code does (before fix):** Only `Ask` existed.

**Fix Plan:**
- Step 1: Add `AskMultiple(questions []Question) []Answer` and `Inform(message string, stage string)` to the `Interviewer` interface.
- Step 2: Implement on ALL concrete types: `AutoApproveInterviewer`, `ConsoleInterviewer`, `CallbackInterviewer`, `QueueInterviewer`, and the new `RecordingInterviewer`. Default `AskMultiple` loops over `Ask`. Default `Inform` is a no-op (except ConsoleInterviewer which prints to stdout).
- Step 3: Add tests: `TestAutoApproveInterviewer_AskMultiple`, `TestCallbackInterviewer_AskMultiple`, `TestConsoleInterviewer_Inform_WritesToOutput`.

**Files Modified:**
- `internal/attractor/engine/handlers.go` — Added `AskMultiple` and `Inform` to `Interviewer` interface; implemented on `AutoApproveInterviewer`.
- `internal/attractor/engine/interviewer_impls.go` — Implemented `AskMultiple` and `Inform` on `ConsoleInterviewer`, `CallbackInterviewer`, `QueueInterviewer`, and `RecordingInterviewer`.
- `internal/attractor/engine/interviewer_test.go` — Added tests for `AskMultiple` and `Inform`.

**Status: FIXED**

### V6.2 — Question struct missing `default`, `timeout_seconds`, and `metadata` fields

**Spec says (§6.2):** Question has fields: text, type, options, default, timeout_seconds, stage, metadata.

**Fix Plan:**
- Step 1: Add `Default *Answer`, `TimeoutSeconds float64`, and `Metadata map[string]any` fields to the `Question` struct.
- Step 2: ConsoleInterviewer.Ask now reads `q.TimeoutSeconds` to apply timeout via `readLineWithTimeout`.
- Step 3: Add test `TestQuestion_DefaultField` verifying the fields are usable.

**Files Modified:**
- `internal/attractor/engine/handlers.go` — Added `Default`, `TimeoutSeconds`, `Metadata` fields to `Question` struct.
- `internal/attractor/engine/interviewer_test.go` — Added `TestQuestion_DefaultField`.

**Status: FIXED**

### V6.3 — QuestionType enum uses non-spec names; `YES_NO` missing

**Spec says (§6.2):** `QuestionType: YES_NO, MULTIPLE_CHOICE, FREEFORM, CONFIRMATION`

**Code does:** Uses `SINGLE_SELECT`, `MULTI_SELECT`, `FREE_TEXT`, `CONFIRM` — matching §11.8 but not §6.2.

**Analysis:** The code's names are more precise and descriptive than §6.2's names. §11.8 already uses the code's names. The spec §6.2 was the outlier.

**Fix Plan:**
- Step 1: Update spec §6.2 to use the code's names: `SINGLE_SELECT`, `MULTI_SELECT`, `FREE_TEXT`, `CONFIRM`. Add `YES_NO` as a semantically distinct type for binary yes/no choices.
- Step 2: Add `QuestionYesNo QuestionType = "YES_NO"` to the code's enum constants.
- Step 3: Handle `QuestionYesNo` in `ConsoleInterviewer.Ask` (same behavior as `QuestionConfirm`).
- Step 4: Update spec §6.4 pseudocode to use new type names.
- Step 5: Add test `TestConsoleInterviewer_YesNo_ParsesLikeConfirm`.

**Files Modified:**
- `internal/attractor/engine/handlers.go` — Added `QuestionYesNo` constant.
- `internal/attractor/engine/interviewer_impls.go` — Added `QuestionYesNo` to the `ConsoleInterviewer.Ask` switch case (same branch as `QuestionConfirm`).
- `docs/strongdm/attractor/attractor-spec.md` — §6.2: replaced `MULTIPLE_CHOICE`, `FREEFORM`, `CONFIRMATION` with `SINGLE_SELECT`, `MULTI_SELECT`, `FREE_TEXT`, `CONFIRM`; kept `YES_NO`. Updated §6.4 pseudocode to use new names. Updated §11.8 to include `YES_NO`.
- `internal/attractor/engine/interviewer_test.go` — Added `TestConsoleInterviewer_YesNo_ParsesLikeConfirm`.

**Status: FIXED** (also resolves V11.3 — §6.2 and §11.8 now use identical names)

### V6.4 — Answer struct missing `selected_option`; uses booleans for SKIPPED/TIMEOUT

**Spec says (§6.3):** Answer has `value`, `selected_option`, `text`. SKIPPED/TIMEOUT are AnswerValue enum variants.

**Analysis:** The `TimedOut`/`Skipped` booleans are pragmatically better than enum variants — they allow unambiguous detection independent of the `value` field. The spec was updated to document the boolean approach.

**Fix Plan:**
- Step 1: Add `SelectedOption *Option` field to the `Answer` struct.
- Step 2: Update spec §6.3 to document the actual Answer model with `values`, `selected_option`, `timed_out`, and `skipped` fields.
- Step 3: Add test `TestAnswer_SelectedOptionField`.

**Files Modified:**
- `internal/attractor/engine/handlers.go` — Added `SelectedOption *Option` field to `Answer` struct.
- `docs/strongdm/attractor/attractor-spec.md` — §6.3: replaced AnswerValue enum with boolean `timed_out`/`skipped` fields; added `values` and `selected_option` fields; added explanatory prose.
- `internal/attractor/engine/interviewer_test.go` — Added `TestAnswer_SelectedOptionField`.

**Status: FIXED**

### V6.5 — QueueInterviewer returns empty Answer instead of SKIPPED when queue empty

**Spec says (§6.4):** QueueInterviewer returns SKIPPED when queue is empty.

**Fix Plan:**
- Step 1: In `QueueInterviewer.Ask`, change `return Answer{}` to `return Answer{Skipped: true}` when the queue is empty.
- Step 2: Update spec §6.4 QueueInterviewer pseudocode to use `Answer(skipped=true)`.
- Step 3: Add tests: `TestQueueInterviewer_EmptyQueue_ReturnsSkipped`, `TestQueueInterviewer_AskMultiple_ReturnsSKIPPEDWhenExhausted`.

**Files Modified:**
- `internal/attractor/engine/interviewer_impls.go` — Changed empty queue return from `Answer{}` to `Answer{Skipped: true}`.
- `docs/strongdm/attractor/attractor-spec.md` — §6.4: updated QueueInterviewer pseudocode to return `Answer(skipped=true)`.
- `internal/attractor/engine/interviewer_test.go` — Added `TestQueueInterviewer_EmptyQueue_ReturnsSkipped` and `TestQueueInterviewer_AskMultiple_ReturnsSKIPPEDWhenExhausted`.

**Status: FIXED**

### V6.6 — RecordingInterviewer does not exist

**Spec says (§6.4):** RecordingInterviewer wraps another interviewer and records all Q&A pairs.

**Fix Plan:**
- Step 1: Implement `RecordingInterviewer` struct wrapping an `Inner Interviewer` with a mutex-protected `Recordings []QAPair` slice.
- Step 2: Implement `Ask` (delegates to inner, records pair), `AskMultiple` (delegates to inner, records all pairs), `Inform` (delegates to inner without recording).
- Step 3: Add `QAPair` struct containing `Question` and `Answer`.
- Step 4: Add tests: `TestRecordingInterviewer_RecordsQAPairs`, `TestRecordingInterviewer_AskMultiple_RecordsAll`, `TestRecordingInterviewer_DelegatesToInner`.

**Files Modified:**
- `internal/attractor/engine/interviewer_impls.go` — Added `RecordingInterviewer` struct, `QAPair` struct, and `Ask`/`AskMultiple`/`Inform` methods.
- `internal/attractor/engine/interviewer_test.go` — Added 3 tests for RecordingInterviewer.

**Status: FIXED**

### V6.7 — ConsoleInterviewer has no timeout support

**Spec says (§6.4):** ConsoleInterviewer supports timeout via non-blocking read.

**Fix Plan:**
- Step 1: Add `readLineWithTimeout(f *os.File, timeout time.Duration) (string, bool)` helper function. When timeout > 0, reads in a goroutine and selects on the result channel vs `time.After`. When timeout <= 0, blocks normally.
- Step 2: Refactor all `bufio.NewReader(in).ReadString('\n')` calls in `ConsoleInterviewer.Ask` to use `readLineWithTimeout(in, timeout)` where `timeout` is derived from `q.TimeoutSeconds`.
- Step 3: Return `Answer{TimedOut: true}` when the timeout fires.
- Step 4: Update spec §6.4 ConsoleInterviewer pseudocode to document `read_input_with_timeout` and timeout behavior.
- Step 5: Add tests: `TestConsoleInterviewer_Timeout_ReturnsTimedOut` (100ms timeout with no input), `TestConsoleInterviewer_ZeroTimeout_BlocksNormally`.

**Files Modified:**
- `internal/attractor/engine/interviewer_impls.go` — Added `readLineWithTimeout` helper; refactored `ConsoleInterviewer.Ask` to use it; all cases now return `Answer{TimedOut: true}` on timeout.
- `docs/strongdm/attractor/attractor-spec.md` — §6.4: updated ConsoleInterviewer pseudocode with `read_input_with_timeout`, timeout parameter, and `TIMEOUT` return.
- `internal/attractor/engine/interviewer_test.go` — Added `TestConsoleInterviewer_Timeout_ReturnsTimedOut` and `TestConsoleInterviewer_ZeroTimeout_BlocksNormally`.

**Status: FIXED**

### V6.8 — Timeout handling does not check default answer

**Status: FIXED** (resolved by V4.1 fix)

**Spec says (§6.5):** On timeout: (1) if question has default answer, use it; (2) if no default, return TIMEOUT.

**Fix:** The V4.1 fix in `WaitHumanHandler.Execute` now checks `node.Attr("human.default_choice", "")` when `ans.TimedOut` is true, matching any available option before falling back to RETRY. This covers the same code path described here from the interviewer-pattern perspective.

---

## Section 7: Validation and Linting

### V7.1 — Diagnostic `edge` field split into two separate fields

**Spec says (§7.1):** `edge : (String, String) or NONE` — a single tuple.

**Code does:** `validate/validate.go:22-30`:
```go
type Diagnostic struct {
    Rule     string   `json:"rule"`
    Severity Severity `json:"severity"`
    Message  string   `json:"message"`
    NodeID   string   `json:"node_id,omitempty"`
    EdgeFrom string   `json:"edge_from,omitempty"`
    EdgeTo   string   `json:"edge_to,omitempty"`
    Fix      string   `json:"fix,omitempty"`
}
```

The information is present but the JSON shape differs from the spec (`edge_from`/`edge_to` vs a single `edge` object).

**Analysis:** The code's approach (two separate string fields) is arguably better for Go -- no need for a custom tuple type. The separate fields are cleaner for JSON serialization (`edge_from`/`edge_to` with `omitempty`) and more idiomatic for Go than a `[2]string` array or a custom struct.

**Fix Plan:**
- Step 1: Update spec §7.1 `Diagnostic` definition to use `edge_from : String or NONE` and `edge_to : String or NONE` instead of `edge : (String, String) or NONE`.
- Step 2: No code changes needed -- the code is already correct.

**Files Modified:**
- `docs/strongdm/attractor/attractor-spec.md` -- §7.1: changed `edge` field from tuple `(String, String)` to separate `edge_from` and `edge_to` fields.

**Status: FIXED**

### V7.2 — `type_known` lint rule is missing

**Spec says (§7.2):** Rule `type_known` (WARNING): node type values should be recognized by handler registry.

**Code does:** No `lintTypeKnown` function exists. Grep for `type_known` returns zero results in Go source.

**Impact:** Nodes with unrecognized `type` values silently fall through to the default codergen handler with no warning.

**Analysis:** The validate package should not import the engine package (circular dependency). Instead, the `type_known` rule is implemented as a `LintRule` (V7.4) that accepts a list of known type strings at construction time. The engine constructs it with `HandlerRegistry.KnownTypes()` and passes it to `Validate` via the `extra_rules` parameter (V7.3).

**Fix Plan:**
- Step 1: Add `TypeKnownRule` struct implementing the `LintRule` interface in `validate/validate.go`. Constructor `NewTypeKnownRule(knownTypes []string)` builds a lookup map.
- Step 2: Add `KnownTypes() []string` method to `HandlerRegistry` in `engine/handlers.go`.
- Step 3: Add tests: `TestValidate_TypeKnownRule_RecognizedType_NoWarning`, `TestValidate_TypeKnownRule_UnrecognizedType_Warning`, `TestValidate_TypeKnownRule_NoTypeOverride_NoWarning`.

**Files Modified:**
- `internal/attractor/validate/validate.go` -- Added `TypeKnownRule` struct with `Name()` and `Apply()` methods; added `NewTypeKnownRule()` constructor.
- `internal/attractor/engine/handlers.go` -- Added `KnownTypes() []string` method to `HandlerRegistry`.
- `internal/attractor/validate/validate_test.go` -- Added 3 tests for `TypeKnownRule`.

**Status: FIXED**

### V7.3 — `validate()` does not accept `extra_rules` parameter

**Spec says (§7.3):** `FUNCTION validate(graph, extra_rules=NONE) -> List<Diagnostic>`

**Code does:** `validate/validate.go:32`:
```go
func Validate(g *model.Graph) []Diagnostic {
```

No `extra_rules` parameter. Only hardcoded built-in rules.

**Fix Plan:**
- Step 1: Change `Validate` signature to `func Validate(g *model.Graph, extraRules ...LintRule) []Diagnostic`. Variadic parameter preserves backward compatibility with existing callers.
- Step 2: At the end of `Validate`, iterate `extraRules` and append their diagnostics. Skip nil rules.
- Step 3: Update `ValidateOrError` signature to accept and forward `...LintRule`.
- Step 4: Add tests: `TestValidate_ExtraRules_AreAppendedToBuiltInRules`, `TestValidate_ExtraRules_NilRulesIgnored`.

**Files Modified:**
- `internal/attractor/validate/validate.go` -- Changed `Validate` and `ValidateOrError` signatures to accept `...LintRule`; added extra-rules iteration at end of `Validate`.
- `internal/attractor/validate/validate_test.go` -- Added 2 tests for extra_rules parameter.

**Status: FIXED**

### V7.4 — Custom lint rule interface (`LintRule`) is missing

**Spec says (§7.4):**
```
INTERFACE LintRule:
    name : String
    FUNCTION apply(graph) -> List<Diagnostic>
```

**Code does:** No `LintRule` interface exists. Grep returns only doc files. Combined with V7.3, the entire custom lint extensibility story is unimplemented.

**Fix Plan:**
- Step 1: Define `LintRule` interface with `Name() string` and `Apply(g *model.Graph) []Diagnostic` methods in `validate/validate.go`.
- Step 2: Resolved together with V7.3 (the interface IS the type for `extra_rules`).

**Files Modified:**
- `internal/attractor/validate/validate.go` -- Added `LintRule` interface definition.

**Status: FIXED** (resolved together with V7.3)

### V7.5 — `ValidateOrError` is dead code; engine uses inline checking

**Spec says (§7.1, §7.3):** Engine must refuse to execute with error-severity diagnostics. `validate_or_raise` should collect all errors.

**Code does:** The engine uses inline logic at `engine.go:265-270` that returns on the **first** error found. `ValidateOrError` (which collects all errors) at `validate.go:60` is never called -- dead code.

**Impact:** Users see only one validation error at a time instead of all errors.

**Fix Plan:**
- Step 1: In `engine.go` `PrepareWithOptions`, replace the inline for-loop that returns on the first error with logic that collects ALL error-severity diagnostics and joins them into a single error message (matching `ValidateOrError` semantics).
- Step 2: `ValidateOrError` remains as a public API for callers outside the engine; it is no longer dead code since it's a supported API.
- Step 3: Add test: `TestValidateOrError_CollectsAllErrors` verifying multiple errors are reported together.

**Files Modified:**
- `internal/attractor/engine/engine.go` -- Replaced inline first-error validation with collect-all-errors logic.
- `internal/attractor/validate/validate_test.go` -- Added `TestValidateOrError_CollectsAllErrors`.

**Status: FIXED**

### V7.6 — `terminal_node` rule enforces "exactly one" exit node; spec says "at least one"

**Spec says (§7.2):** `terminal_node` (ERROR): "At least one terminal node."

**Code does:** `validate/validate.go:94-112`:
```go
if len(ids) != 1 {
    return []Diagnostic{{
        Rule:     "terminal_node",
        Severity: SeverityError,
        Message:  fmt.Sprintf("pipeline must have exactly one exit node (found %d: %v)", len(ids), ids),
    }}
}
```

Rejects pipelines with 0 or >1 exit nodes. Spec only requires >=1.

**Impact:** Pipelines with multiple terminal nodes (e.g., success exit + error exit) are incorrectly rejected.

**Analysis:** All existing DOT files have exactly one exit node, so this is a relaxation. Multi-exit pipelines (success exit + error exit) are a real use case. The `exit_no_outgoing` rule and `lintGoalGateExitStatusContract` were updated to check ALL exit nodes, not just the first one.

**Fix Plan:**
- Step 1: Change `lintExitNode` to check `len(ids) == 0` (at least one) instead of `len(ids) != 1` (exactly one).
- Step 2: Add `findAllExitNodeIDs` helper that returns all exit node IDs.
- Step 3: Update `lintExitNoOutgoing` to check all exit nodes.
- Step 4: Update `lintGoalGateExitStatusContract` to check edges to any exit node.
- Step 5: Update spec §11.2 to say "at least one" instead of "exactly one".
- Step 6: Add tests: `TestValidate_MultipleExitNodes_NoError`, `TestValidate_ZeroExitNodes_Error`, `TestValidate_MultipleExitNodes_ExitNoOutgoingChecksAll`.

**Files Modified:**
- `internal/attractor/validate/validate.go` -- Changed `lintExitNode` to check `len(ids) == 0`; added `findAllExitNodeIDs` helper; updated `lintExitNoOutgoing` and `lintGoalGateExitStatusContract` to handle multiple exit nodes.
- `internal/attractor/engine/engine_stage_timeout_test.go` -- Updated timeout tests that used two Msquare nodes (now valid) to use simpler single-exit graphs.
- `docs/strongdm/attractor/attractor-spec.md` -- §11.2: changed "Exactly one exit node" to "At least one exit node".
- `internal/attractor/validate/validate_test.go` -- Added 3 tests for multi-exit node validation.

**Status: FIXED** (also resolves V11.2)

### V7.7 — Eight extra lint rules beyond spec (some ERROR severity)

The following rules exist in code but are NOT in the spec's §7.2 table: `goal_gate_exit_status_contract` (ERROR), `goal_gate_prompt_status_hint` (WARNING), `prompt_on_conditional_node` (WARNING), `prompt_file_conflict` (ERROR), `llm_provider_required` (ERROR), `loop_restart_failure_class_guard` (WARNING), `escalation_models_syntax` (WARNING), `graph_nil` (ERROR).

**Impact:** The ERROR-severity extras (`llm_provider_required`, `prompt_file_conflict`, `goal_gate_exit_status_contract`) can reject pipelines the spec considers valid.

**Analysis:** These extra rules are valuable -- they catch real problems. Rather than removing them from the code, the right fix is to add them to the spec's §7.2 table, documenting the full rule set. This makes the spec match reality and documents the validation contract.

**Fix Plan:**
- Step 1: Add all 8 rules to the spec's §7.2 table with their correct severities and descriptions.
- Step 2: No code changes needed.

**Files Modified:**
- `docs/strongdm/attractor/attractor-spec.md` -- §7.2: added 8 rules to the built-in lint rules table.

**Status: FIXED**

---

## Section 8: Model Stylesheet

### V8.1 — Specificity values shifted to accommodate shape selector

**Spec says (§8.3):** `*`=0, `.class`=1, `#id`=2.

**Code does:** `style/stylesheet.go:168-191`:
```go
SelectorUniversal -> 0
SelectorShape     -> 1   // NOT IN SPEC
SelectorClass     -> 2   // spec says 1
SelectorID        -> 3   // spec says 2
```

Relative ordering among spec-defined selectors is preserved, but absolute values differ.

**Analysis:** This is a direct consequence of V8.2 (shape selector insertion). The shape selector was inserted between universal and class in the specificity hierarchy, pushing class and ID up by 1 each. The relative ordering `* < .class < #id` is preserved. Notably, §11.10 already documents the 4-level specificity order as `universal < shape < class < ID`, confirming this was always the intended design. The §8.3 table was simply never updated to match §11.10.

**Fix Plan:** Resolved together with V8.2 as a single spec update. See V8.2 for details.

**Status: FIXED** (resolved as part of V8.2 spec update — §8.3 specificity table now documents 4-level hierarchy: `*`=0, `shape`=1, `.class`=2, `#id`=3)

### V8.2 — Shape selector implemented but absent from spec grammar

**Spec says (§8.2):** `Selector ::= '*' | '#' Identifier | '.' ClassName` — exactly three types.

**Code does:** `style/stylesheet.go:186-191` adds a fourth selector type `SelectorShape` matching bare identifiers (e.g., `box`, `diamond`) against node shapes:
```go
case SelectorShape:
    return n.Shape() == r.Value
```

**Impact:** The parser accepts `box { reasoning_effort: low; }` which has no spec basis in §8.2.

**Analysis:** The shape selector is a useful CSS-like pattern that matches nodes by their Graphviz shape attribute. This follows the CSS convention where bare element names match by type (e.g., `div`, `p` in CSS). The feature is already working and tested, including in `stylesheet_test.go:TestStylesheet_ParseAndApply` (uses `box { reasoning_effort: low; }`) and `validate_test.go:TestValidate_ConditionAndStylesheetSyntax` (uses `box { llm_model: gpt-5.2; }`). Crucially, §11.10 (Definition of Done) already documents shape selectors as a requirement: "Selectors by shape name work (e.g., `box { model = \"claude-opus-4-6\" }`)" and "Specificity order: universal < shape < class < ID". This confirms the feature was always intended; the §8 grammar and specificity table were simply not updated to match §11.10.

No real-world DOT files currently use shape selectors in their stylesheets (all use `*`, `.class`, and `#id`), but the feature fills a natural gap: applying LLM configuration by node type (e.g., lower reasoning effort for all box/codergen nodes, different models for diamond/conditional nodes).

**Recommendation:** Update the spec to document the shape selector. The code is correct; §8.2 and §8.3 need to align with §11.10.

**Fix Plan:**
- Step 1: In spec §8.2, add `ShapeName` to the `Selector` production and add a `ShapeName` production rule. Add explanatory prose about shape selectors following the CSS bare-element-name convention.
- Step 2: In spec §8.3, add a shape selector row to the specificity table with specificity 1, between `*` (0) and `.class_name` (2). Update `.class_name` to specificity 2 and `#node_id` to specificity 3.
- Step 3: In spec §8.5, update the specificity description from "ID > class > universal" to "ID > class > shape > universal".
- Step 4: In spec §8.6, add a shape selector rule to the example (`box { reasoning_effort: low; }`) and update the explanation text.
- Step 5: No code changes needed — the code is already correct and matches §11.10.
- Step 6: Run style and engine tests to verify no regressions (spec-only change).

**Files Modified:**
- `docs/strongdm/attractor/attractor-spec.md` — §8.2: added `ShapeName` production to grammar and explanatory prose; §8.3: expanded specificity table to 4 levels with shape selector; §8.5: updated precedence description; §8.6: added shape selector to example and updated explanation text

**Status: FIXED** (also resolves V8.1)

### V8.3 — `parseClassName` accepts uppercase and Unicode letters

**Spec says (§8.2):** `ClassName ::= [a-z0-9-]+` — lowercase ASCII only.

**Code does:** `style/stylesheet.go:206-224` uses `unicode.IsLetter(r)` which accepts uppercase, accented, and CJK characters.

**Impact:** `.MyClass` or accented class names parse successfully when the spec says they should be rejected.

**Status: FIXED** (resolved as part of V2.1 fix — `parseClassName` now uses `[a-z0-9-]+` ASCII-only checks; `parseIdentLike` and `isIdentStart`/`isIdentContinue` also fixed to ASCII-only; tests added)

---

## Section 9: Transforms and Extensibility

### V9.1 — Transform.Apply mutates in-place instead of returning a new graph

**Spec says (§9.1):** `FUNCTION apply(graph) -> Graph` — returns a new or modified graph. "Should not modify the input graph."

**Code does:** `engine/transforms.go:13-16`:
```go
type Transform interface {
    ID() string
    Apply(g *model.Graph) error
}
```

Returns `error` instead of a new `Graph`. All built-in transforms mutate the input graph in-place via pointer. `PrepareWithOptions` (line 228-272) passes the same `*model.Graph` through every transform sequentially.

**Impact:** No ability to compare pre/post-transform graphs. Side effects between transforms are invisible. The functional contract from the spec is replaced with an imperative mutation pattern.

**Analysis:**

The spec's functional signature (`apply(graph) -> Graph`, "should not modify the input graph") originates from a language-agnostic perspective where immutability is a natural default (Python, Haskell). In Go, the idiomatic pattern for transforms is mutation through a pointer receiver — the caller passes ownership of the graph, and the transform modifies it in place. The code's `Apply(g *model.Graph) error` signature is textbook Go: mutate via pointer, return only an error to signal failure.

Scope assessment:
- **1 built-in transform** implements the `Transform` interface: `goalExpansionTransform`
- **3 test transforms** in `transforms_test.go`: `setGraphAttrTransform`, `appendGraphAttrTransform`, `fixBadConditionTransform`
- **2 additional built-in transforms** (`expandPromptFiles`, `style.ApplyStylesheet`) are called directly in `PrepareWithOptions` and also mutate in-place via pointer, but do NOT implement the `Transform` interface
- **1 caller** of `Transform.Apply`: `PrepareWithOptions` (lines 253, 260)

Changing the code to return `(*model.Graph, error)` would require:
1. Deep-copying `model.Graph` (which contains `map[string]*Node`, `[]*Edge`, and two index maps) for every transform — significant allocation for zero practical benefit
2. Updating all 4 transform implementations
3. Updating `PrepareWithOptions` to reassign `g` from the return value
4. The spec's own pseudocode contradicts the "should not modify" guidance — the `VariableExpansionTransform` example (§9.2) modifies `node.prompt` in-place on the input graph and then returns `graph` (the same graph, now mutated)

The "should not modify the input graph" guidance has no practical benefit in this codebase:
- **Pre/post comparison:** `PrepareWithOptions` already captures `DotSource` (the raw DOT bytes) for replay/resume. If pre/post comparison were needed, it would be done at the DOT level, not the graph struct level.
- **Transform isolation:** Transforms run sequentially in a deterministic order. Each transform expects to see the output of all previous transforms. Immutability would require deep-copy between each step for no semantic benefit.
- **Go idiom:** Every graph-mutating function in the codebase (`dot.Parse`, `style.ApplyStylesheet`, `expandPromptFiles`, `expandGoal`, `expandBaseSHA`) uses the same pattern: take `*model.Graph`, mutate in place, return `error` or nothing.

The code also returns `error` instead of a new `Graph` — this is strictly better because it allows transforms to report failures (e.g., `expandPromptFiles` returns an error when the referenced file is missing or when `prompt` and `prompt_file` conflict). The spec's `apply(graph) -> Graph` has no error channel, forcing transforms to either panic or silently produce a bad graph.

**Recommendation:** Update the spec to match the code. The code's approach is more idiomatic for Go, more memory-efficient, and provides better error handling. The spec's functional style is aspirational but impractical in this implementation language.

**Fix Plan:**
- Step 1: Update spec §9.1 to change the `Transform` interface from `FUNCTION apply(graph) -> Graph` to `FUNCTION apply(graph) -> Error`, documenting that transforms mutate the graph in-place and return an error on failure. Remove "Should not modify the input graph." Replace with a note that transforms mutate the graph in-place (the caller passes ownership) and that the execution order is deterministic.
- Step 2: Update spec §9.1 `prepare_pipeline` pseudocode to use `error = transform.apply(graph)` instead of `graph = transform.apply(graph)`.
- Step 3: Update spec §9.2 `VariableExpansionTransform` pseudocode signature to match: `FUNCTION apply(graph) -> Error`.
- Step 4: Update spec §11.11 DoD checklist to change `transform(graph) -> graph` to `transform(graph) -> error`.
- Step 5: No code changes needed — the code is already correct.
- Step 6: Run engine tests to verify no regressions (spec-only change).

**Files Modified:**
- `docs/strongdm/attractor/attractor-spec.md` — §9.1: changed Transform interface to `apply(graph) -> Error` with in-place mutation semantics; updated `prepare_pipeline` pseudocode; §9.2: updated VariableExpansionTransform signature; §11.11: updated DoD checklist item

**Status: FIXED**

### V9.2 — 9 of 14 typed observability events missing

**Spec says (§9.6):** Engine must emit typed events: PipelineStarted, PipelineCompleted, PipelineFailed, StageStarted, StageCompleted, StageFailed, StageRetrying, ParallelStarted, ParallelBranchStarted, ParallelBranchCompleted, ParallelCompleted, InterviewStarted, InterviewCompleted, InterviewTimeout, CheckpointSaved.

**Code does:** The CXDB event registry (`cxdb/kilroy_registry.go`) defines only: `RunStarted`, `RunCompleted`, `RunFailed`, `StageStarted`, `StageFinished`, `CheckpointSaved` (plus auxiliary types).

Missing entirely:
- `StageFailed` — failures are generic `StageFinished` with status
- `StageRetrying` — retries are untyped progress events, not typed CXDB events
- All 4 parallel events (`ParallelStarted`, `ParallelBranchStarted`, `ParallelBranchCompleted`, `ParallelCompleted`)
- All 3 interview events (`InterviewStarted`, `InterviewCompleted`, `InterviewTimeout`)

Additionally, spec says `StageCompleted` but code emits `StageFinished`.

**Impact:** Consumers relying on the spec's event types for observability, metrics, or UI would not receive 9 of 14 specified events.

**Status: FIXED** — All 9 missing event types added to the CXDB registry (`kilroy_registry.go`) with typed field definitions. Emitter methods added to `cxdb_events.go`. Events emitted from: `executeWithRetry` (StageFailed, StageRetrying), `ParallelHandler.Execute`/`runBranch` (ParallelStarted, ParallelBranchStarted, ParallelBranchCompleted, ParallelCompleted), and `WaitHumanHandler.Execute` (InterviewStarted, InterviewCompleted, InterviewTimeout).

### V9.3 — Tool call hooks parsed but never executed

**Spec says (§9.7):** `tool_hooks.pre` and `tool_hooks.post` specify shell commands around each LLM tool call. Pre-hook exit 0 = proceed, non-zero = skip. Post-hook for logging. Failures recorded in stage log.

**Code does:** The DOT parser correctly accepts `tool_hooks.pre`/`tool_hooks.post` as attributes (confirmed in `dot/parser_test.go:65-87`). However, no code reads these attributes at execution time. No shell commands are executed before/after tool calls. No exit code checking. No hook failure recording.

**Impact:** Users configuring tool hooks in their DOT files have them silently ignored.

**Status: FIXED** — Tool hook execution implemented in `tool_hooks.go`. `resolveToolHook` reads hook commands from node attrs then graph attrs. `runToolHook` executes shell commands with 30s timeout, stdin JSON payload, and KILROY_* env vars. `executeToolHookForEvent` integrates with the agent event loop in `codergen_router.go` for EventToolCallStart (pre-hook) and EventToolCallEnd (post-hook). Pre-hook non-zero exit logged as warning. Hook results logged to stage directory. 12 unit tests in `tool_hooks_test.go`.

### V9.4 — CheckpointSaved event missing required `node_id` field

**Spec says (§9.6):** `CheckpointSaved(node_id)` — event defined with a `node_id` parameter.

**Code does:** `engine/cxdb_events.go:149-155`:
```go
e.CXDB.Append(ctx, "com.kilroy.attractor.CheckpointSaved", 1, map[string]any{
    "run_id":            e.Options.RunID,
    "timestamp_ms":      nowMS(),
    "checkpoint_path":   cpPath,
    // no node_id
})
```

The `nodeID` parameter is available in the calling function but only used for the preceding `GitCheckpoint` event, not passed to `CheckpointSaved`.

**Analysis:** One-line fix. The `cxdbCheckpointSaved` function already receives `nodeID` as its second parameter (used for the `GitCheckpoint` event on line 140). It just wasn't included in the `CheckpointSaved` event map literal.

**Fix Plan:**
- Step 1: Add `"node_id": nodeID` to the `CheckpointSaved` event map literal in `cxdb_events.go`.
- Step 2: Run engine test suite to verify no regressions.

**Files Modified:**
- `internal/attractor/engine/cxdb_events.go` -- Added `"node_id": nodeID` to the `CheckpointSaved` event data map.

**Status: FIXED**

---

## Section 10: Condition Expression Language

### V10.1 — Outcome comparison uses alias canonicalization instead of exact case-sensitive matching

**Spec says (§10.3):** "String comparison is exact and case-sensitive." The `resolve_key` pseudocode for `outcome` simply returns `outcome.status as string` with no normalization.

**Code does:** `cond/cond.go:78-86`:
```go
case "outcome":
    co, err := outcome.Canonicalize()
    if err != nil {
        return string(outcome.Status)
    }
    return string(co.Status)
```

And `evalClause` (line 51) canonicalizes the comparison value via `canonicalizeCompareValue`, which calls `ParseStageStatus` (`runtime/status.go:19-41`):
```go
switch strings.ToLower(strings.TrimSpace(s)) {
case "success", "ok":      return StatusSuccess, nil
case "fail", "failure", "error": return StatusFail, nil
case "skipped", "skip":    return StatusSkipped, nil
```

This means `outcome=ok` matches SUCCESS, `outcome=failure` matches FAIL, `outcome=Skip` matches SKIPPED, and `outcome=SUCCESS` matches `success`. All of these would fail under the spec's exact case-sensitive comparison.

**Impact:** DOT files using alias forms (`ok`, `failure`, `skip`) or case variants work in this implementation but are non-portable to a spec-compliant implementation that follows §10.3 literally.

**Analysis:** The alias canonicalization is essential for real-world DOT files. Existing pipelines use `outcome=skip` (in `semport.dot`, `SKILL.md`, and planning docs), which would fail without canonicalization because the canonical status is `"skipped"`. The aliases are ergonomic improvements that let DOT authors use natural terms (`skip`, `failure`, `ok`) while the engine stores canonical forms (`skipped`, `fail`, `success`). No existing DOT file uses the aliases `ok`, `failure`, or `error` in conditions, but `outcome=skip` is actively used and would break if canonicalization were removed. Custom (non-canonical) outcome values like `process`, `done`, `port` (used in `consensus_task.dot` and `semport.dot`) pass through canonicalization unchanged but are lowercased.

**Recommendation:** Update the spec to document alias canonicalization as an official feature. This makes existing DOT files portable by definition and ensures other implementations support the same aliases.

**Fix Plan:**
- Step 1: In spec §10.3, update the semantics to document outcome alias canonicalization with a table of aliases and their canonical forms. Clarify that "exact and case-sensitive" applies after canonicalization.
- Step 2: In spec §10.4, update `resolve_key` pseudocode to show canonicalization of `outcome.status`.
- Step 3: In spec §10.5, update `evaluate_clause` pseudocode to show `canonicalize_compare_value` applied to the comparison literal when the key is `outcome`. Add a new `canonicalize_compare_value` helper function.
- Step 4: In spec §10.6, add examples showing alias usage (`outcome=skip`, `outcome=failure`).
- Step 5: No code changes needed -- the code is already correct.

**Files Modified:**
- `docs/strongdm/attractor/attractor-spec.md` -- §10.3: added outcome alias canonicalization table and semantics; §10.4: updated `resolve_key` pseudocode to show canonicalization; §10.5: updated `evaluate_clause` to use `canonicalize_compare_value`, added helper function; §10.6: added alias examples

**Status: FIXED**

### V10.2 — Bare-key truthy check treats "no", "false", "0" as falsy beyond spec's `bool()` semantics

**Spec says (§10.5):** `RETURN bool(resolve_key(trim(clause), outcome, context))` — standard `bool()` coercion where any non-empty string is truthy.

**Code does:** `cond/cond.go:65-75`:
```go
switch strings.ToLower(got) {
case "false", "0", "no":
    return false, nil
default:
    return true, nil
}
```

Treats `"false"`, `"0"`, and `"no"` (case-insensitive) as falsy. Under the spec's `bool()`, these are non-empty strings and should be truthy.

**Impact:** A bare-key check on a context value of `"false"` or `"no"` evaluates to false here but true per spec. The `"no"` case is particularly surprising.

**Analysis:** No existing DOT files use bare-key conditions (conditions without `=` or `!=` operators). The extended falsy coercion is a reasonable ergonomic improvement for context values that hold boolean-like strings. When a context key like `notifications_enabled` holds the string `"false"`, the bare-key check `condition="context.notifications_enabled"` should intuitively evaluate to false. Under the spec's `bool()` semantics, `"false"` is a non-empty string and would be truthy -- counterintuitive for pipeline authors. The extended falsy set (`"false"`, `"0"`, `"no"`, case-insensitive) covers the most common boolean-like string representations. While `"no"` is slightly surprising as a general falsy value, it is a natural response in human-in-the-loop contexts (e.g., a gate answer of "no" stored in context).

**Recommendation:** Update the spec to document extended falsy coercion, replacing the ambiguous `bool()` with explicit rules. This is a small, self-contained ergonomic feature that makes bare-key checks more useful for boolean-like context values.

**Fix Plan:**
- Step 1: In spec §10.5, replace the bare-key `bool()` call with explicit rules: empty string is falsy, `"false"`, `"0"`, `"no"` (case-insensitive) are falsy, everything else is truthy.
- Step 2: In spec §10.6, add an example showing bare-key usage with extended falsy coercion.
- Step 3: No code changes needed -- the code is already correct.

**Files Modified:**
- `docs/strongdm/attractor/attractor-spec.md` -- §10.5: replaced `bool()` with explicit extended falsy coercion rules in `evaluate_clause` pseudocode; §10.6: added bare-key example

**Status: FIXED**

---

## Section 11: Definition of Done

### V11.1 — Spec-Internal Contradiction: §11.5 says retry on FAIL; §3.5 says never retry on FAIL

**§11.5 says (line 1826):** "Nodes with `max_retries > 0` are retried on RETRY **or FAIL** outcomes"

**§3.5 says (lines 512-513):** `IF outcome.status == FAIL: RETURN outcome` — immediate return, no retry.

**Code does:** `failure_policy.go:15-20` — retries FAIL outcomes only when the failure class is retryable (`transient_infra`, etc.). This is a third interpretation distinct from both spec passages. (Related to V3.8, but the spec-internal contradiction is new.)

**Status: FIXED** (resolved as part of V3.6 fix — §11.5 updated to clarify that retries are gated on failure class, and §3.5 now documents failure classification as a safety net; the code's behavior is now the canonical spec behavior)

### V11.2 — Spec-Internal Contradiction: §11.2 says "exactly one" exit; §7.2 says "at least one"

**§11.2 says (line 1796):** "Exactly one exit node (shape=Msquare) is required"

**§7.2 says (line 1400):** `terminal_node | ERROR | Pipeline must have at least one terminal node`

**Code does:** `validate/validate.go:104-108` enforces "exactly one," matching §11.2 but contradicting §7.2. (Related to V7.6, but the spec-internal contradiction is new.)

**Impact:** Multi-exit pipelines (success exit + error exit) are valid per §7.2 but rejected per §11.2.

**Status: FIXED** -- Resolved by V7.6 fix. Code changed to "at least one" (matching §7.2), spec §11.2 updated to say "at least one" (matching §7.2). Both spec sections and code now agree.

### V11.3 — Spec-Internal Contradiction: §11.8 uses different QuestionType names than §6.2

**§11.8 says (line 1856):** "Question supports types: `SINGLE_SELECT`, `MULTI_SELECT`, `FREE_TEXT`, `CONFIRM`"

**§6.2 says (lines 1269-1272):** `QuestionType: YES_NO, MULTIPLE_CHOICE, FREEFORM, CONFIRMATION`

Completely different enum names. §11.8 includes `MULTI_SELECT` (not in §6.2); §6.2 has `YES_NO` (not in §11.8).

**Code does:** `handlers.go:623-630` matches §11.8's names exactly. (Related to V6.3, but the spec-internal contradiction is new.)

**Status: FIXED** (resolved by V6.3 fix — §6.2 now uses the same names as §11.8 and the code: `YES_NO`, `SINGLE_SELECT`, `MULTI_SELECT`, `FREE_TEXT`, `CONFIRM`. §11.8 updated to include `YES_NO`.)

### V11.4 — Spec-Internal Contradiction: §11.6 says conditional "passes through"; §4.7 says always SUCCESS

**§11.6 says (line 1838):** "Conditional handler: Passes through; engine evaluates edge conditions"

**§4.7 says (lines 786-791):** `RETURN Outcome(status=SUCCESS, notes="Conditional node evaluated: " + node.id)`

"Passes through" implies propagating the prior outcome; §4.7 explicitly says always return SUCCESS. (Related to V4.5, but the spec-internal contradiction is new.)

**Status: FIXED** -- Resolved by V4.5 fix. Spec §4.7 now documents pass-through design with pseudocode matching the code. §11.6 updated to explicitly reference pass-through of previous node's outcome with cross-reference to §4.7. Both sections are now consistent.

### V11.5 — Cross-Feature Parity Matrix and Integration Smoke Test have no dedicated test suite

**Spec says (§11.12, §11.13):** A 22-row parity matrix where each cell must pass, plus a specific 5-node integration smoke test with LLM callback, artifact verification, and checkpoint verification.

**Code does:** No file or test function corresponding to the parity matrix or integration smoke test exists. Many individual rows are exercised by scattered tests (edge selection, goal gates, checkpoint resume, etc.), but the following have no corresponding test:
- "Stylesheet applies model override to nodes by shape" (end-to-end)
- "Custom handler registration and execution works" (through `Run()`)
- "Pipeline with 10+ nodes completes without errors"
- The §11.13 integration smoke test (single test exercising parse->validate->execute->goal gate->artifacts->checkpoint)

**Status: FIXED** -- Added `internal/attractor/engine/parity_matrix_test.go` containing 22 self-contained subtests (one per §11.12 matrix row) plus `TestIntegrationSmokeTest_Section11_13` covering the full §11.13 lifecycle. New tests include: Row 3 (multi-line attrs), Row 5 (missing exit), Row 6 (orphan diagnostic), Row 18 (stylesheet by shape end-to-end), Row 21 (custom handler through engine.run()), Row 22 (12-node pipeline). All tests are deterministic, use SimulatedCodergenBackend, and pass.

**Files Modified:**
- `internal/attractor/engine/parity_matrix_test.go` (new) -- 22 parity matrix tests + integration smoke test

---

## Cross-Cutting Themes

### Intentional Deviations
Several violations appear to be deliberate design choices documented in code comments:
- **V3.1** (jitter off) — comment says "for determinism"
- **V4.5** (conditional pass-through) — comment says "conditional pass-through" design
- **V3.6** (failure classification) — sophisticated retry policy that goes beyond spec (previously V3.8)

### RETRY vs FAIL Confusion (V4.3, V4.9)
Both codergen backend errors and handler Go errors produce RETRY where the spec says FAIL. This shifts error handling toward automatic retry, which may be pragmatically correct for transient LLM failures but contradicts the spec's explicit error categorization.

### Unimplemented Features (V6.6)
RecordingInterviewer is still unimplemented. Previously deferred items V4.6 (ParallelHandler join/error policies), V4.7 (ManagerLoopHandler), V5.4 (ArtifactStore), V9.2 (observability events), and V9.3 (tool hooks) have all been implemented.

### Unicode Permissiveness (V2.1, V8.3)
Both the DOT lexer and stylesheet parser use `unicode.IsLetter`/`unicode.IsDigit` where the spec requires ASCII-only patterns. Consistent deviation, likely from Go's idiomatic use of `unicode` helpers.

### Interviewer Pattern Gaps (V6.1–V6.8)
The human-in-the-loop subsystem has the most violations of any section. The Interviewer interface is minimal (one method vs three), the Question/Answer structs are missing fields, and the timeout-with-default flow is completely absent. This suggests the interviewer pattern was implemented as a minimal viable version.

### Extra Validation Rules (V7.7)
The validator includes 8 rules not in the spec, some at ERROR severity. These extend beyond the spec rather than violating it, but can reject spec-valid pipelines.

### Shallow Clone Risk (V5.3)
Context.Clone() does a shallow copy where the spec requires deep copy. Combined with parallel execution, this is a correctness hazard for any pipeline that stores nested data structures in context.

### Observability Gaps (V9.2, V9.4) -- RESOLVED
All 9 missing spec-defined typed events have been added to the CXDB registry (V9.2 FIXED). The CheckpointSaved event now includes the required `node_id` field (V9.4 FIXED).

### Condition Language Ergonomic Extensions (V10.1, V10.2) -- RESOLVED
The condition evaluator adds alias resolution (`ok`→`success`, `failure`→`fail`, `skip`→`skipped`) and extended falsy values (`"false"`, `"0"`, `"no"`). The spec has been updated to document these as official features: §10.3 now includes an alias canonicalization table, §10.4 shows canonicalization in `resolve_key`, §10.5 replaces `bool()` with explicit extended falsy coercion rules, and §10.6 includes examples. These are now portable by definition -- any compliant implementation must support them.

### Spec-Internal Contradictions (V11.1–V11.4)
Section 11 (Definition of Done) contradicts earlier normative sections in 4 places: retry-on-FAIL semantics (§11.5 vs §3.5), exit node cardinality (§11.2 vs §7.2), QuestionType enum names (§11.8 vs §6.2), and conditional handler behavior (§11.6 vs §4.7). The code sides with §11 in 3 of 4 cases.

---

## Deferred Implementation Plans

These violations represent unimplemented features that require significant new code (not bug fixes or spec updates). Each has a phased implementation plan.

### V4.6 — ParallelHandler join_policy / error_policy -- IMPLEMENTED

All 4 join policies (wait_all, first_success, k_of_n, quorum) and 3 error policies (continue, fail_fast, ignore) implemented in `parallel_policy.go` (~250 lines) with 30 unit tests in `parallel_policy_test.go`. ParallelHandler.Execute reads policies from node attributes and uses `evaluateJoinPolicy` for aggregate outcome. Early termination supported via `dispatchParallelBranchesWithPolicy` with cancellable context.

---

### V4.7 — ManagerLoopHandler -- IMPLEMENTED

Full observation loop implementation in `manager_loop.go` (~190 lines) with 9 unit tests in `manager_loop_test.go`. Supports: configurable poll_interval/max_cycles, stop condition evaluation via `cond.Evaluate`, child pipeline execution via `Prepare` + `runSubgraphUntil`, observe/wait actions. Steer action deferred to v2.
