package engine

import (
	"encoding/json"
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

func TestDefaultCLIInvocation_StripsProviderPrefixFromModelID(t *testing.T) {
	// Model IDs from .dot stylesheets use OpenRouter format (provider/model).
	// CLI binaries expect the bare model name without the provider prefix.
	tests := []struct {
		name      string
		provider  string
		modelID   string
		wantModel string
	}{
		{"anthropic with prefix and dots", "anthropic", "anthropic/claude-sonnet-4.5", "claude-sonnet-4-5"},
		{"anthropic with prefix no dots", "anthropic", "anthropic/claude-sonnet-4", "claude-sonnet-4"},
		{"anthropic without prefix", "anthropic", "claude-sonnet-4.5", "claude-sonnet-4-5"},
		{"google with prefix", "google", "google/gemini-3-flash-preview", "gemini-3-flash-preview"},
		{"google without prefix", "google", "gemini-3-flash-preview", "gemini-3-flash-preview"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, args := defaultCLIInvocation(tt.provider, tt.modelID, "/tmp/worktree")
			for i := 0; i < len(args)-1; i++ {
				if args[i] == "--model" {
					if args[i+1] != tt.wantModel {
						t.Fatalf("expected --model %s but got %s", tt.wantModel, args[i+1])
					}
					return
				}
			}
			t.Fatalf("--model flag not found in args: %v", args)
		})
	}
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
	exe, args := defaultCLIInvocation("openai", "gpt-5.4", "/tmp/worktree")
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
	// Explicitly unset OPENAI_API_KEY so we exercise the subscription-auth
	// fallback path (auth.json copied from the user profile).
	t.Setenv("OPENAI_API_KEY", "")
	stateBase := filepath.Join(t.TempDir(), "codex-state-base")
	t.Setenv("KILROY_CODEX_STATE_BASE", stateBase)

	stageDir := t.TempDir()
	env, meta, err := buildCodexIsolatedEnv(stageDir, os.Environ())
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
	// config.toml must NOT be copied into the isolated codex home. Kilroy's
	// isolation contract: run configuration comes from kilroy or the .dot
	// graph, not by accident from the user's shell profile. Leaking
	// model_reasoning_effort, personality, or other user-scoped codex
	// settings would let environment state change run behavior.
	if _, err := os.Stat(filepath.Join(wantStateRoot, "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("config.toml should not be seeded into isolated codex home (got err=%v)", err)
	}
	authInfo, err := os.Stat(filepath.Join(wantStateRoot, "auth.json"))
	if err != nil {
		t.Fatalf("stat auth.json: %v", err)
	}
	// copyIfExists preserves source perms (0o644 for the fake profile above).
	// When the apikey path writes a fresh auth.json it uses 0o600. Either
	// mode is fine; we mainly care that the file was written.
	_ = authInfo
}

func TestBuildCodexIsolatedEnv_SeedsFromUserProfileWhenHomeUnset(t *testing.T) {
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

	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")
	// Exercise the no-apikey fallback path so auth.json is copied from the
	// user profile rather than written fresh.
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("KILROY_CODEX_STATE_BASE", filepath.Join(t.TempDir(), "codex-state-base"))

	stageDir := t.TempDir()
	_, meta, err := buildCodexIsolatedEnv(stageDir, os.Environ())
	if err != nil {
		t.Fatalf("buildCodexIsolatedEnv: %v", err)
	}

	stateRoot := strings.TrimSpace(anyToString(meta["state_root"]))
	assertExists(t, filepath.Join(stateRoot, "auth.json"))
	// config.toml is not seeded — see TestBuildCodexIsolatedEnv_ConfiguresCodexScopedOverrides.
	if _, err := os.Stat(filepath.Join(stateRoot, "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("config.toml should not be seeded into isolated codex home (got err=%v)", err)
	}
}

// TestBuildCodexIsolatedEnv_WritesFreshApiKeyAuthWhenKeySet covers the happy
// path: OPENAI_API_KEY is available in the parent environment, so kilroy
// writes a fresh apikey auth.json into the isolated codex home rather than
// copying whatever the user has configured for their interactive codex
// sessions. Without this, apikey-only models like gpt-5-codex can't run
// under kilroy even when the user has a valid key.
func TestBuildCodexIsolatedEnv_WritesFreshApiKeyAuthWhenKeySet(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Write a stale subscription-shaped auth.json that would silently break
	// gpt-5-codex runs if copied verbatim into the isolated home.
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(`{"auth_mode":"chatgpt","token":"stale"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("KILROY_CODEX_STATE_BASE", filepath.Join(t.TempDir(), "codex-state-base"))
	t.Setenv("OPENAI_API_KEY", "sk-test-forced-apikey")

	stageDir := t.TempDir()
	_, meta, err := buildCodexIsolatedEnv(stageDir, os.Environ())
	if err != nil {
		t.Fatalf("buildCodexIsolatedEnv: %v", err)
	}

	stateRoot := strings.TrimSpace(anyToString(meta["state_root"]))
	authPath := filepath.Join(stateRoot, "auth.json")
	data, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("read isolated auth.json: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("parse isolated auth.json: %v\n%s", err, data)
	}
	if got["auth_mode"] != "apikey" {
		t.Fatalf("auth_mode: got %q want %q", got["auth_mode"], "apikey")
	}
	if got["OPENAI_API_KEY"] != "sk-test-forced-apikey" {
		t.Fatalf("OPENAI_API_KEY: got %q want %q", got["OPENAI_API_KEY"], "sk-test-forced-apikey")
	}
	info, err := os.Stat(authPath)
	if err != nil {
		t.Fatalf("stat auth.json: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("auth.json perms: got %#o want %#o", info.Mode().Perm(), 0o600)
	}
}

func TestCodexStateBaseRoot_FallsBackToUserProfileWhenHomeUnset(t *testing.T) {
	userProfile := t.TempDir()
	t.Setenv("KILROY_CODEX_STATE_BASE", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", userProfile)
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")

	got := codexStateBaseRoot()
	want := filepath.Join(userProfile, ".local", "state", "kilroy", "attractor", "codex-state")
	if got != want {
		t.Fatalf("codexStateBaseRoot: got %q want %q", got, want)
	}
}

func TestEnvHasKey(t *testing.T) {
	env := []string{"HOME=/tmp", "PATH=/usr/bin", "CARGO_TARGET_DIR=/foo/bar"}
	if !envHasKey(env, "CARGO_TARGET_DIR") {
		t.Fatal("expected CARGO_TARGET_DIR to be found")
	}
	if envHasKey(env, "CARGO_HOME") {
		t.Fatal("expected CARGO_HOME to not be found")
	}
	if envHasKey(nil, "HOME") {
		t.Fatal("expected nil env to return false")
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
	_, meta, err := buildCodexIsolatedEnv(stageDir, os.Environ())
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
