# Adaptive Turn Budget + Unknown-Complexity Recovery Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Make Attractor robust when a node unexpectedly needs much more work than estimated by adding adaptive turn-budget continuation and raising planned turn budgets to a 4x baseline.

**Architecture:** Add a runtime policy layer for agent-loop turn budgets (including one in-session extension path) so a stage can continue instead of restarting from scratch when it hits `max_agent_turns`. Keep failure semantics explicit by tagging turn-budget exhaustion separately from infra failures and route it to targeted recovery paths. Update `english-to-dotfile` guidance so generated graphs start with materially larger budgets (4x) and include a turn-budget recovery pattern by default.

**Tech Stack:** Go (`internal/agent`, `internal/attractor/engine`), DOT graph conventions, skill docs (`skills/english-to-dotfile/SKILL.md`), Go test suite.

---

### Task 1: Add Runtime Policy Fields for Adaptive Agent Turn Budgets

**Files:**
- Modify: `internal/attractor/engine/config.go`
- Modify: `internal/attractor/engine/run_with_config.go`
- Modify: `internal/attractor/engine/engine.go`
- Test: `internal/attractor/engine/config_test.go`
- Test: `internal/attractor/engine/run_with_config_test.go`

**Step 1: Write failing config tests for new policy fields**

```go
func TestApplyConfigDefaults_AgentTurnBudgetPolicyDefaults(t *testing.T) {
    cfg := &RunConfigFile{}
    applyConfigDefaults(cfg)

    if cfg.RuntimePolicy.AgentTurnAutoExtendEnabled == nil || !*cfg.RuntimePolicy.AgentTurnAutoExtendEnabled {
        t.Fatalf("expected agent_turn_auto_extend_enabled default=true")
    }
    if cfg.RuntimePolicy.AgentTurnAutoExtendMultiplier == nil || *cfg.RuntimePolicy.AgentTurnAutoExtendMultiplier != 4 {
        t.Fatalf("expected agent_turn_auto_extend_multiplier default=4")
    }
    if cfg.RuntimePolicy.AgentTurnAutoExtendMaxExtensions == nil || *cfg.RuntimePolicy.AgentTurnAutoExtendMaxExtensions != 1 {
        t.Fatalf("expected agent_turn_auto_extend_max_extensions default=1")
    }
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/attractor/engine -run AgentTurnBudgetPolicyDefaults -count=1`
Expected: FAIL with unknown fields / missing defaults.

**Step 3: Implement minimal config + option plumbing**

```go
type RuntimePolicyConfig struct {
    StageTimeoutMS                    *int  `json:"stage_timeout_ms,omitempty" yaml:"stage_timeout_ms,omitempty"`
    StallTimeoutMS                    *int  `json:"stall_timeout_ms,omitempty" yaml:"stall_timeout_ms,omitempty"`
    StallCheckIntervalMS              *int  `json:"stall_check_interval_ms,omitempty" yaml:"stall_check_interval_ms,omitempty"`
    MaxLLMRetries                     *int  `json:"max_llm_retries,omitempty" yaml:"max_llm_retries,omitempty"`
    AgentTurnAutoExtendEnabled        *bool `json:"agent_turn_auto_extend_enabled,omitempty" yaml:"agent_turn_auto_extend_enabled,omitempty"`
    AgentTurnAutoExtendMultiplier     *int  `json:"agent_turn_auto_extend_multiplier,omitempty" yaml:"agent_turn_auto_extend_multiplier,omitempty"`
    AgentTurnAutoExtendMaxExtensions  *int  `json:"agent_turn_auto_extend_max_extensions,omitempty" yaml:"agent_turn_auto_extend_max_extensions,omitempty"`
}
```

```go
if cfg.RuntimePolicy.AgentTurnAutoExtendEnabled == nil {
    v := true
    cfg.RuntimePolicy.AgentTurnAutoExtendEnabled = &v
}
if cfg.RuntimePolicy.AgentTurnAutoExtendMultiplier == nil {
    v := 4
    cfg.RuntimePolicy.AgentTurnAutoExtendMultiplier = &v
}
if cfg.RuntimePolicy.AgentTurnAutoExtendMaxExtensions == nil {
    v := 1
    cfg.RuntimePolicy.AgentTurnAutoExtendMaxExtensions = &v
}
```

