# Fix Environment Asymmetry Across Handler Types

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Eliminate the environment mismatch between tool nodes and codergen nodes that causes toolchain gates to pass while downstream work nodes fail.

**Architecture:** Extract a shared `buildBaseNodeEnv` function that both `ToolHandler` and `CodergenRouter` use, ensuring all handler types see the same toolchain paths (RUSTUP_HOME, CARGO_HOME, CARGO_TARGET_DIR, etc.). The skill's existing guidance (`shape=parallelogram` for toolchain gates) is architecturally correct — deterministic shell preflights belong in the tool handler, not in an LLM codergen node. The engine fix makes tool nodes use the same base environment as codergen nodes, so the shape choice becomes irrelevant to env consistency.

**Tech Stack:** Go (engine), Markdown (skill)

---

## The Problem

The Kilroy Attractor engine has three distinct execution environments depending on node type:

| Handler | How `cmd.Env` is set | HOST toolchain visible? | CARGO_TARGET_DIR? |
|---------|---------------------|------------------------|-------------------|
| **Tool** (`parallelogram`) | `nil` — inherits `os.Environ()` | Yes | No |
| **Codergen + codex** (`box`, openai) | `buildCodexIsolatedEnv()` + overrides HOME | **No** (HOME changed) | Yes |
| **Codergen + other CLI** (`box`, anthropic/google) | `scrubConflictingProviderEnvKeys()` | Yes | No |

This asymmetry causes a specific, repeatable failure pattern:

1. `check_toolchain` (`shape=parallelogram`) runs `command -v cargo` in the **host environment** where `$HOME/.cargo/bin` is on PATH. **Passes.**
2. `implement` (`shape=box`, codex backend) gets `buildCodexIsolatedEnv()` which overrides `HOME` to an isolated temp dir. If `RUSTUP_HOME`/`CARGO_HOME` aren't explicitly set in the system env, cargo/rustup default to `$HOME/.cargo`/`$HOME/.rustup` — which now points to the **empty isolated dir**. Toolchain not found.
3. `verify_impl` (`shape=box`, codex backend) — same isolated env, same failure. **`rust_toolchain_unavailable`.**
4. The pipeline retries identically and loops until it exhausts retries.

Additionally, the `CARGO_TARGET_DIR` fix (commit `b7cbbc3a`) that prevents EXDEV (cross-device link) errors only applies to the codex backend path. Non-codex CLI backends and tool nodes that invoke cargo also hit EXDEV but have no mitigation.

### Why This Is the Right Fix

**Option considered: just fix the skill (make `check_toolchain` a box node).** This was initially attractive but is architecturally wrong. `shape=parallelogram` is the deterministic external-tool handler per the Attractor spec — it runs a shell command and returns an exit code. Converting toolchain checks to `shape=box` (codergen/LLM handler) would turn a deterministic shell preflight into an LLM-dependent stage, contradicting the "before expensive LLM stages" intent. It also doesn't fix the underlying engine bug — any future graph that mixes tool and codergen nodes would hit the same asymmetry.

**Option considered: just fix the codex CARGO_TARGET_DIR path.** This fixes the immediate Rust/cargo EXDEV case but leaves the fundamental design flaw: tool nodes and codergen nodes have completely independent env construction with no shared contract. The next toolchain (Zig, Haskell, Deno, etc.) will hit the same pattern.

**The chosen approach (engine-level shared env construction)** is the idiomatic solution because:

1. **Shared `buildBaseNodeEnv`** — a single function produces a base environment that both `ToolHandler` and `CodergenRouter` use. Toolchain-related env vars (`RUSTUP_HOME`, `CARGO_HOME`, `GOPATH`, `PATH` entries for these) are always preserved, even when HOME is overridden. `CARGO_TARGET_DIR` is set whenever a worktree is involved, not just for codex. This makes the engine's environment handling a **single code path** instead of three divergent ones.

2. **Handler semantics preserved** — `shape=parallelogram` remains the correct shape for toolchain readiness checks. The Attractor spec defines it as the deterministic shell handler, which is exactly what `command -v cargo` is. The engine fix ensures that tool nodes see the same toolchain paths as codergen nodes, so the shape choice is orthogonal to env consistency.

3. **All paths covered** — the codex isolation, non-codex CLI, and tool handler paths all start from the same base. The codex retry paths (state-DB fallback, timeout fallback) also use the base env, so CARGO_TARGET_DIR and toolchain paths cannot be dropped on retries.

---

### Task 1: Extract `buildBaseNodeEnv` and preserve toolchain paths in codex isolation

This task creates a shared env construction function and fixes `buildCodexIsolatedEnvWithName` to preserve toolchain-related paths.

