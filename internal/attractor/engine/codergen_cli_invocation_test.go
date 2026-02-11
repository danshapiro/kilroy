package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultCLIInvocation_GoogleGeminiNonInteractive(t *testing.T) {
	exe, args := defaultCLIInvocation("google", "gemini-3-flash-preview", "/tmp/worktree")
	if exe == "" {
		t.Fatalf("expected non-empty executable for google")
	}
	if !hasArg(args, "-p") {
		t.Fatalf("expected -p in args (headless prompt mode); args=%v", args)
	}
	// Spec/metaspec: CLI adapters must not block on interactive approvals.
	if !hasArg(args, "--yolo") {
		t.Fatalf("expected --yolo in args to force non-interactive approvals; args=%v", args)
	}
	// Ensure we pass the model explicitly.
	foundModel := false
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--model" && args[i+1] == "gemini-3-flash-preview" {
			foundModel = true
			break
		}
	}
	if !foundModel {
		t.Fatalf("expected --model gemini-3-flash-preview in args; args=%v", args)
	}
}

func TestDefaultCLIInvocation_AnthropicNormalizesDotsToHyphens(t *testing.T) {
	exe, args := defaultCLIInvocation("anthropic", "claude-sonnet-4.5", "/tmp/worktree")
	if exe == "" {
		t.Fatalf("expected non-empty executable for anthropic")
	}
	// The Claude CLI expects dashes in version numbers (claude-sonnet-4-5),
	// but the OpenRouter catalog uses dots (claude-sonnet-4.5).
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--model" {
			if args[i+1] != "claude-sonnet-4-5" {
				t.Fatalf("expected --model claude-sonnet-4-5 but got %s", args[i+1])
			}
			return
		}
	}
	t.Fatalf("--model flag not found in args: %v", args)
}

func TestDefaultCLIInvocation_AnthropicIncludesVerboseForStreamJSON(t *testing.T) {
	exe, args := defaultCLIInvocation("anthropic", "claude-sonnet-4", "/tmp/worktree")
	if exe == "" {
		t.Fatalf("expected non-empty executable for anthropic")
	}
	if !hasArg(args, "--output-format") {
		t.Fatalf("expected --output-format in args; args=%v", args)
	}
	if !hasArg(args, "stream-json") {
		t.Fatalf("expected stream-json output format; args=%v", args)
	}
	if !hasArg(args, "--verbose") {
		t.Fatalf("expected --verbose for stream-json contract compatibility; args=%v", args)
	}
}

func TestDefaultCLIInvocation_AnthropicSkipsPermissions(t *testing.T) {
	_, args := defaultCLIInvocation("anthropic", "claude-sonnet-4-5", "/tmp/worktree")
	if !hasArg(args, "--dangerously-skip-permissions") {
		t.Fatalf("expected --dangerously-skip-permissions for headless CLI mode; args=%v", args)
	}
}

func TestDefaultCLIInvocation_OpenAI_DoesNotUseDeprecatedAskForApproval(t *testing.T) {
	exe, args := defaultCLIInvocation("openai", "gpt-5.3-codex", "/tmp/worktree")
	if exe == "" {
		t.Fatalf("expected non-empty executable for openai")
	}
	if hasArg(args, "--ask-for-approval") {
		t.Fatalf("unexpected deprecated --ask-for-approval flag: %v", args)
	}
	if !hasArg(args, "--json") {
		t.Fatalf("expected --json: %v", args)
	}
}

func TestBuildCodexIsolatedEnv_ConfiguresCodexScopedOverrides(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(`{"token":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte(`model = "gpt-5"`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	stateBase := filepath.Join(t.TempDir(), "codex-state-base")
	t.Setenv("KILROY_CODEX_STATE_BASE", stateBase)

	stageDir := t.TempDir()
	env, meta, err := buildCodexIsolatedEnv(stageDir)
	if err != nil {
		t.Fatalf("buildCodexIsolatedEnv: %v", err)
	}

	wantHome, err := codexIsolatedHomeDir(stageDir, "codex-home")
	if err != nil {
		t.Fatalf("codexIsolatedHomeDir: %v", err)
	}
	stateRoot := strings.TrimSpace(anyToString(meta["state_root"]))
	wantStateRoot := filepath.Join(wantHome, ".codex")
	if stateRoot != wantStateRoot {
		t.Fatalf("state_root: got %q want %q", stateRoot, wantStateRoot)
	}
	if got := envLookup(env, "HOME"); got != wantHome {
		t.Fatalf("HOME: got %q want %q", got, wantHome)
	}
	if got := envLookup(env, "CODEX_HOME"); got != wantStateRoot {
		t.Fatalf("CODEX_HOME: got %q want %q", got, wantStateRoot)
	}
	if got := envLookup(env, "XDG_CONFIG_HOME"); got != filepath.Join(wantHome, ".config") {
		t.Fatalf("XDG_CONFIG_HOME: got %q", got)
	}
	if got := envLookup(env, "XDG_DATA_HOME"); got != filepath.Join(wantHome, ".local", "share") {
		t.Fatalf("XDG_DATA_HOME: got %q", got)
	}
	if got := envLookup(env, "XDG_STATE_HOME"); got != filepath.Join(wantHome, ".local", "state") {
		t.Fatalf("XDG_STATE_HOME: got %q", got)
	}
	if strings.HasPrefix(stateRoot, filepath.Clean(stageDir)+string(filepath.Separator)) || stateRoot == filepath.Clean(stageDir) {
		t.Fatalf("state_root should not be inside stageDir: stage=%q state_root=%q", stageDir, stateRoot)
	}
	if !strings.HasPrefix(stateRoot, filepath.Clean(stateBase)+string(filepath.Separator)) && stateRoot != filepath.Clean(stateBase) {
		t.Fatalf("state_root should be inside KILROY_CODEX_STATE_BASE=%q, got %q", stateBase, stateRoot)
	}

	assertExists(t, filepath.Join(wantStateRoot, "auth.json"))
	assertExists(t, filepath.Join(wantStateRoot, "config.toml"))
	authInfo, err := os.Stat(filepath.Join(wantStateRoot, "auth.json"))
	if err != nil {
		t.Fatalf("stat auth.json: %v", err)
	}
	if got := authInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("auth.json perms: got %#o want %#o", got, 0o600)
	}
	cfgInfo, err := os.Stat(filepath.Join(wantStateRoot, "config.toml"))
	if err != nil {
		t.Fatalf("stat config.toml: %v", err)
	}
	if got := cfgInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("config.toml perms: got %#o want %#o", got, 0o600)
	}
}

func TestIsStateDBDiscrepancy_MatchesRecordDiscrepancySignature(t *testing.T) {
	if !isStateDBDiscrepancy("fatal: record_discrepancy while loading thread state") {
		t.Fatalf("expected bare record_discrepancy signature to match")
	}
}

func TestCodexCLIInvocation_StateRootIsAbsolute(t *testing.T) {
	wd := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(wd); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldWD) }()
	t.Setenv("KILROY_CODEX_STATE_BASE", filepath.Join(wd, "state-base"))

	stageDir := filepath.Join("relative", "stage")
	_, meta, err := buildCodexIsolatedEnv(stageDir)
	if err != nil {
		t.Fatalf("buildCodexIsolatedEnv: %v", err)
	}
	stateRoot := strings.TrimSpace(anyToString(meta["state_root"]))
	if !filepath.IsAbs(stateRoot) {
		t.Fatalf("state_root should be absolute, got %q", stateRoot)
	}
}

func envLookup(env []string, key string) string {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}