```go
type RunOptions struct {
    // existing fields...
    AgentTurnAutoExtendEnabled       bool
    AgentTurnAutoExtendMultiplier    int
    AgentTurnAutoExtendMaxExtensions int
}
```

**Step 4: Run focused tests to verify pass**

Run: `go test ./internal/attractor/engine -run 'AgentTurnBudgetPolicyDefaults|LoadRunConfig|RunWithConfig' -count=1`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/config.go internal/attractor/engine/run_with_config.go internal/attractor/engine/engine.go internal/attractor/engine/config_test.go internal/attractor/engine/run_with_config_test.go
git commit -m "feat(engine): add runtime policy controls for adaptive agent turn-budget extension with 4x default multiplier"
```

### Task 2: Add Session-Level MaxTurns Mutation for In-Session Continuation

**Files:**
- Modify: `internal/agent/session.go`
- Modify: `internal/agent/events.go`
- Test: `internal/agent/session_dod_test.go`

**Step 1: Write failing test for increasing max turns after turn-limit hit**

```go
func TestSession_CanIncreaseMaxTurnsAfterTurnLimit(t *testing.T) {
    // Arrange: session with MaxTurns=1 and fake client requiring >1 rounds.
    // Act: ProcessInput returns ErrTurnLimit, then SetMaxTurns(4), then ProcessInput again.
    // Assert: second call succeeds without creating a new session.
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/agent -run IncreaseMaxTurnsAfterTurnLimit -count=1`
Expected: FAIL because no setter exists.

**Step 3: Implement minimal API and event emission**

```go
const (
    // existing kinds...
    EventTurnBudgetAdjusted EventKind = "TURN_BUDGET_ADJUSTED"
)
```

```go
func (s *Session) SetMaxTurns(maxTurns int) {
    if maxTurns <= 0 {
        return
    }
    s.mu.Lock()
    defer s.mu.Unlock()
    if s.closed {
        return
    }
    if maxTurns <= s.cfg.MaxTurns {
        return
    }
    prev := s.cfg.MaxTurns
    s.cfg.MaxTurns = maxTurns
    s.emit(EventTurnBudgetAdjusted, map[string]any{"previous": prev, "current": maxTurns})
}
```

**Step 4: Run targeted tests**

Run: `go test ./internal/agent -run 'IncreaseMaxTurnsAfterTurnLimit|TurnLimit' -count=1`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/agent/session.go internal/agent/events.go internal/agent/session_dod_test.go
git commit -m "feat(agent): support in-session max_turns increases so turn-limit hits can continue without session reset"
```

### Task 3: Implement Adaptive Turn-Budget Continuation in Codergen Agent-Loop

**Files:**
- Modify: `internal/attractor/engine/codergen_router.go`
- Create: `internal/attractor/engine/codergen_turn_budget_test.go`
- Modify: `internal/attractor/engine/codergen_failover_test.go`

**Step 1: Write failing tests for adaptive extension behavior**

```go
func TestCodergenAgentLoop_ExtendsTurnBudgetOnTurnLimit_AndContinuesSameSession(t *testing.T) {
    // node.max_agent_turns=10, runtime multiplier=4, max_extensions=1
    // fake client requires >10 turns but <40 turns
    // expect success and progress event turn_budget_extended with from=10 to=40
}

func TestCodergenAgentLoop_ExhaustedAfterMaxExtensions_ReturnsDeterministicTurnBudgetFailure(t *testing.T) {
    // fake client requires >40 turns
    // expect fail/retry outcome with failure_code=turn_budget_exhausted
}
```

**Step 2: Run tests to verify fail**

Run: `go test ./internal/attractor/engine -run CodergenAgentLoop_ExtendsTurnBudget -count=1`
Expected: FAIL (no extension behavior yet).

**Step 3: Implement extension loop in `agent_loop` execution path**

```go
type turnBudgetPolicy struct {
    enabled       bool
    multiplier    int
    maxExtensions int
}

func resolveTurnBudgetPolicy(execCtx *Execution, node *model.Node) turnBudgetPolicy {
    // precedence: node attrs -> graph attrs -> runtime policy defaults
}
```

```go
text, runErr := sess.ProcessInput(ctx, prompt)
if errors.Is(runErr, agent.ErrTurnLimit) && policy.enabled {
    current := sessCfg.MaxTurns
    for ext := 0; ext < policy.maxExtensions && errors.Is(runErr, agent.ErrTurnLimit); ext++ {
        next := current * policy.multiplier
        sess.SetMaxTurns(next)
        appendProgressTurnBudgetExtended(execCtx, node.ID, current, next, ext+1, policy.maxExtensions)
        current = next
        text, runErr = sess.ProcessInput(ctx, "Continue from your current work. Do not restart analysis. Finish remaining acceptance criteria and write status JSON.")
    }
}
```

**Step 4: Run targeted engine tests**

Run: `go test ./internal/attractor/engine -run 'CodergenAgentLoop_ExtendsTurnBudget|FailoverSkipsTurnLimit' -count=1`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/codergen_router.go internal/attractor/engine/codergen_turn_budget_test.go internal/attractor/engine/codergen_failover_test.go
git commit -m "feat(codergen): auto-extend agent-loop turn budgets 4x and continue in-session on unexpected turn-limit exhaustion"
```

### Task 4: Add Explicit Failure Code for Turn-Budget Exhaustion

**Files:**
- Modify: `internal/attractor/engine/provider_error_classification.go`
- Modify: `internal/attractor/engine/handlers.go`
- Modify: `internal/attractor/engine/provider_error_classification_test.go`
- Modify: `internal/attractor/engine/retry_failure_class_test.go`

**Step 1: Write failing tests for turn-budget failure coding**

```go
func TestClassifyAPIError_TurnLimitMapsToTurnBudgetExhaustedCode(t *testing.T) {
    cls, sig, code := classifyAPIErrorDetailed(fmt.Errorf("turn limit reached (max_turns=40)"))
    if cls != failureClassDeterministic || code != "turn_budget_exhausted" {
        t.Fatalf("got cls=%q code=%q sig=%q", cls, code, sig)
    }
}
```

**Step 2: Run tests to verify fail**

Run: `go test ./internal/attractor/engine -run TurnLimitMapsToTurnBudgetExhaustedCode -count=1`
Expected: FAIL (code not emitted yet).

**Step 3: Implement detailed classification + context update propagation**

```go
type apiFailureInfo struct {
    Class     string
    Signature string
    Code      string
}

func classifyAPIErrorDetailed(err error) apiFailureInfo {
    // include explicit turn-limit detection
    // Code: "turn_budget_exhausted"
}
```

```go
info := classifyAPIErrorDetailed(err)
return runtime.Outcome{
    Status:        runtime.StatusRetry,
    FailureReason: err.Error(),
    Meta: map[string]any{
        "failure_class":     info.Class,
        "failure_signature": info.Signature,
        "failure_code":      info.Code,
    },
    ContextUpdates: map[string]any{
        "failure_class": info.Class,
        "failure_code":  info.Code,
    },
}, nil
```

**Step 4: Run focused tests**

Run: `go test ./internal/attractor/engine -run 'TurnLimitMapsToTurnBudgetExhaustedCode|retry_failure_class' -count=1`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/provider_error_classification.go internal/attractor/engine/handlers.go internal/attractor/engine/provider_error_classification_test.go internal/attractor/engine/retry_failure_class_test.go
git commit -m "feat(engine): tag turn-limit failures with failure_code=turn_budget_exhausted for targeted routing"
```

### Task 5: Update English-to-Dotfile Guidance for Unknown Complexity + 4x Budgets

**Files:**
- Modify: `skills/english-to-dotfile/SKILL.md`

**Step 1: Add failing test-like acceptance checks (grep-based contract checks)**

```bash
rg -n "unknown complexity|turn_budget_exhausted|4x|max_agent_turns" skills/english-to-dotfile/SKILL.md
```

Expected initially: Missing one or more required guidance sections.

**Step 2: Add concise guidance + pattern snippet**

```dot
check_X -> continue_X [condition="outcome=fail && context.failure_code=turn_budget_exhausted"]
check_X -> impl_X     [condition="outcome=fail && context.failure_code!=turn_budget_exhausted && context.failure_class!=transient_infra"]
check_X -> impl_X     [condition="outcome=fail && context.failure_class=transient_infra", loop_restart=true]
```

Add guidance text:
- Treat node complexity estimates as uncertain; default to high initial budgets.
- Use a 4x turn-budget baseline shift relative to prior templates.
- Prefer continuation/recovery node over full-stage restart for `turn_budget_exhausted`.

**Step 3: Include concrete budget examples**

```text
Previous heuristic examples: 10 / 25 / 60
New baseline examples (4x): 40 / 100 / 240
```

**Step 4: Validate the skill text contains all required guidance**

Run: `rg -n "unknown complexity|turn_budget_exhausted|4x|40 / 100 / 240|continue_X" skills/english-to-dotfile/SKILL.md`
Expected: all patterns found.

**Step 5: Commit**

```bash
git add skills/english-to-dotfile/SKILL.md
git commit -m "docs(skill): teach unknown-complexity handling and 4x max_agent_turns baseline with turn-budget recovery routing"
```

### Task 6: Apply New Budget/Routing Pattern to Rogue Fast Dot and Validate

**Files:**
- Modify: `demo/rogue/rogue_fast_regen.dot`
- Modify (if this is the active launch graph): `demo/rogue/rogue_fast.dot`
- Test/validate: `demo/rogue/rogue_fast_regen.dot`

**Step 1: Write failing validation goal**

Run:
```bash
./kilroy attractor validate --graph demo/rogue/rogue_fast_regen.dot
```

Expected: likely passes today, but does not yet include turn-budget exhaustion recovery edges and still uses pre-4x budgets.

**Step 2: Perform 4x budget update + targeted recovery edges**

Example edits:
```dot
impl_analysis [max_agent_turns=240, ...]      // was 60
verify_analysis [max_agent_turns=96, ...]     // was 24
```

Add continuation node:
```dot
continue_analysis [
  shape=box,
  class="hard",
  max_agent_turns=240,
  prompt="Read .ai/rogue_analysis.md and continue from existing partial work. Do not restart from scratch. Finish gaps and write status JSON."
]

check_analysis -> continue_analysis [condition="outcome=fail && context.failure_code=turn_budget_exhausted", label="continue"]
continue_analysis -> verify_analysis
```

**Step 3: Validate graph again**

Run:
```bash
./kilroy attractor validate --graph demo/rogue/rogue_fast_regen.dot
```
Expected: PASS.

**Step 4: Run focused tests for routing assumptions**

Run:
```bash
go test ./internal/attractor/engine -run 'RetryFailureClass|LoopRestart|EdgeSelection' -count=1
```
Expected: PASS.

**Step 5: Commit**

```bash
git add demo/rogue/rogue_fast_regen.dot demo/rogue/rogue_fast.dot
git commit -m "demo(rogue_fast): adopt 4x turn budgets and add turn_budget_exhausted continuation paths to avoid full-stage rework"
```

### Task 7: End-to-End Verification Matrix

**Files:**
- Modify (if needed): `docs/strongdm/attractor/README.md`
- Create: `docs/plans/2026-02-11-adaptive-turn-budget-and-unknown-complexity-recovery-validation.md`

**Step 1: Define verification scenarios**

```text
A) Unexpectedly heavy node now succeeds after one 4x extension
B) Truly runaway node fails with failure_code=turn_budget_exhausted
C) Transient infra failure still follows loop_restart guarded path
D) Deterministic failure still emits stage_retry_blocked
```

**Step 2: Execute targeted tests**

Run:
```bash
go test ./internal/agent -run 'TurnLimit|IncreaseMaxTurns' -count=1
go test ./internal/attractor/engine -run 'CodergenAgentLoop_ExtendsTurnBudget|retry_failure_class|LoopRestart|DeterministicFailureCycle' -count=1
```
Expected: all PASS.

**Step 3: Document observed behavior + commands**

Write `docs/plans/2026-02-11-adaptive-turn-budget-and-unknown-complexity-recovery-validation.md` with:
- exact command lines
- pass/fail outcomes
- sample `progress.ndjson` events (`turn_budget_extended`, `stage_retry_blocked`, `loop_restart`)

**Step 4: Commit verification artifact**

```bash
git add docs/plans/2026-02-11-adaptive-turn-budget-and-unknown-complexity-recovery-validation.md docs/strongdm/attractor/README.md
git commit -m "docs: record adaptive turn-budget verification outcomes and operational event signatures"
```

