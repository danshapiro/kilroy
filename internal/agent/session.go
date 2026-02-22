package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/danshapiro/kilroy/internal/llm"
)

type SessionConfig struct {
	MaxToolRoundsPerInput          int
	MaxTurns                       int
	DefaultCommandTimeoutMS        int
	MaxCommandTimeoutMS            int
	RepeatedMalformedToolCallLimit int
	MaxSubagentDepth               int

	// ToolOutputLimits overrides default per-tool truncation behavior.
	ToolOutputLimits map[string]ToolOutputLimit

	// UserInstructionOverride is appended to the end of the system prompt (highest priority).
	UserInstructionOverride string

	// ReasoningEffort is passed through to the Unified LLM request when non-empty.
	// Valid values are provider-dependent but typically include: low|medium|high.
	ReasoningEffort string

	// MaxTokens overrides the provider adapter's default max_tokens when non-nil.
	// Use this to allow larger outputs (e.g., large write_file tool calls).
	MaxTokens *int

	// ProviderOptions is merged into every LLM request as provider_options.
	// Use this for provider-specific parameters (e.g., Cerebras clear_thinking).
	ProviderOptions map[string]any

	// ToolCallFilter, when non-nil, is invoked before each tool call is executed.
	// It receives the tool name, call ID, and arguments JSON. If it returns a
	// non-empty string, the tool call is skipped and the returned string is used
	// as the tool result (with IsError=true). This enables pre-hook scripts to
	// veto tool calls.
	ToolCallFilter func(toolName, callID, argsJSON string) (skipReason string)

	EnableLoopDetection *bool
	LoopDetectionWindow int

	// LLMRetryPolicy controls retries for retryable Unified LLM errors (429, 5xx, etc).
	// Nil means use llm.DefaultRetryPolicy().
	LLMRetryPolicy *llm.RetryPolicy
	LLMSleep       llm.SleepFunc
}

// ErrTurnLimit indicates the session exceeded its configured MaxTurns budget.
var ErrTurnLimit = errors.New("turn limit reached")

func (c *SessionConfig) applyDefaults() {
	if c.MaxToolRoundsPerInput <= 0 {
		c.MaxToolRoundsPerInput = 200
	}
	if c.DefaultCommandTimeoutMS <= 0 {
		c.DefaultCommandTimeoutMS = 10_000
	}
	if c.MaxCommandTimeoutMS <= 0 {
		c.MaxCommandTimeoutMS = 600_000
	}
	if c.RepeatedMalformedToolCallLimit <= 0 {
		c.RepeatedMalformedToolCallLimit = 3
	}
	if c.MaxSubagentDepth <= 0 {
		c.MaxSubagentDepth = 1
	}
	if c.EnableLoopDetection == nil {
		v := true
		c.EnableLoopDetection = &v
	}
	if c.LoopDetectionWindow <= 0 {
		c.LoopDetectionWindow = 10
	}
}

type Session struct {
	id      string
	cfg     SessionConfig
	client  *llm.Client
	profile ProviderProfile
	env     ExecutionEnvironment

	events  chan SessionEvent
	envInfo EnvironmentInfo

	mu      sync.Mutex
	closed  bool
	turns   int
	history []Turn

	reg *ToolRegistry

	steeringQueue []string
	followups     []string

	// subagents
	depth     int
	subagents map[string]*subagent
}

func NewSession(client *llm.Client, profile ProviderProfile, env ExecutionEnvironment, cfg SessionConfig) (*Session, error) {
	if client == nil {
		return nil, fmt.Errorf("llm client is nil")
	}
	if profile == nil {
		return nil, fmt.Errorf("profile is nil")
	}
	if env == nil {
		return nil, fmt.Errorf("execution environment is nil")
	}
	cfg.applyDefaults()

	s := &Session{
		id:        ulid.Make().String(),
		cfg:       cfg,
		client:    client,
		profile:   profile,
		env:       env,
		events:    make(chan SessionEvent, 256),
		history:   []Turn{},
		subagents: map[string]*subagent{},
	}

	// Snapshot environment context once per session (spec).
	ei := envInfoFromEnv(env)
	if inRepo, branch, mod, untracked, commits := snapshotGit(env, ei.WorkingDir); inRepo {
		ei.IsGitRepo = true
		ei.GitBranch = branch
		ei.GitModifiedFiles = mod
		ei.GitUntrackedFiles = untracked
		ei.GitRecentCommitTitles = commits
	}
	s.envInfo = ei

	reg := NewToolRegistry()
	if err := registerCoreTools(reg, s); err != nil {
		return nil, err
	}
	// Allow SessionConfig to override default tool output limits (spec).
	if len(cfg.ToolOutputLimits) > 0 {
		reg.mu.Lock()
		for name, lim := range cfg.ToolOutputLimits {
			t, ok := reg.tools[name]
			if !ok {
				continue
			}
			// Merge: only positive overrides take effect.
			if lim.MaxChars > 0 {
				t.Limit.MaxChars = lim.MaxChars
			}
			if lim.MaxLines > 0 {
				t.Limit.MaxLines = lim.MaxLines
			}
			if lim.Strategy != "" {
				t.Limit.Strategy = lim.Strategy
			}
			reg.tools[name] = t
		}
		reg.mu.Unlock()
	}
	s.reg = reg

	s.emit(EventSessionStart, map[string]any{
		"profile": profile.ID(),
		"model":   profile.Model(),
	})
	return s, nil
}

