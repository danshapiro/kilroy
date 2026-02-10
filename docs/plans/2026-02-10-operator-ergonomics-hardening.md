# Operator Ergonomics and Reliability Guardrails Implementation Plan

**Goal:** Make Kilroy choose safer, more operable behavior by default by adding code guardrails, versioned run-config knobs, explicit operator controls, and aligned docs/samples (excluding `AGENTS.md` changes).

**Architecture:** Put operability in code first (CLI + engine). Add first-class run controls (`status`, `stop`) and bounded runtime policy controls (`stage timeout`, `stall watchdog`, `LLM retry cap`) wired from run config. Keep preflight behavior config-driven and runtime-shape-aware, then update skills/docs/samples to match.

**Tech Stack:** Go (`cmd/kilroy`, `internal/attractor/engine`), YAML/JSON run-config schema, run artifacts (`progress.ndjson`, `live.json`, `final.json`, `run.pid`), Go tests, Markdown docs, Kilroy skills.

---

## Scope and Non-Goals

In scope (approved):
- `#1` Code-level guardrails and defaults.
- `#2` Versioned run-config policy knobs.
- `#3` Skill workflow updates.
- `#5` Samples + docs/runbook updates.

Out of scope:
- `#4` `AGENTS.md` additions/rewrites.

---

## Fresh-Eyes Resolution Checklist

This revision explicitly addresses each fresh-eyes finding:
- Split `runstate` type definitions from loader implementation (`types.go` + `snapshot.go`).
- Define strict artifact precedence: terminal `final.json` state wins and live/progress is not allowed to override terminal fields.
- Replace Linux-only `/proc/<pid>` checks with PID aliveness checks using `syscall.Kill(pid, 0)` (with `EPERM` treated as alive) to avoid `/proc` dependency.
- Clarify `run.pid` parsing to trim whitespace so both newline-free and newline-terminated PID files are handled.
- Remove `fmt.Sprint(nil)` behavior from event extraction (`nil` maps to empty string).
- Fix `attractor status` test design: running state requires a live PID, not only `live.json`.
- Avoid duplicate PID parsing in `attractor stop` by using `runstate.LoadSnapshot`.
- Make stop polling interval adaptive for short grace windows.
- Resolve zero-value ambiguity for runtime-policy knobs by using pointer fields in config.
- Preserve explicit-vs-unset retry semantics through `RunOptions` by representing `MaxLLMRetries` as a pointer and defaulting it in `applyDefaults`.
- Make runtime defaults explicit and consistent across config parsing, engine behavior, and docs.
- Add `RunOptions` runtime fields in the same task where config mapping is introduced.
- Define global-stage-timeout vs node-timeout semantics explicitly (effective timeout is the minimum positive timeout).
- Confirm `shape=parallelogram` + `tool_command` is already a supported execution path and keep timeout tests on that supported path.
- Define `TestRun_StallWatchdog` explicitly in Task 5 Step 1 (not only in verification commands).
- Specify synchronization for `lastProgressAt` between `appendProgress` and watchdog reads (single mutex or atomic path used consistently).
- Clarify retry-cap override placement inside `attractorLLMRetryPolicy` (after default assignment, before return).
- Include missing preflight helper definitions (`configuredAPIPromptProbeTransports`, policy-from-config resolver) directly in implementation steps.
- Remove undefined `boolPtr` dependency from tests.
- Make shell check scripts portable (`rg` when present, otherwise `grep`).
- Remove ambiguous “if exists” doc edit instructions by explicitly checking file existence in the plan before edits.
- Ensure sample config keys are first-class schema fields (not unknown-field side effects) before documenting them in demos.

---

### Task 1: Add Run Snapshot Reader for Status/Stop

**Files:**
- Create: `internal/attractor/runstate/types.go`
- Create: `internal/attractor/runstate/snapshot.go`
- Create: `internal/attractor/runstate/snapshot_test.go`

**Step 1: Write failing tests**

