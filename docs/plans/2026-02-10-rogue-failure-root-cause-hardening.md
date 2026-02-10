# Rogue Failure Root-Cause Hardening Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Eliminate the rogue/rogue-fast deterministic failure pattern by introducing an explicit stage status contract, aligning `loop_restart` usage with runtime policy, and codifying those rules in validation and skill guidance.

**Architecture:** Fix this in layers: (1) make stage outcome signaling explicit and absolute-path based so `cd` does not break status ingestion, (2) pass that contract through both CLI and API execution paths, (3) prevent invalid `loop_restart` failure edges at authoring time with a validator lint, (4) patch `demo/rogue/rogue_fast.dot` and update `english-to-dotfile` so future graphs are generated correctly by default.

**Tech Stack:** Go (`internal/attractor/engine`, `internal/attractor/validate`, `internal/agent`), DOT graphs (`demo/rogue/*.dot`), skill docs (`skills/english-to-dotfile/SKILL.md`), Go test tooling, Attractor graph validator.

---

### Task 1: Add Failing Regression Tests for Stage Status Contract

**Files:**
- Create: `internal/attractor/engine/stage_status_contract_test.go`
- Modify: `internal/attractor/engine/status_json_worktree_test.go`

**Step 1: Write failing tests for explicit status contract behavior**

```go
func TestStageStatusContract_AbsolutePaths_FromRelativeWorktreeInput(t *testing.T) {
    rel := filepath.Join("tmp", "wt")
    c := buildStageStatusContract(rel)

    if !filepath.IsAbs(c.PrimaryPath) {
        t.Fatalf("primary path must be absolute, got %q", c.PrimaryPath)
    }
    if !filepath.IsAbs(c.FallbackPath) {
        t.Fatalf("fallback path must be absolute, got %q", c.FallbackPath)
    }
}

func TestStageStatusContract_DefaultPaths(t *testing.T) {
    wt := t.TempDir()
    c := buildStageStatusContract(wt)

    if got, want := c.PrimaryPath, filepath.Join(wt, "status.json"); got != want {
        t.Fatalf("primary path: got %q want %q", got, want)
    }
    if got, want := c.FallbackPath, filepath.Join(wt, ".ai", "status.json"); got != want {
        t.Fatalf("fallback path: got %q want %q", got, want)
    }
    if c.EnvVars[stageStatusPathEnvKey] == "" {
        t.Fatalf("missing %s in EnvVars", stageStatusPathEnvKey)
    }
    if !strings.Contains(c.PromptPreamble, stageStatusPathEnvKey) {
        t.Fatalf("prompt preamble missing env key %s", stageStatusPathEnvKey)
    }
}

func TestRunWithConfig_CLIBackend_StatusContractPath_HandlesNestedCD(t *testing.T) {
    // Fake CLI exits if KILROY_STAGE_STATUS_PATH is absent.
    // When present, it cds into demo/rogue/rogue-wasm and writes status to that absolute path.
    // This reproduces the rogue verify_scaffold failure mode while proving the fix.
}

func TestRunWithConfig_CLIBackend_StatusContract_ClearsStaleWorktreeStatusBeforeStage(t *testing.T) {
    // Seed worktree root status.json with stale data before codergen executes.
    // Fake CLI writes no status file.
    // Assert stage does NOT ingest the stale file (must fail missing status.json, not stale outcome).
}
```

**Step 2: Run tests to verify failure before implementation**

Run: `go test ./internal/attractor/engine -run 'StageStatusContract|AbsolutePaths_FromRelativeWorktreeInput|StatusContractPath_HandlesNestedCD|StatusContract_ClearsStaleWorktreeStatusBeforeStage' -count=1`
Expected: FAIL with `undefined: buildStageStatusContract` and missing contract symbols.

**Step 3: Add a failing assertion that the status contract is visible in codergen prompt artifacts**

```go
func TestRunWithConfig_CLIBackend_StatusContractPromptPreambleWritten(t *testing.T) {
    // Execute one codergen node, then read logs_root/a/prompt.md and assert it contains:
    // - "Execution status contract"
    // - KILROY_STAGE_STATUS_PATH
    // - absolute worktree status path
}
```

