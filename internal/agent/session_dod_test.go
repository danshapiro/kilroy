package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/llm"
)

func TestSession_MaxToolRoundsPerInput_StopsLoop(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	toolMsg := func(id string) llm.Response {
		call := llm.ToolCallData{
			ID:        id,
			Name:      "glob",
			Arguments: json.RawMessage(`{"pattern":"*.go","path":"."}`),
			Type:      "function",
		}
		return llm.Response{
			Message: llm.Message{
				Role:    llm.RoleAssistant,
				Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}},
			},
		}
	}

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolMsg("1") },
			func(req llm.Request) llm.Response { return toolMsg("2") },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.4"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxToolRoundsPerInput: 2,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, "loop")
	if err == nil || !strings.Contains(err.Error(), "max tool rounds") {
		t.Fatalf("expected max tool rounds error, got %v", err)
	}
	sess.Close()

	if got := len(f.Requests()); got != 2 {
		t.Fatalf("requests: got %d want 2", got)
	}
}

func TestSession_LifecycleEvents_BracketSession(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.4"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sess.Close()

	var kinds []EventKind
	for ev := range sess.Events() {
		kinds = append(kinds, ev.Kind)
	}
	if len(kinds) < 2 {
		t.Fatalf("expected at least 2 events, got %v", kinds)
	}
	if kinds[0] != EventSessionStart {
		t.Fatalf("first event: got %q want %q (kinds=%v)", kinds[0], EventSessionStart, kinds)
	}
	if kinds[len(kinds)-1] != EventSessionEnd {
		t.Fatalf("last event: got %q want %q (kinds=%v)", kinds[len(kinds)-1], EventSessionEnd, kinds)
	}
}

func TestSession_EventSystem_NaturalCompletion_EmitsUserAndAssistantTextEventsInOrder(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("hello")} },
		},
	})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.4"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if out, err := sess.ProcessInput(ctx, "hi"); err != nil || strings.TrimSpace(out) != "hello" {
		t.Fatalf("ProcessInput: out=%q err=%v", out, err)
	}
	sess.Close()

	var kinds []EventKind
	for ev := range sess.Events() {
		kinds = append(kinds, ev.Kind)
	}

	// Assert ordered subsequence.
	want := []EventKind{
		EventSessionStart,
		EventUserInput,
		EventAssistantTextStart,
		EventAssistantTextDelta,
		EventAssistantTextEnd,
		EventSessionEnd,
	}
	at := 0
	for _, k := range kinds {
		if at < len(want) && k == want[at] {
			at++
		}
	}
	if at != len(want) {
		t.Fatalf("event order missing; got kinds=%v want subsequence=%v", kinds, want)
	}
}

func TestSession_EventSystem_ToolCall_EmitsStartDeltaEnd(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	call := llm.ToolCallData{
		ID:        "c1",
		Name:      "write_file",
		Arguments: json.RawMessage(`{"file_path":"a.txt","content":"hello"}`),
		Type:      "function",
	}
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}}}
			},
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("ok")} },
		},
	})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.4"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if out, err := sess.ProcessInput(ctx, "write"); err != nil || strings.TrimSpace(out) != "ok" {
		t.Fatalf("ProcessInput: out=%q err=%v", out, err)
	}
	sess.Close()

	seenStart := false
	seenDelta := false
	seenEnd := false
	for ev := range sess.Events() {
		switch ev.Kind {
		case EventToolCallStart:
			seenStart = true
			if ev.Data["call_id"] != "c1" || ev.Data["tool_name"] != "write_file" {
				t.Fatalf("TOOL_CALL_START data: %+v", ev.Data)
			}
		case EventToolCallOutputDelta:
			seenDelta = true
			if !seenStart || seenEnd {
				t.Fatalf("TOOL_CALL_OUTPUT_DELTA ordering violated (start=%t end=%t)", seenStart, seenEnd)
			}
		case EventToolCallEnd:
			seenEnd = true
			if !seenStart {
				t.Fatalf("TOOL_CALL_END before TOOL_CALL_START")
			}
		}
	}
	if !seenStart || !seenDelta || !seenEnd {
		t.Fatalf("expected TOOL_CALL_START/DELTA/END, got start=%t delta=%t end=%t", seenStart, seenDelta, seenEnd)
	}
}

