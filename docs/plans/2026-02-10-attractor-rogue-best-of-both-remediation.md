# Attractor Rogue Best-of-Both Reliability Remediation Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Remove the reliability failures found in rogue-fast and rogue-slow by hardening artifact persistence, status ingestion, watchdog/liveness behavior, subgraph cancellation/cycle handling, failure causality/classification, and failover policy enforcement.

**Architecture:** Land fixes in dependency order: persistence + ingestion contract first, then liveness and traversal controls, then failure semantics, then observability/spec updates, then full regression and validation runs. Every task is test-first with small delta commits.

**Tech Stack:** Go (`internal/attractor/engine`, `internal/attractor/runtime`, `internal/llm`), attractor specs (`docs/strongdm/attractor/*.md`), run configs (`demo/rogue/*.yaml`), and Go test tooling.

---

### Task 1: Add Shared Atomic JSON Write Helper and Migrate Core Call Sites

**Files:**
- Create: `internal/attractor/runtime/atomic_write.go`
- Modify: `internal/attractor/runtime/final.go`
- Modify: `internal/attractor/runtime/checkpoint.go`
- Modify: `internal/attractor/engine/engine.go`
- Test: `internal/attractor/runtime/final_test.go`
- Test: `internal/attractor/runtime/checkpoint_test.go`

**Step 1: Write failing tests for atomic JSON persistence path**

```go
func TestFinalOutcomeSave_UsesAtomicWrite(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "final.json")

    fo := &FinalOutcome{Status: FinalFail, FailureReason: "x"}
    require.NoError(t, fo.Save(path))

    b, err := os.ReadFile(path)
    require.NoError(t, err)
    var got FinalOutcome
    require.NoError(t, json.Unmarshal(b, &got))
    require.Equal(t, FinalFail, got.Status)
}
```

**Step 2: Run test to verify it fails or proves current non-atomic path**

Run: `go test ./internal/attractor/runtime -run 'FinalOutcomeSave_UsesAtomicWrite|Checkpoint' -count=1`
Expected: FAIL or missing atomic-writer assertions.

**Step 3: Implement helper and migrate `Save`/`writeJSON` callers**

```go
func WriteFileAtomic(path string, data []byte) (err error) {
    if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
        return err
    }

    tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*.json")
    if err != nil {
        return err
    }
    tmpName := tmp.Name()
    defer func() {
        if tmpName != "" {
            _ = os.Remove(tmpName)
        }
    }()

    if _, err := tmp.Write(data); err != nil {
        _ = tmp.Close()
        return err
    }
    if err := tmp.Sync(); err != nil {
        _ = tmp.Close()
        return err
    }
    if err := tmp.Close(); err != nil {
        return err
    }

    if err := os.Rename(tmpName, path); err != nil {
        return err
    }
    tmpName = ""
    return nil
}

func WriteJSONAtomicFile(path string, v any) error {
    b, err := json.MarshalIndent(v, "", "  ")
    if err != nil {
        return err
    }
    return WriteFileAtomic(path, b)
}
```

**Step 4: Run runtime + engine tests for migrated call sites**

Run: `go test ./internal/attractor/runtime ./internal/attractor/engine -run 'Final|Checkpoint|writeJSON' -count=1`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/runtime/atomic_write.go internal/attractor/runtime/final.go internal/attractor/runtime/checkpoint.go internal/attractor/engine/engine.go internal/attractor/runtime/final_test.go internal/attractor/runtime/checkpoint_test.go
git commit -m "runtime/engine: use atomic json writes for final/checkpoint and shared writeJSON path"
```

### Task 2: Harden Status Ingestion Precedence and Legacy Fallback Copying

**Files:**
- Modify: `internal/attractor/engine/handlers.go`
- Modify: `internal/attractor/engine/status_json_test.go`
- Modify: `internal/attractor/engine/status_json_worktree_test.go`
- Modify: `internal/attractor/engine/status_json_legacy_details_test.go`

**Step 1: Add failing tests for precedence and fallback behavior**

```go
func TestCodergenStatusIngestion_CanonicalStageStatusWins(t *testing.T) {
    out, source := runStatusIngestionFixture(t, true, true, false)
    if source != "canonical" {
        t.Fatalf("source=%q want canonical", source)
    }
    if out.Status != runtime.StatusSuccess {
        t.Fatalf("status=%q want %q", out.Status, runtime.StatusSuccess)
    }
}