**Files:**
- Create: `internal/attractor/engine/node_env.go`
- Modify: `internal/attractor/engine/codergen_router.go:890-904` (codex env path)
- Modify: `internal/attractor/engine/codergen_router.go:994-996` (non-codex env path)
- Modify: `internal/attractor/engine/codergen_router.go:1312-1363` (`buildCodexIsolatedEnvWithName`)
- Test: `internal/attractor/engine/node_env_test.go`

**Step 1: Write the failing test for `buildBaseNodeEnv`**

Create `internal/attractor/engine/node_env_test.go`:

```go
package engine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildBaseNodeEnv_PreservesToolchainPaths(t *testing.T) {
	home := t.TempDir()
	cargoHome := filepath.Join(home, ".cargo")
	rustupHome := filepath.Join(home, ".rustup")
	gopath := filepath.Join(home, "go")

	t.Setenv("HOME", home)
	t.Setenv("CARGO_HOME", cargoHome)
	t.Setenv("RUSTUP_HOME", rustupHome)
	t.Setenv("GOPATH", gopath)

	worktree := t.TempDir()
	env := buildBaseNodeEnv(worktree)

	if got := envLookup(env, "CARGO_HOME"); got != cargoHome {
		t.Fatalf("CARGO_HOME: got %q want %q", got, cargoHome)
	}
	if got := envLookup(env, "RUSTUP_HOME"); got != rustupHome {
		t.Fatalf("RUSTUP_HOME: got %q want %q", got, rustupHome)
	}
	if got := envLookup(env, "GOPATH"); got != gopath {
		t.Fatalf("GOPATH: got %q want %q", got, gopath)
	}
	if got := envLookup(env, "CARGO_TARGET_DIR"); got != filepath.Join(worktree, ".cargo-target") {
		t.Fatalf("CARGO_TARGET_DIR: got %q want %q", got, filepath.Join(worktree, ".cargo-target"))
	}
}

func TestBuildBaseNodeEnv_InfersGoPathsFromHOME(t *testing.T) {
	// When GOPATH/GOMODCACHE are not set, Go defaults them to $HOME/go
	// and $HOME/go/pkg/mod. buildBaseNodeEnv should pin them explicitly
	// so that later HOME overrides (codex isolation) don't break Go
	// toolchain resolution.
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.Unsetenv("GOPATH")
	os.Unsetenv("GOMODCACHE")

	worktree := t.TempDir()
	env := buildBaseNodeEnv(worktree)

	if got := envLookup(env, "GOPATH"); got != filepath.Join(home, "go") {
		t.Fatalf("GOPATH: got %q want %q", got, filepath.Join(home, "go"))
	}
	if got := envLookup(env, "GOMODCACHE"); got != filepath.Join(home, "go", "pkg", "mod") {
		t.Fatalf("GOMODCACHE: got %q want %q", got, filepath.Join(home, "go", "pkg", "mod"))
	}
}

func TestBuildBaseNodeEnv_SetsCargoTargetDirToWorktree(t *testing.T) {
	worktree := t.TempDir()
	env := buildBaseNodeEnv(worktree)

	got := envLookup(env, "CARGO_TARGET_DIR")
	want := filepath.Join(worktree, ".cargo-target")
	if got != want {
		t.Fatalf("CARGO_TARGET_DIR: got %q want %q", got, want)
	}
}

func TestBuildBaseNodeEnv_DoesNotOverrideExplicitCargoTargetDir(t *testing.T) {
	t.Setenv("CARGO_TARGET_DIR", "/custom/target")
	worktree := t.TempDir()
	env := buildBaseNodeEnv(worktree)

	got := envLookup(env, "CARGO_TARGET_DIR")
	if got != "/custom/target" {
		t.Fatalf("CARGO_TARGET_DIR: got %q want %q (should not override explicit)", got, "/custom/target")
	}
}

func TestBuildBaseNodeEnv_InfersToolchainPathsFromHOME(t *testing.T) {
	// When CARGO_HOME/RUSTUP_HOME are not set, they default to $HOME/.cargo and $HOME/.rustup.
	// buildBaseNodeEnv should set them explicitly so downstream HOME overrides don't break them.
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.Unsetenv("CARGO_HOME")
	os.Unsetenv("RUSTUP_HOME")

	worktree := t.TempDir()
	env := buildBaseNodeEnv(worktree)

	if got := envLookup(env, "CARGO_HOME"); got != filepath.Join(home, ".cargo") {
		t.Fatalf("CARGO_HOME: got %q want %q", got, filepath.Join(home, ".cargo"))
	}
	if got := envLookup(env, "RUSTUP_HOME"); got != filepath.Join(home, ".rustup") {
		t.Fatalf("RUSTUP_HOME: got %q want %q", got, filepath.Join(home, ".rustup"))
	}
}

func TestBuildBaseNodeEnv_StripsClaudeCode(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	worktree := t.TempDir()
	env := buildBaseNodeEnv(worktree)

	if envHasKey(env, "CLAUDECODE") {
		t.Fatal("CLAUDECODE should be stripped from base env")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/attractor/engine/ -run TestBuildBaseNodeEnv -v -count=1`