**Step 4: Re-run targeted tests and confirm expected failures**

Run: `go test ./internal/attractor/engine -run 'StageStatusContract|AbsolutePaths_FromRelativeWorktreeInput|StatusContract_ClearsStaleWorktreeStatusBeforeStage|StatusContractPromptPreambleWritten' -count=1`
Expected: FAIL on missing preamble/contract implementation.

**Step 5: Commit the failing test scaffolding**

```bash
git add internal/attractor/engine/stage_status_contract_test.go internal/attractor/engine/status_json_worktree_test.go
git commit -m "test(engine): add regression coverage for explicit stage status contract and nested-cd status writes"
```

### Task 2: Implement Stage Status Contract in Engine Prompt and Ingestion Path

**Files:**
- Create: `internal/attractor/engine/stage_status_contract.go`
- Modify: `internal/attractor/engine/handlers.go`

**Step 1: Implement a shared stage-status contract helper**

```go
package engine

import (
    "fmt"
    "path/filepath"
    "strings"
)

const (
    stageStatusPathEnvKey         = "KILROY_STAGE_STATUS_PATH"
    stageStatusFallbackPathEnvKey = "KILROY_STAGE_STATUS_FALLBACK_PATH"
)

type stageStatusContract struct {
    PrimaryPath    string
    FallbackPath   string
    PromptPreamble string
    EnvVars        map[string]string
    Fallbacks      []fallbackStatusPath
}

func buildStageStatusContract(worktreeDir string) stageStatusContract {
    wt := strings.TrimSpace(worktreeDir)
    if wt == "" {
        return stageStatusContract{}
    }
    wtAbs, err := filepath.Abs(wt)
    if err != nil {
        return stageStatusContract{}
    }
    primary := filepath.Join(wtAbs, "status.json")
    fallback := filepath.Join(wtAbs, ".ai", "status.json")
    return stageStatusContract{
        PrimaryPath:  primary,
        FallbackPath: fallback,
        PromptPreamble: fmt.Sprintf(
            "Execution status contract:\n"+
                "- Write status JSON to $%s (absolute path).\n"+
                "- Primary path: %s\n"+
                "- Fallback path: %s\n"+
                "- Do not write status.json to nested module directories.\n",
            stageStatusPathEnvKey,
            primary,
            fallback,
        ),
        EnvVars: map[string]string{
            stageStatusPathEnvKey:         primary,
            stageStatusFallbackPathEnvKey: fallback,
        },
        Fallbacks: []fallbackStatusPath{
            {path: primary, source: statusSourceWorktree},
            {path: fallback, source: statusSourceDotAI},
        },
    }
}
```

**Step 2: Wire contract into `CodergenHandler.Execute` before backend call**

```go
contract := buildStageStatusContract(exec.WorktreeDir)
worktreeStatusPaths := contract.Fallbacks

// Preserve current behavior: remove stale worktree status files from prior stages
// so fallback ingestion cannot attribute old outcomes to the current node.
for _, statusPath := range worktreeStatusPaths {
    _ = os.Remove(statusPath.path)
}

promptParts := make([]string, 0, 3)
if strings.TrimSpace(contract.PromptPreamble) != "" {
    promptParts = append(promptParts, strings.TrimSpace(contract.PromptPreamble))
}
if strings.TrimSpace(promptText) != "" {
    promptParts = append(promptParts, strings.TrimSpace(promptText))
}
promptText = strings.Join(promptParts, "\n\n")

if exec != nil && exec.Engine != nil {
    exec.Engine.appendProgress(map[string]any{
        "event":                "status_contract",
        "node_id":              node.ID,
        "status_path":          contract.PrimaryPath,
        "status_fallback_path": contract.FallbackPath,
    })
}
```

**Step 3: Keep ingestion precedence deterministic and contract-aligned**

```go
source, err := copyFirstValidFallbackStatus(stageStatusPath, worktreeStatusPaths)
if err != nil {
    return runtime.Outcome{Status: runtime.StatusFail, FailureReason: err.Error()}, nil
}
```

**Step 4: Run targeted tests and verify pass**

