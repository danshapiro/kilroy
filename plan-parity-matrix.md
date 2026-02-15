# V11.5 Cross-Feature Parity Matrix & Integration Smoke Test

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Create the spec's 22-row cross-feature parity matrix (SS11.12) as a Go test suite and the SS11.13 integration smoke test, closing the final coverage gap in V11.5.

**Architecture:** A single test file `internal/attractor/engine/parity_matrix_test.go` containing one subtest per matrix row plus the integration smoke test. Tests reuse the existing `engine.Run()` entry point and internal engine construction patterns (manual `Engine{}` construction with `SimulatedCodergenBackend`). For rows already covered by existing tests, we delegate via a thin wrapper that runs the existing test function, or include a reference comment and skip.

**Tech Stack:** Go 1.22+ testing, `engine.Run()`/`engine.Prepare()`, `dot.Parse()`, `validate.Validate()`, `runtime.LoadCheckpoint()`, `SimulatedCodergenBackend`, `QueueInterviewer`, `AutoApproveInterviewer`

---

## Existing Coverage Analysis

Before writing tests, here is what already exists for each matrix row:

| # | Matrix Row | Existing Coverage | Action |
|---|-----------|-------------------|--------|
| 1 | Parse simple linear pipeline | `engine_test.go:TestRun_CreatesWorktreeAndCommitsPerNode` (parses + runs linear) | Thin wrapper |
| 2 | Parse pipeline with graph-level attributes | `prepare_test.go:TestPrepare_ExpandsGoalInPrompts` | Thin wrapper |
| 3 | Parse multi-line node attributes | Several tests use multi-line attrs | New (explicit) |
| 4 | Validate: missing start node -> error | `prepare_validation_test.go:TestPrepare_ReturnsErrorOnValidationErrors` (missing start) | Thin wrapper |
| 5 | Validate: missing exit node -> error | No explicit test | New |
| 6 | Validate: orphan node -> warning | No explicit test | New |
| 7 | Execute linear 3-node pipeline | `engine_test.go:TestRun_CreatesWorktreeAndCommitsPerNode` | Thin wrapper |
| 8 | Execute with conditional branching | `conditional_passthrough_test.go` | Thin wrapper |
| 9 | Execute with retry on failure | `retry_policy_test.go:TestRun_RetriesOnFail_ThenSucceeds` | Thin wrapper |
| 10 | Goal gate blocks exit when unsatisfied | `goal_gate_test.go:TestRun_GoalGateUnsatisfied_NoRetryTargetFails` | Thin wrapper |
| 11 | Goal gate allows exit when satisfied | `goal_gate_test.go:TestRun_GoalGateEnforcedAtExit_RoutesToRetryTarget` | Thin wrapper |
| 12 | Wait.human presents choices and routes | `wait_human_test.go:TestRun_WaitHuman_RoutesOnQueueInterviewerSelection` | Thin wrapper |
| 13 | Edge: condition match wins over weight | `edge_selection_test.go:TestSelectNextEdge_ConditionBeatsUnconditionalWeight` | Thin wrapper |
| 14 | Edge: weight breaks ties for unconditional | `edge_selection_test.go:TestSelectNextEdge_WeightThenLexicalThenOrder` | Thin wrapper (uses weight) |
| 15 | Edge: lexical tiebreak as final fallback | `edge_selection_test.go:TestSelectNextEdge_WeightThenLexicalThenOrder` | Thin wrapper |
| 16 | Context updates from one node visible to next | `context_updates_test.go:TestRun_ContextUpdatesAreMergedAndSavedInCheckpoint` | Thin wrapper |
| 17 | Checkpoint save and resume produces same result | `resume_test.go:TestResume_EngineOptionsAreFullyHydrated` | Thin wrapper |
| 18 | Stylesheet applies model override by shape | No end-to-end test through Run() | **New** |
| 19 | Prompt variable expansion ($goal) | `prepare_test.go:TestPrepare_ExpandsGoalInPrompts` | Thin wrapper |
| 20 | Parallel fan-out and fan-in | `parallel_test.go:TestRun_ParallelFanOutAndFanIn_FastForwardsWinner` | Thin wrapper |
| 21 | Custom handler registration and execution | `context_updates_test.go` uses custom handler but not through Run() | **New** |
| 22 | Pipeline with 10+ nodes completes | No test | **New** |

