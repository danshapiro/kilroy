// Smoke tests that spawn real CLI tools via tmux.
// Skipped in CI (require API keys and installed CLI tools).
// Run manually with: go test -run TestSmoke -v -timeout 120s
package tmux

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func hasBinary(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func TestSmoke_Claude_PrintMode(t *testing.T) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}
	if !hasBinary("claude") {
		t.Skip("claude not found")
	}

	mgr := testManager()
	name := "smoke-claude"
	defer mgr.DestroySession(name)

	s, err := mgr.CreateSession(name, "/tmp",
		"claude --dangerously-skip-permissions --print 'Say exactly: KILROY_SMOKE_OK'",
		map[string]string{"ANTHROPIC_API_KEY": os.Getenv("ANTHROPIC_API_KEY")})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	ctx := context.Background()
	err = mgr.WaitForExit(ctx, s.Name, 60*time.Second)
	if err != nil {
		t.Fatalf("WaitForExit: %v", err)
	}

	output, _ := mgr.CaptureOutput(s.Name, 0)
	t.Logf("Claude output (%d chars):\n%s", len(output), truncateForLog(output, 500))
	if !strings.Contains(output, "KILROY_SMOKE_OK") {
		t.Logf("WARNING: output doesn't contain KILROY_SMOKE_OK (may be paraphrased)")
	}
}

func TestSmoke_Codex_PrintMode(t *testing.T) {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set")
	}
	if !hasBinary("codex") {
		t.Skip("codex not found")
	}

	mgr := testManager()
	name := "smoke-codex"
	defer mgr.DestroySession(name)

	s, err := mgr.CreateSession(name, "/tmp",
		"codex --full-auto 'Say exactly: KILROY_SMOKE_OK'",
		map[string]string{"OPENAI_API_KEY": os.Getenv("OPENAI_API_KEY")})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	ctx := context.Background()
	err = mgr.WaitForExit(ctx, s.Name, 60*time.Second)
	if err != nil {
		t.Fatalf("WaitForExit: %v", err)
	}

	output, _ := mgr.CaptureOutput(s.Name, 0)
	t.Logf("Codex output (%d chars):\n%s", len(output), truncateForLog(output, 500))
}

func truncateForLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