func (s *Session) Events() <-chan SessionEvent { return s.events }

// SetReasoningEffort updates the reasoning effort used for future LLM calls.
// Takes effect on the next request (spec).
func (s *Session) SetReasoningEffort(effort string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.cfg.ReasoningEffort = strings.TrimSpace(effort)
}

// Steer queues a message to inject after the current tool round completes.
func (s *Session) Steer(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if strings.TrimSpace(msg) == "" {
		return
	}
	s.steeringQueue = append(s.steeringQueue, msg)
}

// FollowUp queues a message to process after the current input completes.
func (s *Session) FollowUp(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if strings.TrimSpace(msg) == "" {
		return
	}
	s.followups = append(s.followups, msg)
}

func (s *Session) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()

	s.emit(EventSessionEnd, map[string]any{})
	close(s.events)
}

func (s *Session) ProcessInput(ctx context.Context, input string) (string, error) {
	outputs := []string{}
	next := input
	for {
		out, err := s.processOneInput(ctx, next)
		if strings.TrimSpace(out) != "" {
			outputs = append(outputs, out)
		}
		if err != nil {
			// Spec: abort signal closes the session and stops the loop.
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
				s.Close()
			}
			return strings.Join(outputs, "\n"), err
		}
		fu := s.popFollowUp()
		if strings.TrimSpace(fu) == "" {
			return strings.Join(outputs, "\n"), nil
		}
		next = fu
	}
}

func (s *Session) execTool(ctx context.Context, call llm.ToolCallData) ToolExecResult {
	argsJSON, _ := json.Marshal(call.Arguments)
	s.emit(EventToolCallStart, map[string]any{
		"tool_name":      call.Name,
		"call_id":        call.ID,
		"arguments_json": string(argsJSON),
	})

	// Spec §9.7: ToolCallFilter allows pre-hooks to veto tool calls.
	if s.cfg.ToolCallFilter != nil {
		if skipReason := s.cfg.ToolCallFilter(call.Name, call.ID, string(argsJSON)); skipReason != "" {
			res := ToolExecResult{
				ToolName:   call.Name,
				CallID:     call.ID,
				Output:     skipReason,
				FullOutput: skipReason,
				IsError:    true,
			}
			s.emit(EventToolCallEnd, map[string]any{
				"tool_name":   res.ToolName,
				"call_id":     res.CallID,
				"is_error":    res.IsError,
				"full_output": res.FullOutput,
				"skipped":     true,
			})
			return res
		}
	}

	// Session-level tools (subagents) are registered in the registry with closures.
	res := s.reg.ExecuteCall(ctx, s.env, call)

	// Emit output deltas (best-effort). Even for non-streaming tools, this gives consumers a uniform
	// incremental event pattern that mirrors provider LLM streaming.
	full := res.FullOutput
	const chunk = 4000
	for i := 0; i < len(full); i += chunk {
		j := i + chunk
		if j > len(full) {
			j = len(full)
		}
		s.emit(EventToolCallOutputDelta, map[string]any{
			"tool_name": res.ToolName,
			"call_id":   res.CallID,
			"delta":     full[i:j],
		})
	}

	s.emit(EventToolCallEnd, map[string]any{
		"tool_name":   res.ToolName,
		"call_id":     res.CallID,
		"is_error":    res.IsError,
		"full_output": res.FullOutput,
	})
	return res
}

func (s *Session) appendTurn(kind TurnKind, m llm.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = append(s.history, Turn{Kind: kind, Message: m})
}

