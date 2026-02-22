package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"github.com/danshapiro/kilroy/internal/jsonschemautil"
	"github.com/danshapiro/kilroy/internal/llm"
)

type TruncationStrategy string

const (
	TruncHeadTail TruncationStrategy = "head_tail"
	TruncTail     TruncationStrategy = "tail"
)

type ToolOutputLimit struct {
	MaxChars int
	MaxLines int
	Strategy TruncationStrategy
}

type ToolExecResult struct {
	ToolName string
	CallID   string

	// Output is the truncated output sent back to the model.
	Output string

	// FullOutput is the untruncated output (available via TOOL_CALL_END).
	FullOutput string

	IsError bool
}

type RegisteredTool struct {
	Definition llm.ToolDefinition
	Schema     *jsonschema.Schema
	Exec       func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error)

	Limit ToolOutputLimit
}

type ToolRegistry struct {
	mu                 sync.RWMutex
	tools              map[string]RegisteredTool
	validationFailures map[string]int // consecutive validation failures per tool
}

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools:              map[string]RegisteredTool{},
		validationFailures: map[string]int{},
	}
}

func (r *ToolRegistry) Register(t RegisteredTool) error {
	if err := llm.ValidateToolName(t.Definition.Name); err != nil {
		return err
	}
	if t.Exec == nil {
		return fmt.Errorf("tool %s missing executor", t.Definition.Name)
	}
	if t.Limit.MaxChars == 0 {
		t.Limit = defaultToolLimit(t.Definition.Name)
	}
	if t.Schema == nil {
		s, err := compileSchema(t.Definition.Parameters)
		if err != nil {
			return fmt.Errorf("tool %s schema: %w", t.Definition.Name, err)
		}
		t.Schema = s
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tools == nil {
		r.tools = map[string]RegisteredTool{}
	}
	r.tools[t.Definition.Name] = t
	return nil
}

func (r *ToolRegistry) Definitions() []llm.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]llm.ToolDefinition, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t.Definition)
	}
	return out
}

func (r *ToolRegistry) ExecuteCall(ctx context.Context, env ExecutionEnvironment, call llm.ToolCallData) ToolExecResult {
	name := call.Name
	callID := call.ID
	if strings.TrimSpace(callID) == "" {
		callID = "call_" + shortHash(call.Arguments)
	}

	r.mu.RLock()
	t, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		msg := fmt.Sprintf("unknown tool: %s", name)
		return truncateResult(name, callID, msg, true, defaultToolLimit(name))
	}

	var args map[string]any
	if len(call.Arguments) > 0 {
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			msg := fmt.Sprintf("invalid tool arguments JSON: %v", err)
			msg = r.recordValidationFailure(name, msg, t)
			return truncateResult(name, callID, msg, true, t.Limit)
		}
	}
	if args == nil {
		args = map[string]any{}
	}

	if err := t.Schema.Validate(args); err != nil {
		msg := fmt.Sprintf("tool args schema validation failed: %v", err)
		msg = r.recordValidationFailure(name, msg, t)
		return truncateResult(name, callID, msg, true, t.Limit)
	}

	r.resetValidationFailures(name)

	v, err := t.Exec(ctx, env, args)
	if err != nil {
		full := ""
		if v != nil {
			full = toolValueToString(v)
		}
		if strings.TrimSpace(full) == "" {
			full = fmt.Sprintf("%v", err)
		}
		return truncateResult(name, callID, full, true, t.Limit)
	}

	full := toolValueToString(v)
	return truncateResult(name, callID, full, false, t.Limit)
}

const circuitBreakerThreshold = 3

// recordValidationFailure increments the consecutive validation failure counter
// for the given tool and returns the error message, potentially with an escalation
// suffix if the threshold has been reached.
func (r *ToolRegistry) recordValidationFailure(name, msg string, t RegisteredTool) string {
	r.mu.Lock()
	if r.validationFailures == nil {
		r.validationFailures = map[string]int{}
	}
	r.validationFailures[name]++
	count := r.validationFailures[name]
	r.mu.Unlock()

	if count >= circuitBreakerThreshold {
		fields := requiredFields(t.Definition.Parameters)
		msg += fmt.Sprintf("\nCIRCUIT BREAKER: Tool '%s' has failed validation %d times consecutively. Required fields: %s. Do NOT call this tool again without providing all required fields.", name, count, strings.Join(fields, ", "))
	}
	return msg
}

