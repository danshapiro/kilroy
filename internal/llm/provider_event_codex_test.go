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

func TestParseCodexAppServerToolLifecycle_CompletedCanceledIsError(t *testing.T) {
	ev := StreamEvent{
		Type:      StreamEventProviderEvent,
		EventType: "item/completed",
		Raw: map[string]any{
			"item": map[string]any{
				"id":     "cmd_cancel",
				"type":   "commandExecution",
				"status": "cancelled",
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
		t.Fatalf("expected cancelled completion to be marked is_error")
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

func TestParseCodexAppServerToolLifecycle_CompletedIncludesCommandOutput(t *testing.T) {
	ev := StreamEvent{
		Type:      StreamEventProviderEvent,
		EventType: "item/completed",
		Raw: map[string]any{
			"item": map[string]any{
				"id":               "cmd_2",
				"type":             "commandExecution",
				"status":           "completed",
				"aggregatedOutput": "alpha\nbeta\n",
				"stderr":           "warning",
				"message":          "done",
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
	if lifecycle.IsError {
		t.Fatalf("unexpected error classification for successful command")
	}
	if lifecycle.FullOutput == "" {
		t.Fatalf("expected full output from completed command event")
	}
	if lifecycle.FullOutput != "alpha\nbeta\n" {
		t.Fatalf("full output mismatch: got %q", lifecycle.FullOutput)
	}
}

func TestParseCodexAppServerToolLifecycle_CompletedUsesSnakeCaseAggregatedOutput(t *testing.T) {
	ev := StreamEvent{
		Type:      StreamEventProviderEvent,
		EventType: "item/completed",
		Raw: map[string]any{
			"item": map[string]any{
				"id":                "cmd_2b",
				"type":              "commandExecution",
				"status":            "completed",
				"aggregated_output": "omega\n",
			},
		},
	}

	lifecycle, ok := ParseCodexAppServerToolLifecycle(ev)
	if !ok {
		t.Fatalf("expected lifecycle match")
	}
	if lifecycle.FullOutput != "omega\n" {
		t.Fatalf("full output mismatch: got %q", lifecycle.FullOutput)
	}
}

func TestParseCodexAppServerToolLifecycle_CompletedIncludesStructuredError(t *testing.T) {
	ev := StreamEvent{
		Type:      StreamEventProviderEvent,
		EventType: "item/completed",
		Raw: map[string]any{
			"item": map[string]any{
				"id":     "mcp_2",
				"type":   "mcpToolCall",
				"tool":   "search",
				"status": "failed",
				"error": map[string]any{
					"message": "timeout",
					"code":    "ETIMEDOUT",
				},
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
		t.Fatalf("expected failed status to mark is_error")
	}
	if lifecycle.FullOutput == "" {
		t.Fatalf("expected structured error payload to be preserved in full output")
	}
	if lifecycle.FullOutput != `{"code":"ETIMEDOUT","message":"timeout"}` {
		t.Fatalf("structured error output mismatch: got %q", lifecycle.FullOutput)
	}
}

func TestParseCodexAppServerToolOutputDelta_CommandExecution(t *testing.T) {
	ev := StreamEvent{
		Type:      StreamEventProviderEvent,
		EventType: "item/commandExecution/outputDelta",
		Raw: map[string]any{
			"itemId": "cmd_3",
			"delta":  "line 1\n",
		},
	}
	delta, ok := ParseCodexAppServerToolOutputDelta(ev)
	if !ok {
		t.Fatalf("expected command output delta parse")
	}
	if delta.ToolName != "exec_command" {
		t.Fatalf("tool name: got %q", delta.ToolName)
	}
	if delta.CallID != "cmd_3" {
		t.Fatalf("call id: got %q", delta.CallID)
	}
	if delta.Delta != "line 1\n" {
		t.Fatalf("delta mismatch: got %q", delta.Delta)
	}
}

func TestParseCodexAppServerToolOutputDelta_CommandExecutionSnakeCaseItemID(t *testing.T) {
	ev := StreamEvent{
		Type:      StreamEventProviderEvent,
		EventType: "item/commandExecution/outputDelta",
		Raw: map[string]any{
			"item_id": "cmd_3b",
			"delta":   "line 2\n",
		},
	}
	delta, ok := ParseCodexAppServerToolOutputDelta(ev)
	if !ok {
		t.Fatalf("expected command output delta parse")
	}
	if delta.CallID != "cmd_3b" {
		t.Fatalf("call id: got %q", delta.CallID)
	}
	if delta.Delta != "line 2\n" {
		t.Fatalf("delta mismatch: got %q", delta.Delta)
	}
}

func TestParseCodexAppServerToolOutputDelta_FileChange(t *testing.T) {
	ev := StreamEvent{
		Type:      StreamEventProviderEvent,
		EventType: "item/fileChange/outputDelta",
		Raw: map[string]any{
			"itemId": "patch_7",
			"delta":  "updated README.md",
		},
	}
	delta, ok := ParseCodexAppServerToolOutputDelta(ev)
	if !ok {
		t.Fatalf("expected file change output delta parse")
	}
	if delta.ToolName != "apply_patch" {
		t.Fatalf("tool name: got %q", delta.ToolName)
	}
	if delta.CallID != "patch_7" {
		t.Fatalf("call id: got %q", delta.CallID)
	}
	if delta.Delta != "updated README.md" {
		t.Fatalf("delta mismatch: got %q", delta.Delta)
	}
}