func TestCodergenStatusIngestion_FallbackOnlyWhenCanonicalMissing(t *testing.T) {
    out, source := runStatusIngestionFixture(t, false, true, false)
    if source != "worktree" {
        t.Fatalf("source=%q want worktree", source)
    }
    if out.Status != runtime.StatusFail {
        t.Fatalf("status=%q want %q", out.Status, runtime.StatusFail)
    }
}

func TestCodergenStatusIngestion_InvalidFallbackIsRejected(t *testing.T) {
    _, source := runStatusIngestionFixture(t, false, false, true)
    if source != "" {
        t.Fatalf("source=%q want empty", source)
    }
}
```

**Step 2: Run ingestion-focused tests and confirm failure gaps**

Run: `go test ./internal/attractor/engine -run 'StatusIngestion|WorktreeStatusJSON|status_json' -count=1`
Expected: FAIL on at least one new precedence case.

**Step 3: Implement explicit ingestion helper in `CodergenHandler.Execute` path**

```go
func copyFirstValidFallbackStatus(stageStatusPath string, fallbackPaths []string) (string, error) {
    if _, err := os.Stat(stageStatusPath); err == nil {
        return "canonical", nil
    }
    for _, p := range fallbackPaths {
        b, err := os.ReadFile(p)
        if err != nil {
            continue
        }
        // Validate that the fallback payload can drive routing.
        _, err = runtime.DecodeOutcomeJSON(b)
        if err != nil {
            continue
        }
        // Preserve raw payload bytes so we do not drop unknown legacy fields.
        if err := runtime.WriteFileAtomic(stageStatusPath, b); err != nil {
            return "", err
        }
        _ = os.Remove(p)
        return p, nil
    }
    return "", nil
}
```

**Step 4: Re-run ingestion tests**

Run: `go test ./internal/attractor/engine -run 'StatusIngestion|WorktreeStatusJSON|status_json|LegacyDetails' -count=1`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/handlers.go internal/attractor/engine/status_json_test.go internal/attractor/engine/status_json_worktree_test.go internal/attractor/engine/status_json_legacy_details_test.go
git commit -m "engine: make status ingestion precedence deterministic and reject invalid fallback status files"
```

### Task 3: Stop Heartbeat Emission Immediately After Stage Process Exit

**Files:**
- Modify: `internal/attractor/engine/codergen_router.go`
- Modify: `internal/attractor/engine/codergen_heartbeat_test.go`
- Modify: `internal/attractor/engine/progress_test.go`

**Step 1: Add failing test for stale `stage_heartbeat` after process completion**

```go
func TestRunWithConfig_HeartbeatStopsAfterProcessExit(t *testing.T) {
    events := runHeartbeatFixture(t)
    endIdx := findEventIndex(events, "stage_attempt_end", "a")
    if endIdx < 0 {
        t.Fatal("missing stage_attempt_end for node a")
    }
    for _, ev := range events[endIdx+1:] {
        if ev["event"] == "stage_heartbeat" && ev["node_id"] == "a" {
            t.Fatalf("unexpected heartbeat after attempt end: %+v", ev)
        }
    }
}
```

**Step 2: Run heartbeat tests and confirm failure**

Run: `go test ./internal/attractor/engine -run 'Heartbeat' -count=1`
Expected: FAIL with post-completion heartbeat leak.

**Step 3: Add explicit heartbeat stop channel in `runOnce`**

```go
heartbeatStop := make(chan struct{})
heartbeatDone := make(chan struct{})
go func() {
    defer close(heartbeatDone)
    ticker := time.NewTicker(interval)
    defer ticker.Stop()
    for {
        select {
        case <-ticker.C:
            emitHeartbeat(...)
        case <-heartbeatStop:
            return
        case <-ctx.Done():
            return
        }
    }
}()

runErr, idleTimedOut, err = waitWithIdleWatchdog(...)
close(heartbeatStop)
<-heartbeatDone
```

**Step 4: Re-run heartbeat and progress tests**