```go
func TestLoadSnapshot_FinalStateWinsAndIgnoresLiveForStateAndNode(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "final.json"), []byte(`{"status":"success","run_id":"r1"}`), 0o644)
	_ = os.WriteFile(filepath.Join(root, "live.json"), []byte(`{"event":"llm_retry","node_id":"impl"}`), 0o644)

	s, err := LoadSnapshot(root)
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	if s.State != StateSuccess {
		t.Fatalf("state=%q want %q", s.State, StateSuccess)
	}
	if s.RunID != "r1" {
		t.Fatalf("run_id=%q want r1", s.RunID)
	}
	if s.CurrentNodeID != "" {
		t.Fatalf("current_node_id=%q want empty when final.json is present", s.CurrentNodeID)
	}
}

func TestLoadSnapshot_InfersRunningFromAlivePID(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "run.pid"), []byte(strconv.Itoa(os.Getpid())), 0o644)

	s, err := LoadSnapshot(root)
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	if !s.PIDAlive {
		t.Fatal("expected pid to be alive")
	}
	if s.State != StateRunning {
		t.Fatalf("state=%q want %q", s.State, StateRunning)
	}
}

func TestLoadSnapshot_NilEventFieldsDoNotRenderAsNilString(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "live.json"), []byte(`{"event":null,"node_id":null}`), 0o644)

	s, err := LoadSnapshot(root)
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	if s.LastEvent != "" || s.CurrentNodeID != "" {
		t.Fatalf("expected empty strings, got event=%q node=%q", s.LastEvent, s.CurrentNodeID)
	}
}
```

**Step 2: Run tests to verify failure**

Run: `go test ./internal/attractor/runstate -v`
Expected: FAIL (`package runstate` does not exist yet).

**Step 3: Write minimal implementation**

`types.go` (only shared model types):

```go
type State string

const (
	StateUnknown State = "unknown"
	StateRunning State = "running"
	StateSuccess State = "success"
	StateFail    State = "fail"
)

type Snapshot struct {
	LogsRoot      string    `json:"logs_root"`
	RunID         string    `json:"run_id,omitempty"`
	State         State     `json:"state"`
	CurrentNodeID string    `json:"current_node_id,omitempty"`
	LastEvent     string    `json:"last_event,omitempty"`
	LastEventAt   time.Time `json:"last_event_at,omitempty"`
	FailureReason string    `json:"failure_reason,omitempty"`
	PID           int       `json:"pid,omitempty"`
	PIDAlive      bool      `json:"pid_alive"`
}
```

`snapshot.go` behavior:
- Load `final.json` first.
- If final status is terminal (`success`/`fail`), do not use `live.json`/`progress.ndjson` to set `LastEvent` or `CurrentNodeID`.
- Always decode `run.pid` if present for observability fields (`PID`, `PIDAlive`) but do not override terminal state.
- Parse PID with `strings.TrimSpace` so both production format (no newline) and newline-terminated files are accepted.
- Infer `running` only when state is still unknown and PID is alive.
- Use `pidAlive(pid)` helper with `syscall.Kill(pid, 0)` + `EPERM` handling.
- In event decoding, treat `nil` as empty string.

**Step 4: Run tests to verify pass**

Run: `go test ./internal/attractor/runstate -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/runstate/types.go internal/attractor/runstate/snapshot.go internal/attractor/runstate/snapshot_test.go
git commit -m "feat(runstate): add artifact-backed run snapshot model with terminal precedence and pid liveness"
```

---

### Task 2: Add `attractor status` CLI Command

**Files:**
- Modify: `cmd/kilroy/main.go`
- Create: `cmd/kilroy/attractor_status.go`
- Create: `cmd/kilroy/main_status_test.go`

**Step 1: Write failing tests**