func TestSession_MalformedToolArgs_StillPairsToolResultsByCallID(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "glob:20", Name: "glob", Arguments: json.RawMessage(`{"pattern":"*.c"}{"path":"demo/rogue/original-rogue"}`)}},
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "glob:21", Name: "glob", Arguments: json.RawMessage(`{"pattern":"*.h"}{"path":"demo/rogue/original-rogue"}`)}},
						},
					},
				}
			},
			func(req llm.Request) llm.Response {
				seen := map[string]bool{"glob:20": false, "glob:21": false}
				for _, m := range req.Messages {
					if m.Role != llm.RoleTool {
						continue
					}
					for _, p := range m.Content {
						if p.Kind != llm.ContentToolResult || p.ToolResult == nil {
							continue
						}
						id := strings.TrimSpace(p.ToolResult.ToolCallID)
						if _, ok := seen[id]; !ok {
							continue
						}
						seen[id] = true
						if !p.ToolResult.IsError {
							t.Fatalf("expected tool result for %q to be error", id)
						}
						content := fmt.Sprint(p.ToolResult.Content)
						if !strings.Contains(content, "invalid tool arguments JSON") {
							t.Fatalf("expected invalid args error for %q, got %q", id, content)
						}
					}
				}
				for id, ok := range seen {
					if !ok {
						t.Fatalf("missing tool result for call_id %q", id)
					}
				}
				return llm.Response{Message: llm.Assistant("ok")}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.4"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if out, err := sess.ProcessInput(ctx, "check malformed tool args"); err != nil || strings.TrimSpace(out) != "ok" {
		t.Fatalf("ProcessInput: out=%q err=%v", out, err)
	}
	sess.Close()

	if got := len(f.Requests()); got != 2 {
		t.Fatalf("requests: got %d want 2", got)
	}
}

func TestSession_RepeatedMalformedToolCalls_FailsFast(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{
								Kind: llm.ContentToolCall,
								ToolCall: &llm.ToolCallData{
									ID:        "glob:1",
									Name:      "glob",
									Arguments: json.RawMessage(`{"pattern":"*.c"}{"path":"demo/rogue/original-rogue"}`),
								},
							},
						},
					},
				}
			},
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{
								Kind: llm.ContentToolCall,
								ToolCall: &llm.ToolCallData{
									ID:        "glob:2",
									Name:      "glob",
									Arguments: json.RawMessage(`{"pattern":"*.c"}{"path":"demo/rogue/original-rogue"}`),
								},
							},
						},
					},
				}
			},
			func(req llm.Request) llm.Response {
				t.Fatalf("unexpected third request after malformed-loop guard; req=%+v", req)
				return llm.Response{Message: llm.Assistant("unreachable")}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.4"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxToolRoundsPerInput:          50,
		MaxTurns:                       50,
		RepeatedMalformedToolCallLimit: 2,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, "trigger malformed loop")
	if err == nil || !strings.Contains(err.Error(), "repeated malformed tool calls detected") {
		t.Fatalf("expected repeated malformed tool call error, got %v", err)
	}
	if strings.Contains(err.Error(), "turn limit reached") {
		t.Fatalf("expected malformed-loop guard before turn limit, got %v", err)
	}
	sess.Close()

	if got := len(f.Requests()); got != 2 {
		t.Fatalf("requests: got %d want 2", got)
	}
}

