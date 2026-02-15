package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/danshapiro/kilroy/internal/attractor/model"
)

func TestResolveToolHook_NodeOverridesGraph(t *testing.T) {
	node := &model.Node{
		ID:    "n1",
		Attrs: map[string]string{"tool_hooks.pre": "echo node"},
	}
	graph := &model.Graph{
		Attrs: map[string]string{"tool_hooks.pre": "echo graph"},
	}
	got := resolveToolHook(node, graph, "tool_hooks.pre")
	if got != "echo node" {
		t.Fatalf("expected node attr, got %q", got)
	}
}

func TestResolveToolHook_FallsBackToGraph(t *testing.T) {
	node := &model.Node{
		ID:    "n1",
		Attrs: map[string]string{},
	}
	graph := &model.Graph{
		Attrs: map[string]string{"tool_hooks.pre": "echo graph"},
	}
	got := resolveToolHook(node, graph, "tool_hooks.pre")
	if got != "echo graph" {
		t.Fatalf("expected graph attr, got %q", got)
	}
}

func TestResolveToolHook_Empty(t *testing.T) {
	node := &model.Node{ID: "n1", Attrs: map[string]string{}}
	graph := &model.Graph{Attrs: map[string]string{}}
	got := resolveToolHook(node, graph, "tool_hooks.pre")
	if got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestResolveToolHook_NilNodeAndGraph(t *testing.T) {
	got := resolveToolHook(nil, nil, "tool_hooks.pre")
	if got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestRunToolHook_ExitZero_Proceeds(t *testing.T) {
	stageDir := t.TempDir()
	exitCode, err := runToolHook(
		context.Background(),
		"exit 0",
		"",
		os.Environ(),
		"{}",
		stageDir,
		"pre",
		"call-1",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit 0, got %d", exitCode)
	}
	// Verify log file was written.
	logPath := filepath.Join(stageDir, "tool_hook_pre_call-1.json")
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("expected log file at %s: %v", logPath, err)
	}
}

func TestRunToolHook_ExitNonZero_ReportsCode(t *testing.T) {
	exitCode, err := runToolHook(
		context.Background(),
		"exit 42",
		"",
		os.Environ(),
		"{}",
		t.TempDir(),
		"pre",
		"call-2",
	)
	if err == nil {
		t.Fatalf("expected error for non-zero exit")
	}
	if exitCode != 42 {
		t.Fatalf("expected exit 42, got %d", exitCode)
	}
}

func TestRunToolHook_EmptyCommand_Noop(t *testing.T) {
	exitCode, err := runToolHook(
		context.Background(),
		"",
		"",
		os.Environ(),
		"{}",
		t.TempDir(),
		"pre",
		"call-3",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit 0 for no-op, got %d", exitCode)
	}
}

func TestBuildToolHookStdinJSON_PreHook(t *testing.T) {
	got := buildToolHookStdinJSON("bash", "call-1", `{"cmd":"ls"}`, "", false, "pre")
	if got == "" || got == "{}" {
		t.Fatalf("expected non-empty JSON, got %q", got)
	}
	// Pre-hook should NOT contain output or is_error.
	if contains(got, `"output"`) {
		t.Fatalf("pre-hook stdin should not contain output field")
	}
	if !contains(got, `"tool_name"`) {
		t.Fatalf("pre-hook stdin should contain tool_name")
	}
}

func TestBuildToolHookStdinJSON_PostHook(t *testing.T) {
	got := buildToolHookStdinJSON("bash", "call-1", "", "some output", true, "post")
	if got == "" || got == "{}" {
		t.Fatalf("expected non-empty JSON, got %q", got)
	}
	// Post-hook should contain output and is_error.
	if !contains(got, `"output"`) {
		t.Fatalf("post-hook stdin should contain output field")
	}
	if !contains(got, `"is_error"`) {
		t.Fatalf("post-hook stdin should contain is_error field")
	}
}

func TestToolHookEnv_AddsKilroyVars(t *testing.T) {
	env := toolHookEnv([]string{"PATH=/usr/bin"}, "node-1", "bash", "call-1")
	found := map[string]bool{}
	for _, e := range env {
		switch {
		case e == "KILROY_NODE_ID=node-1":
			found["node_id"] = true
		case e == "KILROY_TOOL_NAME=bash":
			found["tool_name"] = true
		case e == "KILROY_CALL_ID=call-1":
			found["call_id"] = true
		}
	}
	if !found["node_id"] || !found["tool_name"] || !found["call_id"] {
		t.Fatalf("missing env vars: %v", found)
	}
}

func TestSanitizeHookCallID(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"abc-123", "abc-123"},
		{"a/b/c", "a_b_c"},
		{"", "unknown"},
		{"   ", "unknown"},
		{"ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-extra", "ABCDEFGHIJKLMNOPQRSTUVWXYZ012345"},
	}
	for _, tc := range tests {
		got := sanitizeHookCallID(tc.in)
		if got != tc.want {
			t.Errorf("sanitizeHookCallID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRunToolHook_ReadsStdin(t *testing.T) {
	stageDir := t.TempDir()
	// The hook echoes stdin to a file so we can verify it was passed.
	outPath := filepath.Join(stageDir, "stdin_capture.txt")
	hookCmd := "cat > " + outPath
	stdinJSON := `{"tool_name":"test","hook_type":"pre"}`
	exitCode, err := runToolHook(
		context.Background(),
		hookCmd,
		"",
		os.Environ(),
		stdinJSON,
		stageDir,
		"pre",
		"call-stdin",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit 0, got %d", exitCode)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read captured stdin: %v", err)
	}
	if string(got) != stdinJSON {
		t.Fatalf("stdin mismatch: got %q, want %q", string(got), stdinJSON)
	}
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && len(s) >= len(substr) && indexOf(s, substr) >= 0
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