Expected: FAIL with "undefined: buildBaseNodeEnv"

**Step 3: Write `buildBaseNodeEnv` implementation**

Create `internal/attractor/engine/node_env.go`:

```go
package engine

import (
	"os"
	"path/filepath"
	"strings"
)

// toolchainEnvKeys are environment variables that locate build toolchains
// (Rust, Go, etc.) relative to HOME. When a handler overrides HOME (e.g.,
// codex isolation), these must be pinned to their original absolute values
// so toolchains remain discoverable.
var toolchainEnvKeys = []string{
	"CARGO_HOME",   // Rust: defaults to $HOME/.cargo
	"RUSTUP_HOME",  // Rust: defaults to $HOME/.rustup
	"GOPATH",       // Go: defaults to $HOME/go
	"GOMODCACHE",   // Go: defaults to $GOPATH/pkg/mod
}

// toolchainDefaults maps env keys to their default relative-to-HOME paths.
// If the key is not set in the environment, buildBaseNodeEnv pins it to
// $HOME/<default> so that later HOME overrides don't break toolchain lookup.
// Go defaults: GOPATH=$HOME/go, GOMODCACHE=$GOPATH/pkg/mod.
var toolchainDefaults = map[string]string{
	"CARGO_HOME":  ".cargo",
	"RUSTUP_HOME": ".rustup",
	"GOPATH":      "go",
}

// buildBaseNodeEnv constructs the base environment for any node execution.
// It:
//   - Starts from os.Environ()
//   - Strips CLAUDECODE (nested session protection)
//   - Pins toolchain paths to absolute values (immune to HOME overrides)
//   - Sets CARGO_TARGET_DIR inside worktree to avoid EXDEV errors
//
// Both ToolHandler and CodergenRouter should use this as their starting env,
// then apply handler-specific overrides on top.
func buildBaseNodeEnv(worktreeDir string) []string {
	base := os.Environ()

	// Snapshot HOME before any overrides.
	home := strings.TrimSpace(os.Getenv("HOME"))

	// Pin toolchain paths to absolute values. If not explicitly set,
	// infer from current HOME so a later HOME override doesn't break them.
	toolchainOverrides := map[string]string{}
	for _, key := range toolchainEnvKeys {
		val := strings.TrimSpace(os.Getenv(key))
		if val != "" {
			// Already set — pin the explicit value.
			toolchainOverrides[key] = val
		} else if defaultRel, ok := toolchainDefaults[key]; ok && home != "" {
			// Not set — pin the default (HOME-relative) path.
			toolchainOverrides[key] = filepath.Join(home, defaultRel)
		}
	}

	// GOMODCACHE defaults to $GOPATH/pkg/mod (not directly to HOME).
	// Pin it after the loop so we can use the resolved GOPATH value.
	if strings.TrimSpace(os.Getenv("GOMODCACHE")) == "" {
		gopath := toolchainOverrides["GOPATH"]
		if gopath == "" {
			gopath = strings.TrimSpace(os.Getenv("GOPATH"))
		}
		if gopath != "" {
			toolchainOverrides["GOMODCACHE"] = filepath.Join(gopath, "pkg", "mod")
		}
	}

	// Set CARGO_TARGET_DIR inside the worktree to avoid EXDEV errors
	// when cargo moves intermediate artifacts across filesystem boundaries.
	// Harmless for non-Rust projects (unused env var).
	if worktreeDir != "" && strings.TrimSpace(os.Getenv("CARGO_TARGET_DIR")) == "" {
		toolchainOverrides["CARGO_TARGET_DIR"] = filepath.Join(worktreeDir, ".cargo-target")
	}

	env := mergeEnvWithOverrides(base, toolchainOverrides)

	// Strip CLAUDECODE — it prevents the Claude CLI from launching
	// (nested session protection). All handler types need this stripped.
	return stripEnvKey(env, "CLAUDECODE")
}

// stripEnvKey removes all entries with the given key from an env slice.
func stripEnvKey(env []string, key string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env))
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) || entry == key {
			continue
		}
		out = append(out, entry)
	}
	return out
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/attractor/engine/ -run TestBuildBaseNodeEnv -v -count=1`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/attractor/engine/node_env.go internal/attractor/engine/node_env_test.go
git commit -m "feat(engine): add buildBaseNodeEnv helper for shared toolchain env