Run: `go test ./internal/attractor/engine -run 'Heartbeat|Progress' -count=1`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/codergen_router.go internal/attractor/engine/codergen_heartbeat_test.go internal/attractor/engine/progress_test.go
git commit -m "engine: stop codergen heartbeat goroutine as soon as stage process exits"
```

### Task 4: Make Watchdog Liveness Fanout-Aware (Parent Sees Branch Progress)

**Files:**
- Modify: `internal/attractor/engine/engine.go`
- Modify: `internal/attractor/engine/progress.go`
- Modify: `internal/attractor/engine/parallel_handlers.go`
- Modify: `internal/attractor/engine/engine_stall_watchdog_test.go`
- Modify: `internal/attractor/engine/parallel_guardrails_test.go`
- Modify: `internal/attractor/engine/parallel_test.go`

**Step 1: Add failing test where branch activity should prevent parent stall timeout**

```go
func TestRun_StallWatchdog_ParallelBranchProgressKeepsParentAlive(t *testing.T) {
    err := runParallelWatchdogFixture(t, 500*time.Millisecond)
    if err != nil {
        t.Fatalf("expected no stall watchdog timeout, got %v", err)
    }
}
```

**Step 2: Run watchdog/parallel tests to confirm failure**

Run: `go test ./internal/attractor/engine -run 'StallWatchdog|Parallel' -count=1`
Expected: FAIL with false watchdog timeout in fanout case.

**Step 3: Add parent progress forwarding from branch engines**

```go
// Engine field
progressSink func(map[string]any)

func (e *Engine) appendProgress(ev map[string]any) {
    // existing write to progress.ndjson/live.json
    if sink := e.progressSink; sink != nil {
        sink(copyMap(ev))
    }
}

func copyMap(in map[string]any) map[string]any {
    out := make(map[string]any, len(in))
    for k, v := range in {
        out[k] = v
    }
    return out
}

branchEng.progressSink = func(ev map[string]any) {
    eventName := strings.TrimSpace(fmt.Sprint(ev["event"]))
    if eventName == "" {
        return
    }
    // Emit a normalized parent-only liveness event to avoid interleaving full
    // branch progress streams into parent progress.ndjson.
    exec.Engine.appendProgress(map[string]any{
        "event":            "branch_liveness",
        "branch_key":       key,
        "branch_logs_root": branchRoot,
        "branch_event":     eventName,
    })
}
```

**Step 4: Re-run watchdog and parallel suites**

Run: `go test ./internal/attractor/engine -run 'StallWatchdog|Parallel' -count=1`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/engine.go internal/attractor/engine/progress.go internal/attractor/engine/parallel_handlers.go internal/attractor/engine/engine_stall_watchdog_test.go internal/attractor/engine/parallel_guardrails_test.go internal/attractor/engine/parallel_test.go
git commit -m "engine: forward branch liveness to parent watchdog in parallel fanout runs"
```

### Task 5: Add Cancellation Guards in `runSubgraphUntil`

**Files:**
- Modify: `internal/attractor/engine/subgraph.go`
- Modify: `internal/attractor/engine/parallel_guardrails_test.go`
- Modify: `internal/attractor/engine/next_hop_test.go`

**Step 1: Add failing tests proving no new attempts start after cancellation**

```go
func TestRunSubgraphUntil_ContextCanceled_StopsBeforeNextNode(t *testing.T) {
    got := runCanceledSubgraphFixture(t)
    if got.scheduledAfterCancel {
        t.Fatalf("scheduled node %q after cancellation", got.nextNode)
    }
}

func TestParallelCancelPrecedence_IgnorePolicyDoesNotScheduleNewWork(t *testing.T) {
    got := runParallelCancelFixture(t, "ignore")
    if got.startedNodesAfterCancel > 0 {
        t.Fatalf("started %d nodes after cancel", got.startedNodesAfterCancel)
    }
}
```

**Step 2: Run cancel guard tests and confirm failure**

Run: `go test ./internal/attractor/engine -run 'SubgraphUntil|CancelPrecedence|Cancel' -count=1`
Expected: FAIL with post-cancel node scheduling.

**Step 3: Insert guard checks at loop boundaries and before edge traversal**