```go
func TestAttractorStatus_PrintsRunningState_WhenPIDAlive(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("requires sleep binary")
	}
	bin := buildKilroyBinary(t)
	logs := t.TempDir()

	proc := exec.Command("sleep", "60")
	if err := proc.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() { _ = proc.Process.Kill() })

	_ = os.WriteFile(filepath.Join(logs, "run.pid"), []byte(strconv.Itoa(proc.Process.Pid)), 0o644)
	_ = os.WriteFile(filepath.Join(logs, "live.json"), []byte(`{"event":"stage_attempt_start","node_id":"impl"}`), 0o644)

	out, err := exec.Command(bin, "attractor", "status", "--logs-root", logs).CombinedOutput()
	if err != nil {
		t.Fatalf("status failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "state=running") {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestAttractorStatus_PrintsUnknownWithoutFinalOrLivePID(t *testing.T) {
	bin := buildKilroyBinary(t)
	logs := t.TempDir()
	_ = os.WriteFile(filepath.Join(logs, "live.json"), []byte(`{"event":"stage_attempt_start","node_id":"impl"}`), 0o644)

	out, err := exec.Command(bin, "attractor", "status", "--logs-root", logs).CombinedOutput()
	if err != nil {
		t.Fatalf("status failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "state=unknown") {
		t.Fatalf("unexpected output: %s", out)
	}
}
```

**Step 2: Run tests to verify failure**

Run: `go test ./cmd/kilroy -run 'TestAttractorStatus_' -v`
Expected: FAIL (`unknown arg: status`).

**Step 3: Write minimal implementation**

Implementation notes:
- Add a testable helper:
  - `func runAttractorStatus(args []string, stdout io.Writer, stderr io.Writer) int`
  - `attractorStatus(args []string)` becomes a thin wrapper calling helper + `os.Exit(code)`.
- Parse flags: `--logs-root`, `--json`.
- Use `runstate.LoadSnapshot(logsRoot)`.
- Text output includes state, run_id, node, event, pid, pid_alive.
- JSON output uses `json.Encoder` with indentation.
- Wire dispatch + usage updates in `cmd/kilroy/main.go`.

**Step 4: Run tests to verify pass**

Run: `go test ./cmd/kilroy -run 'TestAttractorStatus_' -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add cmd/kilroy/main.go cmd/kilroy/attractor_status.go cmd/kilroy/main_status_test.go
git commit -m "feat(cli): add attractor status command backed by runstate snapshot"
```

---

### Task 3: Add `attractor stop` CLI Command

**Files:**
- Modify: `cmd/kilroy/main.go`
- Create: `cmd/kilroy/attractor_stop.go`
- Create: `cmd/kilroy/main_stop_test.go`

**Step 1: Write failing tests**

```go
func TestAttractorStop_KillsProcessFromRunPID(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("requires sleep binary")
	}
	bin := buildKilroyBinary(t)
	logs := t.TempDir()

	proc := exec.Command("sleep", "60")
	if err := proc.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	pid := proc.Process.Pid
	_ = os.WriteFile(filepath.Join(logs, "run.pid"), []byte(strconv.Itoa(pid)), 0o644)

	out, err := exec.Command(bin, "attractor", "stop", "--logs-root", logs, "--grace-ms", "100", "--force").CombinedOutput()
	if err != nil {
		t.Fatalf("stop failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "stopped=") {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestAttractorStop_ErrorsWhenNoPID(t *testing.T) {
	bin := buildKilroyBinary(t)
	logs := t.TempDir()
	out, err := exec.Command(bin, "attractor", "stop", "--logs-root", logs).CombinedOutput()
	if err == nil {
		t.Fatalf("expected non-zero exit; output=%s", out)
	}
}
```

**Step 2: Run tests to verify failure**

Run: `go test ./cmd/kilroy -run 'TestAttractorStop_' -v`
Expected: FAIL (`unknown arg: stop`).

**Step 3: Write minimal implementation**

Implementation notes:
- Add testable helper:
  - `func runAttractorStop(args []string, stdout io.Writer, stderr io.Writer) int`
  - `attractorStop(args []string)` wraps helper + `os.Exit(code)`.