func (s *Session) maybeWarnContextUsage(msgs []llm.Message) bool {
	if s == nil || s.profile == nil {
		return false
	}
	cw := s.profile.ContextWindowSize()
	if cw <= 0 {
		return false
	}

	totalChars := 0
	for _, m := range msgs {
		totalChars += messageCharCount(m)
	}
	approxTokens := float64(totalChars) / 4.0
	threshold := float64(cw) * 0.8
	if approxTokens <= threshold {
		return false
	}

	pct := int(math.Round((approxTokens / float64(cw)) * 100.0))
	msg := fmt.Sprintf("Context usage at ~%d%% of context window", pct)
	s.emit(EventWarning, map[string]any{
		"message":             msg,
		"approx_tokens":       int(math.Round(approxTokens)),
		"context_window_size": cw,
		"percent":             pct,
	})
	return true
}

func messageCharCount(m llm.Message) int {
	n := 0
	n += len(m.Name)
	n += len(m.ToolCallID)
	for _, p := range m.Content {
		switch p.Kind {
		case llm.ContentText:
			n += len(p.Text)
		case llm.ContentToolCall:
			if p.ToolCall != nil {
				n += len(p.ToolCall.ID)
				n += len(p.ToolCall.Name)
				n += len(p.ToolCall.Arguments)
			}
		case llm.ContentToolResult:
			if p.ToolResult != nil {
				n += len(p.ToolResult.ToolCallID)
				n += len(p.ToolResult.Name)
				switch x := p.ToolResult.Content.(type) {
				case string:
					n += len(x)
				case []byte:
					n += len(x)
				default:
					b, _ := json.Marshal(x)
					n += len(b)
				}
			}
		case llm.ContentThinking, llm.ContentRedThinking:
			if p.Thinking != nil {
				n += len(p.Thinking.Text)
				n += len(p.Thinking.Signature)
			}
		default:
			// Fallback to a best-effort JSON encoding.
			b, _ := json.Marshal(p)
			n += len(b)
		}
	}
	return n
}

func (s *Session) emit(kind EventKind, data map[string]any) {
	if s == nil || s.events == nil {
		return
	}
	ev := SessionEvent{
		Kind:      kind,
		Timestamp: time.Now().UTC(),
		SessionID: s.id,
		Data:      data,
	}
	// Close() may happen concurrently with emit (abort signal while tools run in parallel).
	// Sending on a closed channel would panic; v1 semantics are best-effort delivery.
	defer func() { _ = recover() }()
	select {
	case s.events <- ev:
	default:
		// Drop events if consumer is too slow; v1 is best-effort.
	}
}