Adds a new function that constructs a base environment with toolchain
paths pinned to absolute values (CARGO_HOME, RUSTUP_HOME, GOPATH,
GOMODCACHE), CARGO_TARGET_DIR set inside the worktree, and CLAUDECODE
stripped. Not yet wired to any handler — Tasks 2 and 3 will integrate
it into ToolHandler and CodergenRouter respectively."
```

---

### Task 2: Wire `ToolHandler` to use `buildBaseNodeEnv`

Currently `ToolHandler.Execute` never sets `cmd.Env`, so it inherits the raw parent process environment. This means tool nodes see the host env (including CLAUDECODE) while codergen nodes see a different env. Wire it to use `buildBaseNodeEnv`.

**Files:**
- Modify: `internal/attractor/engine/handlers.go:440-474` (ToolHandler.Execute)
- Test: `internal/attractor/engine/node_env_test.go` (add integration-style test)

**Step 1: Write the failing test**

Add to `internal/attractor/engine/node_env_test.go` (also add `"context"` and `"strings"` to the import block):

```go
func TestToolHandler_UsesBaseNodeEnv(t *testing.T) {
	// A tool node should see pinned toolchain env vars and have CLAUDECODE stripped.
	// We can verify by running a tool_command that echoes env vars.
	t.Setenv("CLAUDECODE", "1")

	dot := []byte(`digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  exit [shape=Msquare]
  check [shape=parallelogram, tool_command="bash -c 'echo CLAUDECODE=$CLAUDECODE; echo CARGO_TARGET_DIR=$CARGO_TARGET_DIR'"]
  start -> check -> exit
}`)
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	result, err := Run(context.Background(), dot, RunOptions{RepoPath: repo, LogsRoot: logsRoot})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != "success" {
		t.Fatalf("expected success, got %s: %s", result.Status, result.FailureReason)
	}

	// Read stdout to verify env was set correctly.
	stdout, err := os.ReadFile(filepath.Join(logsRoot, "check", "stdout.log"))
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	output := string(stdout)
	if strings.Contains(output, "CLAUDECODE=1") {
		t.Fatal("CLAUDECODE should be stripped from tool node env")
	}
	if !strings.Contains(output, "CARGO_TARGET_DIR=") {
		t.Fatal("CARGO_TARGET_DIR should be set in tool node env")
	}
}
```

Note: This test uses the actual `Run()` + `initTestRepo()` pattern from the engine test suite (see `engine_stage_timeout_test.go` and `run_with_config_integration_test.go:801` for examples). `initTestRepo` creates a temp git repo with an initial commit — required because `Run()` creates worktrees.

**Step 2: Run test to verify it fails**

Run: `go test ./internal/attractor/engine/ -run TestToolHandler_UsesBaseNodeEnv -v -count=1`
Expected: FAIL — CLAUDECODE=1 will appear in output (tool handler inherits parent env)

**Step 3: Wire ToolHandler to use `buildBaseNodeEnv`**

In `internal/attractor/engine/handlers.go`, in `ToolHandler.Execute`, add `cmd.Env` after `cmd.Dir`:

Find (around line 454-456):
```go
	cmd := exec.CommandContext(cctx, "bash", "-c", cmdStr)
	cmd.Dir = execCtx.WorktreeDir
	// Avoid hanging on interactive reads; tool_command doesn't provide a way to supply stdin.
	cmd.Stdin = strings.NewReader("")
```

Replace with:
```go
	cmd := exec.CommandContext(cctx, "bash", "-c", cmdStr)
	cmd.Dir = execCtx.WorktreeDir
	cmd.Env = buildBaseNodeEnv(execCtx.WorktreeDir)
	// Avoid hanging on interactive reads; tool_command doesn't provide a way to supply stdin.
	cmd.Stdin = strings.NewReader("")
```

Also update the `env_mode` in the `tool_invocation.json` write (around line 447) from `"inherit"` to `"base"`:

Find:
```go
		"env_mode":    "inherit",
```

Replace with:
```go
		"env_mode":    "base",
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/attractor/engine/ -run TestToolHandler_UsesBaseNodeEnv -v -count=1`
Expected: PASS

**Step 5: Run full test suite to check for regressions**

Run: `go test ./internal/attractor/engine/ -count=1 -timeout 300s`
Expected: PASS (all existing tool node tests should still pass since they only use simple commands)

**Step 6: Commit**

```bash
git add internal/attractor/engine/handlers.go internal/attractor/engine/node_env_test.go
git commit -m "fix(engine): wire ToolHandler to use buildBaseNodeEnv