- Parse flags: `--logs-root`, `--grace-ms`, `--force`.
- Load PID via `runstate.LoadSnapshot(logsRoot)` instead of re-parsing `run.pid` directly.
- If PID missing or not alive, return clear non-zero error.
- Send `SIGTERM`, poll for exit until grace deadline.
- Poll interval is adaptive for short grace windows:
  - `poll := min(100ms, max(10ms, grace/5))`
- If `--force` and still alive, send `SIGKILL` and report `stopped=forced`.
- Wire usage + dispatch in `cmd/kilroy/main.go`.

**Step 4: Run tests to verify pass**

Run: `go test ./cmd/kilroy -run 'TestAttractorStop_' -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add cmd/kilroy/main.go cmd/kilroy/attractor_stop.go cmd/kilroy/main_stop_test.go
git commit -m "feat(cli): add attractor stop command using runstate snapshot and adaptive grace polling"
```

---

### Task 4: Add Versioned Runtime Policy Knobs in Run Config

**Files:**
- Modify: `internal/attractor/engine/config.go`
- Modify: `internal/attractor/engine/engine.go`
- Modify: `internal/attractor/engine/run_with_config.go`
- Modify: `internal/attractor/engine/run_options_test.go`
- Create: `internal/attractor/engine/config_runtime_policy_test.go`

**Step 1: Write failing tests**

```go
func TestRuntimePolicy_DefaultsAndValidation(t *testing.T) {
	cfg := &RunConfigFile{}
	applyConfigDefaults(cfg)

	if cfg.RuntimePolicy.StallTimeoutMS == nil || *cfg.RuntimePolicy.StallTimeoutMS != 600000 {
		t.Fatalf("expected default stall_timeout_ms=600000")
	}
	if cfg.RuntimePolicy.StallCheckIntervalMS == nil || *cfg.RuntimePolicy.StallCheckIntervalMS != 5000 {
		t.Fatalf("expected default stall_check_interval_ms=5000")
	}
	if cfg.RuntimePolicy.MaxLLMRetries == nil || *cfg.RuntimePolicy.MaxLLMRetries != 6 {
		t.Fatalf("expected default max_llm_retries=6")
	}

	zero := 0
	cfg.RuntimePolicy.MaxLLMRetries = &zero
	if err := validateConfig(cfg); err != nil {
		t.Fatalf("max_llm_retries=0 should be valid: %v", err)
	}

	neg := -1
	cfg.RuntimePolicy.MaxLLMRetries = &neg
	if err := validateConfig(cfg); err == nil {
		t.Fatal("expected validation error for negative max_llm_retries")
	}
}
```

**Step 2: Run test to verify failure**

Run: `go test ./internal/attractor/engine -run TestRuntimePolicy_DefaultsAndValidation -v`
Expected: FAIL (`RuntimePolicy` missing).

**Step 3: Implement config model + options mapping**

Use pointer fields to preserve explicit zero values:

```go
type RuntimePolicyConfig struct {
	StageTimeoutMS       *int `json:"stage_timeout_ms,omitempty" yaml:"stage_timeout_ms,omitempty"`
	StallTimeoutMS       *int `json:"stall_timeout_ms,omitempty" yaml:"stall_timeout_ms,omitempty"`
	StallCheckIntervalMS *int `json:"stall_check_interval_ms,omitempty" yaml:"stall_check_interval_ms,omitempty"`
	MaxLLMRetries        *int `json:"max_llm_retries,omitempty" yaml:"max_llm_retries,omitempty"`
}
```

Defaults in `applyConfigDefaults`:
- `stage_timeout_ms`: default `0` (disabled)
- `stall_timeout_ms`: default `600000`
- `stall_check_interval_ms`: default `5000`
- `max_llm_retries`: default `6`

Validation in `validateConfig`:
- All runtime policy values must be `>= 0`.
- If `stall_timeout_ms > 0`, then `stall_check_interval_ms` must be `> 0`.