**New tests needed:** Rows 3, 5, 6, 18, 21, 22, and the SS11.13 integration smoke test.

---

## Task 1: Create the test file skeleton with helper functions

**Files:**
- Create: `internal/attractor/engine/parity_matrix_test.go`

**Step 1: Write the file skeleton with shared helpers**

Create the test file with:
- Package declaration and imports
- `initTestRepo(t)` helper that creates a temp git repo (reusable pattern from existing tests)
- A `TestParityMatrix` parent test that will contain subtests

**Step 2: Run test to verify it compiles**

Run: `cd /home/user/code/kilroy/.worktrees/spec-review && go test ./internal/attractor/engine/... -run '^TestParityMatrix$' -count=1 -timeout=60s`
Expected: PASS (with no subtests yet)

**Step 3: Commit**

```bash
git add internal/attractor/engine/parity_matrix_test.go
git commit -m "test: add parity matrix test file skeleton (V11.5)"
```

---

## Task 2: Add thin wrapper subtests for rows 1-2 (parsing)

**Files:**
- Modify: `internal/attractor/engine/parity_matrix_test.go`

**Step 1: Add Row 1 and Row 2 subtests**

Row 1 "Parse simple linear pipeline" - parse via `dot.Parse()` and verify node/edge count.
Row 2 "Parse pipeline with graph-level attributes" - parse and check `g.Attrs["goal"]` and `g.Attrs["label"]`.

**Step 2: Run tests**

Run: `cd /home/user/code/kilroy/.worktrees/spec-review && go test ./internal/attractor/engine/... -run '^TestParityMatrix' -count=1 -timeout=60s`
Expected: PASS

---

## Task 3: Add Row 3 (parse multi-line node attributes) -- NEW

**Step 1: Write the test**

Parse a DOT graph where a node has attributes spread across multiple lines. Verify all attributes are correctly captured.

**Step 2: Run and verify PASS**

---

## Task 4: Add Rows 4-6 (validation tests)

**Step 1: Write subtests**

Row 4: "Validate: missing start node" - Prepare/validate a graph with no start node, expect error.
Row 5: "Validate: missing exit node" - Prepare/validate a graph with no exit node, expect error. **NEW**
Row 6: "Validate: orphan node -> warning" - Parse then validate a graph with an unreachable node, check for warning diagnostic. **NEW**

**Step 2: Run and verify PASS**

**Step 3: Commit**

```bash
git commit -am "test: parity matrix rows 1-6 (parsing + validation)"
```

---

## Task 5: Add Rows 7-9 (execution: linear, conditional, retry)

**Step 1: Write subtests**

Row 7: Execute linear 3-node pipeline end-to-end through Run(). Verify status=success.
Row 8: Execute with conditional branching. Use a tool node that succeeds/fails to route through condition edges.
Row 9: Execute with retry (max_retries=2). Use marker file pattern from existing retry test.

**Step 2: Run and verify PASS**

**Step 3: Commit**

```bash
git commit -am "test: parity matrix rows 7-9 (execution: linear, conditional, retry)"
```

---

## Task 6: Add Rows 10-12 (goal gates + human gate)

**Step 1: Write subtests**

Row 10: Goal gate blocks exit when unsatisfied (tool fails, no retry target, expect error).
Row 11: Goal gate allows exit when all satisfied (tool succeeds).
Row 12: Wait.human presents choices and routes on selection (use QueueInterviewer).

**Step 2: Run and verify PASS**

**Step 3: Commit**

```bash
git commit -am "test: parity matrix rows 10-12 (goal gates + human gate)"
```

---

## Task 7: Add Rows 13-15 (edge selection)

**Step 1: Write subtests**

Row 13: Condition match wins over weight (delegate to edge selection logic).
Row 14: Weight breaks ties for unconditional edges.
Row 15: Lexical tiebreak as final fallback.

These can use `selectNextEdge()` directly or go through full execution.

**Step 2: Run and verify PASS**

---

## Task 8: Add Rows 16-17 (context updates + checkpoint/resume)

**Step 1: Write subtests**

Row 16: Context updates from one node are visible to the next. Use custom handler that sets context, verify in checkpoint.
Row 17: Checkpoint save and resume produces same result. Run, modify checkpoint to reset, resume, verify same final state.