Tool nodes now use the same base environment as codergen nodes instead
of inheriting the raw parent process env. This ensures toolchain paths
are pinned, CARGO_TARGET_DIR is set, and CLAUDECODE is stripped.
Fixes the environment asymmetry where check_toolchain (tool node)
validated a different env than implement (codergen node)."
```

---

### Task 3: Wire `CodergenRouter` to use `buildBaseNodeEnv` as its base

The codergen router currently has two separate env construction paths (codex isolated vs. scrubbed inherit). Both should start from `buildBaseNodeEnv` so toolchain paths and CLAUDECODE stripping are handled uniformly.

**Files:**
- Modify: `internal/attractor/engine/codergen_router.go:886-905` (codex path)
- Modify: `internal/attractor/engine/codergen_router.go:991-997` (non-codex path)
- Modify: `internal/attractor/engine/codergen_router.go:1312-1363` (`buildCodexIsolatedEnvWithName`)
- Test: `internal/attractor/engine/node_env_test.go`

**Step 1: Write the failing test**

Add to `internal/attractor/engine/node_env_test.go`:

```go
func TestBuildCodexIsolatedEnv_PreservesToolchainPaths(t *testing.T) {
	home := t.TempDir()
	cargoHome := filepath.Join(home, ".cargo")
	rustupHome := filepath.Join(home, ".rustup")

	t.Setenv("HOME", home)
	t.Setenv("CARGO_HOME", cargoHome)
	t.Setenv("RUSTUP_HOME", rustupHome)
	t.Setenv("CLAUDECODE", "1")

	stateBase := filepath.Join(t.TempDir(), "codex-state-base")
	t.Setenv("KILROY_CODEX_STATE_BASE", stateBase)

	stageDir := t.TempDir()
	worktree := t.TempDir()
	env, _, err := buildCodexIsolatedEnv(stageDir, buildBaseNodeEnv(worktree))
	if err != nil {
		t.Fatalf("buildCodexIsolatedEnv: %v", err)
	}

	// HOME should be overridden to isolated dir (not the original home).
	if got := envLookup(env, "HOME"); got == home {
		t.Fatalf("HOME should be overridden to isolated dir, got original: %q", got)
	}

	// But toolchain paths should still point to the ORIGINAL home's paths.
	if got := envLookup(env, "CARGO_HOME"); got != cargoHome {
		t.Fatalf("CARGO_HOME: got %q want %q (should survive HOME override)", got, cargoHome)
	}
	if got := envLookup(env, "RUSTUP_HOME"); got != rustupHome {
		t.Fatalf("RUSTUP_HOME: got %q want %q (should survive HOME override)", got, rustupHome)
	}

	// CLAUDECODE should be stripped.
	if envHasKey(env, "CLAUDECODE") {
		t.Fatal("CLAUDECODE should be stripped")
	}

	// CARGO_TARGET_DIR should be set (from buildBaseNodeEnv).
	if !envHasKey(env, "CARGO_TARGET_DIR") {
		t.Fatal("CARGO_TARGET_DIR should be set")
	}
}

