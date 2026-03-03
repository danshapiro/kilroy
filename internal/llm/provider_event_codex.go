package llm

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ProviderToolLifecycle captures provider-native tool lifecycle events that can
// be surfaced through generic progress telemetry.
type ProviderToolLifecycle struct {
	ToolName      string
	CallID        string
	ArgumentsJSON string
	FullOutput    string
	Completed     bool
	IsError       bool
}

// ProviderToolOutputDelta captures provider-native streamed tool output chunks.
type ProviderToolOutputDelta struct {
	ToolName string
	CallID   string
	Delta    string
}

// ParseCodexAppServerToolLifecycle maps codex-app-server item lifecycle
// provider events into a normalized tool lifecycle shape.
func ParseCodexAppServerToolLifecycle(ev StreamEvent) (ProviderToolLifecycle, bool) {
	if ev.Type != StreamEventProviderEvent {
		return ProviderToolLifecycle{}, false
	}
	method := strings.TrimSpace(ev.EventType)
	if method != "item/started" && method != "item/completed" {
		return ProviderToolLifecycle{}, false
	}
	item := asMapAny(ev.Raw["item"])
	if item == nil {
		return ProviderToolLifecycle{}, false
	}
	itemType := strings.TrimSpace(asStringAny(item["type"]))
	if !isCodexToolItemType(itemType) {
		return ProviderToolLifecycle{}, false
	}
	callID := firstNonEmptyString(
		strings.TrimSpace(asStringAny(item["id"])),
		firstNonEmptyString(
			strings.TrimSpace(asStringAny(item["itemId"])),
			strings.TrimSpace(asStringAny(item["item_id"])),
		),
	)
	if callID == "" {
		return ProviderToolLifecycle{}, false
	}

	lifecycle := ProviderToolLifecycle{
		ToolName:  codexToolName(itemType, item),
		CallID:    callID,
		Completed: method == "item/completed",
	}
	if lifecycle.ToolName == "" {
		lifecycle.ToolName = itemType
	}
	if args := codexToolStartArgs(itemType, item); len(args) > 0 {
		if b, err := json.Marshal(args); err == nil {
			lifecycle.ArgumentsJSON = string(b)
		}
	}
	if strings.TrimSpace(lifecycle.ArgumentsJSON) == "" {
		lifecycle.ArgumentsJSON = "{}"
	}
	if lifecycle.Completed {
		lifecycle.IsError = codexItemIsError(item)
		lifecycle.FullOutput = codexToolCompletedOutput(item)
	}
	return lifecycle, true
}

// ParseCodexAppServerToolOutputDelta maps codex-app-server output/progress
// notifications into normalized tool output deltas.
func ParseCodexAppServerToolOutputDelta(ev StreamEvent) (ProviderToolOutputDelta, bool) {
	if ev.Type != StreamEventProviderEvent {
		return ProviderToolOutputDelta{}, false
	}
	method := strings.TrimSpace(ev.EventType)
	itemID := strings.TrimSpace(asStringAny(ev.Raw["itemId"]))
	if itemID == "" {
		itemID = strings.TrimSpace(asStringAny(ev.Raw["item_id"]))
	}
	switch method {
	case "item/commandExecution/outputDelta":
		delta := asStringAny(ev.Raw["delta"])
		if itemID == "" || delta == "" {
			return ProviderToolOutputDelta{}, false
		}
		return ProviderToolOutputDelta{
			ToolName: "exec_command",
			CallID:   itemID,
			Delta:    delta,
		}, true
	case "item/fileChange/outputDelta":
		delta := asStringAny(ev.Raw["delta"])
		if itemID == "" || delta == "" {
			return ProviderToolOutputDelta{}, false
		}
		return ProviderToolOutputDelta{
			ToolName: "apply_patch",
			CallID:   itemID,
			Delta:    delta,
		}, true
	case "item/mcpToolCall/progress":
		msg := asStringAny(ev.Raw["message"])
		if itemID == "" || msg == "" {
			return ProviderToolOutputDelta{}, false
		}
		toolName := firstNonEmptyString(
			strings.TrimSpace(asStringAny(ev.Raw["tool"])),
			"mcp_tool_call",
		)
		return ProviderToolOutputDelta{
			ToolName: toolName,
			CallID:   itemID,
			Delta:    msg,
		}, true
	default:
		return ProviderToolOutputDelta{}, false
	}
}

func isCodexToolItemType(itemType string) bool {
	switch itemType {
	case "commandExecution", "fileChange", "mcpToolCall", "collabToolCall", "collabAgentToolCall", "webSearch", "imageView":
		return true
	default:
		return false
	}
}

func codexToolName(itemType string, item map[string]any) string {
	switch itemType {
	case "commandExecution":
		return "exec_command"
	case "fileChange":
		return "apply_patch"
	case "mcpToolCall":
		return firstNonEmptyString(
			strings.TrimSpace(asStringAny(item["tool"])),
			"mcp_tool_call",
		)
	case "collabToolCall", "collabAgentToolCall":
		return firstNonEmptyString(
			strings.TrimSpace(asStringAny(item["tool"])),
			"collab_tool_call",
		)
	case "webSearch":
		return "web_search"
	case "imageView":
		return "view_image"
	default:
		return ""
	}
}

