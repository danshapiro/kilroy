package agent

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLocalExecutionEnvironment_ExecCommand_TimesOutAndKillsProcessGroup(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	res, err := env.ExecCommand(ctx, "sleep 30", 50, "", nil)
	dur := time.Since(start)

	if err == nil {
		t.Fatalf("expected error, got nil (res=%+v)", res)
	}
	if !res.TimedOut {
		t.Fatalf("expected timed_out=true, got %+v", res)
	}
	if res.ExitCode != 124 {
		t.Fatalf("exit_code: got %d want 124", res.ExitCode)
	}
	if dur > 3*time.Second {
		t.Fatalf("expected timeout handling to return quickly; took %s", dur)
	}
}

func TestLocalExecutionEnvironment_ExecCommand_ContextCancel_KillsProcessGroup(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	var res ExecResult
	var err error
	start := time.Now()
	go func() {
		res, err = env.ExecCommand(ctx, "sleep 30", 30_000, "", nil)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("ExecCommand did not return promptly after context cancel")
	}
	if err == nil {
		t.Fatalf("expected error, got nil (res=%+v)", res)
	}
	if !res.TimedOut {
		t.Fatalf("expected timed_out=true on cancel, got %+v", res)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("expected cancel handling to return quickly; took %s", time.Since(start))
	}
}

func TestFilteredEnv_ExcludesSensitiveVars(t *testing.T) {
	t.Setenv("MY_API_KEY", "secret")
	t.Setenv("MY_SECRET", "secret2")
	env := filteredEnv(nil, nil)
	for _, kv := range env {
		if strings.HasPrefix(kv, "MY_API_KEY=") || strings.HasPrefix(kv, "MY_SECRET=") {
			t.Fatalf("sensitive env var leaked: %q", kv)
		}
	}
	// sanity check: PATH should be present in most environments
	foundPath := false
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			foundPath = true
		}
	}
	if !foundPath {
		t.Fatalf("expected PATH to be present in filtered env")
	}
}

func TestLocalExecutionEnvironment_ReadWriteEditFile(t *testing.T) {
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	if _, err := env.WriteFile("a.txt", "hello\nworld\n"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := env.ReadFile("a.txt", nil, nil)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(got, "1 | hello") {
		t.Fatalf("expected line numbers, got:\n%s", got)
	}
	if _, err := env.EditFile("a.txt", "world", "WORLD", false); err != nil {
		t.Fatalf("EditFile: %v", err)
	}
	b, _ := os.ReadFile(dir + "/a.txt")
	if !strings.Contains(string(b), "WORLD") {
		t.Fatalf("edit did not apply: %q", string(b))
	}
}

func TestLocalExecutionEnvironment_ListDirectory_Depth(t *testing.T) {
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	if _, err := env.WriteFile("a.txt", "a"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := env.WriteFile("sub/b.txt", "b"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ents1, err := env.ListDirectory("", 1)
	if err != nil {
		t.Fatalf("ListDirectory depth=1: %v", err)
	}
	seen := map[string]bool{}
	for _, e := range ents1 {
		seen[e.Name] = true
	}
	if !seen["a.txt"] || !seen["sub"] {
		t.Fatalf("depth=1 entries: %+v", ents1)
	}
	if seen["sub/b.txt"] {
		t.Fatalf("did not expect nested entries at depth=1: %+v", ents1)
	}

	ents2, err := env.ListDirectory("", 2)
	if err != nil {
		t.Fatalf("ListDirectory depth=2: %v", err)
	}
	seen = map[string]bool{}
	for _, e := range ents2 {
		seen[e.Name] = true
	}
	if !seen["sub/b.txt"] {
		t.Fatalf("expected nested entry at depth=2: %+v", ents2)
	}
}

func TestLocalExecutionEnvironment_ExecCommand_MergesBaseEnvAndCallEnv(t *testing.T) {
	base := map[string]string{
		"KILROY_STAGE_STATUS_PATH":          "/tmp/base/status.json",
		"KILROY_STAGE_STATUS_FALLBACK_PATH": "/tmp/base/.ai/status.json",
		"BASE_ONLY":                         "base",
	}
	env := NewLocalExecutionEnvironmentWithBaseEnv(t.TempDir(), base)
	base["BASE_ONLY"] = "mutated"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := env.ExecCommand(
		ctx,
		"printf '%s|%s|%s' \"$KILROY_STAGE_STATUS_PATH\" \"$KILROY_STAGE_STATUS_FALLBACK_PATH\" \"$BASE_ONLY\"",
		5_000,
		"",
		map[string]string{"BASE_ONLY": "override"},
	)
	if err != nil {
		t.Fatalf("ExecCommand: %v (res=%+v)", err, res)
	}
	if got, want := strings.TrimSpace(res.Stdout), "/tmp/base/status.json|/tmp/base/.ai/status.json|override"; got != want {
		t.Fatalf("stdout: got %q want %q", got, want)
	}
}

func TestLocalExecutionEnvironment_ExecCommand_StripsConfiguredEnvKeys(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	env := NewLocalExecutionEnvironmentWithPolicy(
		t.TempDir(),
		map[string]string{"BASE_ONLY": "base"},
		[]string{"CLAUDECODE"},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := env.ExecCommand(ctx, "printf '%s' \"${CLAUDECODE:-}\"", 5_000, "", nil)
	if err != nil {
		t.Fatalf("ExecCommand: %v (res=%+v)", err, res)
	}
	if got := strings.TrimSpace(res.Stdout); got != "" {
		t.Fatalf("CLAUDECODE leaked into child process: %q", got)
	}
}