func TestBuildCodexIsolatedEnvWithName_RetryPreservesToolchainPaths(t *testing.T) {
	// Regression test: retry-rebuilt codex envs must preserve toolchain
	// paths. This is the highest-risk path — state-DB and timeout retries
	// rebuild the env on each attempt. If they don't receive baseEnv,
	// CARGO_TARGET_DIR and toolchain paths are silently dropped.
	home := t.TempDir()
	cargoHome := filepath.Join(home, ".cargo")
	rustupHome := filepath.Join(home, ".rustup")

	t.Setenv("HOME", home)
	t.Setenv("CARGO_HOME", cargoHome)
	t.Setenv("RUSTUP_HOME", rustupHome)

	stateBase := filepath.Join(t.TempDir(), "codex-state-base")
	t.Setenv("KILROY_CODEX_STATE_BASE", stateBase)

	stageDir := t.TempDir()
	worktree := t.TempDir()
	baseEnv := buildBaseNodeEnv(worktree)

	// Simulate multiple retry attempts like the real retry loops.
	for attempt := 1; attempt <= 3; attempt++ {
		name := fmt.Sprintf("codex-home-retry%d", attempt)
		env, _, err := buildCodexIsolatedEnvWithName(stageDir, name, baseEnv)
		if err != nil {
			t.Fatalf("attempt %d: buildCodexIsolatedEnvWithName: %v", attempt, err)
		}

		// Each retry must have its own isolated HOME.
		retryHome := envLookup(env, "HOME")
		if retryHome == home {
			t.Fatalf("attempt %d: HOME should be isolated, got original: %q", attempt, retryHome)
		}

		// Toolchain paths must survive every retry rebuild.
		if got := envLookup(env, "CARGO_HOME"); got != cargoHome {
			t.Fatalf("attempt %d: CARGO_HOME: got %q want %q", attempt, got, cargoHome)
		}
		if got := envLookup(env, "RUSTUP_HOME"); got != rustupHome {
			t.Fatalf("attempt %d: RUSTUP_HOME: got %q want %q", attempt, got, rustupHome)
		}
		if !envHasKey(env, "CARGO_TARGET_DIR") {
			t.Fatalf("attempt %d: CARGO_TARGET_DIR should be set", attempt)
		}
	}
}
```

Note: This test requires an additional `"fmt"` import — add it to the import block when implementing.

**Step 2: Run test to verify it fails**

Run: `go test ./internal/attractor/engine/ -run TestBuildCodexIsolatedEnv_PreservesToolchainPaths -v -count=1`
Expected: FAIL — `buildCodexIsolatedEnv` doesn't exist yet

**Step 3: Refactor `buildCodexIsolatedEnvWithName` to accept a base env**

The key change: instead of calling `os.Environ()` internally, `buildCodexIsolatedEnvWithName` accepts a pre-built base env (from `buildBaseNodeEnv`) and layers codex-specific overrides on top. This ensures toolchain paths survive HOME isolation on ALL paths — including retry loops.

**All callers must be updated.** There are 3 production call sites and 3 test call sites:

| File | Line | Call | Context |
|------|------|------|---------|
| `codergen_router.go` | 892 | `buildCodexIsolatedEnv(stageDir)` | Initial codex env construction |
| `codergen_router.go` | 1186 | `buildCodexIsolatedEnvWithName(stageDir, "codex-home-retry%d")` | State-DB failure retry |
| `codergen_router.go` | 1220 | `buildCodexIsolatedEnvWithName(stageDir, "codex-home-timeout-retry%d")` | Timeout failure retry |
| `codergen_cli_invocation_test.go` | 136 | `buildCodexIsolatedEnv(stageDir)` | Test: ConfiguresCodexScopedOverrides |
| `codergen_cli_invocation_test.go` | 222 | `buildCodexIsolatedEnv(stageDir)` | Test: RelativePathResolvesAbsolute |
| `resume_from_restart_dir_test.go` | 65 | `buildCodexIsolatedEnvWithName(relStageDir, "codex-home")` | Test: ResumeFromRestartDir |

**Important:** The retry call sites (lines 1186, 1220) are the most critical — they rebuild the codex env on each retry attempt. If these are not updated to pass a base env from `buildBaseNodeEnv`, CARGO_TARGET_DIR and toolchain paths will be dropped on retries, which is exactly the bug class we're fixing.

In `internal/attractor/engine/codergen_router.go`, change the signature of `buildCodexIsolatedEnvWithName` (lines 1312-1363) to accept a base env instead of calling `os.Environ()`:

```go
// buildCodexIsolatedEnvWithName applies codex-specific HOME/XDG isolation on top
// of the provided base environment (from buildBaseNodeEnv). Toolchain paths
// (CARGO_HOME, RUSTUP_HOME, CARGO_TARGET_DIR, etc.) are already pinned in baseEnv
// so they survive the HOME override.
func buildCodexIsolatedEnvWithName(stageDir string, homeDirName string, baseEnv []string) ([]string, map[string]any, error) {
	codexHome, err := codexIsolatedHomeDir(stageDir, homeDirName)
	if err != nil {
		return nil, nil, err
	}
	codexStateRoot := filepath.Join(codexHome, ".codex")
	xdgConfigHome := filepath.Join(codexHome, ".config")
	xdgDataHome := filepath.Join(codexHome, ".local", "share")
	xdgStateHome := filepath.Join(codexHome, ".local", "state")

	for _, dir := range []string{codexHome, codexStateRoot, xdgConfigHome, xdgDataHome, xdgStateHome} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, nil, err
		}
	}

	seeded := []string{}
	seedErrors := []string{}
	// Seed codex config from the ORIGINAL home (before isolation).
	// Use os.Getenv("HOME") since baseEnv may already have HOME pinned
	// to the original value by buildBaseNodeEnv.
	srcHome := strings.TrimSpace(os.Getenv("HOME"))
	if srcHome != "" {
		for _, name := range []string{"auth.json", "config.toml"} {
			src := filepath.Join(srcHome, ".codex", name)
			dst := filepath.Join(codexStateRoot, name)
			copied, err := copyIfExists(src, dst)
			if err != nil {
				seedErrors = append(seedErrors, fmt.Sprintf("%s: %v", name, err))
				continue
			}
			if copied {
				seeded = append(seeded, dst)
			}
		}
	}

	// Apply codex-specific overrides on top of the base env.
	// Toolchain paths (CARGO_HOME, RUSTUP_HOME, etc.) are already pinned
	// in baseEnv by buildBaseNodeEnv, so they survive this HOME override.
	env := mergeEnvWithOverrides(baseEnv, map[string]string{
		"HOME":            codexHome,
		"CODEX_HOME":      codexStateRoot,
		"XDG_CONFIG_HOME": xdgConfigHome,
		"XDG_DATA_HOME":   xdgDataHome,
		"XDG_STATE_HOME":  xdgStateHome,
	})

	meta := map[string]any{
		"state_base_root":  codexStateBaseRoot(),
		"state_root":       codexStateRoot,
		"env_seeded_files": seeded,
	}
	if len(seedErrors) > 0 {
		meta["env_seed_errors"] = seedErrors
	}
	return env, meta, nil
}

