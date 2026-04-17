// Tests for the Claude JSONL conversation log parser.
package agentlog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseClaudeLog(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "conversation.jsonl")

	// Simulate a Claude conversation JSONL.
	lines := `{"type":"user","message":{"content":[{"type":"text","text":"Fix the build"}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"I'll fix the build error."},{"type":"tool_use","id":"toolu_01","name":"Read","input":{"file_path":"main.go"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_01","content":"package main\nfunc main() {}"}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_02","name":"Edit","input":{"file_path":"main.go","old_string":"func main() {}","new_string":"func main() {\n\tfmt.Println(\"hello\")\n}"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_02","content":"File edited successfully"}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"Done! The build is fixed."}]}}
`
	os.WriteFile(logPath, []byte(lines), 0o644)

	events, err := ParseClaudeLog(logPath)
	if err != nil {
		t.Fatal(err)
	}

	// Expected: text, tool_call(Read), tool_result, tool_call(Edit), tool_result, text
	if len(events) != 6 {
		t.Fatalf("expected 6 events, got %d", len(events))
	}

	if events[0].Type != "text" {
		t.Errorf("event 0: expected text, got %s", events[0].Type)
	}
	if events[1].Type != "tool_call" || events[1].Tool != "Read" {
		t.Errorf("event 1: expected tool_call/Read, got %s/%s", events[1].Type, events[1].Tool)
	}
	if events[1].Message != "Read(main.go)" {
		t.Errorf("event 1: unexpected message %q", events[1].Message)
	}
	if events[2].Type != "tool_result" {
		t.Errorf("event 2: expected tool_result, got %s", events[2].Type)
	}
	if events[3].Type != "tool_call" || events[3].Tool != "Edit" {
		t.Errorf("event 3: expected tool_call/Edit, got %s/%s", events[3].Type, events[3].Tool)
	}
	if events[5].Type != "text" {
		t.Errorf("event 5: expected text, got %s", events[5].Type)
	}
}

func TestParseClaudeLog_Empty(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "empty.jsonl")
	os.WriteFile(logPath, []byte(""), 0o644)

	events, err := ParseClaudeLog(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events from empty log, got %d", len(events))
	}
}

func TestFormatToolCall(t *testing.T) {
	tests := []struct {
		name   string
		input  map[string]any
		expect string
	}{
		{"Read", map[string]any{"file_path": "/tmp/test.go"}, "Read(/tmp/test.go)"},
		{"Write", map[string]any{"file_path": "/tmp/out.go"}, "Write(/tmp/out.go)"},
		{"Bash", map[string]any{"command": "go build ./..."}, "Bash(go build ./...)"},
		{"Grep", map[string]any{"pattern": "func main"}, "Grep(func main)"},
		{"Unknown", map[string]any{"foo": "bar"}, `Unknown({"foo":"bar"})`},
	}
	for _, tt := range tests {
		got := formatToolCall(tt.name, tt.input)
		if got != tt.expect {
			t.Errorf("formatToolCall(%s): got %q, want %q", tt.name, got, tt.expect)
		}
	}
}
