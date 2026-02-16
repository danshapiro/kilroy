# Rogue Tooling Recovery + CLI/API Parity Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Prevent Rogue-style tooling failures from burning long retry loops by making deterministic checks first-class tool nodes, enforcing checkpoint artifact hygiene, and unifying CLI/API execution environments.

**Architecture:** Fix this in two layers. Layer 1 is engine hardening: make API agent-loop execution honor the same node-env invariants and env-stripping contract used by CLI/tool handlers, and prevent artifact-path checkpoint commits that poison subsequent verification. Layer 2 is graph-authoring hardening: make `english-to-dotfile` generate deterministic tool gates with failure-class-aware routing, and require machine-stable failure payloads so retry and cycle-break behavior is predictable.

**Tech Stack:** Go (`internal/attractor/engine`, `internal/attractor/gitutil`, `internal/attractor/runtime`, `internal/attractor/validate`, `internal/agent`), DOT templates (`skills/english-to-dotfile/reference_template.dot`), Markdown skill docs (`skills/english-to-dotfile/SKILL.md`)

---

## Incident Contract (Locked)

Run IDs and timeline (UTC):
- `01KHHJFQSQJZ1GD6WJ57AMSZGB` started `2026-02-15T21:14:48Z`, ended `2026-02-15T21:15:02Z`, failure: `stopped by signal terminated`
- `01KHHJGH6X89SV2EMV9Q0CJ9SK` started `2026-02-15T21:15:11Z`, ended `2026-02-15T23:46:47Z`, failure: deterministic cycle breaker on `verify_impl|deterministic|environmental_tooling_blocks`

Observed mechanics to preserve in tests:
- Graph set `max_node_visits=100` but still aborted via deterministic signature breaker before visit-limit exhaustion.
- Verify-stage commands hit `Invalid cross-device link (os error 18)` and `wasm-pack Permission denied (os error 13)`.
- Artifact directories with underscore variants (for example `.cargo_target_local*`) were committed into run history and kept failing artifact hygiene checks.

Canonical alignment to enforce:
- Attractor spec: codergen and tool handlers are distinct handler classes and should be modeled distinctly in DOT (`docs/strongdm/attractor/attractor-spec.md:1506`, `docs/strongdm/attractor/attractor-spec.md:1508`).
- Attractor spec: retry policy is failure-class-aware, deterministic is not retryable (`docs/strongdm/attractor/attractor-spec.md:520`).
- Attractor spec: existing guardrail warns when failure loop restarts are not failure-class-guarded (`docs/strongdm/attractor/attractor-spec.md:1509`).
- Coding-agent-loop spec: execution environment abstraction and tool-error recovery contract (`docs/strongdm/attractor/coding-agent-loop-spec.md:1400`, `docs/strongdm/attractor/coding-agent-loop-spec.md:1452`).

---

### Task 1: Lock API Agent-Loop Env Contract With Failing Tests

**Files:**
- Create: `internal/attractor/engine/api_env_parity_test.go`
- Modify: `internal/agent/env_local_test.go`
- Modify: `internal/attractor/engine/node_env.go` (helper export/use only after red test)

**Step 1: Write failing tests for API env contract**

Create `internal/attractor/engine/api_env_parity_test.go`:

```go
package engine

import (
	"os"
	"testing"
)

func TestBuildAgentLoopOverrides_UsesBaseNodeEnvContract(t *testing.T) {
	origTarget, hadTarget := os.LookupEnv("CARGO_TARGET_DIR")
	_ = os.Unsetenv("CARGO_TARGET_DIR")
	t.Cleanup(func() {
		if hadTarget {
			_ = os.Setenv("CARGO_TARGET_DIR", origTarget)
			return
		}
		_ = os.Unsetenv("CARGO_TARGET_DIR")
	})
	worktree := t.TempDir()
	env := buildAgentLoopOverrides(worktree, map[string]string{"KILROY_STAGE_STATUS_PATH": "/tmp/status.json"})

	if env["CARGO_TARGET_DIR"] == "" {
		t.Fatal("CARGO_TARGET_DIR must be present for API agent_loop path")
	}
	if env["KILROY_STAGE_STATUS_PATH"] != "/tmp/status.json" {
		t.Fatal("stage status env must be preserved")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/attractor/engine -run TestBuildAgentLoopOverrides_UsesBaseNodeEnvContract -count=1`