Run: `go test ./internal/attractor/engine -run 'StageStatusContract|AbsolutePaths_FromRelativeWorktreeInput|StatusContractPath_HandlesNestedCD|StatusContract_ClearsStaleWorktreeStatusBeforeStage|StatusContractPromptPreambleWritten' -count=1`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/stage_status_contract.go internal/attractor/engine/handlers.go internal/attractor/engine/stage_status_contract_test.go internal/attractor/engine/status_json_worktree_test.go
git commit -m "engine: introduce explicit stage status contract and inject absolute-path status preamble into codergen prompts"
```

### Task 3: Propagate Status Contract Through CLI/API Execution Environments

**Files:**
- Modify: `internal/attractor/engine/codergen_router.go`
- Modify: `internal/agent/env_local.go`
- Modify: `internal/agent/env_local_test.go`
- Modify: `internal/attractor/engine/run_with_config_integration_test.go`

**Step 1: Add failing tests for env propagation in CLI invocations**

```go
func TestRunWithConfig_CLIBackend_StatusContractEnvInjected(t *testing.T) {
    // Fake CLI script asserts KILROY_STAGE_STATUS_PATH is set and absolute.
    // It writes status using that exact path.
    // Test also reads logs_root/a/cli_invocation.json and asserts status_path metadata is present.
}
```

**Step 2: Run tests to confirm failure before env wiring**

Run: `go test ./internal/attractor/engine -run 'StatusContractEnvInjected' -count=1`
Expected: FAIL with missing `KILROY_STAGE_STATUS_PATH` and/or missing invocation metadata.

**Step 3: Inject status contract env vars in CLI path (`runCLI`)**

```go
contract := buildStageStatusContract(execCtx.WorktreeDir)

if codexSemantics {
    cmd.Env = mergeEnvWithOverrides(isolatedEnv, contract.EnvVars)
} else {
    baseEnv := scrubConflictingProviderEnvKeys(os.Environ(), providerKey)
    cmd.Env = mergeEnvWithOverrides(baseEnv, contract.EnvVars)
}

inv["status_path"] = contract.PrimaryPath
inv["status_fallback_path"] = contract.FallbackPath
inv["status_env_key"] = stageStatusPathEnvKey
```

**Step 4: Add base env support for API agent loop tools**

```go
type LocalExecutionEnvironment struct {
    RootDir string
    BaseEnv map[string]string
}

func NewLocalExecutionEnvironmentWithBaseEnv(rootDir string, baseEnv map[string]string) *LocalExecutionEnvironment {
    cp := map[string]string{}
    for k, v := range baseEnv {
        cp[k] = v
    }
    return &LocalExecutionEnvironment{RootDir: rootDir, BaseEnv: cp}
}

func NewLocalExecutionEnvironment(rootDir string) *LocalExecutionEnvironment {
    return NewLocalExecutionEnvironmentWithBaseEnv(rootDir, nil)
}

func (e *LocalExecutionEnvironment) ExecCommand(ctx context.Context, command string, timeoutMS int, workingDir string, envVars map[string]string) (ExecResult, error) {
    merged := map[string]string{}
    for k, v := range e.BaseEnv {
        merged[k] = v
    }
    for k, v := range envVars {
        merged[k] = v
    }
    cmd.Env = filteredEnv(merged)
    // existing behavior unchanged otherwise
}
```

And in `runAPI`:

```go
contract := buildStageStatusContract(execCtx.WorktreeDir)
env := agent.NewLocalExecutionEnvironmentWithBaseEnv(execCtx.WorktreeDir, contract.EnvVars)
```

**Step 5: Re-run env tests**

Run: `go test ./internal/agent ./internal/attractor/engine -run 'StatusContractEnvInjected|LocalExecutionEnvironment' -count=1`
Expected: PASS.

**Step 6: Commit**

```bash
git add internal/attractor/engine/codergen_router.go internal/agent/env_local.go internal/agent/env_local_test.go internal/attractor/engine/run_with_config_integration_test.go
git commit -m "engine/agent: propagate stage status contract env vars through cli and api execution environments"
```

### Task 4: Add Validator Lint for Unsafe `loop_restart` Failure Edges

**Files:**
- Modify: `internal/attractor/validate/validate.go`
- Modify: `internal/attractor/validate/validate_test.go`

**Step 1: Add failing validator tests**

```go
func TestValidate_LoopRestartFailureEdgeRequiresTransientInfraGuard(t *testing.T) {
    g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="x"]
  check [shape=diamond]
  start -> a -> check
  check -> a [condition="outcome=fail", loop_restart=true]
  check -> exit [condition="outcome=success"]
}
`))
    if err != nil {
        t.Fatalf("parse: %v", err)
    }
    diags := Validate(g)
    assertHasRule(t, diags, "loop_restart_failure_class_guard", SeverityWarning)
}

func TestValidate_LoopRestartFailureEdgeWithTransientInfraGuard_NoWarning(t *testing.T) {
    // Same graph but condition="outcome=fail && context.failure_class=transient_infra"
    // Assert no loop_restart_failure_class_guard warning.
}
```