func TestSession_MaxTurns_StopsAcrossRoundsAndEmitsEvent(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	call := llm.ToolCallData{
		ID:        "c1",
		Name:      "glob",
		Arguments: json.RawMessage(`{"pattern":"*.go","path":"."}`),
		Type:      "function",
	}
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Message{
						Role:    llm.RoleAssistant,
						Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}},
					},
				}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.4"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxTurns: 1,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, "hit max turns")
	if err == nil || !strings.Contains(err.Error(), "turn limit") {
		t.Fatalf("expected turn limit error, got %v", err)
	}
	sess.Close()

	// Only one LLM request should have been sent (second round is blocked by the turn limit check).
	if got := len(f.Requests()); got != 1 {
		t.Fatalf("requests: got %d want 1", got)
	}

	turnLimit := false
	for ev := range sess.Events() {
		if ev.Kind == EventTurnLimit {
			turnLimit = true
		}
	}
	if !turnLimit {
		t.Fatalf("expected TURN_LIMIT event")
	}
}

func TestSession_MultipleSequentialInputs_Work(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("first")} },
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("second")} },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.4"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if out, err := sess.ProcessInput(ctx, "one"); err != nil || strings.TrimSpace(out) != "first" {
		t.Fatalf("first: out=%q err=%v", out, err)
	}
	if out, err := sess.ProcessInput(ctx, "two"); err != nil || strings.TrimSpace(out) != "second" {
		t.Fatalf("second: out=%q err=%v", out, err)
	}
	sess.Close()

	if got := len(f.Requests()); got != 2 {
		t.Fatalf("requests: got %d want 2", got)
	}
}

func TestSession_Steer_IsInjectedAfterCurrentToolRound(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	started := make(chan struct{}, 1)
	release := make(chan struct{})

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "1", Name: "slow", Arguments: json.RawMessage(`{}`)}},
						},
					},
				}
			},
			func(req llm.Request) llm.Response {
				found := false
				for _, m := range req.Messages {
					if m.Role == llm.RoleUser && strings.Contains(m.Text(), "steer: do X") {
						found = true
					}
				}
				if !found {
					return llm.Response{Message: llm.Assistant("missing steering")}
				}
				return llm.Response{Message: llm.Assistant("ok")}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.4"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	_ = sess.reg.Register(RegisteredTool{
		Definition: llm.ToolDefinition{Name: "slow"},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			_ = args
			started <- struct{}{}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-release:
				return "ok", nil
			}
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := sess.ProcessInput(ctx, "run")
		done <- result{out: out, err: err}
	}()

	select {
	case <-started:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for tool to start")
	}
	sess.Steer("steer: do X")
	close(release)

	r := <-done
	if r.err != nil {
		t.Fatalf("ProcessInput: %v", r.err)
	}
	if strings.TrimSpace(r.out) != "ok" {
		t.Fatalf("out: %q", r.out)
	}

	// Spec: steering messages appear as SteeringTurn in history (converted to user-role messages for the LLM).
	sess.mu.Lock()
	turns := append([]Turn{}, sess.history...)
	sess.mu.Unlock()
	foundSteering := false
	for _, tr := range turns {
		if tr.Kind == TurnSteering && tr.Message.Role == llm.RoleUser && strings.Contains(tr.Message.Text(), "steer: do X") {
			foundSteering = true
		}
	}
	if !foundSteering {
		t.Fatalf("expected steering turn in history; got %+v", turns)
	}
	sess.Close()

	toolEndIdx := -1
	steerIdx := -1
	i := 0
	for ev := range sess.Events() {
		switch ev.Kind {
		case EventToolCallEnd:
			toolEndIdx = i
		case EventSteeringInjected:
			if ev.Data["text"] != "steer: do X" {
				t.Fatalf("STEERING_INJECTED data: %+v", ev.Data)
			}
			steerIdx = i
		}
		i++
	}
	if toolEndIdx == -1 {
		t.Fatalf("expected TOOL_CALL_END event")
	}
	if steerIdx == -1 {
		t.Fatalf("expected STEERING_INJECTED event")
	}
	if steerIdx <= toolEndIdx {
		t.Fatalf("expected steering injection after tool round; TOOL_CALL_END=%d STEERING_INJECTED=%d", toolEndIdx, steerIdx)
	}
}