func (s *Session) processOneInput(ctx context.Context, input string) (string, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return "", fmt.Errorf("session is closed")
	}
	s.mu.Unlock()

	select {
	case <-ctx.Done():
		s.emit(EventError, map[string]any{"error": ctx.Err().Error()})
		return "", ctx.Err()
	default:
	}

	s.emit(EventUserInput, map[string]any{"text": input})
	s.appendTurn(TurnUserInput, llm.User(input))

	docs, _ := LoadProjectDocs(s.env, s.profile.ProjectDocFiles()...)
	sys := s.profile.BuildSystemPrompt(s.envInfo, docs)
	if strings.TrimSpace(s.cfg.UserInstructionOverride) != "" {
		sys = sys + "\n\n" + strings.TrimSpace(s.cfg.UserInstructionOverride) + "\n"
	}

	var lastToolFP string
	repeats := 0
	var lastMalformedToolFP string
	malformedRepeats := 0
	loopWarned := false
	ctxWarned := false

	for round := 0; round < s.cfg.MaxToolRoundsPerInput; round++ {
		select {
		case <-ctx.Done():
			s.emit(EventError, map[string]any{"error": ctx.Err().Error()})
			return "", ctx.Err()
		default:
		}
		s.mu.Lock()
		s.turns++
		turns := s.turns
		historyTurns := append([]Turn{}, s.history...)
		s.mu.Unlock()

		history := make([]llm.Message, 0, len(historyTurns))
		for _, t := range historyTurns {
			if t.Kind == TurnSteering {
				history = append(history, llm.User(t.Message.Text()))
				continue
			}
			history = append(history, t.Message)
		}

		if s.cfg.MaxTurns > 0 && turns > s.cfg.MaxTurns {
			s.emit(EventTurnLimit, map[string]any{"max_turns": s.cfg.MaxTurns})
			return "", fmt.Errorf("%w (max_turns=%d)", ErrTurnLimit, s.cfg.MaxTurns)
		}

		req := llm.Request{
			Model:    s.profile.Model(),
			Provider: s.profile.ID(),
			Messages: append([]llm.Message{llm.System(sys)}, history...),
			Tools:    s.profile.ToolDefinitions(),
		}
		if strings.TrimSpace(s.cfg.ReasoningEffort) != "" {
			v := strings.TrimSpace(s.cfg.ReasoningEffort)
			req.ReasoningEffort = &v
		}
		if s.cfg.MaxTokens != nil {
			req.MaxTokens = s.cfg.MaxTokens
		}
		if len(s.cfg.ProviderOptions) > 0 {
			req.ProviderOptions = s.cfg.ProviderOptions
		}

		policy := llm.DefaultRetryPolicy()
		if s.cfg.LLMRetryPolicy != nil {
			policy = *s.cfg.LLMRetryPolicy
		}
		resp, err := llm.Retry(ctx, policy, s.cfg.LLMSleep, nil, func() (llm.Response, error) {
			return s.client.Complete(ctx, req)
		})
		if err != nil {
			s.emit(EventError, map[string]any{"error": err.Error()})
			// Spec: context overflow should emit a warning (no automatic compaction).
			var cle *llm.ContextLengthError
			if errors.As(err, &cle) {
				s.emit(EventWarning, map[string]any{"message": "Context length exceeded"})
			}
			// Spec: non-retryable/unrecoverable errors transition the session to CLOSED.
			var le llm.Error
			if errors.As(err, &le) && !le.Retryable() {
				s.Close()
			}
			return "", err
		}

		// Context window awareness: emit a warning when we exceed ~80% of the profile's context window.
		if !ctxWarned {
			if s.maybeWarnContextUsage(req.Messages) {
				ctxWarned = true
			}
		}

		txt := resp.Text()
		s.emit(EventAssistantTextStart, map[string]any{})
		s.appendTurn(TurnAssistant, resp.Message)
		if strings.TrimSpace(txt) != "" {
			s.emit(EventAssistantTextDelta, map[string]any{"delta": txt})
		}
		s.emit(EventAssistantTextEnd, map[string]any{"text": txt})

		calls := resp.ToolCalls()
		if len(calls) == 0 {
			return txt, nil
		}

		// Loop detection: if the model keeps emitting identical tool call patterns, warn once.
		if s.cfg.EnableLoopDetection != nil && *s.cfg.EnableLoopDetection && !loopWarned {
			fp := toolCallsFingerprint(calls)
			if fp != "" && fp == lastToolFP {
				repeats++
			} else {
				lastToolFP = fp
				repeats = 1
			}
			if repeats >= s.cfg.LoopDetectionWindow {
				loopWarned = true
				s.emit(EventLoopDetection, map[string]any{"fingerprint": fp, "repeats": repeats})
				s.appendTurn(TurnSteering, llm.User(loopDetectionSteeringPrompt))
				s.emit(EventSteeringInjected, map[string]any{"text": loopDetectionSteeringPrompt})
			}
		}

		// Execute tool calls (possibly in parallel) and send results back.
		results := make([]ToolExecResult, len(calls))
		if s.profile.SupportsParallelToolCalls() && len(calls) > 1 {
			var wg sync.WaitGroup
			wg.Add(len(calls))
			for i := range calls {
				i := i
				go func() {
					defer wg.Done()
					results[i] = s.execTool(ctx, calls[i])
				}()
			}
			wg.Wait()
		} else {
			for i := range calls {
				results[i] = s.execTool(ctx, calls[i])
			}
		}

		// Guardrail: malformed tool-arguments loops are deterministic and should
		// fail fast instead of burning large turn budgets.
		if malformedFP := malformedToolCallsFingerprint(calls, results); malformedFP != "" {
			if malformedFP == lastMalformedToolFP {
				malformedRepeats++
			} else {
				lastMalformedToolFP = malformedFP
				malformedRepeats = 1
			}
			if s.cfg.RepeatedMalformedToolCallLimit > 0 && malformedRepeats >= s.cfg.RepeatedMalformedToolCallLimit {
				err := fmt.Errorf("repeated malformed tool calls detected (repeats=%d limit=%d)", malformedRepeats, s.cfg.RepeatedMalformedToolCallLimit)
				s.emit(EventError, map[string]any{
					"error":   err.Error(),
					"repeats": malformedRepeats,
					"limit":   s.cfg.RepeatedMalformedToolCallLimit,
				})
				return "", err
			}
		} else {
			lastMalformedToolFP = ""
			malformedRepeats = 0
		}

		for _, r := range results {
			s.appendTurn(TurnTool, llm.ToolResultNamed(r.CallID, r.ToolName, r.Output, r.IsError))
		}

		// Inject any queued steering messages before the next model call.
		for _, msg := range s.drainSteering() {
			s.appendTurn(TurnSteering, llm.User(msg))
			s.emit(EventSteeringInjected, map[string]any{"text": msg})
		}
	}

	return "", fmt.Errorf("max tool rounds reached")
}