```go
for {
    if err := runContextError(ctx); err != nil {
        return parallelBranchResult{
            HeadSHA:    headSHA,
            LastNodeID: lastNode,
            Outcome:    lastOutcome,
            Completed:  completed,
        }, err
    }

    out, err := eng.executeWithRetry(ctx, node, nodeRetries)
    if err != nil {
        return parallelBranchResult{}, err
    }
    if err := runContextError(ctx); err != nil {
        return parallelBranchResult{
            HeadSHA:    headSHA,
            LastNodeID: lastNode,
            Outcome:    out,
            Completed:  completed,
        }, err
    }

    next, err := selectNextEdge(...)
    // ...
}
```

**Step 4: Re-run cancellation tests**

Run: `go test ./internal/attractor/engine -run 'SubgraphUntil|Cancel|NextHop' -count=1`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/subgraph.go internal/attractor/engine/parallel_guardrails_test.go internal/attractor/engine/next_hop_test.go
git commit -m "engine: stop subgraph traversal immediately when run context is canceled"
```

### Task 6: Add Deterministic Failure Cycle Breaker Parity to Subgraph Path

**Files:**
- Modify: `internal/attractor/engine/subgraph.go`
- Modify: `internal/attractor/engine/deterministic_failure_cycle_test.go`
- Modify: `internal/attractor/engine/deterministic_failure_cycle_resume_test.go`
- Modify: `internal/attractor/engine/loop_restart_guardrails_test.go`

**Step 1: Add failing subgraph-specific cycle-breaker test**

```go
func TestRunSubgraphUntil_DeterministicFailureCycleBreaksAtLimit(t *testing.T) {
    err := runDeterministicSubgraphCycleFixture(t, 2)
    if err == nil || !strings.Contains(err.Error(), "deterministic failure cycle") {
        t.Fatalf("expected deterministic failure cycle error, got %v", err)
    }
}
```

**Step 2: Run cycle tests and confirm missing subgraph parity**

Run: `go test ./internal/attractor/engine -run 'DeterministicFailureCycle|SubgraphUntil|loop_restart' -count=1`
Expected: FAIL in subgraph cycle case.

**Step 3: Reuse existing loop signature primitives in subgraph loop**

```go
failureClass := classifyFailureClass(out)
if isFailureLoopRestartOutcome(out) && normalizedFailureClassOrDefault(failureClass) == failureClassDeterministic {
    sig := restartFailureSignature(node.ID, out, failureClass)
    eng.loopFailureSignatures[sig]++
    if eng.loopFailureSignatures[sig] >= loopRestartSignatureLimit(eng.Graph) {
        return parallelBranchResult{}, fmt.Errorf("deterministic failure cycle detected in subgraph: %s", sig)
    }
}
```

**Step 4: Re-run cycle-related tests**

Run: `go test ./internal/attractor/engine -run 'DeterministicFailureCycle|loop_restart' -count=1`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/subgraph.go internal/attractor/engine/deterministic_failure_cycle_test.go internal/attractor/engine/deterministic_failure_cycle_resume_test.go internal/attractor/engine/loop_restart_guardrails_test.go
git commit -m "engine: apply deterministic failure cycle breaker in subgraph traversal"
```

### Task 7: Preserve Failure Causality Through Conditional Routing and Branch Context

**Files:**
- Modify: `internal/attractor/engine/handlers.go`
- Modify: `internal/attractor/engine/subgraph.go`
- Modify: `internal/attractor/engine/conditional_passthrough_test.go`
- Modify: `internal/attractor/engine/failure_routing_fanin_test.go`

**Step 1: Add failing tests for failure metadata pass-through**

```go
func TestConditionalPassThrough_PreservesFailureReasonAndClass(t *testing.T) {
    out := runConditionalFixture(t, "fail", "provider timeout", "transient_infra")
    if out.FailureReason != "provider timeout" {
        t.Fatalf("failure_reason=%q", out.FailureReason)
    }
    if out.ContextUpdates["failure_class"] != "transient_infra" {
        t.Fatalf("failure_class=%v", out.ContextUpdates["failure_class"])
    }
}

func TestSubgraphContext_PreservesFailureReasonAcrossNodes(t *testing.T) {
    ctx := runSubgraphFailureFixture(t)
    if got := ctx.GetString("failure_reason", ""); got == "" {
        t.Fatal("failure_reason missing in context")
    }
    if got := ctx.GetString("failure_class", ""); got == "" {
        t.Fatal("failure_class missing in context")
    }
}
```

