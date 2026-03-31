package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildBaseNodeEnv_RustVarsComeFromResolvedPolicy(t *testing.T) {
	rp := ResolvedArtifactPolicy{
		Env: ResolvedArtifactEnv{
			Vars: map[string]string{
				"CARGO_TARGET_DIR": "/tmp/policy-target",
			},
		},
	}
	env := buildBaseNodeEnv(rp)
	if !containsEnv(env, "CARGO_TARGET_DIR=/tmp/policy-target") {
		t.Fatal("expected CARGO_TARGET_DIR from resolved artifact policy")
	}
}

func TestBuildBaseNodeEnv_NoImplicitRustOrGoInjectionWithoutPolicy(t *testing.T) {
	unset := func(key string) {
		orig, had := os.LookupEnv(key)
		_ = os.Unsetenv(key)
		t.Cleanup(func() {
			if had {
				_ = os.Setenv(key, orig)
				return
			}
			_ = os.Unsetenv(key)
		})
	}
	unset("CARGO_TARGET_DIR")
	unset("GOPATH")
	unset("GOMODCACHE")

	env := buildBaseNodeEnv(ResolvedArtifactPolicy{})
	if findEnvPrefix(env, "CARGO_TARGET_DIR=") != "" {
		t.Fatal("unexpected implicit Rust env injection")
	}
	if findEnvPrefix(env, "GOPATH=") != "" {
		t.Fatal("unexpected implicit Go env injection")
	}
	if findEnvPrefix(env, "GOMODCACHE=") != "" {
		t.Fatal("unexpected implicit Go env injection")
	}
}

func TestBuildBaseNodeEnv_StripsClaudeCode(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	env := buildBaseNodeEnv(ResolvedArtifactPolicy{})
	if envHasKey(env, "CLAUDECODE") {
		t.Fatal("CLAUDECODE should be stripped from base env")
	}
}

func TestBuildBaseNodeEnv_StripsKilroyContractEnvKeys(t *testing.T) {
	outerValues := map[string]string{
		runIDEnvKey:                   "outer-run",
		nodeIDEnvKey:                  "outer-node",
		logsRootEnvKey:                "/tmp/outer-logs",
		stageLogsDirEnvKey:            "/tmp/outer-logs/outer-node",
		worktreeDirEnvKey:             "/tmp/outer-worktree",
		inputsManifestEnvKey:          "/tmp/outer-inputs-manifest.json",
		stageStatusPathEnvKey:         "/tmp/outer-status.json",
		stageStatusFallbackPathEnvKey: "/tmp/outer-fallback-status.json",
	}
	for key, value := range outerValues {
		t.Setenv(key, value)
	}

	env := buildBaseNodeEnv(ResolvedArtifactPolicy{})
	for key := range outerValues {
		if envHasKey(env, key) {
			t.Fatalf("%s should be stripped from base env", key)
		}
	}
}

func TestBuildBaseNodeEnv_PreservesExplicitToolchainPaths(t *testing.T) {
	home := t.TempDir()
	cargoHome := filepath.Join(home, ".cargo")
	rustupHome := filepath.Join(home, ".rustup")
	gopath := filepath.Join(home, "go")

	t.Setenv("HOME", home)
	t.Setenv("CARGO_HOME", cargoHome)
	t.Setenv("RUSTUP_HOME", rustupHome)
	t.Setenv("GOPATH", gopath)

	rp := ResolvedArtifactPolicy{
		Env: ResolvedArtifactEnv{
			Vars: map[string]string{
				"CARGO_TARGET_DIR": filepath.Join(t.TempDir(), "cargo-target"),
			},
		},
	}
	env := buildBaseNodeEnv(rp)

	if got := envLookup(env, "CARGO_HOME"); got != cargoHome {
		t.Fatalf("CARGO_HOME: got %q want %q", got, cargoHome)
	}
	if got := envLookup(env, "RUSTUP_HOME"); got != rustupHome {
		t.Fatalf("RUSTUP_HOME: got %q want %q", got, rustupHome)
	}
	if got := envLookup(env, "GOPATH"); got != gopath {
		t.Fatalf("GOPATH: got %q want %q", got, gopath)
	}
	if got := envLookup(env, "CARGO_TARGET_DIR"); got == "" {
		t.Fatal("CARGO_TARGET_DIR should come from resolved artifact policy")
	}
}