func (s *Session) drainSteering() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.steeringQueue) == 0 {
		return nil
	}
	out := append([]string{}, s.steeringQueue...)
	s.steeringQueue = nil
	return out
}

func (s *Session) popFollowUp() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.followups) == 0 {
		return ""
	}
	msg := s.followups[0]
	s.followups = s.followups[1:]
	return msg
}

func toolCallsFingerprint(calls []llm.ToolCallData) string {
	if len(calls) == 0 {
		return ""
	}
	var b strings.Builder
	for _, c := range calls {
		b.WriteString(strings.TrimSpace(c.Name))
		b.WriteByte(':')
		b.WriteString(shortHash(c.Arguments))
		b.WriteByte(';')
	}
	return b.String()
}

func malformedToolCallsFingerprint(calls []llm.ToolCallData, results []ToolExecResult) string {
	if len(calls) == 0 || len(calls) != len(results) {
		return ""
	}
	var b strings.Builder
	for i := range calls {
		if !results[i].IsError || !strings.Contains(results[i].FullOutput, "invalid tool arguments JSON") {
			continue
		}
		b.WriteString(strings.TrimSpace(calls[i].Name))
		b.WriteByte(':')
		b.WriteString(shortHash(calls[i].Arguments))
		b.WriteByte(';')
	}
	return b.String()
}

// Tool registration.