func (r *ToolRegistry) resetValidationFailures(name string) {
	r.mu.Lock()
	delete(r.validationFailures, name)
	r.mu.Unlock()
}

// requiredFields extracts the "required" array from a tool's parameter schema.
func requiredFields(params map[string]any) []string {
	if params == nil {
		return nil
	}
	req, ok := params["required"]
	if !ok {
		return nil
	}
	switch v := req.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func truncateResult(toolName, callID, full string, isErr bool, lim ToolOutputLimit) ToolExecResult {
	out := full
	out = truncateChars(out, lim.MaxChars, lim.Strategy)
	if lim.MaxLines > 0 {
		out = truncateLines(out, lim.MaxLines)
	}
	return ToolExecResult{
		ToolName:   toolName,
		CallID:     callID,
		Output:     out,
		FullOutput: full,
		IsError:    isErr,
	}
}

func truncateChars(s string, max int, strat TruncationStrategy) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	removed := len(s) - max
	switch strat {
	case TruncTail:
		// Spec: keep the last max_chars characters and prepend a warning.
		marker := fmt.Sprintf("[WARNING: Tool output was truncated. First %d characters were removed. The full output is available in the event stream.]\n\n", removed)
		return marker + s[len(s)-max:]
	default:
		// Spec: head/tail split plus an explicit warning about omitted middle.
		headCount := max / 2
		tailCount := max - headCount
		marker := fmt.Sprintf("\n\n[WARNING: Tool output was truncated. %d characters were removed from the middle. The full output is available in the event stream. If you need to see specific parts, re-run the tool with more targeted parameters.]\n\n", removed)
		return s[:headCount] + marker + s[len(s)-tailCount:]
	}
}

func truncateLines(s string, max int) string {
	if max <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= max {
		return s
	}
	headCount := max / 2
	tailCount := max - headCount
	omitted := len(lines) - headCount - tailCount
	marker := fmt.Sprintf("\n[... %d lines omitted ...]\n", omitted)
	head := strings.Join(lines[:headCount], "\n")
	tail := strings.Join(lines[len(lines)-tailCount:], "\n")
	return head + marker + tail
}

func defaultToolLimit(toolName string) ToolOutputLimit {
	switch toolName {
	case "read_file":
		return ToolOutputLimit{MaxChars: 50_000, Strategy: TruncHeadTail}
	case "shell":
		return ToolOutputLimit{MaxChars: 30_000, MaxLines: 256, Strategy: TruncHeadTail}
	case "grep":
		return ToolOutputLimit{MaxChars: 20_000, MaxLines: 200, Strategy: TruncTail}
	case "glob":
		return ToolOutputLimit{MaxChars: 20_000, MaxLines: 500, Strategy: TruncTail}
	case "edit_file":
		return ToolOutputLimit{MaxChars: 10_000, Strategy: TruncTail}
	case "apply_patch":
		return ToolOutputLimit{MaxChars: 10_000, Strategy: TruncTail}
	case "write_file":
		return ToolOutputLimit{MaxChars: 1_000, Strategy: TruncTail}
	case "spawn_agent":
		return ToolOutputLimit{MaxChars: 20_000, Strategy: TruncHeadTail}
	default:
		return ToolOutputLimit{MaxChars: 20_000, Strategy: TruncHeadTail}
	}
}

func compileSchema(params map[string]any) (*jsonschema.Schema, error) {
	if params == nil {
		// Default to empty object schema.
		params = map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
	}
	return jsonschemautil.CompileMapSchema(params, nil)
}

func toolValueToString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	default:
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return fmt.Sprint(v)
		}
		return string(b)
	}
}

func shortHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}