**Step 2: Run pass-through tests and confirm gaps**

Run: `go test ./internal/attractor/engine -run 'ConditionalPassThrough|SubgraphContext|FanIn' -count=1`
Expected: FAIL in at least one metadata path.

**Step 3: Propagate `failure_reason` and `failure_class` explicitly in context updates**

```go
// ConditionalHandler
return runtime.Outcome{
    Status:         prevStatus,
    PreferredLabel: prevPreferred,
    FailureReason:  prevFailure,
    ContextUpdates: map[string]any{"failure_class": prevFailureClass},
}

// subgraph loop
eng.Context.Set("failure_reason", out.FailureReason)
eng.Context.Set("failure_class", classifyFailureClass(out))
```

**Step 4: Re-run routing and pass-through tests**

Run: `go test ./internal/attractor/engine -run 'Conditional|FanIn|SubgraphContext' -count=1`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/handlers.go internal/attractor/engine/subgraph.go internal/attractor/engine/conditional_passthrough_test.go internal/attractor/engine/failure_routing_fanin_test.go
git commit -m "engine: preserve failure reason/class metadata across conditional and subgraph routing"
```

### Task 8: Separate Canceled-Run Classification From Deterministic API Failures

**Files:**
- Modify: `internal/attractor/engine/loop_restart_policy.go`
- Modify: `internal/attractor/engine/failure_policy.go`
- Modify: `internal/attractor/engine/provider_error_classification.go`
- Modify: `internal/attractor/engine/provider_error_classification_test.go`
- Modify: `internal/attractor/engine/failure_policy_test.go`
- Modify: `internal/attractor/engine/retry_failure_class_test.go`

**Step 1: Add failing tests for canceled-class semantics**

```go
func TestClassifyAPIError_AbortErrorMapsToCanceledClass(t *testing.T) {
    cls, _ := classifyAPIError(llm.NewAbortError("operator canceled"))
    if cls != failureClassCanceled {
        t.Fatalf("class=%q want %q", cls, failureClassCanceled)
    }
}

func TestShouldRetryOutcome_CanceledNeverRetries(t *testing.T) {
    out := runtime.Outcome{Status: runtime.StatusFail, FailureReason: "run canceled"}
    if shouldRetryOutcome(out, failureClassCanceled) {
        t.Fatal("canceled outcome must not retry")
    }
}
```

**Step 2: Run classification/retry tests and confirm failure**

Run: `go test ./internal/attractor/engine -run 'ClassifyAPIError|ShouldRetryOutcome|retry_failure_class' -count=1`
Expected: FAIL because canceled class is not yet modeled.

**Step 3: Add `failureClassCanceled` and thread it through existing classifiers**

```go
const (
    failureClassTransientInfra = "transient_infra"
    failureClassDeterministic  = "deterministic"
    failureClassCanceled       = "canceled"
)

func classifyAPIError(err error) (string, string) {
    var abortErr *llm.AbortError
    if errors.As(err, &abortErr) {
        return failureClassCanceled, "api_canceled|api|abort"
    }
    // Keep WrapContextError contract for context-derived errors.
    // Existing typed/non-typed logic follows...
}
```

**Step 4: Re-run classifier and retry-policy tests**

Run: `go test ./internal/attractor/engine -run 'ClassifyAPIError|FailurePolicy|retry_failure_class' -count=1`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/loop_restart_policy.go internal/attractor/engine/failure_policy.go internal/attractor/engine/provider_error_classification.go internal/attractor/engine/provider_error_classification_test.go internal/attractor/engine/failure_policy_test.go internal/attractor/engine/retry_failure_class_test.go
git commit -m "engine: introduce canceled failure class and prevent canceled outcomes from retrying"
```

### Task 9: Enforce No-Failover Pinning via Existing Runtime Failover Semantics