func TestSession_ReasoningEffort_PassedThroughAndCanChange(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	started := make(chan struct{}, 1)
	release := make(chan struct{})

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "1", Name: "slow", Arguments: json.RawMessage(`{}`)}},
						},
					},
				}
			},
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("ok")}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.4"), NewLocalExecutionEnvironment(dir), SessionConfig{
		ReasoningEffort: "low",
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	_ = sess.reg.Register(RegisteredTool{
		Definition: llm.ToolDefinition{Name: "slow"},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			_ = args
			started <- struct{}{}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-release:
				return "ok", nil
			}
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := sess.ProcessInput(ctx, "run")
		done <- err
	}()

	select {
	case <-started:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for tool to start")
	}
	sess.SetReasoningEffort("high")
	close(release)

	if err := <-done; err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	reqs := f.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests: got %d want 2", len(reqs))
	}
	if reqs[0].ReasoningEffort == nil || *reqs[0].ReasoningEffort != "low" {
		t.Fatalf("req1 reasoning_effort: %#v", reqs[0].ReasoningEffort)
	}
	if reqs[1].ReasoningEffort == nil || *reqs[1].ReasoningEffort != "high" {
		t.Fatalf("req2 reasoning_effort: %#v", reqs[1].ReasoningEffort)
	}
}

type tinyProfile struct {
	id  string
	cw  int
	mod string
}

func (p tinyProfile) ID() string                                             { return p.id }
func (p tinyProfile) Model() string                                          { return p.mod }
func (p tinyProfile) ToolDefinitions() []llm.ToolDefinition                  { return nil }
func (p tinyProfile) SupportsParallelToolCalls() bool                        { return false }
func (p tinyProfile) ContextWindowSize() int                                 { return p.cw }
func (p tinyProfile) ProjectDocFiles() []string                              { return nil }
func (p tinyProfile) BuildSystemPrompt(EnvironmentInfo, []ProjectDoc) string { return "" }

func TestSession_ContextWindowAwareness_EmitsWarningOver80Percent(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "tiny",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("ok")} },
		},
	}
	c.Register(f)

	// 40 chars => approxTokens=10. With cw=10, warning should emit at ~100% usage (>80% threshold).
	sess, err := NewSession(c, tinyProfile{id: "tiny", mod: "m", cw: 10}, NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, strings.Repeat("a", 40))
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	warn := ""
	for ev := range sess.Events() {
		if ev.Kind == EventWarning {
			if msg, ok := ev.Data["message"].(string); ok {
				warn = msg
			}
		}
	}
	if warn == "" {
		t.Fatalf("expected WARNING event")
	}
	if !strings.Contains(warn, "~100% of context window") {
		t.Fatalf("warning message: %q", warn)
	}
}

func TestSession_ContextWindowAwareness_DoesNotWarnUnderThreshold(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "tiny",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("ok")} },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, tinyProfile{id: "tiny", mod: "m", cw: 1000}, NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, strings.Repeat("a", 40))
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	warned := false
	for ev := range sess.Events() {
		if ev.Kind == EventWarning {
			warned = true
		}
	}
	if warned {
		t.Fatalf("did not expect WARNING event")
	}
}