Add these concrete fields to `RunOptions` in this task (not deferred):
- `StageTimeout time.Duration`
- `StallTimeout time.Duration`
- `StallCheckInterval time.Duration`
- `MaxLLMRetries *int`

Map config into `RunOptions` in `run_with_config.go` with explicit pointer handling so `0` remains a valid explicit value.
Update `run_options_test.go` to assert `applyDefaults` sets `MaxLLMRetries` when unset and preserves an explicit pointer to `0`.

**Step 4: Run test to verify pass**

Run: `go test ./internal/attractor/engine -run 'TestRuntimePolicy_DefaultsAndValidation|TestRunOptionsApplyDefaults' -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/config.go internal/attractor/engine/engine.go internal/attractor/engine/run_with_config.go internal/attractor/engine/run_options_test.go internal/attractor/engine/config_runtime_policy_test.go
git commit -m "feat(config): add pointer-based runtime_policy knobs with explicit defaults and RunOptions mapping"
```

---

### Task 5: Enforce Engine Guardrails (Timeouts, Stall Watchdog, Retry Cap)

**Files:**
- Modify: `internal/attractor/engine/engine.go`
- Modify: `internal/attractor/engine/progress.go`
- Modify: `internal/attractor/engine/codergen_router.go`
- Create: `internal/attractor/engine/engine_stage_timeout_test.go`
- Create: `internal/attractor/engine/engine_stall_watchdog_test.go`

**Step 1: Write failing tests**

```go
// Intentionally uses shape=parallelogram/tool_command because this is the
// existing supported ToolHandler path in the current engine.
func TestRun_GlobalStageTimeoutCapsToolNode(t *testing.T) {
	dot := []byte(`digraph G {
  start [shape=Mdiamond]
  wait [shape=parallelogram, tool_command="sleep 2"]
  exit [shape=Msquare]
  start -> wait -> exit
}`)
	repo := initTestRepo(t)
	opts := RunOptions{RepoPath: repo, StageTimeout: 100 * time.Millisecond}
	_, err := Run(context.Background(), dot, opts)
	if err == nil {
		t.Fatal("expected stage timeout error")
	}
}

func TestRun_GlobalAndNodeTimeout_UsesSmallerTimeout(t *testing.T) {
	dot := []byte(`digraph G {
  start [shape=Mdiamond]
  wait [shape=parallelogram, timeout="1s", tool_command="sleep 2"]
  exit [shape=Msquare]
  start -> wait -> exit
}`)
	repo := initTestRepo(t)
	opts := RunOptions{RepoPath: repo, StageTimeout: 5 * time.Second}
	_, err := Run(context.Background(), dot, opts)
	if err == nil {
		t.Fatal("expected timeout from node/global min timeout")
	}
}

func TestRun_StallWatchdog(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("requires sleep binary")
	}
	dot := []byte(`digraph G {
  start [shape=Mdiamond]
  wait [shape=parallelogram, tool_command="sleep 2"]
  exit [shape=Msquare]
  start -> wait -> exit
}`)
	repo := initTestRepo(t)
	opts := RunOptions{
		RepoPath:            repo,
		StallTimeout:        150 * time.Millisecond,
		StallCheckInterval:  25 * time.Millisecond,
	}
	_, err := Run(context.Background(), dot, opts)
	if err == nil {
		t.Fatal("expected stall watchdog timeout")
	}
}
```

**Step 2: Run tests to verify failure**

Run: `go test ./internal/attractor/engine -run 'TestRun_GlobalStageTimeoutCapsToolNode|TestRun_GlobalAndNodeTimeout_UsesSmallerTimeout|TestRun_StallWatchdog' -v`
Expected: FAIL (global stage timeout and stall watchdog not wired yet).

**Step 3: Implement guardrails**