**Files:**
- Modify: `internal/attractor/engine/provider_runtime.go`
- Modify: `internal/attractor/engine/codergen_router.go`
- Modify: `internal/attractor/engine/provider_runtime_test.go`
- Modify: `internal/attractor/engine/codergen_failover_test.go`
- Modify: `demo/rogue/run-fast.yaml`
- Modify: `demo/rogue/run.yaml`

**Step 1: Add failing tests for explicit `failover: []` meaning "hard pin"**

```go
func TestWithFailoverText_ExplicitEmptyFailoverDoesNotFallback(t *testing.T) {
    _, _, err := runNoFailoverFixture(t)
    if err == nil || !strings.Contains(err.Error(), "no failover allowed") {
        t.Fatalf("expected explicit no-failover error, got %v", err)
    }
}

func TestResolveProviderRuntimes_ExplicitEmptyFailoverPreserved(t *testing.T) {
    rt := loadProviderRuntimeFixture(t, "zai", []string{})
    if !rt.FailoverExplicit {
        t.Fatal("expected failover to be marked explicit")
    }
    if len(rt.Failover) != 0 {
        t.Fatalf("expected zero failover targets, got %v", rt.Failover)
    }
}
```

**Step 2: Run failover tests and confirm failure behavior**

Run: `go test ./internal/attractor/engine -run 'Failover|ProviderRuntime' -count=1`
Expected: FAIL if any implicit fallback still occurs.

**Step 3: Wire no-failover behavior in router runtime path**

```go
// provider_runtime.go
if pc.Failover != nil {
    rt.FailoverExplicit = true
    rt.Failover = providerspec.CanonicalizeProviderList(pc.Failover)
}

// codergen_router.go
order, explicit := failoverOrderFromRuntime(provider, runtimes)
if explicit && len(order) == 0 {
    return "", providerUse{}, fmt.Errorf("no failover allowed by runtime config for provider %s", provider)
}
if !explicit && len(order) == 0 {
    order = failoverOrder(provider) // legacy default only when failover omitted
}
```

**Step 4: Re-run failover tests + config tests**

Run: `go test ./internal/attractor/engine -run 'Failover|ProviderRuntime|LoadRunConfig' -count=1`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/provider_runtime.go internal/attractor/engine/codergen_router.go internal/attractor/engine/provider_runtime_test.go internal/attractor/engine/codergen_failover_test.go demo/rogue/run-fast.yaml demo/rogue/run.yaml
git commit -m "engine/config: enforce explicit no-failover behavior and pin rogue configs to declared failover chains"
```

### Task 10: Add Missing Observability Events for Ingestion/Cycle/Cancel Decisions

**Files:**
- Modify: `internal/attractor/engine/progress.go`
- Modify: `internal/attractor/engine/handlers.go`
- Modify: `internal/attractor/engine/subgraph.go`
- Modify: `internal/attractor/engine/engine.go`
- Modify: `internal/attractor/engine/progress_test.go`

**Step 1: Add failing tests for required progress events**

```go
func TestProgressIncludesStatusIngestionDecisionEvent(t *testing.T) {
    events := runProgressFixture(t)
    if !hasEvent(events, "status_ingestion_decision") {
        t.Fatal("missing status_ingestion_decision event")
    }
}

func TestProgressIncludesSubgraphCycleBreakEvent(t *testing.T) {
    events := runProgressFixture(t)
    if !hasEvent(events, "subgraph_deterministic_failure_cycle_breaker") {
        t.Fatal("missing subgraph cycle breaker event")
    }
}

func TestProgressIncludesCancellationExitEvent(t *testing.T) {
    events := runProgressFixture(t)
    if !hasEvent(events, "subgraph_canceled_exit") {
        t.Fatal("missing subgraph cancellation exit event")
    }
}
```

**Step 2: Run progress tests and confirm missing events**

Run: `go test ./internal/attractor/engine -run 'ProgressIncludes|progress' -count=1`
Expected: FAIL because events are not emitted yet.

**Step 3: Emit structured events with stable keys at decision points**

```go
e.appendProgress(map[string]any{
    "event": "status_ingestion_decision",
    "node_id": node.ID,
    "source": source,
    "copied": copied,
})
```

**Step 4: Re-run progress tests**

Run: `go test ./internal/attractor/engine -run 'ProgressIncludes|progress' -count=1`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/progress.go internal/attractor/engine/handlers.go internal/attractor/engine/subgraph.go internal/attractor/engine/engine.go internal/attractor/engine/progress_test.go
git commit -m "engine: emit structured progress events for status ingestion, cancellation exits, and cycle-break decisions"
```

