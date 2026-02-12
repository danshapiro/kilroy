// Emits CXDB turns from parsed CLI stream-json events, decomposing
// opaque conversation blobs into individual queryable turns.
package engine

import (
	"context"
	"fmt"
	"strings"
)

// emitCXDBCLIStreamEvent converts a parsed CLI stream event into CXDB turns.
// callMap tracks tool_use call IDs to tool names so that subsequent tool_result
// turns can include the tool name (which only appears in the assistant message).
func emitCXDBCLIStreamEvent(ctx context.Context, eng *Engine, nodeID string, ev *cliStreamEvent, callMap map[string]string) {
	if eng == nil || eng.CXDB == nil || ev == nil {
		return
	}
	runID := eng.Options.RunID

	switch ev.Type {
	case "assistant":
		if ev.Message == nil {
			return
		}
		text := extractAssistantText(ev.Message)
		calls := extractToolCalls(ev.Message)

		// For tool-only messages (no text), synthesize a descriptive label.
		if strings.TrimSpace(text) == "" && len(calls) > 0 {
			names := make([]string, len(calls))
			for i, c := range calls {
				names[i] = c.Name
			}
			text = "[tool_use: " + strings.Join(names, ", ") + "]"
		}

		var inputTokens, outputTokens uint64
		if ev.Message.Usage != nil {
			inputTokens = uint64(ev.Message.Usage.InputTokens)
			outputTokens = uint64(ev.Message.Usage.OutputTokens)
		}

		if _, _, err := eng.CXDB.Append(ctx, "com.kilroy.attractor.AssistantMessage", 1, map[string]any{
			"run_id":         runID,
			"node_id":        nodeID,
			"text":           truncate(text, 8_000),
			"model":          ev.Message.Model,
			"input_tokens":   inputTokens,
			"output_tokens":  outputTokens,
			"tool_use_count": uint32(len(calls)),
			"timestamp_ms":   nowMS(),
		}); err != nil {
			eng.Warn(fmt.Sprintf("cxdb append AssistantMessage failed (node=%s): %v", nodeID, err))
		}

		// Emit a ToolCall turn for each tool_use block.
		for _, call := range calls {
			if callMap != nil {
				callMap[call.ID] = call.Name
			}
			if _, _, err := eng.CXDB.Append(ctx, "com.kilroy.attractor.ToolCall", 1, map[string]any{
				"run_id":         runID,
				"node_id":        nodeID,
				"tool_name":      call.Name,
				"call_id":        call.ID,
				"arguments_json": truncate(call.InputJSON, 8_000),
			}); err != nil {
				eng.Warn(fmt.Sprintf("cxdb append ToolCall failed (node=%s tool=%s call_id=%s): %v", nodeID, call.Name, call.ID, err))
			}
		}

	case "user":
		if ev.Message == nil {
			return
		}
		results := extractToolResults(ev.Message)
		for _, result := range results {
			toolName := ""
			if callMap != nil {
				toolName = callMap[result.ToolUseID]
			}
			if _, _, err := eng.CXDB.Append(ctx, "com.kilroy.attractor.ToolResult", 1, map[string]any{
				"run_id":    runID,
				"node_id":   nodeID,
				"tool_name": toolName,
				"call_id":   result.ToolUseID,
				"output":    truncate(result.Content, 8_000),
				"is_error":  result.IsError,
			}); err != nil {
				eng.Warn(fmt.Sprintf("cxdb append ToolResult failed (node=%s call_id=%s): %v", nodeID, result.ToolUseID, err))
			}
		}

	default:
		// system, result, etc. — skip silently
	}
}