func TestSession_AbortSignal_ClosesSessionAndEmitsSessionEnd(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	started := make(chan struct{}, 1)

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "1", Name: "slow", Arguments: json.RawMessage(`{}`)}},
						},
					},
				}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.4"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	_ = sess.reg.Register(RegisteredTool{
		Definition: llm.ToolDefinition{Name: "slow"},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			_ = args
			started <- struct{}{}
			<-ctx.Done()
			return "", ctx.Err()
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := sess.ProcessInput(ctx, "run")
		done <- err
	}()

	select {
	case <-started:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for tool to start")
	}
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("expected abort error, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("ProcessInput did not abort promptly")
	}

	sess.mu.Lock()
	closed := sess.closed
	sess.mu.Unlock()
	if !closed {
		t.Fatalf("expected session to be closed on abort signal")
	}

	gotEnd := false
	gotErr := false
	gotToolEnd := false
	errIdx := -1
	endIdx := -1
	i := 0
	for ev := range sess.Events() {
		if ev.Kind == EventError {
			gotErr = true
			errIdx = i
		}
		if ev.Kind == EventSessionEnd {
			gotEnd = true
			endIdx = i
		}
		if ev.Kind == EventToolCallEnd {
			gotToolEnd = true
		}
		i++
	}
	if !gotEnd {
		t.Fatalf("expected SESSION_END event")
	}
	if !gotErr {
		t.Fatalf("expected ERROR event on abort signal")
	}
	if !gotToolEnd {
		t.Fatalf("expected TOOL_CALL_END event on abort signal")
	}
	if errIdx != -1 && endIdx != -1 && errIdx > endIdx {
		t.Fatalf("expected ERROR event before SESSION_END on abort (err=%d end=%d)", errIdx, endIdx)
	}
}

func TestSession_CustomToolRegistration_OverridesExistingTool(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.4"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	// Override a built-in tool implementation.
	if err := sess.reg.Register(RegisteredTool{
		Definition: llm.ToolDefinition{
			Name: "read_file",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"file_path": map[string]any{"type": "string"}},
				"required":   []string{"file_path"},
			},
		},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			_ = args
			return "OVERRIDE", nil
		},
	}); err != nil {
		t.Fatalf("Register override: %v", err)
	}
	res := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "read_file",
		Arguments: json.RawMessage(`{"file_path":"x"}`),
		Type:      "function",
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Output)
	}
	if strings.TrimSpace(res.Output) != "OVERRIDE" {
		t.Fatalf("output: %q", res.Output)
	}
	sess.Close()
}

type errAdapter struct {
	name  string
	err   error
	calls int
}

func (a *errAdapter) Name() string { return a.name }
func (a *errAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	_ = ctx
	_ = req
	a.calls++
	return llm.Response{}, a.err
}
func (a *errAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	_ = ctx
	_ = req
	a.calls++
	return nil, a.err
}

type flaky429Adapter struct {
	name      string
	failCount int
	calls     int
}

func (a *flaky429Adapter) Name() string { return a.name }
func (a *flaky429Adapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	_ = ctx
	_ = req
	a.calls++
	if a.calls <= a.failCount {
		return llm.Response{}, llm.ErrorFromHTTPStatus(a.name, 429, "rate limited", nil, nil)
	}
	return llm.Response{Message: llm.Assistant("ok")}, nil
}
func (a *flaky429Adapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	_ = req
	a.calls++
	if a.calls <= a.failCount {
		return nil, llm.ErrorFromHTTPStatus(a.name, 429, "rate limited", nil, nil)
	}
	return streamFromResponse(ctx, llm.Response{Provider: a.name, Model: req.Model, Message: llm.Assistant("ok")}), nil
}

func TestSession_AuthenticationError_ClosesSession(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	a := &errAdapter{name: "openai", err: llm.ErrorFromHTTPStatus("openai", 401, "bad key", nil, nil)}
	c.Register(a)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.4"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, "hi")
	if err == nil {
		t.Fatalf("expected error")
	}

	sess.mu.Lock()
	closed := sess.closed
	sess.mu.Unlock()
	if !closed {
		t.Fatalf("expected session to be closed on authentication error")
	}
	if a.calls != 1 {
		t.Fatalf("adapter calls: got %d want 1", a.calls)
	}

	gotEnd := false
	for ev := range sess.Events() {
		if ev.Kind == EventSessionEnd {
			gotEnd = true
		}
	}
	if !gotEnd {
		t.Fatalf("expected SESSION_END event")
	}
}

