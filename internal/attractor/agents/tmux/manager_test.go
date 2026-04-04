// Integration tests against real tmux on an isolated socket.
package tmux

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const testSocket = "kilroy-test"

func TestMain(m *testing.M) {
	// Kill any stale test server.
	_ = exec.Command("tmux", "-u", "-L", testSocket, "kill-server").Run()
	code := m.Run()
	// Clean up test server.
	_ = exec.Command("tmux", "-u", "-L", testSocket, "kill-server").Run()
	os.Exit(code)
}

func testManager() *Manager {
	return NewManager(testSocket)
}

func TestCreateAndDestroy(t *testing.T) {
	mgr := testManager()
	s, err := mgr.CreateSession("test-create", "/tmp", "sleep 30", nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if s.Name != "test-create" {
		t.Fatalf("Name = %q, want test-create", s.Name)
	}
	if !mgr.HasSession("test-create") {
		t.Fatal("session should exist after creation")
	}
	if err := mgr.DestroySession("test-create"); err != nil {
		t.Fatalf("DestroySession: %v", err)
	}
	if mgr.HasSession("test-create") {
		t.Fatal("session should not exist after destruction")
	}
}

func TestCreateSession_InvalidName(t *testing.T) {
	mgr := testManager()
	_, err := mgr.CreateSession("bad name with spaces", "/tmp", "echo hi", nil)
	if err == nil {
		t.Fatal("expected error for invalid session name")
	}
}

func TestCreateSession_WithEnv(t *testing.T) {
	mgr := testManager()
	env := map[string]string{"KILROY_TEST_VAR": "hello_from_test"}
	s, err := mgr.CreateSession("test-env", "/tmp", "bash -c 'echo $KILROY_TEST_VAR && sleep 5'", env)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	defer mgr.DestroySession(s.Name)

	// Wait for echo to execute.
	time.Sleep(500 * time.Millisecond)
	output, err := mgr.CaptureOutput(s.Name, 5)
	if err != nil {
		t.Fatalf("CaptureOutput: %v", err)
	}
	if !strings.Contains(output, "hello_from_test") {
		t.Fatalf("output = %q, want to contain hello_from_test", output)
	}
}

func TestSendInputAndCapture(t *testing.T) {
	mgr := testManager()
	s, err := mgr.CreateSession("test-io", "/tmp", "bash", nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	defer mgr.DestroySession(s.Name)

	// Wait for bash to start.
	time.Sleep(500 * time.Millisecond)

	if err := mgr.SendInput(s.Name, "echo KILROY_OUTPUT_TEST"); err != nil {
		t.Fatalf("SendInput: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	output, err := mgr.CaptureOutput(s.Name, 10)
	if err != nil {
		t.Fatalf("CaptureOutput: %v", err)
	}
	if !strings.Contains(output, "KILROY_OUTPUT_TEST") {
		t.Fatalf("output = %q, want to contain KILROY_OUTPUT_TEST", output)
	}
}

func TestWaitForExit(t *testing.T) {
	mgr := testManager()
	// Use sleep to avoid the immediate-exit health check failure.
	s, err := mgr.CreateSession("test-exit", "/tmp", "sleep 0.5 && echo done", nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	defer mgr.DestroySession(s.Name)

	ctx := context.Background()
	err = mgr.WaitForExit(ctx, s.Name, 5*time.Second)
	if err != nil {
		t.Fatalf("WaitForExit: %v", err)
	}
}

func TestHealthCheck(t *testing.T) {
	mgr := testManager()

	// Non-existent session.
	if h := mgr.CheckHealth("nonexistent"); h != Dead {
		t.Fatalf("health = %d, want Dead", h)
	}

	// Running session.
	s, _ := mgr.CreateSession("test-health", "/tmp", "sleep 30", nil)
	defer mgr.DestroySession(s.Name)
	if h := mgr.CheckHealth(s.Name); h != Healthy {
		t.Fatalf("health = %d, want Healthy", h)
	}
}

func TestListSessions(t *testing.T) {
	mgr := testManager()
	s, _ := mgr.CreateSession("test-list", "/tmp", "sleep 30", nil)
	defer mgr.DestroySession(s.Name)

	sessions, err := mgr.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	found := false
	for _, name := range sessions {
		if name == "test-list" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("session test-list not found in %v", sessions)
	}
}

func TestSetAndGetEnvironment(t *testing.T) {
	mgr := testManager()
	s, _ := mgr.CreateSession("test-setenv", "/tmp", "sleep 30", nil)
	defer mgr.DestroySession(s.Name)

	if err := mgr.SetEnvironment(s.Name, "KILROY_TEST_KEY", "test_value"); err != nil {
		t.Fatalf("SetEnvironment: %v", err)
	}
	val, err := mgr.GetEnvironment(s.Name, "KILROY_TEST_KEY")
	if err != nil {
		t.Fatalf("GetEnvironment: %v", err)
	}
	if val != "test_value" {
		t.Fatalf("value = %q, want test_value", val)
	}
}

func TestSanitizeInput(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello world", "hello world"},
		{"hello\tworld", "hello world"},  // tab → space
		{"hello\x1bworld", "helloworld"}, // ESC stripped
		{"hello\rworld", "helloworld"},   // CR stripped
		{"hello\bworld", "helloworld"},   // BS stripped
		{"hello\nworld", "hello\nworld"}, // newlines preserved
	}
	for _, tt := range tests {
		got := sanitizeInput(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeInput(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestChunkString(t *testing.T) {
	s := strings.Repeat("a", 1500)
	chunks := chunkString(s, 512)
	if len(chunks) != 3 {
		t.Fatalf("len(chunks) = %d, want 3", len(chunks))
	}
	total := 0
	for _, c := range chunks {
		total += len(c)
	}
	if total != 1500 {
		t.Fatalf("total = %d, want 1500", total)
	}
}

func TestDestroySession_NonExistent(t *testing.T) {
	mgr := testManager()
	// Should not error.
	if err := mgr.DestroySession("nonexistent"); err != nil {
		t.Fatalf("DestroySession: %v", err)
	}
}