**Step 2: Run and verify PASS**

**Step 3: Commit**

```bash
git commit -am "test: parity matrix rows 13-17 (edge selection, context, checkpoint)"
```

---

## Task 9: Add Row 18 (stylesheet applies model override by shape) -- NEW

**Step 1: Write the test**

This is a NEW test. Create a DOT graph with `model_stylesheet` that targets `box` shape nodes and overrides `llm_model`. Parse via `Prepare()` and verify the model attribute was applied to box-shaped nodes but NOT to diamond/hexagon nodes.

Then run it end-to-end through `Run()` with `SimulatedCodergenBackend` to verify the full pipeline works with stylesheet-applied models.

**Step 2: Run and verify PASS**

---

## Task 10: Add Rows 19-20 (prompt expansion + parallel)

**Step 1: Write subtests**

Row 19: Prompt variable expansion ($goal). Parse and verify `$goal` in prompt is expanded.
Row 20: Parallel fan-out and fan-in complete correctly. Full Run() with parallel nodes.

**Step 2: Run and verify PASS**

**Step 3: Commit**

```bash
git commit -am "test: parity matrix rows 18-20 (stylesheet, prompt expansion, parallel)"
```

---

## Task 11: Add Row 21 (custom handler registration and execution) -- NEW

**Step 1: Write the test**

Create a custom handler, register it, build engine with custom registry, run pipeline through `Run()`-style path. Verify the custom handler executes and its output (context update or file write) is observable.

This must go through the engine's `run()` method, not just unit-test the handler.

**Step 2: Run and verify PASS**

---

## Task 12: Add Row 22 (10+ node pipeline) -- NEW

**Step 1: Write the test**

Create a pipeline with 12+ nodes (mix of tool nodes and codergen nodes). Run end-to-end. Verify all nodes complete and final status is success.

Use tool nodes (parallelogram shape with tool_command) and codergen nodes (box shape) so we exercise both paths.

**Step 2: Run and verify PASS**

**Step 3: Commit**

```bash
git commit -am "test: parity matrix rows 21-22 (custom handler, 10+ nodes)"
```

---

## Task 13: Add SS11.13 Integration Smoke Test

**Step 1: Write the comprehensive integration smoke test**

Single test: `TestIntegrationSmokeTest_Section11_13`

1. **Parse** the exact DOT graph from the spec (plan -> implement -> review -> done, 5 nodes, 6 edges)
2. **Validate** - run validation, assert no error-severity diagnostics
3. **Execute** with `SimulatedCodergenBackend` (not real LLM), verify `FinalStatus == "success"`
4. **Verify artifacts** - check that `plan/prompt.md`, `plan/response.md`, `plan/status.json` exist (and same for implement, review)
5. **Verify goal gate** - the implement node has `goal_gate=true`, verify it was satisfied (check status.json for success)
6. **Verify checkpoint** - load `checkpoint.json`, assert `current_node == "done"` (or exit), assert completed_nodes contains plan, implement, review

**Step 2: Run and verify PASS**

Run: `cd /home/user/code/kilroy/.worktrees/spec-review && go test ./internal/attractor/engine/... -run '^TestIntegrationSmokeTest' -count=1 -timeout=120s`
Expected: PASS

**Step 3: Commit**

```bash
git commit -am "test: add SS11.13 integration smoke test (V11.5)"
```

---

## Task 14: Run full test suite and verify no regressions

**Step 1: Run full engine test suite**

Run: `cd /home/user/code/kilroy/.worktrees/spec-review && go test ./internal/attractor/engine/... -count=1 -timeout=300s`
Expected: All PASS

**Step 2: Run the existing guardrail matrix**

Run: `cd /home/user/code/kilroy/.worktrees/spec-review && bash scripts/e2e-guardrail-matrix.sh`
Expected: PASS

---

## Task 15: Update the audit doc

**Files:**
- Modify: `spec-compliance-audit.md`

**Step 1: Update V11.5 status to FIXED**

Change the V11.5 entry from describing the gap to noting it's been fixed with the new test file.

**Step 2: Commit**

```bash
git commit -am "docs: mark V11.5 as FIXED in spec compliance audit"
```