func TestSession_ContextLengthError_EmitsWarningAndClosesSession(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	a := &errAdapter{name: "openai", err: llm.ErrorFromHTTPStatus("openai", 413, "too large", nil, nil)}
	c.Register(a)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.4"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, "hi")
	if err == nil {
		t.Fatalf("expected error")
	}

	warn := false
	end := false
	for ev := range sess.Events() {
		if ev.Kind == EventWarning {
			if msg, ok := ev.Data["message"].(string); ok && strings.Contains(msg, "Context length") {
				warn = true
			}
		}
		if ev.Kind == EventSessionEnd {
			end = true
		}
	}
	if !warn {
		t.Fatalf("expected WARNING event for context length overflow")
	}
	if !end {
		t.Fatalf("expected SESSION_END event")
	}
}

func TestSession_LLMError_EmitsErrorEvent(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	a := &errAdapter{name: "openai", err: llm.ErrorFromHTTPStatus("openai", 500, "boom", nil, nil)}
	c.Register(a)

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.4"), NewLocalExecutionEnvironment(dir), SessionConfig{
		LLMRetryPolicy: &policy,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, "hi")
	if err == nil {
		t.Fatalf("expected error")
	}
	sess.Close()

	errEv := false
	for ev := range sess.Events() {
		if ev.Kind == EventError {
			if s, _ := ev.Data["error"].(string); strings.Contains(s, "openai") {
				errEv = true
			}
		}
	}
	if !errEv {
		t.Fatalf("expected ERROR event")
	}
}

func TestSession_LLMTransientErrors_RetryWithBackoff(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	ad := &flaky429Adapter{name: "openai", failCount: 2}
	c.Register(ad)

	var sleeps []time.Duration
	sleep := func(ctx context.Context, d time.Duration) error {
		_ = ctx
		sleeps = append(sleeps, d)
		return nil
	}

	policy := llm.RetryPolicy{
		MaxRetries:        5,
		BaseDelay:         1 * time.Millisecond,
		MaxDelay:          1 * time.Second,
		BackoffMultiplier: 2.0,
		Jitter:            false,
	}

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.4"), NewLocalExecutionEnvironment(dir), SessionConfig{
		LLMRetryPolicy: &policy,
		LLMSleep:       sleep,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "hi")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if strings.TrimSpace(out) != "ok" {
		t.Fatalf("out: %q", out)
	}
	sess.Close()

	if got, want := ad.calls, 3; got != want {
		t.Fatalf("adapter calls: got %d want %d", got, want)
	}
	if got, want := len(sleeps), 2; got != want {
		t.Fatalf("sleep calls: got %d want %d (%v)", got, want, sleeps)
	}
	if sleeps[0] != 1*time.Millisecond {
		t.Fatalf("sleep[0]: got %s want %s", sleeps[0], 1*time.Millisecond)
	}
	if sleeps[1] != 2*time.Millisecond {
		t.Fatalf("sleep[1]: got %s want %s", sleeps[1], 2*time.Millisecond)
	}
}