// buildCodexIsolatedEnv is the convenience wrapper with default home dir name.
func buildCodexIsolatedEnv(stageDir string, baseEnv []string) ([]string, map[string]any, error) {
	return buildCodexIsolatedEnvWithName(stageDir, "codex-home", baseEnv)
}
```

Then update **all 3 production call sites** to pass `baseEnv`:

**Call site 1** — initial codex env (line 892):
```go
if codexSemantics {
	var err error
	baseEnv := buildBaseNodeEnv(execCtx.WorktreeDir)
	isolatedEnv, isolatedMeta, err = buildCodexIsolatedEnv(stageDir, baseEnv)
	if err != nil {
		return "", classifiedFailure(err, ""), nil
	}
	// CARGO_TARGET_DIR is already set by buildBaseNodeEnv — no need for
	// the duplicate check that was here before.
}
```

**Call site 2** — state-DB retry (line 1186):
```go
retryEnv, retryMeta, buildErr := buildCodexIsolatedEnvWithName(
	stageDir, fmt.Sprintf("codex-home-retry%d", stateDBAttempt), baseEnv)
```
Note: `baseEnv` must be computed once before the retry loop and reused. Hoist the `baseEnv := buildBaseNodeEnv(execCtx.WorktreeDir)` above the retry loops so both retry paths can reference it.

**Call site 3** — timeout retry (line 1220):
```go
retryEnv, retryMeta, buildErr := buildCodexIsolatedEnvWithName(
	stageDir, fmt.Sprintf("codex-home-timeout-retry%d", timeoutAttempt), baseEnv)
```

**Non-codex path** (line 994-996):
```go
} else {
	baseEnv := buildBaseNodeEnv(execCtx.WorktreeDir)
	scrubbed := scrubConflictingProviderEnvKeys(baseEnv, providerKey)
	cmd.Env = mergeEnvWithOverrides(scrubbed, contract.EnvVars)
}
```

**Test call sites** — update all 3 to pass `os.Environ()` (tests don't need toolchain pinning, but the signature must match):
- `codergen_cli_invocation_test.go:136` → `buildCodexIsolatedEnv(stageDir, os.Environ())`
- `codergen_cli_invocation_test.go:222` → `buildCodexIsolatedEnv(stageDir, os.Environ())`
- `resume_from_restart_dir_test.go:65` → `buildCodexIsolatedEnvWithName(relStageDir, "codex-home", os.Environ())`

**Step 4: Run test to verify it passes**

Run: `go test ./internal/attractor/engine/ -run TestBuildCodexIsolatedEnv_PreservesToolchainPaths -v -count=1`
Expected: PASS

**Step 5: Run existing codex isolation test to verify no regression**

Run: `go test ./internal/attractor/engine/ -run TestBuildCodexIsolatedEnv -v -count=1`
Expected: PASS (both old and new tests)

**Step 6: Run full test suite**

Run: `go test ./internal/attractor/engine/ -count=1 -timeout 300s`
Expected: PASS

**Step 7: Commit**

```bash
git add internal/attractor/engine/codergen_router.go internal/attractor/engine/node_env.go internal/attractor/engine/node_env_test.go
git commit -m "fix(engine): wire CodergenRouter to use buildBaseNodeEnv

Both codex and non-codex CLI backends now start from buildBaseNodeEnv,
ensuring toolchain paths survive codex HOME isolation. Removes the
duplicate CARGO_TARGET_DIR logic from the codex-specific path since
buildBaseNodeEnv handles it for all handlers.

