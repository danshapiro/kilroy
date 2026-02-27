package llm

import "testing"

func TestParseCodexAppServerToolLifecycle_CommandExecutionStarted(t *testing.T) {
	ev := StreamEvent{
		Type:      StreamEventProviderEvent,
		EventType: "item/started",
		Raw: map[string]any{
			"item": map[string]any{
				"id":      "cmd_1",
				"type":    "commandExecution",
				"command": "pwd",
				"cwd":     "/tmp/worktree",
				"status":  "inProgress",
			},
		},
	}

	lifecycle, ok := ParseCodexAppServerToolLifecycle(ev)
	if !ok {
		t.Fatalf("expected lifecycle match")
	}
	if lifecycle.Completed {
		t.Fatalf("expected start event, got completed")
	}
	if lifecycle.CallID != "cmd_1" {
		t.Fatalf("call id: got %q want %q", lifecycle.CallID, "cmd_1")
	}
	if lifecycle.ToolName != "exec_command" {
		t.Fatalf("tool name: got %q want %q", lifecycle.ToolName, "exec_command")
	}
	if lifecycle.ArgumentsJSON == "" {
		t.Fatalf("expected non-empty arguments json")
	}
}

func TestParseCodexAppServerToolLifecycle_CompletedFailedIsError(t *testing.T) {
	ev := StreamEvent{
		Type:      StreamEventProviderEvent,
		EventType: "item/completed",
		Raw: map[string]any{
			"item": map[string]any{
				"id":     "mcp_1",
				"type":   "mcpToolCall",
				"tool":   "search",
				"status": "failed",
				"error":  map[string]any{"message": "upstream timeout"},
			},
		},
	}

	lifecycle, ok := ParseCodexAppServerToolLifecycle(ev)
	if !ok {
		t.Fatalf("expected lifecycle match")
	}
	if !lifecycle.Completed {
		t.Fatalf("expected completed event")
	}
	if !lifecycle.IsError {
		t.Fatalf("expected failed completion to be marked is_error")
	}
	if lifecycle.ToolName != "search" {
		t.Fatalf("tool name: got %q want %q", lifecycle.ToolName, "search")
	}
}

func TestParseCodexAppServerToolLifecycle_StartedSparsePayloadDefaultsArgumentsJSON(t *testing.T) {
	ev := StreamEvent{
		Type:      StreamEventProviderEvent,
		EventType: "item/started",
		Raw: map[string]any{
			"item": map[string]any{
				"id":   "search_1",
				"type": "webSearch",
			},
		},
	}

	lifecycle, ok := ParseCodexAppServerToolLifecycle(ev)
	if !ok {
		t.Fatalf("expected lifecycle match")
	}
	if lifecycle.Completed {
		t.Fatalf("expected start event")
	}
	if lifecycle.ArgumentsJSON != "{}" {
		t.Fatalf("arguments json: got %q want %q", lifecycle.ArgumentsJSON, "{}")
	}
}