func TestSession_Subagents_SpawnWaitClose_AndDepthLimit(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("subok")} },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.4"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	spawnRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "spawn_agent",
		Arguments: json.RawMessage(`{"task":"do it"}`),
	})
	if spawnRes.IsError {
		t.Fatalf("spawn_agent error: %s", spawnRes.Output)
	}
	var spawned map[string]any
	if err := json.Unmarshal([]byte(spawnRes.Output), &spawned); err != nil {
		t.Fatalf("unmarshal spawn_agent output: %v (out=%q)", err, spawnRes.Output)
	}
	agentID := strings.TrimSpace(fmt.Sprint(spawned["agent_id"]))
	if agentID == "" {
		t.Fatalf("missing agent_id in spawn output: %v", spawned)
	}

	waitRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c2",
		Name:      "wait",
		Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"timeout_ms":2000}`, agentID)),
	})
	if waitRes.IsError {
		t.Fatalf("wait error: %s", waitRes.Output)
	}
	if strings.TrimSpace(waitRes.Output) != "subok" {
		t.Fatalf("wait output: %q", waitRes.Output)
	}

	// Depth limiting: subagent cannot spawn further subagents when MaxSubagentDepth=1.
	sub := sess.getSub(agentID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("missing subagent session for %q", agentID)
	}
	if _, err := sub.sess.spawnAgent(context.Background(), "nested"); err == nil {
		t.Fatalf("expected depth limit error, got nil")
	}

	closeRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c3",
		Name:      "close_agent",
		Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q}`, agentID)),
	})
	if closeRes.IsError {
		t.Fatalf("close_agent error: %s", closeRes.Output)
	}
	sess.Close()
}

func TestSession_ShellTool_UsesDefaultTimeoutAndAllowsOverride(t *testing.T) {
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "1", Name: "shell", Arguments: json.RawMessage(`{"command":"echo hi"}`)}},
						},
					},
				}
			},
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("ok")} },
		},
	}
	c.Register(f)

	env := &captureEnv{wd: "/tmp"}
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.4"), env, SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "run"); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	if got := env.LastTimeoutMS(); got != 600_000 {
		t.Fatalf("default shell timeout: got %d want %d", got, 600_000)
	}

	// Override per-call timeout_ms.
	env2 := &captureEnv{wd: "/tmp"}
	f2 := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "1", Name: "shell", Arguments: json.RawMessage(`{"command":"echo hi","timeout_ms":1234}`)}},
						},
					},
				}
			},
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("ok")} },
		},
	}
	c2 := llm.NewClient()
	c2.Register(f2)
	sess2, err := NewSession(c2, NewOpenAIProfile("gpt-5.4"), env2, SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession2: %v", err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if _, err := sess2.ProcessInput(ctx2, "run"); err != nil {
		t.Fatalf("ProcessInput2: %v", err)
	}
	sess2.Close()
	if got := env2.LastTimeoutMS(); got != 1234 {
		t.Fatalf("override shell timeout: got %d want %d", got, 1234)
	}
}

func TestSession_ShellTool_CapsTimeoutToMaxCommandTimeoutMS(t *testing.T) {
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "1", Name: "shell", Arguments: json.RawMessage(`{"command":"echo hi","timeout_ms":999999}`)}},
						},
					},
				}
			},
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("ok")} },
		},
	}
	c.Register(f)

	env := &captureEnv{wd: "/tmp"}
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.4"), env, SessionConfig{
		MaxCommandTimeoutMS: 5000,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "run"); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	if got := env.LastTimeoutMS(); got != 5000 {
		t.Fatalf("capped shell timeout: got %d want %d", got, 5000)
	}
}

func TestSession_ShellTool_TimeoutAppendsMessageToToolResult(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "1", Name: "shell", Arguments: json.RawMessage(`{"command":"sleep 30"}`)}},
						},
					},
				}
			},
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("ok")} },
		},
	}
	c.Register(f)

	env := &timeoutEnv{wd: dir}
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.4"), env, SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "run"); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	reqs := f.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests: got %d want 2", len(reqs))
	}
	toolResult := ""
	for _, m := range reqs[1].Messages {
		if m.Role != llm.RoleTool {
			continue
		}
		for _, p := range m.Content {
			if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
				if s, ok := p.ToolResult.Content.(string); ok {
					toolResult = s
				}
			}
		}
	}
	if toolResult == "" {
		t.Fatalf("expected tool result content in second request")
	}
	for _, want := range []string{
		"timed_out=true",
		"Command timed out after 600000ms",
		"You can retry with a longer timeout",
	} {
		if !strings.Contains(toolResult, want) {
			t.Fatalf("tool result missing %q:\n%s", want, toolResult)
		}
	}
}