The codex isolation now layers on top of the base env:
  os.Environ() -> buildBaseNodeEnv (pin toolchains, strip CLAUDECODE,
  set CARGO_TARGET_DIR) -> buildCodexIsolatedEnv(baseEnv)
  (override HOME, XDG_*, seed codex config files)"
```

---

### Task 4: Run full test suite and verify end-to-end

No skill changes are needed. The engine fix (Tasks 1-3) ensures all handler types use the same base environment, so `shape=parallelogram` toolchain gates now correctly validate the same toolchain paths as downstream `shape=box` codergen nodes. The skill's existing guidance is architecturally correct: `parallelogram` is the deterministic shell handler, which is the right handler for `command -v cargo`.

**Step 1: Run full engine tests**

Run: `go test ./internal/attractor/engine/ -count=1 -timeout 300s`
Expected: PASS

**Step 2: Run validator on existing dotfiles to check for regressions**

Run:
```bash
go run ./cmd/kilroy attractor validate --graph demo/rogue/rogue.dot
go run ./cmd/kilroy attractor validate --graph docs/strongdm/dot\ specs/consensus_task.dot
```
Expected: Both pass (or same warnings as before — no new errors)

**Step 3: Verify new env function is used by both handlers**

Run: `grep -n 'buildBaseNodeEnv' internal/attractor/engine/handlers.go internal/attractor/engine/codergen_router.go`
Expected: Both files reference `buildBaseNodeEnv`

**Step 4: Verify all codex retry paths pass base env**

Run: `grep -n 'buildCodexIsolatedEnv' internal/attractor/engine/codergen_router.go`
Expected: All call sites pass `baseEnv` as a parameter (no calls to the old no-baseEnv signature)

**Step 5: Verify `reference_template.dot` is consistent**

The template-first workflow uses `skills/english-to-dotfile/reference_template.dot`. Verify it uses `shape=parallelogram` for `check_toolchain` (which is correct — the engine fix ensures tool nodes see the same env):

Run: `grep -A2 'check_toolchain' skills/english-to-dotfile/reference_template.dot`
Expected: `shape=parallelogram` with `tool_command`

**Step 6: Verify skill guidance is consistent**

Run: `grep -n 'shape=parallelogram' skills/english-to-dotfile/SKILL.md | head -5`
Expected: Skill recommends `shape=parallelogram` for toolchain gates (anti-pattern #20, toolchain bootstrap section). This is correct — no changes needed.

**Step 7: Commit (if any fixups needed)**

Only if fixes were needed. Otherwise this task is just verification.

---

## Verification Checklist

### Integration (Tasks 2 & 3 wire the helper into all handler paths)
- [ ] `buildBaseNodeEnv` is called by ToolHandler (`handlers.go` — `cmd.Env = buildBaseNodeEnv(...)`)
- [ ] `buildBaseNodeEnv` is called by CodergenRouter for codex path (`codergen_router.go` line ~892)
- [ ] `buildBaseNodeEnv` is called by CodergenRouter for non-codex path (`codergen_router.go` line ~994)
- [ ] `baseEnv` is hoisted above retry loops and passed to ALL codex retry paths (state-DB retry line ~1186, timeout retry line ~1220)
- [ ] No remaining calls to `os.Environ()` inside `buildCodexIsolatedEnvWithName` (it accepts `baseEnv` parameter)
- [ ] Tool nodes log `env_mode: "base"` (not `"inherit"`)

### Env correctness
- [ ] CARGO_HOME, RUSTUP_HOME pinned to absolute values in all handler types
- [ ] GOPATH, GOMODCACHE pinned to absolute values (defaults inferred from HOME if unset)
- [ ] CARGO_TARGET_DIR set for all handler types (not just codex)
- [ ] CLAUDECODE stripped for all handler types (not just via `conflictingProviderEnvKeys`)

### Test coverage
- [ ] Unit tests: `TestBuildBaseNodeEnv_*` (toolchain preservation, Go paths, CARGO_TARGET_DIR, CLAUDECODE)
- [ ] Integration test: `TestToolHandler_UsesBaseNodeEnv` (end-to-end via `Run()`)
- [ ] Unit test: `TestBuildCodexIsolatedEnv_PreservesToolchainPaths` (codex HOME override doesn't break toolchain)
- [ ] Regression test: `TestBuildCodexIsolatedEnvWithName_RetryPreservesToolchainPaths` (retry-rebuilt envs preserve toolchain paths across multiple attempts)
- [ ] All existing tests pass (including 3 updated test call sites)

### Consistency
- [ ] `reference_template.dot` keeps `check_toolchain` as `shape=parallelogram`
- [ ] Skill keeps `shape=parallelogram` guidance for toolchain gates