Expected: FAIL with `undefined: buildAgentLoopOverrides`

**Step 3: Add agent env test that proves strip-keys are honored**

In `internal/agent/env_local_test.go`, add a red test:

```go
func TestLocalExecutionEnvironment_ExecCommand_StripsConfiguredEnvKeys(t *testing.T)
```

Assert `CLAUDECODE` is absent from `env` output inside executed command when strip list contains it.
Set `CLAUDECODE=1` in the parent process for the test so the assertion proves stripping from inherited `os.Environ()`, not only from override maps.

**Step 4: Commit red tests**

```bash
git add internal/attractor/engine/api_env_parity_test.go internal/agent/env_local_test.go
git commit -m "test(engine): lock failing API agent-loop env contract and strip-key behavior"
```

---

### Task 2: Implement API Agent-Loop Env Unification Without Full-Env Duplication

**Files:**
- Modify: `internal/attractor/engine/codergen_router.go`
- Modify: `internal/attractor/engine/node_env.go` (env override helper)
- Modify: `internal/agent/env_local.go`
- Test: `internal/attractor/engine/api_env_parity_test.go`
- Test: `internal/agent/env_local_test.go`

**Step 1: Implement helper that returns only required overrides**

Add helper near env utilities:

```go
func buildAgentLoopOverrides(worktreeDir string, contractEnv map[string]string) map[string]string {
	base := buildBaseNodeEnv(worktreeDir)
	keep := map[string]bool{
		"CARGO_HOME": true, "RUSTUP_HOME": true, "GOPATH": true,
		"GOMODCACHE": true, "CARGO_TARGET_DIR": true,
	}
	m := make(map[string]string, len(contractEnv)+len(keep))
	for _, kv := range base {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if keep[k] {
			m[k] = v
		}
	}
	for k, v := range contractEnv {
		m[k] = v
	}
	return m
}
```

This helper intentionally bridges engine env format (`[]string`) to agent overlay format (`map[string]string`).

**Step 2: Add explicit strip-key support to local execution environment**

In `internal/agent/env_local.go`:
- add `StripEnvKeys []string` to `LocalExecutionEnvironment`
- add constructor `NewLocalExecutionEnvironmentWithPolicy(rootDir string, baseEnv map[string]string, stripKeys []string)`
- keep `NewLocalExecutionEnvironmentWithBaseEnv` as wrapper to preserve callers
- update `filteredEnv(...)` signature to accept strip keys, and remove strip keys from both inherited `os.Environ()` entries and extra env maps.

Root-cause note: this is required because `filteredEnv(...)` currently re-hydrates `os.Environ()` regardless of engine-side env shaping.

**Step 3: Wire `runAPI(..., mode=agent_loop)` to helper + strip policy**

Replace:

```go
env := agent.NewLocalExecutionEnvironmentWithBaseEnv(execCtx.WorktreeDir, contract.EnvVars)
```

With:

```go
overrides := buildAgentLoopOverrides(execCtx.WorktreeDir, contract.EnvVars)
env := agent.NewLocalExecutionEnvironmentWithPolicy(execCtx.WorktreeDir, overrides, []string{"CLAUDECODE"})
```

**Step 4: Run targeted tests**