type captureEnv struct {
	wd string

	mu        sync.Mutex
	lastCmd   string
	lastTOms  int
	lastWdArg string
}

func (e *captureEnv) WorkingDirectory() string { return e.wd }
func (e *captureEnv) Platform() string         { return "linux" }
func (e *captureEnv) OSVersion() string        { return "test" }

func (e *captureEnv) ReadFile(path string, offsetLine *int, limitLines *int) (string, error) {
	return "", fmt.Errorf("not implemented")
}
func (e *captureEnv) WriteFile(path string, content string) (string, error) {
	return "", fmt.Errorf("not implemented")
}
func (e *captureEnv) EditFile(path string, oldString string, newString string, replaceAll bool) (string, error) {
	return "", fmt.Errorf("not implemented")
}
func (e *captureEnv) FileExists(path string) bool { return false }
func (e *captureEnv) Glob(pattern string, basePath string) ([]string, error) {
	return nil, fmt.Errorf("not implemented")
}
func (e *captureEnv) Grep(pattern string, path string, globFilter string, caseInsensitive bool, maxResults int) (string, error) {
	return "", fmt.Errorf("not implemented")
}
func (e *captureEnv) ListDirectory(path string, depth int) ([]DirEntry, error) {
	return nil, fmt.Errorf("not implemented")
}
func (e *captureEnv) ExecCommand(ctx context.Context, command string, timeoutMS int, workingDir string, envVars map[string]string) (ExecResult, error) {
	_ = ctx
	_ = envVars
	e.mu.Lock()
	e.lastCmd = command
	e.lastTOms = timeoutMS
	e.lastWdArg = workingDir
	e.mu.Unlock()
	return ExecResult{Stdout: "ok", Stderr: "", ExitCode: 0, TimedOut: false, DurationMS: 1}, nil
}

func (e *captureEnv) LastTimeoutMS() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastTOms
}

type timeoutEnv struct {
	wd string
}

func (e *timeoutEnv) WorkingDirectory() string { return e.wd }
func (e *timeoutEnv) Platform() string         { return "linux" }
func (e *timeoutEnv) OSVersion() string        { return "test" }
func (e *timeoutEnv) ReadFile(path string, offsetLine *int, limitLines *int) (string, error) {
	return "", fmt.Errorf("not implemented")
}
func (e *timeoutEnv) WriteFile(path string, content string) (string, error) {
	return "", fmt.Errorf("not implemented")
}
func (e *timeoutEnv) EditFile(path string, oldString string, newString string, replaceAll bool) (string, error) {
	return "", fmt.Errorf("not implemented")
}
func (e *timeoutEnv) FileExists(path string) bool { return false }
func (e *timeoutEnv) Glob(pattern string, basePath string) ([]string, error) {
	return nil, fmt.Errorf("not implemented")
}
func (e *timeoutEnv) Grep(pattern string, path string, globFilter string, caseInsensitive bool, maxResults int) (string, error) {
	return "", fmt.Errorf("not implemented")
}
func (e *timeoutEnv) ListDirectory(path string, depth int) ([]DirEntry, error) {
	return nil, fmt.Errorf("not implemented")
}
func (e *timeoutEnv) ExecCommand(ctx context.Context, command string, timeoutMS int, workingDir string, envVars map[string]string) (ExecResult, error) {
	_ = ctx
	_ = workingDir
	_ = envVars
	// Pretend git isn't available for this environment (session snapshot + doc discovery fall back cleanly).
	if strings.HasPrefix(strings.TrimSpace(command), "git ") {
		return ExecResult{ExitCode: 1}, fmt.Errorf("not a git repo")
	}
	return ExecResult{
		Stdout:     "partial output\n",
		Stderr:     "",
		ExitCode:   124,
		TimedOut:   true,
		DurationMS: int64(timeoutMS),
	}, context.DeadlineExceeded
}