**Step 2: Run tests and confirm failure before lint implementation**

Run: `go test ./internal/attractor/validate -run 'LoopRestartFailureEdge' -count=1`
Expected: FAIL because new rule does not exist yet.

**Step 3: Implement lint rule and register it in `Validate`**

```go
func Validate(g *model.Graph) []Diagnostic {
    var diags []Diagnostic
    // existing lints...
    diags = append(diags, lintLLMProviderPresent(g)...)
    diags = append(diags, lintLoopRestartFailureClassGuard(g)...)
    return diags
}

func lintLoopRestartFailureClassGuard(g *model.Graph) []Diagnostic {
    var diags []Diagnostic
    for _, e := range g.Edges {
        if e == nil || !strings.EqualFold(strings.TrimSpace(e.Attr("loop_restart", "false")), "true") {
            continue
        }
        condExpr := strings.TrimSpace(e.Condition())
        if !conditionMentionsFailureOutcome(condExpr) {
            continue
        }
        if conditionHasTransientInfraGuard(condExpr) {
            continue
        }
        diags = append(diags, Diagnostic{
            Rule:     "loop_restart_failure_class_guard",
            Severity: SeverityWarning,
            Message:  "loop_restart on failure edge should be guarded by context.failure_class=transient_infra and paired with a non-restart deterministic fail edge",
            EdgeFrom: e.From,
            EdgeTo:   e.To,
            Fix:      "Split edge into transient-infra restart + non-transient retry edges",
        })
    }
    return diags
}
```

**Step 4: Re-run validator tests**

Run: `go test ./internal/attractor/validate -run 'LoopRestartFailureEdge' -count=1`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/validate/validate.go internal/attractor/validate/validate_test.go
git commit -m "validate: warn when loop_restart failure edges are not guarded by transient_infra failure_class"
```

### Task 5: Patch `rogue_fast.dot` Retry Edges to Match Runtime `loop_restart` Policy

**Files:**
- Modify: `demo/rogue/rogue_fast.dot`

**Step 1: Replace each unsafe failure restart edge with explicit transient/non-transient split**

```dot
check_scaffold -> impl_scaffold [condition="outcome=fail && context.failure_class=transient_infra", label="retry-infra", loop_restart=true]
check_scaffold -> impl_scaffold [condition="outcome=fail && context.failure_class!=transient_infra", label="retry"]
```

Apply the same pattern to existing `loop_restart=true` failure edges:
- `check_analysis -> impl_analysis`
- `check_architecture -> impl_architecture`
- `check_scaffold -> impl_scaffold`
- `check_integration -> impl_integration`
- `check_qa -> impl_qa`
- `check_review -> impl_integration`

**Step 2: Validate both rogue graphs**

Run: `./kilroy attractor validate --graph demo/rogue/rogue_fast.dot`
Expected: no `loop_restart_failure_class_guard` warning.

Run: `./kilroy attractor validate --graph demo/rogue/rogue_port.dot`
Expected: clean/unchanged validation output.

**Step 3: Commit**

```bash
git add demo/rogue/rogue_fast.dot
git commit -m "demo(rogue_fast): gate loop_restart fail edges on transient_infra and keep deterministic retries in-run"
```

### Task 6: Update `english-to-dotfile` Skill to Generate Correct Status and Restart Patterns

**Files:**
- Modify: `skills/english-to-dotfile/SKILL.md`

**Step 1: Add a mandatory status-contract rule for prompt templates**

```md
#### Mandatory status-file contract