func codexToolStartArgs(itemType string, item map[string]any) map[string]any {
	out := map[string]any{}
	switch itemType {
	case "commandExecution":
		if cmd := strings.TrimSpace(asStringAny(item["command"])); cmd != "" {
			out["command"] = cmd
		}
		if cwd := strings.TrimSpace(asStringAny(item["cwd"])); cwd != "" {
			out["cwd"] = cwd
		}
	case "fileChange":
		if changes, ok := item["changes"].([]any); ok {
			out["change_count"] = len(changes)
		}
	case "mcpToolCall":
		if server := strings.TrimSpace(asStringAny(item["server"])); server != "" {
			out["server"] = server
		}
		if tool := strings.TrimSpace(asStringAny(item["tool"])); tool != "" {
			out["tool"] = tool
		}
		if args, ok := item["arguments"]; ok && args != nil {
			out["arguments"] = args
		}
	case "collabToolCall", "collabAgentToolCall":
		if tool := strings.TrimSpace(asStringAny(item["tool"])); tool != "" {
			out["tool"] = tool
		}
		if sender := strings.TrimSpace(asStringAny(item["senderThreadId"])); sender != "" {
			out["sender_thread_id"] = sender
		}
		if receivers := asStringSlice(item["receiverThreadIds"]); len(receivers) > 0 {
			out["receiver_thread_ids"] = receivers
		} else if receiver := strings.TrimSpace(asStringAny(item["receiverThreadId"])); receiver != "" {
			out["receiver_thread_ids"] = []string{receiver}
		}
	case "webSearch":
		if query := strings.TrimSpace(asStringAny(item["query"])); query != "" {
			out["query"] = query
		}
	case "imageView":
		if path := strings.TrimSpace(asStringAny(item["path"])); path != "" {
			out["path"] = path
		}
	}
	return out
}

func codexItemIsError(item map[string]any) bool {
	status := strings.ToLower(strings.TrimSpace(asStringAny(item["status"])))
	switch status {
	case "failed", "declined", "denied", "error", "canceled", "cancelled":
		return true
	}
	if errVal, ok := item["error"]; ok && !isZeroValue(errVal) {
		return true
	}
	return false
}

func codexToolCompletedOutput(item map[string]any) string {
	// Preserve provider-native aggregated output bytes when available.
	if raw, ok := codexRawNonEmpty(item["aggregatedOutput"]); ok {
		return raw
	}
	if raw, ok := codexRawNonEmpty(item["aggregated_output"]); ok {
		return raw
	}

	parts := make([]string, 0, 4)
	appendUnique := func(text string) {
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			return
		}
		for _, existing := range parts {
			if existing == trimmed {
				return
			}
		}
		parts = append(parts, trimmed)
	}

	appendUnique(codexValueAsText(item["stdout"]))
	appendUnique(codexValueAsText(item["stderr"]))

	// Prefer structured output fields when explicit stdio is absent.
	if len(parts) == 0 {
		appendUnique(codexValueAsText(item["output"]))
		appendUnique(codexValueAsText(item["result"]))
		appendUnique(codexValueAsText(item["response"]))
		appendUnique(codexValueAsText(item["value"]))
	}

	// Preserve deterministic failure details for debugging.
	appendUnique(codexValueAsText(item["error"]))
	if len(parts) == 0 {
		appendUnique(codexValueAsText(item["message"]))
	}

	return strings.Join(parts, "\n\n")
}

func codexRawNonEmpty(v any) (string, bool) {
	switch typed := v.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return "", false
		}
		return typed, true
	case []byte:
		s := string(typed)
		if strings.TrimSpace(s) == "" {
			return "", false
		}
		return s, true
	default:
		return "", false
	}
}

func codexValueAsText(v any) string {
	switch typed := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case []byte:
		return strings.TrimSpace(string(typed))
	}

	b, err := json.Marshal(v)
	if err != nil {
		return strings.TrimSpace(fmt.Sprint(v))
	}
	trimmed := strings.TrimSpace(string(b))
	switch trimmed {
	case "", "null":
		return ""
	}
	return trimmed
}

func asMapAny(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asStringAny(v any) string {
	s, _ := v.(string)
	return s
}

func isZeroValue(v any) bool {
	switch typed := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(typed) == ""
	case map[string]any:
		return len(typed) == 0
	default:
		return false
	}
}

func asStringSlice(v any) []string {
	seq, ok := v.([]any)
	if !ok || len(seq) == 0 {
		return nil
	}
	out := make([]string, 0, len(seq))
	for _, item := range seq {
		s := strings.TrimSpace(asStringAny(item))
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func firstNonEmptyString(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