func TestToolHandler_UsesBaseNodeEnv(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	explicitTarget := filepath.Join(t.TempDir(), "explicit-target")
	t.Setenv("CARGO_TARGET_DIR", explicitTarget)

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
	if result.FinalStatus != "success" {
		t.Fatalf("expected success, got %s", result.FinalStatus)
	}

	stdout, err := os.ReadFile(filepath.Join(logsRoot, "check", "stdout.log"))
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	output := string(stdout)
	if strings.Contains(output, "CLAUDECODE=1") {
		t.Fatal("CLAUDECODE should be stripped from tool node env")
	}
	if !strings.Contains(output, "CARGO_TARGET_DIR="+explicitTarget) {
		t.Fatalf("CARGO_TARGET_DIR should preserve explicit environment value; output: %s", output)
	}
}

func TestBuildCodexIsolatedEnv_PreservesToolchainPaths(t *testing.T) {
	home := t.TempDir()
	cargoHome := filepath.Join(home, ".cargo")
	rustupHome := filepath.Join(home, ".rustup")
	targetDir := filepath.Join(t.TempDir(), "cargo-target")

	t.Setenv("HOME", home)
	t.Setenv("CARGO_HOME", cargoHome)
	t.Setenv("RUSTUP_HOME", rustupHome)
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("KILROY_CODEX_STATE_BASE", filepath.Join(t.TempDir(), "codex-state-base"))

	rp := ResolvedArtifactPolicy{
		Env: ResolvedArtifactEnv{
			Vars: map[string]string{
				"CARGO_TARGET_DIR": targetDir,
			},
		},
	}

	stageDir := t.TempDir()
	env, _, err := buildCodexIsolatedEnv(stageDir, buildBaseNodeEnv(rp))
	if err != nil {
		t.Fatalf("buildCodexIsolatedEnv: %v", err)
	}

	if got := envLookup(env, "HOME"); got == home {
		t.Fatalf("HOME should be overridden to isolated dir, got original: %q", got)
	}
	if got := envLookup(env, "CARGO_HOME"); got != cargoHome {
		t.Fatalf("CARGO_HOME: got %q want %q (should survive HOME override)", got, cargoHome)
	}
	if got := envLookup(env, "RUSTUP_HOME"); got != rustupHome {
		t.Fatalf("RUSTUP_HOME: got %q want %q (should survive HOME override)", got, rustupHome)
	}
	if got := envLookup(env, "CARGO_TARGET_DIR"); got != targetDir {
		t.Fatalf("CARGO_TARGET_DIR: got %q want %q", got, targetDir)
	}
	if envHasKey(env, "CLAUDECODE") {
		t.Fatal("CLAUDECODE should be stripped")
	}
}

func TestBuildCodexIsolatedEnvWithName_RetryPreservesToolchainPaths(t *testing.T) {
	home := t.TempDir()
	cargoHome := filepath.Join(home, ".cargo")
	rustupHome := filepath.Join(home, ".rustup")
	targetDir := filepath.Join(t.TempDir(), "cargo-target")

	t.Setenv("HOME", home)
	t.Setenv("CARGO_HOME", cargoHome)
	t.Setenv("RUSTUP_HOME", rustupHome)
	t.Setenv("KILROY_CODEX_STATE_BASE", filepath.Join(t.TempDir(), "codex-state-base"))

	rp := ResolvedArtifactPolicy{
		Env: ResolvedArtifactEnv{
			Vars: map[string]string{
				"CARGO_TARGET_DIR": targetDir,
			},
		},
	}

	stageDir := t.TempDir()
	baseEnv := buildBaseNodeEnv(rp)

	for attempt := 1; attempt <= 3; attempt++ {
		name := fmt.Sprintf("codex-home-retry%d", attempt)
		env, _, err := buildCodexIsolatedEnvWithName(stageDir, name, baseEnv)
		if err != nil {
			t.Fatalf("attempt %d: buildCodexIsolatedEnvWithName: %v", attempt, err)
		}
		retryHome := envLookup(env, "HOME")
		if retryHome == home {
			t.Fatalf("attempt %d: HOME should be isolated, got original: %q", attempt, retryHome)
		}
		if got := envLookup(env, "CARGO_HOME"); got != cargoHome {
			t.Fatalf("attempt %d: CARGO_HOME: got %q want %q", attempt, got, cargoHome)
		}
		if got := envLookup(env, "RUSTUP_HOME"); got != rustupHome {
			t.Fatalf("attempt %d: RUSTUP_HOME: got %q want %q", attempt, got, rustupHome)
		}
		if got := envLookup(env, "CARGO_TARGET_DIR"); got != targetDir {
			t.Fatalf("attempt %d: CARGO_TARGET_DIR: got %q want %q", attempt, got, targetDir)
		}
	}
}