### Task 11: Document Spec Deltas and Contracts

**Files:**
- Modify: `docs/strongdm/attractor/attractor-spec.md`
- Modify: `docs/strongdm/attractor/coding-agent-loop-spec.md`
- Modify: `docs/strongdm/attractor/unified-llm-spec.md`

**Step 1: Add a failing docs checklist in this plan**

```markdown
- [ ] Canonical vs fallback status ingestion contract documented
- [ ] Fanout-aware watchdog liveness contract documented
- [ ] Subgraph cancellation and deterministic cycle-break parity documented
- [ ] Canceled failure class contract documented
- [ ] Explicit no-failover semantics for `failover: []` documented
```

**Step 2: Run grep checks to show missing wording before edits**

Run: `rg -n 'legacy status fallback|fanout liveness|canceled failure class|loop_restart_signature_limit|failover:\s*\[\]' docs/strongdm/attractor`
Expected: One or more required concepts missing.

**Step 3: Add minimal, normative text blocks in the relevant spec sections**

```markdown
Run-level cancellation takes precedence over branch-local retry/error policy.
No additional stage attempts may start after cancellation is observed.
```

**Step 4: Re-run grep checks**

Run: `rg -n 'legacy status fallback|fanout liveness|canceled failure class|loop_restart_signature_limit|failover:\s*\[\]' docs/strongdm/attractor`
Expected: All checklist concepts now present.

**Step 5: Commit**

```bash
git add docs/strongdm/attractor/attractor-spec.md docs/strongdm/attractor/coding-agent-loop-spec.md docs/strongdm/attractor/unified-llm-spec.md
git commit -m "docs: codify attractor runtime contracts for status ingestion, liveness, cancellation, cycle-breaks, and no-failover semantics"
```

### Task 12: Full Regression Gate + Rogue-Fast Validation Execution

**Files:**
- Modify: `postmortem-rogue-best-of-both-fixes.md`

**Step 1: Add release evidence checklist to postmortem**

```markdown
- [ ] `go test ./internal/attractor/... -count=1`
- [ ] `go test ./internal/llm/... -count=1`
- [ ] `go build -o ./kilroy ./cmd/kilroy`
- [ ] `./kilroy attractor validate --graph demo/rogue/rogue_fast.dot`
- [ ] One rogue-fast validation run recorded (run_id + status artifact)
```

**Step 2: Run full regression suites**

Run: `go test ./internal/attractor/... ./internal/llm/... -count=1`
Expected: PASS.

**Step 3: Build and validate graph with local binary**

Run:
- `go build -o ./kilroy ./cmd/kilroy`
- `./kilroy attractor validate --graph demo/rogue/rogue_fast.dot`

Expected: Build succeeds; graph validator prints success.

**Step 4: Run one rogue-fast validation execution and capture artifacts**

Run:
- if `run-fast.yaml` is real-provider and credentials are available: execute only the exact user-approved production command
- if credentials are unavailable: execute a test-shim dry run using a copied shim config (same graph/runtime policy, `llm.cli_profile=test_shim`, explicit shim executables, `--allow-test-shim`)

Expected: run starts, logs directory created, and `live.json`/`progress.ndjson` appear.

**Step 5: Commit**

```bash
git add postmortem-rogue-best-of-both-fixes.md
git commit -m "postmortem: record regression evidence and rogue-fast validation run details"
```

## Cross-Task Guardrails

- Keep each commit delta-oriented and scoped to one task.
- Do not skip failing-test confirmation before implementation.
- Re-run `go test ./internal/attractor/... -count=1` after any task that touches engine traversal/routing.
- Re-run `go test ./internal/llm/... -count=1` after any task that touches provider error handling.
- `internal/attractor/engine/progress_test.go` is touched in Task 3 and Task 10; merge/rebase those edits before either commit to avoid test drift.

## Suggested Execution Branch

```bash
git checkout -b plan/rogue-best-of-both-remediation-20260210
```
