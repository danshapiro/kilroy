# Fix Tool Validation Error Loops and Anthropic Streaming Corruption

**Date:** 2026-02-21
**Status:** Proposed
**Trigger:** Lab-bench pipeline run `01KJ1T17CWJEXEPGZNAGCA2REA` failed at `consolidate_dod` due to a tool schema validation loop, and `dod_b` failed due to Anthropic API context corruption.

## Problem Statement

Two distinct failure modes caused a lab-bench pipeline run to fail and waste tokens/time:

1. **Anthropic streaming codec corruption** — The `dod_b` branch failed with: `tool_use ids were found without tool_result blocks immediately after`. The conversation history became malformed (a tool call existed without a corresponding tool result), and the Anthropic API rejected further requests with a 400 error.

2. **Tool schema validation loop** — The `consolidate_dod` agent repeatedly called the `shell` tool without the required `command` parameter. Each call failed schema validation and the error was reported back to the model, but the model kept making the same malformed call. The run was eventually killed by signal termination.

## Root Cause Analysis

### Failure 1: Anthropic streaming adapter concatenation bug

**Location:** `internal/llm/providers/anthropic/adapter.go` (tool_use streaming, ~lines 392-450)
**Related investigation:** `docs/plans/2026-02-10-kimi-tool-call-investigation-notes.md`

When the Anthropic provider emits both `content_block_start.tool_use.input` and `content_block_delta.input_json_delta.partial_json`, the adapter concatenates both values, producing invalid JSON. This causes:

1. Tool result validation fails on the malformed JSON
2. Error reported back to model as truncated tool result
3. Model retries with further malformed args
4. Context accumulates orphaned `tool_use` blocks without `tool_result` blocks
5. Anthropic API rejects the entire conversation with HTTP 400

**Why it doesn't recover:** The retry policy in `internal/llm/retry_util.go` (2 retries, 1–60s exponential backoff) classifies HTTP 400 as a non-retryable `InvalidRequestError`. There is no special handling for this specific error pattern.

### Failure 2: No tool-level circuit breaker

**Locations:**
- Tool validation: `internal/agent/tool_registry.go` (lines 97–126)
- Shell tool schema: `internal/agent/profile.go` (lines 291–306)
- Stall watchdog: `internal/attractor/engine/engine.go` (lines 1602–1635)

The `shell` tool requires a `command` field. When the model omits it, `ExecuteCall()` validates against the JSON schema, fails, and returns the error message to the model with `IsError=true`. However:

- **No loop detection** — Each validation failure is handled independently. There is no tracking of consecutive identical failures for the same tool.
- **No circuit breaker** — The model can loop on the same malformed call indefinitely, burning tokens.
- **Stall watchdog is the only safety net** — If configured, the engine's stall watchdog detects no progress and cancels the run with SIGTERM. But `StallTimeout` may not be set in all run configs.

### Parallel branch failure propagation

**Location:** `internal/attractor/engine/parallel_handlers.go` (lines 490–627)

When `dod_b` failed, the `FanInHandler` still received its result (with `StatusFail`). The consolidation step proceeded with the successful branches (`dod_a`, `dod_c`), but the consolidator itself then hit the schema validation loop described above. The branch failure did not directly cause the consolidator failure — these are independent issues that compounded.

## Proposed Fixes

### Fix 1: Anthropic streaming adapter — implement Option A (source precedence)

**File:** `internal/llm/providers/anthropic/adapter.go`

This bug was fully reproduced and localized in `docs/plans/2026-02-10-kimi-tool-call-investigation-notes.md`, which includes regression tests and two proposed fix options. Implement **Option A (source precedence)**: track per-tool-call arg source (`start_input` vs `delta_stream`) separately, and at finalize prefer the delta buffer if any deltas were received, else fall back to start-input.

The Kimi investigation already provides the failing test fixtures (`internal/llm/providers/anthropic/testdata/kimi_tool_call_sequences.ndjson`) and regression tests that should pass once the fix is applied.

### Fix 2: Tool validation circuit breaker

**File:** `internal/agent/tool_registry.go`

Add a per-tool consecutive failure counter to `ExecuteCall()`. After N consecutive schema validation failures for the same tool (suggest N=3), escalate the error:

- Inject a stronger error message: `"Tool '{name}' has failed schema validation {N} times consecutively. The required fields are: {required_fields}. Do NOT call this tool again without providing all required fields."`
- Optionally: fail the stage deterministically with `failure_class=deterministic` to trigger the graph's retry/postmortem routing instead of looping further.

The counter resets on any successful tool call.

### Fix 3: Ensure StallTimeout is configured

**File:** Run config YAML files and/or engine defaults

The stall watchdog in `engine.go` (lines 1602–1635) is the last line of defense against infinite loops, but it requires `StallTimeout` to be set. Either:

- Set a default `StallTimeout` of 5–10 minutes in the engine if none is configured
- Add `stall_timeout_ms: 300000` to the standard run config templates

### Fix 4: Classify tool_use/tool_result mismatch as recoverable

**File:** `internal/llm/retry_util.go` or `internal/llm/providers/anthropic/adapter.go`

Detect the specific Anthropic 400 error pattern (`tool_use ids were found without tool_result blocks`) and handle it specially:

- Option A: Retry by reconstructing the conversation — drop the orphaned `tool_use` block from the message history and re-send
- Option B: Classify as `transient_infra` so the stage-level retry policy can restart the stage cleanly

This prevents a single streaming glitch from permanently failing a branch.

## Priority

| Fix | Impact | Effort | Priority |
|-----|--------|--------|----------|
| Fix 1: Streaming adapter | Prevents context corruption entirely | Low | **P0** |
| Fix 2: Circuit breaker | Prevents token waste on validation loops | Medium | **P1** |
| Fix 3: StallTimeout default | Ensures hung runs are killed | Low | **P1** |
| Fix 4: Recoverable mismatch | Allows branches to self-heal | Medium | **P2** |

## Verification

1. Run the lab-bench pipeline end-to-end and confirm all 3 DoD branches complete without API corruption errors
2. Simulate a schema validation loop (model calling `shell` without `command`) and confirm the circuit breaker fires after 3 attempts
3. Confirm `StallTimeout` is set in run configs and that a stalled stage is killed within the configured window
4. Simulate an orphaned `tool_use` block and confirm the adapter either prevents it (Fix 1) or recovers from it (Fix 4)

## Key File References

| Component | File | Lines |
|-----------|------|-------|
| Tool validation | `internal/agent/tool_registry.go` | 97–142 |
| Shell tool def | `internal/agent/profile.go` | 291–306 |
| Anthropic adapter | `internal/llm/providers/anthropic/adapter.go` | ~392–450 |
| Retry policy | `internal/llm/retry_util.go` | 27–40, 50–87 |
| Stall watchdog | `internal/attractor/engine/engine.go` | 1602–1635 |
| Parallel fan-in | `internal/attractor/engine/parallel_handlers.go` | 490–627 |
| Process termination | `internal/agent/env_local_unix.go` | 14–26 |
| Prior investigation | `docs/plans/2026-02-10-kimi-tool-call-investigation-notes.md` | — |