Implementation details:
- `StageTimeout` semantics:
  - Existing node `timeout` attribute already exists.
  - Effective per-attempt timeout is `minPositive(nodeTimeout, options.StageTimeout)`.
  - Apply this once in `executeNode` (not as a second independent wrapper in `executeWithRetry`) to avoid ambiguous nested timeout behavior.
- Stall watchdog:
  - Track `lastProgressAt` timestamp in engine state.
  - Update timestamp on every `appendProgress` call.
  - Synchronize reads/writes for `lastProgressAt` using a single strategy (recommended: guard with `progressMu`, the same lock already used by `appendProgress`).
  - Add a helper such as `lastProgressTime()` that reads under the same synchronization path used for writes.
  - In `run()`, derive a cancelable context (`context.WithCancelCause`) and start watchdog when `StallTimeout > 0`.
  - Watchdog checks at `StallCheckInterval`; if no progress for `StallTimeout`, append `stall_watchdog_timeout` progress event and cancel run with cause.
- LLM retry cap:
  - `attractorLLMRetryPolicy` uses `RunOptions.MaxLLMRetries` (including explicit `0`), with default resolved in Task 4.
  - In non-config call paths, `applyDefaults` must populate `RunOptions.MaxLLMRetries` when nil before policy wiring reads it.
  - Keep the existing default policy assignment first, then override `p.MaxRetries` from `RunOptions.MaxLLMRetries` immediately before returning `p`.

Example retry-cap wiring:

```go
// Keep current default assignment first.
p.MaxRetries = 6

// Override from RunOptions when explicitly configured.
if execCtx != nil && execCtx.Engine != nil && execCtx.Engine.Options.MaxLLMRetries != nil {
	p.MaxRetries = *execCtx.Engine.Options.MaxLLMRetries
}
```

**Step 4: Run focused tests**

Run: `go test ./internal/attractor/engine -run 'TestRun_GlobalStageTimeoutCapsToolNode|TestRun_GlobalAndNodeTimeout_UsesSmallerTimeout|TestRun_StallWatchdog' -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/engine.go internal/attractor/engine/progress.go internal/attractor/engine/codergen_router.go internal/attractor/engine/engine_stage_timeout_test.go internal/attractor/engine/engine_stall_watchdog_test.go
git commit -m "feat(engine): enforce global timeout cap, stall watchdog, and config-driven llm retry cap"
```

---

### Task 6: Make Preflight Prompt-Probe Policy Config-Driven and Runtime-Shape Aware

**Files:**
- Modify: `internal/attractor/engine/config.go`
- Modify: `internal/attractor/engine/provider_preflight.go`
- Modify: `internal/attractor/engine/provider_preflight_test.go`
- Create: `internal/attractor/engine/provider_preflight_policy_from_config_test.go`

**Step 1: Write failing tests**

```go
func TestConfiguredAPIPromptProbeTransports_FromConfig(t *testing.T) {
	cfg := &RunConfigFile{}
	applyConfigDefaults(cfg)
	enabled := true
	cfg.Preflight.PromptProbes.Enabled = &enabled
	cfg.Preflight.PromptProbes.Transports = []string{"complete", "stream"}

	got := configuredAPIPromptProbeTransports(cfg, nil)
	if len(got) != 2 || got[0] != "complete" || got[1] != "stream" {
		t.Fatalf("unexpected transports: %v", got)
	}
}

func TestPromptProbeMode_ConfigOverridesEnv(t *testing.T) {
	t.Setenv("KILROY_PREFLIGHT_PROMPT_PROBES", "off")
	cfg := &RunConfigFile{}
	applyConfigDefaults(cfg)
	on := true
	cfg.Preflight.PromptProbes.Enabled = &on
	if got := promptProbeMode(cfg); got != "on" {
		t.Fatalf("mode=%q want on", got)
	}
}
```

**Step 2: Run tests to verify failure**

Run: `go test ./internal/attractor/engine -run 'TestConfiguredAPIPromptProbeTransports_FromConfig|TestPromptProbeMode_ConfigOverridesEnv' -v`
Expected: FAIL (`Preflight` config fields / helper functions missing).