Run: `go test ./internal/attractor/engine ./internal/agent -run 'TestBuildAgentLoopOverrides_UsesBaseNodeEnvContract|TestBuildBaseNodeEnv|StripsConfiguredEnvKeys' -count=1`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/attractor/engine/codergen_router.go internal/attractor/engine/node_env.go internal/attractor/engine/api_env_parity_test.go internal/agent/env_local.go internal/agent/env_local_test.go
git commit -m "fix(engine): unify API agent-loop env invariants and enforce strip-key policy"
```

---

### Task 3: Add Checkpoint Artifact Exclusion Contract (Engine + Git Layer)

**Files:**
- Modify: `internal/attractor/engine/config.go`
- Modify: `internal/attractor/gitutil/git.go`
- Modify: `internal/attractor/engine/engine.go`
- Create: `internal/attractor/gitutil/git_exclude_test.go`
- Create: `internal/attractor/engine/checkpoint_exclude_test.go`

**Step 1: Write failing git util tests for exclusion behavior**

Create `internal/attractor/gitutil/git_exclude_test.go` with tests:
- `TestAddAllWithExcludes_DoesNotStageExcludedUntrackedPaths`
- `TestAddAllWithExcludes_DoesNotStageExcludedTrackedModifications`

Expected failure first: `undefined: AddAllWithExcludes`

**Step 2: Implement exclude-capable staging API**

In `gitutil/git.go` add:

```go
func AddAllWithExcludes(worktreeDir string, excludes []string) error {
	args := []string{"add", "-A", "--", "."}
	for _, p := range excludes {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		args = append(args, ":(exclude)"+p)
	}
	_, _, err := runGit(worktreeDir, args...)
	return err
}
```

Update `CommitAllowEmpty` to call `AddAllWithExcludes(..., nil)`.

**Step 3: Add run-config field and defaults**

In `RunConfigFile.Git` add:

```go
CheckpointExcludeGlobs []string `json:"checkpoint_exclude_globs,omitempty" yaml:"checkpoint_exclude_globs,omitempty"`
```

Set defaults in `applyConfigDefaults`:

```go
if len(cfg.Git.CheckpointExcludeGlobs) == 0 {
	cfg.Git.CheckpointExcludeGlobs = []string{
		"**/.cargo-target*/**",
		"**/.cargo_target*/**",
		"**/.wasm-pack/**",
		"**/.tmpbuild/**",
	}
}
```

Do **not** add broad defaults like `**/target/**` or `**/pkg/**`; those can hide legitimate project outputs in non-Rust repos.

**Step 4: Use exclusion list in checkpoint path**

In `engine.checkpoint(...)`, replace raw commit helper call with explicit exclude-aware commit API.
Implement one of:
- `CommitAllowEmptyWithExcludes(worktreeDir, message string, excludes []string) (string, error)`, or
- split commit into `AddAllWithExcludes(...)` + `CommitOnlyAllowEmpty(...)`.

Recommended: `CommitAllowEmptyWithExcludes(...)` to keep checkpoint callsites simple and preserve old helper behavior for non-checkpoint callers.
Thread excludes from `e.RunConfig.Git.CheckpointExcludeGlobs` into this checkpoint commit path explicitly.

**Step 5: Add engine test proving excluded artifacts are not checkpointed**

Create `internal/attractor/engine/checkpoint_exclude_test.go`:
- Build tiny graph where tool node writes file under `.cargo_target_local/...` and source file under `src/ok.txt`.
- Assert source file is committed; artifact file is absent from `git ls-files`.

**Step 6: Run tests**

Run: `go test ./internal/attractor/gitutil ./internal/attractor/engine -run 'Exclude|checkpoint' -count=1`
Expected: PASS

**Step 7: Commit**

```bash
git add internal/attractor/gitutil/git.go internal/attractor/gitutil/git_exclude_test.go internal/attractor/engine/config.go internal/attractor/engine/engine.go internal/attractor/engine/checkpoint_exclude_test.go
git commit -m "feat(engine): add checkpoint artifact exclusion globs and pathspec-aware staging"
```

---

### Task 4: Stabilize Deterministic Failure Signatures for Faster Abort and Better Routing

**Files:**
- Modify: `internal/attractor/runtime/status.go`
- Modify: `internal/attractor/engine/loop_restart_policy.go`
- Modify: `internal/attractor/engine/deterministic_failure_cycle_test.go`
- Create: `internal/attractor/runtime/status_failure_signature_test.go`

**Step 1: Write failing tests for top-level failure metadata and signature override**

Create tests:
- `TestDecodeOutcomeJSON_PromotesTopLevelFailureClassAndSignature`
- `TestRestartFailureSignature_UsesFailureSignatureHint`

Use payload:

```json
{"status":"fail","failure_reason":"verbose prose","failure_class":"deterministic","failure_signature":"environmental_tooling_blocks"}
```

Expected initial failure: metadata is ignored and signature falls back to prose.

**Step 2: Implement status decode promotion**

In `DecodeOutcomeJSON`, decode top-level `failure_class` and `failure_signature` into `Outcome.Meta` when present.
Concrete shape:

```go
var raw map[string]any
if err := json.Unmarshal(b, &raw); err == nil {
	if o.Meta == nil {
		o.Meta = map[string]any{}
	}
	if fc := strings.TrimSpace(fmt.Sprint(raw["failure_class"])); fc != "" && fc != "<nil>" {
		o.Meta["failure_class"] = fc
	}
	if sig := strings.TrimSpace(fmt.Sprint(raw["failure_signature"])); sig != "" && sig != "<nil>" {
		o.Meta["failure_signature"] = sig
	}
}
```

Placement requirement: run this promotion in the canonical parse path **after** successful `json.Unmarshal(b, &o)` and **before** returning `o.Canonicalize()`.
This requires restructuring the current early-return branch so promotion executes before canonical return.

**Step 3: Implement signature override in cycle-breaker keying**

In `loop_restart_policy.go`, add helper:

```go
func readFailureSignatureHint(out runtime.Outcome) string
```

Update `restartFailureSignature` to prefer hint over normalized `failure_reason`.

**Step 4: Extend deterministic cycle test with varied prose but stable signature**

Add a fixture where repeated failures have different human text but same `failure_signature`. Assert breaker trips at configured limit.

**Step 5: Run tests**

Run: `go test ./internal/attractor/runtime ./internal/attractor/engine -run 'FailureSignature|DeterministicFailureCycle' -count=1`
Expected: PASS

**Step 6: Commit**

```bash
git add internal/attractor/runtime/status.go internal/attractor/runtime/status_failure_signature_test.go internal/attractor/engine/loop_restart_policy.go internal/attractor/engine/deterministic_failure_cycle_test.go
git commit -m "fix(engine): support machine-stable failure signatures for deterministic cycle control"
```

---

### Task 5: Hardening `english-to-dotfile` Template and Rules for Tooling Gates

**Files:**
- Modify: `skills/english-to-dotfile/reference_template.dot`
- Modify: `skills/english-to-dotfile/SKILL.md`
- Modify: `demo/rogue/rogue.dot`

**Step 1: Update template to separate deterministic checks into tool nodes**

In `reference_template.dot`, split verification into:
- `verify_fmt` (`shape=parallelogram`)
- `verify_build` (`shape=parallelogram`)
- `verify_test` (`shape=parallelogram`)
- `verify_artifacts` (`shape=parallelogram`)
- `verify_fidelity` (`shape=box`, semantic review only)

Use sequential routing with check diamonds and explicit fail routing.

**Step 2: Add failure-class-aware fail routing in template**

Pattern for inner loop:

```dot
check_build -> implement [condition="outcome=fail && context.failure_class=transient_infra", loop_restart=true]
check_build -> postmortem [condition="outcome=fail && context.failure_class!=transient_infra"]
```

**Step 3: Strengthen skill contract for machine-stable failures and artifact patterns**

In `SKILL.md`, require generated prompts for deterministic checks to emit:
- `failure_class`
- canonical `failure_reason` enum (for example `environmental_tooling_blocks`, `artifact_pollution`)
- optional `failure_signature`

Also require artifact checks to include both hyphen and underscore cargo target variants.

**Step 4: Update `demo/rogue/rogue.dot` to match new pattern**

Move build/test/wasm/artifact checks to tool nodes and retain LLM node only for semantic fidelity checks.

**Step 5: Validate DOT**

Run:
- `./kilroy attractor validate --graph skills/english-to-dotfile/reference_template.dot`
- `./kilroy attractor validate --graph demo/rogue/rogue.dot`

Expected: both PASS

**Step 6: Commit**

```bash
git add skills/english-to-dotfile/reference_template.dot skills/english-to-dotfile/SKILL.md demo/rogue/rogue.dot
git commit -m "fix(dotfile-skill): route deterministic tooling checks through tool nodes with class-aware failure edges"
```

---

### Task 6: Extend Existing Validator Guardrail to Cover Non-`loop_restart` Fail Loops

**Files:**
- Modify: `internal/attractor/validate/validate.go`
- Modify: `internal/attractor/validate/validate_test.go`

**Step 1: Add failing validator tests (extension of existing guardrail)**

Add two cases:
- Warning when a `shape=diamond` check node has `condition="outcome=fail"` edge back to implement with no `context.failure_class` guard and `loop_restart=false`.
- No warning when fail edges are split on `context.failure_class=transient_infra` and `!=transient_infra`.

**Step 2: Implement lint-rule extension**

Extend existing failure-loop guarding logic so non-`loop_restart` fail back-edges are also flagged.
Use a new warning rule name:
- `fail_loop_failure_class_guard`

Heuristic:
- From a diamond node, detect outgoing fail edges that return to predecessor nodes (nodes that can reach the diamond in forward traversal from start).
- If edge condition lacks `context.failure_class` predicate, emit warning.

**Step 3: Run validator tests**

Run: `go test ./internal/attractor/validate -run 'fail_loop_failure_class_guard|loop_restart_failure_class_guard|Validate' -count=1`
Expected: PASS

**Step 4: Commit**

```bash
git add internal/attractor/validate/validate.go internal/attractor/validate/validate_test.go
git commit -m "feat(validate): extend failure-class guardrails to non-restart fail loops"
```

---

### Task 7: Full Verification Sweep

**Files:**
- No new files required

**Step 1: Targeted regression suite**

Run:
- `go test ./internal/attractor/runtime -count=1`
- `go test ./internal/attractor/gitutil -count=1`
- `go test ./internal/attractor/validate -count=1`
- `go test ./internal/attractor/engine -count=1`

Expected: PASS

**Step 2: Full test suite**

Run: `go test ./...`
Expected: PASS

**Step 3: Build binary**

Run: `go build -o ./kilroy ./cmd/kilroy`
Expected: PASS

**Step 4: Commit verification notes (if any docs changed during runbook updates)**

```bash
# only if there are staged changes
git add -A
git commit -m "test(engine): run full regression suite for tooling recovery remediation"
```

---

## Final Acceptance Criteria

- API `agent_loop` path uses the same base env invariants as CLI/tool handlers (`CARGO_TARGET_DIR`, pinned toolchain homes, `CLAUDECODE` stripped).
- Checkpoint commits do not include configured artifact globs, including underscore cargo target variants, without broad default exclusions that hide legitimate source paths.
- Deterministic cycle breaker can use stable `failure_signature` keys independent of prose variation.
- `english-to-dotfile` template routes deterministic command checks via `shape=parallelogram` tool nodes, not codergen prompts.
- Retry edges in generated templates are failure-class aware (`transient_infra` vs deterministic).
- Validator emits guardrail warnings for unguarded deterministic fail loops (restart and non-restart variants).
- `demo/rogue/rogue.dot` validates and reflects the hardened pattern.

## Out of Scope

- Running a new production Rogue run.
- Changing provider/model selection policy beyond existing style/template behavior.
- Altering CXDB schema.