Every codergen prompt MUST explicitly instruct the agent to write status JSON to `$KILROY_STAGE_STATUS_PATH` (absolute path).

Required wording (or equivalent):
- "Write status JSON to `$KILROY_STAGE_STATUS_PATH` (absolute path)."
- "If that path is unavailable, use `$KILROY_STAGE_STATUS_FALLBACK_PATH`."
- "Do not write status.json inside nested module directories after `cd`."
```

**Step 2: Update prompt templates to include the contract line**

```md
Write status.json to `$KILROY_STAGE_STATUS_PATH`: outcome=success if all criteria pass, outcome=fail with failure_reason otherwise.
```

Apply this to implementation, verification, steering, and review templates/examples.

**Step 3: Update loop-restart guidance and anti-patterns**

```md
- For failure-driven restart edges, use `loop_restart=true` ONLY with `context.failure_class=transient_infra`.
- Pair with a non-restart deterministic fail edge.

Example:
check_X -> impl_X [condition="outcome=fail && context.failure_class=transient_infra", loop_restart=true]
check_X -> impl_X [condition="outcome=fail && context.failure_class!=transient_infra"]
```

Update Anti-Pattern #23 accordingly so it no longer recommends unguarded restart on generic fail edges.

**Step 4: Verify skill text updates**

Run: `rg -n "KILROY_STAGE_STATUS_PATH|context.failure_class=transient_infra|loop_restart" skills/english-to-dotfile/SKILL.md`
Expected: new status-contract section and guarded loop-restart guidance present.

**Step 5: Commit**

```bash
git add skills/english-to-dotfile/SKILL.md
git commit -m "skill(english-to-dotfile): require absolute status-path contract and transient_infra-guarded loop_restart patterns"
```

### Task 7: Run Full Verification Matrix and Build

**Files:**
- None (verification only)

**Step 1: Run focused regression suites**

Run: `go test ./internal/agent ./internal/attractor/engine ./internal/attractor/validate -run 'StatusContract|LoopRestart|StatusIngestion|WorktreeStatusJSON' -count=1`
Expected: PASS.

**Step 2: Run broader attractor tests**

Run: `go test ./internal/attractor/...`
Expected: PASS.

**Step 3: Build kilroy from current HEAD**

Run: `go build -o ./kilroy ./cmd/kilroy`
Expected: PASS, binary updated.

**Step 4: Validate rogue graphs one more time**

Run: `./kilroy attractor validate --graph demo/rogue/rogue_fast.dot && ./kilroy attractor validate --graph demo/rogue/rogue_port.dot`
Expected: PASS with no loop-restart policy warnings.

**Step 5: Commit verification artifacts only if needed**

```bash
git status --short
# If no generated files changed, no commit for this task.
```

### Task 8: Post-Implementation Run Readiness Notes (No Unauthorized Production Runs)

**Files:**
- Modify (if needed after validation): `demo/rogue/run-fast.yaml`
- Modify (if needed after validation): `demo/rogue/run.yaml`

**Step 1: Confirm CXDB launcher remains explicit in both run configs**

Run: `rg -n "start-cxdb\.sh|cxdb:|autostart" demo/rogue/run-fast.yaml demo/rogue/run.yaml`
Expected: both configs reference `/home/user/code/kilroy/scripts/start-cxdb.sh` with autostart enabled.

**Step 2: Confirm no additional YAML changes are required**

Run: `git diff -- demo/rogue/run-fast.yaml demo/rogue/run.yaml`
Expected: empty diff unless a concrete run blocker is discovered.

**Step 3: Document exact commands for operator-approved real runs**

```bash
./kilroy attractor run --detach --graph demo/rogue/rogue_fast.dot --config demo/rogue/run-fast.yaml --run-id <run_id> --logs-root <logs_root>
./kilroy attractor run --detach --graph demo/rogue/rogue_port.dot --config demo/rogue/run.yaml --run-id <run_id> --logs-root <logs_root>
```

Expected: Commands are recorded but executed only after explicit user approval (production authorization rule).