**Step 3: Implement config policy model**

Add config model in `RunConfigFile`:

```go
Preflight struct {
	PromptProbes struct {
		Enabled     *bool    `json:"enabled,omitempty" yaml:"enabled,omitempty"`
		Transports  []string `json:"transports,omitempty" yaml:"transports,omitempty"`
		TimeoutMS   *int     `json:"timeout_ms,omitempty" yaml:"timeout_ms,omitempty"`
		Retries     *int     `json:"retries,omitempty" yaml:"retries,omitempty"`
		BaseDelayMS *int     `json:"base_delay_ms,omitempty" yaml:"base_delay_ms,omitempty"`
		MaxDelayMS  *int     `json:"max_delay_ms,omitempty" yaml:"max_delay_ms,omitempty"`
	} `json:"prompt_probes,omitempty" yaml:"prompt_probes,omitempty"`
} `json:"preflight,omitempty" yaml:"preflight,omitempty"`
```

Define missing helpers explicitly in `provider_preflight.go`:
- `configuredAPIPromptProbeTransports(cfg *RunConfigFile, g *model.Graph) []string`
  - If config transports set, normalize and return them.
  - Else default to `[]string{"complete", "stream"}` for runtime-shape coverage.
- `preflightAPIPromptProbePolicyFromConfig(cfg *RunConfigFile) preflightAPIPromptProbePolicy`
  - Resolve timeout/retries/backoff from config first.
  - Keep env vars as fallback only when config values are unset.

Update `promptProbeMode(cfg)` precedence:
1. `cfg.preflight.prompt_probes.enabled` (if non-nil)
2. env `KILROY_PREFLIGHT_PROMPT_PROBES`
3. default (`off` for `llm.cli_profile=test_shim`, otherwise `on`)

**Step 4: Run tests to verify pass**

Run: `go test ./internal/attractor/engine -run 'TestConfiguredAPIPromptProbeTransports_FromConfig|TestPromptProbeMode_ConfigOverridesEnv|TestRunWithConfig_PreflightPromptProbe_' -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/config.go internal/attractor/engine/provider_preflight.go internal/attractor/engine/provider_preflight_test.go internal/attractor/engine/provider_preflight_policy_from_config_test.go
git commit -m "feat(preflight): add config-first prompt probe policy and transport selection helpers"
```

---

### Task 7: Update `skills/using-kilroy` Workflow

**Files:**
- Modify: `skills/using-kilroy/SKILL.md`
- Create: `scripts/check-using-kilroy-skill.sh`

**Step 1: Write failing content-check script (portable)**

```bash
#!/usr/bin/env bash
set -euo pipefail
SKILL="skills/using-kilroy/SKILL.md"
if command -v rg >/dev/null 2>&1; then
  FIND=(rg -n)
else
  FIND=(grep -n)
fi
"${FIND[@]}" "attractor status --logs-root" "$SKILL"
"${FIND[@]}" "attractor stop --logs-root" "$SKILL"
"${FIND[@]}" "runtime_policy" "$SKILL"
"${FIND[@]}" "preflight.prompt_probes" "$SKILL"
```

**Step 2: Run check to verify failure**

Run: `bash scripts/check-using-kilroy-skill.sh`
Expected: FAIL until skill content is updated.

**Step 3: Update skill with launch/observe/intervene flow**

Add sections to `skills/using-kilroy/SKILL.md`:
- Launch long runs detached.
- Observe with `attractor status` and `preflight_report.json`.
- Intervene with `attractor stop`.
- Prefer `run.yaml` policy (`runtime_policy`, `preflight.prompt_probes`) over env tuning.

**Step 4: Re-run check**

Run: `bash scripts/check-using-kilroy-skill.sh`
Expected: PASS.

**Step 5: Commit**

