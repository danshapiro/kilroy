package llm

import (
	"encoding/json"
	"strings"
)

// ProviderToolLifecycle captures provider-native tool lifecycle events that can
// be surfaced through generic progress telemetry.
type ProviderToolLifecycle struct {
	ToolName      string
	CallID        string
	ArgumentsJSON string
	Completed     bool
	IsError       bool
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
	callID := strings.TrimSpace(asStringAny(item["id"]))
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
	}
	return lifecycle, true
}

func isCodexToolItemType(itemType string) bool {
	switch itemType {
	case "commandExecution", "fileChange", "mcpToolCall", "collabToolCall", "webSearch", "imageView":
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
	case "collabToolCall":
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
	case "collabToolCall":
		if tool := strings.TrimSpace(asStringAny(item["tool"])); tool != "" {
			out["tool"] = tool
		}
		if sender := strings.TrimSpace(asStringAny(item["senderThreadId"])); sender != "" {
			out["sender_thread_id"] = sender
		}
		if receiver := strings.TrimSpace(asStringAny(item["receiverThreadId"])); receiver != "" {
			out["receiver_thread_id"] = receiver
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
	case "failed", "declined", "error":
		return true
	}
	if errVal, ok := item["error"]; ok && !isZeroValue(errVal) {
		return true
	}
	return false
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

func firstNonEmptyString(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