// argStr extracts a string argument from a tool-call args map.
// When the key is missing or the value is nil, it returns "" instead of "<nil>"
// (which is what fmt.Sprint(nil) would produce).
func argStr(args map[string]any, key string) string {
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func registerCoreTools(reg *ToolRegistry, s *Session) error {
	// read_file
	if err := reg.Register(RegisteredTool{
		Definition: defReadFile(),
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			path := argStr(args, "file_path")
			var offset *int
			var limit *int
			if v, ok := args["offset"]; ok {
				if n, ok := v.(float64); ok {
					ni := int(n)
					offset = &ni
				}
			}
			if v, ok := args["limit"]; ok {
				if n, ok := v.(float64); ok {
					ni := int(n)
					limit = &ni
				}
			}
			return env.ReadFile(path, offset, limit)
		},
	}); err != nil {
		return err
	}

	// read_many_files (Gemini-aligned; safe to register globally)
	_ = reg.Register(RegisteredTool{
		Definition: defReadManyFiles(),
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			pathsAny := args["file_paths"]
			var paths []string
			switch x := pathsAny.(type) {
			case []any:
				for _, it := range x {
					paths = append(paths, fmt.Sprint(it))
				}
			case []string:
				paths = append(paths, x...)
			}
			var offset *int
			var limit *int
			if v, ok := args["offset"]; ok {
				if n, ok := v.(float64); ok {
					ni := int(n)
					offset = &ni
				}
			}
			if v, ok := args["limit"]; ok {
				if n, ok := v.(float64); ok {
					ni := int(n)
					limit = &ni
				}
			}

			var b strings.Builder
			for _, p := range paths {
				p = strings.TrimSpace(p)
				if p == "" {
					continue
				}
				b.WriteString("----- BEGIN " + p + " -----\n")
				txt, err := env.ReadFile(p, offset, limit)
				if err != nil {
					b.WriteString("[ERROR] " + err.Error() + "\n")
				} else {
					b.WriteString(txt)
					if !strings.HasSuffix(txt, "\n") {
						b.WriteString("\n")
					}
				}
				b.WriteString("----- END " + p + " -----\n")
			}
			return b.String(), nil
		},
	})

	// write_file
	if err := reg.Register(RegisteredTool{
		Definition: defWriteFile(),
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			return env.WriteFile(argStr(args, "file_path"), argStr(args, "content"))
		},
	}); err != nil {
		return err
	}

	// edit_file
	_ = reg.Register(RegisteredTool{
		Definition: defEditFile(),
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			replaceAll := false
			if v, ok := args["replace_all"].(bool); ok {
				replaceAll = v
			}
			return env.EditFile(argStr(args, "file_path"), argStr(args, "old_string"), argStr(args, "new_string"), replaceAll)
		},
	})

	// shell
	if err := reg.Register(RegisteredTool{
		Definition: defShell(),
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			cmd := argStr(args, "command")
			timeout := s.cfg.DefaultCommandTimeoutMS
			if v, ok := args["timeout_ms"].(float64); ok && int(v) > 0 {
				timeout = int(v)
			}
			if s.cfg.MaxCommandTimeoutMS > 0 && timeout > s.cfg.MaxCommandTimeoutMS {
				timeout = s.cfg.MaxCommandTimeoutMS
			}
			res, err := env.ExecCommand(ctx, cmd, timeout, "", nil)

			// Return a line-oriented tool output so line truncation works as intended for shell output.
			var b strings.Builder
			if strings.TrimSpace(res.Stdout) != "" {
				b.WriteString(res.Stdout)
				if !strings.HasSuffix(res.Stdout, "\n") {
					b.WriteString("\n")
				}
			}
			if strings.TrimSpace(res.Stderr) != "" {
				b.WriteString(res.Stderr)
				if !strings.HasSuffix(res.Stderr, "\n") {
					b.WriteString("\n")
				}
			}
			if res.TimedOut {
				b.WriteString(fmt.Sprintf("[ERROR: Command timed out after %dms. Partial output is shown above.\nYou can retry with a longer timeout by setting the timeout_ms parameter.]\n", timeout))
			}
			b.WriteString(fmt.Sprintf("exit_code=%d duration_ms=%d timed_out=%t\n", res.ExitCode, res.DurationMS, res.TimedOut))
			return b.String(), err
		},
	}); err != nil {
		return err
	}

	// list_dir (Gemini-aligned)
	_ = reg.Register(RegisteredTool{
		Definition: defListDir(),
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			path := argStr(args, "path")
			depth := 1
			if v, ok := args["depth"].(float64); ok && int(v) > 0 {
				depth = int(v)
			}
			return env.ListDirectory(path, depth)
		},
	})

	// grep
	if err := reg.Register(RegisteredTool{
		Definition: defGrep(),
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			pat := argStr(args, "pattern")
			path := argStr(args, "path")
			glob := argStr(args, "glob_filter")
			ci := false
			if v, ok := args["case_insensitive"].(bool); ok {
				ci = v
			}
			maxRes := 100
			if v, ok := args["max_results"].(float64); ok && int(v) > 0 {
				maxRes = int(v)
			}
			return env.Grep(pat, path, glob, ci, maxRes)
		},
	}); err != nil {
		return err
	}

	// glob
	if err := reg.Register(RegisteredTool{
		Definition: defGlob(),
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			pat := argStr(args, "pattern")
			path := argStr(args, "path")
			matches, err := env.Glob(pat, path)
			if err != nil {
				return "", err
			}
			return strings.Join(matches, "\n"), nil
		},
	}); err != nil {
		return err
	}

	// apply_patch (OpenAI-specific; best-effort implementation lives in this repo)
	_ = reg.Register(RegisteredTool{
		Definition: defApplyPatch(),
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			patch := argStr(args, "patch")
			return ApplyPatch(env.WorkingDirectory(), patch)
		},
	})

	// Subagent tools (best-effort; synchronous completion for v1).
	_ = reg.Register(RegisteredTool{
		Definition: defSpawnAgent(),
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			task := argStr(args, "task")
			return s.spawnAgent(ctx, task)
		},
	})
	_ = reg.Register(RegisteredTool{
		Definition: defSendInput(),
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			return s.sendInput(ctx, argStr(args, "agent_id"), argStr(args, "input"))
		},
	})
	_ = reg.Register(RegisteredTool{
		Definition: defWait(),
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			timeout := 0
			if v, ok := args["timeout_ms"].(float64); ok && int(v) > 0 {
				timeout = int(v)
			}
			return s.waitAgent(ctx, argStr(args, "agent_id"), timeout)
		},
	})
	_ = reg.Register(RegisteredTool{
		Definition: defCloseAgent(),
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			return s.closeAgent(argStr(args, "agent_id"))
		},
	})

	return nil
}