```bash
git add skills/using-kilroy/SKILL.md scripts/check-using-kilroy-skill.sh
git commit -m "docs(skill): add status/stop and config-first reliability workflow"
```

---

### Task 8: Update README, Attractor Docs, and Sample Run Configs

**Files:**
- Modify: `README.md`
- Modify: `docs/strongdm/attractor/README.md`
- Modify: `docs/strongdm/attractor/reliability-troubleshooting.md`
- Modify: `demo/rogue/run.yaml`
- Conditionally modify if present: `demo/dttf/run.yaml`
- Create: `scripts/check-ergonomics-docs.sh`

**Step 1: Write failing doc-check script (portable + conditional file check)**

```bash
#!/usr/bin/env bash
set -euo pipefail
if command -v rg >/dev/null 2>&1; then
  FIND=(rg -n)
else
  FIND=(grep -n)
fi
"${FIND[@]}" "attractor status --logs-root" README.md docs/strongdm/attractor/README.md docs/strongdm/attractor/reliability-troubleshooting.md
"${FIND[@]}" "attractor stop --logs-root" README.md docs/strongdm/attractor/reliability-troubleshooting.md
"${FIND[@]}" "runtime_policy:" README.md demo/rogue/run.yaml
"${FIND[@]}" "preflight:" README.md demo/rogue/run.yaml
if [[ -f demo/dttf/run.yaml ]]; then
  "${FIND[@]}" "runtime_policy:" demo/dttf/run.yaml
  "${FIND[@]}" "preflight:" demo/dttf/run.yaml
fi
```

**Step 2: Run check to verify failure**

Run: `bash scripts/check-ergonomics-docs.sh`
Expected: FAIL until docs/samples are updated.

**Step 3: Update docs and sample config blocks**

Add CLI commands to command surfaces:

```text
kilroy attractor status --logs-root <dir> [--json]
kilroy attractor stop --logs-root <dir> [--grace-ms <ms>] [--force]
```

Use a consistent runtime policy sample (matches Task 4 defaults):

```yaml
runtime_policy:
  stage_timeout_ms: 0
  stall_timeout_ms: 600000
  stall_check_interval_ms: 5000
  max_llm_retries: 6

preflight:
  prompt_probes:
    enabled: true
    transports: [complete, stream]
    timeout_ms: 15000
    retries: 1
    base_delay_ms: 500
    max_delay_ms: 5000
```

Schema note:
- `runtime_policy` and `preflight` are explicit `RunConfigFile` fields added in Tasks 4 and 6, so these sample keys are validated as first-class config, not unknown-key pass-through.

If `demo/dttf/run.yaml` is absent, do not reference it in changed docs as an edited file.

**Step 4: Re-run check**

Run: `bash scripts/check-ergonomics-docs.sh`
Expected: PASS.

**Step 5: Commit**

```bash
git add README.md docs/strongdm/attractor/README.md docs/strongdm/attractor/reliability-troubleshooting.md demo/rogue/run.yaml scripts/check-ergonomics-docs.sh
git commit -m "docs(samples): publish status/stop commands and aligned runtime/preflight policy examples"
```

---

## End-to-End Verification

Run:

```bash
go test ./cmd/kilroy -v
go test ./internal/attractor/engine -v
go test ./internal/attractor/runstate -v
bash scripts/check-using-kilroy-skill.sh
bash scripts/check-ergonomics-docs.sh
```

Expected:
- All tests pass.
- New CLI commands appear in `usage()`.
- Runtime guardrails are controlled by run config.
- `max_llm_retries: 0` remains a valid explicit setting.
- Global stage timeout and node timeout have explicit min-timeout semantics.
- Preflight probe behavior is config-driven with env fallback.
- Skill/docs/samples match the new operational model.

## Rollout Notes

- Keep env-based preflight knobs as fallback for one release; document precedence (`run config` over `env`).
- Mention new commands and run-config keys in release notes.
- Validate detached-run ergonomics manually once with `run.pid`, `status`, and `stop` in a real long run.
