package anthropic

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/strongdm/kilroy/internal/llm"
	"github.com/strongdm/kilroy/internal/providerspec"
)

type Adapter struct {
	Provider string
	APIKey   string
	BaseURL  string
	Client   *http.Client
}

func init() {
	llm.RegisterEnvAdapterFactory(func() (llm.ProviderAdapter, bool, error) {
		if strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")) == "" {
			return nil, false, nil
		}
		a, err := NewFromEnv()
		if err != nil {
			return nil, true, err
		}
		return a, true, nil
	})
}

func NewFromEnv() (*Adapter, error) {
	key := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
	if key == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY is required")
	}
	return NewWithProvider("anthropic", key, os.Getenv("ANTHROPIC_BASE_URL")), nil
}

func NewWithProvider(provider, apiKey, baseURL string) *Adapter {
	p := providerspec.CanonicalProviderKey(provider)
	if p == "" {
		p = "anthropic"
	}
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = "https://api.anthropic.com"
	}
	return &Adapter{
		Provider: p,
		APIKey:   strings.TrimSpace(apiKey),
		BaseURL:  base,
		// Avoid short client-level timeouts; rely on request context deadlines instead.
		Client: &http.Client{Timeout: 0},
	}
}

func (a *Adapter) Name() string {
	if p := providerspec.CanonicalProviderKey(a.Provider); p != "" {
		return p
	}
	return "anthropic"
}

func (a *Adapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if a.Client == nil {
		// Avoid short client-level timeouts; rely on request context deadlines instead.
		a.Client = &http.Client{Timeout: 0}
	}
	policy := llm.ExecutionPolicy(a.Name())
	req = llm.ApplyExecutionPolicy(req, policy)
	if policy.ForceStream {
		// Kimi Coding has shown request-shape sensitivity on non-stream complete calls,
		// especially on tool-history continuation turns. Enforce stream transport here so
		// callers cannot bypass the contract accidentally.
		return a.completeViaStream(ctx, req)
	}

	system, messages, err := toAnthropicMessages(req.Messages)
	if err != nil {
		return llm.Response{}, err
	}
	system, err = applyAnthropicResponseFormat(system, req.ResponseFormat)
	if err != nil {
		return llm.Response{}, err
	}
	autoCache := anthropicAutoCacheEnabled(a.Name(), req.ProviderOptions)

	maxTokens := 4096
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		maxTokens = *req.MaxTokens
	}

	body := map[string]any{
		"model":      nativeModelID(req.Model),
		"max_tokens": maxTokens,
		"messages":   messages,
	}
	if strings.TrimSpace(system) != "" {
		if autoCache {
			body["system"] = []map[string]any{{
				"type":          "text",
				"text":          system,
				"cache_control": map[string]any{"type": "ephemeral"},
			}}
		} else {
			body["system"] = system
		}
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if len(req.StopSequences) > 0 {
		body["stop_sequences"] = req.StopSequences
	}

	includeTools := len(req.Tools) > 0
	if req.ToolChoice != nil {
		switch strings.ToLower(strings.TrimSpace(req.ToolChoice.Mode)) {
		case "", "auto":
			if includeTools {
				body["tool_choice"] = map[string]any{"type": "auto"}
			}
		case "none":
			// Spec: Anthropic none mode requires omitting tools entirely.
			includeTools = false
		case "required":
			if includeTools {
				body["tool_choice"] = map[string]any{"type": "any"}
			}
		case "named":
			if strings.TrimSpace(req.ToolChoice.Name) == "" {
				return llm.Response{}, &llm.ConfigurationError{Message: "tool_choice mode=named requires name"}
			}
			if includeTools {
				body["tool_choice"] = map[string]any{"type": "tool", "name": req.ToolChoice.Name}
			}
		default:
			return llm.Response{}, llm.NewUnsupportedToolChoiceError("anthropic", req.ToolChoice.Mode)
		}
	}
	if includeTools && len(req.Tools) > 0 {
		tools := toAnthropicTools(req.Tools)
		if autoCache {
			addToolCacheControlBreakpoint(tools)
		}
		body["tools"] = tools
	}
	if req.ProviderOptions != nil {
		if ov, ok := req.ProviderOptions["anthropic"].(map[string]any); ok {
			for k, v := range ov {
				if k == "beta_headers" {
					continue
				}
				if k == "auto_cache" {
					continue
				}
				body[k] = v
			}
		}
	}
	if autoCache {
		addCacheControlBreakpoint(messages)
	}

	b, err := json.Marshal(body)
	if err != nil {
		return llm.Response{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.BaseURL+"/v1/messages", bytes.NewReader(b))
	if err != nil {
		return llm.Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", a.APIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	beta := betaHeaderFromProviderOptions(req.ProviderOptions)
	if autoCache {
		beta = appendBetaHeader(beta, "prompt-caching-2024-07-31")
	}
	if strings.TrimSpace(beta) != "" {
		httpReq.Header.Set("anthropic-beta", beta)
	}

	resp, err := a.Client.Do(httpReq)
	if err != nil {
		return llm.Response{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	rawBytes, _ := io.ReadAll(resp.Body)
	var raw map[string]any
	_ = json.Unmarshal(rawBytes, &raw)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		ra := llm.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		msg := fmt.Sprintf("messages.create failed: %s", strings.TrimSpace(string(rawBytes)))
		return llm.Response{}, llm.ErrorFromHTTPStatus(a.Name(), resp.StatusCode, msg, raw, ra)
	}

	return fromAnthropicResponse(a.Name(), raw, req.Model), nil
}

func (a *Adapter) completeViaStream(ctx context.Context, req llm.Request) (llm.Response, error) {
	st, err := a.Stream(ctx, req)
	if err != nil {
		return llm.Response{}, err
	}
	defer func() { _ = st.Close() }()

	acc := llm.NewStreamAccumulator()
	var streamErr error
	sawFinish := false
	for ev := range st.Events() {
		acc.Process(ev)
		switch ev.Type {
		case llm.StreamEventFinish:
			sawFinish = true
			if ev.Response != nil {
				return *ev.Response, nil
			}
		case llm.StreamEventError:
			if ev.Err != nil {
				streamErr = ev.Err
			}
		}
	}
	if streamErr != nil {
		return llm.Response{}, streamErr
	}
	if resp := acc.Response(); resp != nil {
		return *resp, nil
	}
	if sawFinish {
		return llm.Response{}, llm.NewStreamError(a.Name(), "missing response in finish event")
	}
	return llm.Response{}, llm.NewStreamError(a.Name(), "stream ended without finish event")
}

func (a *Adapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	if a.Client == nil {
		a.Client = &http.Client{Timeout: 0}
	}
	policy := llm.ExecutionPolicy(a.Name())
	req = llm.ApplyExecutionPolicy(req, policy)
	sctx, cancel := context.WithCancel(ctx)

	system, messages, err := toAnthropicMessages(req.Messages)
	if err != nil {
		cancel()
		return nil, err
	}
	system, err = applyAnthropicResponseFormat(system, req.ResponseFormat)
	if err != nil {
		cancel()
		return nil, err
	}
	autoCache := anthropicAutoCacheEnabled(a.Name(), req.ProviderOptions)

	maxTokens := 4096
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		maxTokens = *req.MaxTokens
	}

	body := map[string]any{
		"model":      nativeModelID(req.Model),
		"max_tokens": maxTokens,
		"messages":   messages,
		"stream":     true,
	}
	if strings.TrimSpace(system) != "" {
		if autoCache {
			body["system"] = []map[string]any{{
				"type":          "text",
				"text":          system,
				"cache_control": map[string]any{"type": "ephemeral"},
			}}
		} else {
			body["system"] = system
		}
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if len(req.StopSequences) > 0 {
		body["stop_sequences"] = req.StopSequences
	}

	includeTools := len(req.Tools) > 0
	if req.ToolChoice != nil {
		switch strings.ToLower(strings.TrimSpace(req.ToolChoice.Mode)) {
		case "", "auto":
			if includeTools {
				body["tool_choice"] = map[string]any{"type": "auto"}
			}
		case "none":
			includeTools = false
		case "required":
			if includeTools {
				body["tool_choice"] = map[string]any{"type": "any"}
			}
		case "named":
			if strings.TrimSpace(req.ToolChoice.Name) == "" {
				cancel()
				return nil, &llm.ConfigurationError{Message: "tool_choice mode=named requires name"}
			}
			if includeTools {
				body["tool_choice"] = map[string]any{"type": "tool", "name": req.ToolChoice.Name}
			}
		default:
			cancel()
			return nil, llm.NewUnsupportedToolChoiceError("anthropic", req.ToolChoice.Mode)
		}
	}
	if includeTools && len(req.Tools) > 0 {
		tools := toAnthropicTools(req.Tools)
		if autoCache {
			addToolCacheControlBreakpoint(tools)
		}
		body["tools"] = tools
	}
	if req.ProviderOptions != nil {
		if ov, ok := req.ProviderOptions["anthropic"].(map[string]any); ok {
			for k, v := range ov {
				if k == "beta_headers" {
					continue
				}
				if k == "auto_cache" {
					continue
				}
				body[k] = v
			}
		}
	}
	if autoCache {
		addCacheControlBreakpoint(messages)
	}

	b, err := json.Marshal(body)
	if err != nil {
		cancel()
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(sctx, http.MethodPost, a.BaseURL+"/v1/messages", bytes.NewReader(b))
	if err != nil {
		cancel()
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", a.APIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	beta := betaHeaderFromProviderOptions(req.ProviderOptions)
	if autoCache {
		beta = appendBetaHeader(beta, "prompt-caching-2024-07-31")
	}
	if strings.TrimSpace(beta) != "" {
		httpReq.Header.Set("anthropic-beta", beta)
	}

	resp, err := a.Client.Do(httpReq)
	if err != nil {
		cancel()
		return nil, llm.WrapContextError(a.Name(), err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		rawBytes, _ := io.ReadAll(resp.Body)
		var raw map[string]any
		_ = json.Unmarshal(rawBytes, &raw)
		ra := llm.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		msg := fmt.Sprintf("messages.create(stream) failed: %s", strings.TrimSpace(string(rawBytes)))
		cancel()
		return nil, llm.ErrorFromHTTPStatus(a.Name(), resp.StatusCode, msg, raw, ra)
	}

	s := llm.NewChanStream(cancel)
	s.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart})

	go func() {
		defer func() {
			_ = resp.Body.Close()
			s.CloseSend()
		}()

		finished := false
		type blockState struct {
			typ string

			// text
			textID      string
			textStarted bool
			text        strings.Builder

			// tool_use
			toolID      string
			toolName    string
			toolStarted bool
			toolArgs    strings.Builder
			toolArgsSrc struct {
				fromStart bool
				fromDelta bool
			}

			// thinking / redacted_thinking
			thinkingStarted bool
			thinking        strings.Builder
			signature       strings.Builder
			redacted        bool
		}
		blocks := map[int]*blockState{}
		maxIdx := -1

		getInt := func(v any) int {
			switch x := v.(type) {
			case json.Number:
				n, _ := x.Int64()
				return int(n)
			case float64:
				return int(x)
			case int:
				return x
			default:
				return 0
			}
		}
		getBlock := func(idx int) *blockState {
			st := blocks[idx]
			if st == nil {
				st = &blockState{}
				blocks[idx] = st
			}
			if idx > maxIdx {
				maxIdx = idx
			}
			return st
		}

		var usage llm.Usage
		finish := llm.FinishReason{Reason: "stop"}

		_ = llm.ParseSSE(sctx, resp.Body, func(ev llm.SSEEvent) error {
			if len(ev.Data) == 0 {
				return nil
			}
			var payload map[string]any
			dec := json.NewDecoder(bytes.NewReader(ev.Data))
			dec.UseNumber()
			if err := dec.Decode(&payload); err != nil {
				s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: map[string]any{"event": ev.Event, "data": string(ev.Data)}})
				return nil
			}

			switch ev.Event {
			case "message_start":
				// Capture input token usage when present.
				if msgAny, ok := payload["message"].(map[string]any); ok {
					if u, ok := msgAny["usage"].(map[string]any); ok {
						u2 := parseUsage(u)
						if u2.InputTokens > 0 {
							usage.InputTokens = u2.InputTokens
						}
					}
				}
			case "content_block_start":
				idx := getInt(payload["index"])
				cb, _ := payload["content_block"].(map[string]any)
				typ, _ := cb["type"].(string)
				st := getBlock(idx)
				st.typ = typ
				if cb, ok := payload["content_block"].(map[string]any); ok {
					switch typ {
					case "text":
						if st.textID == "" {
							st.textID = fmt.Sprintf("text_%d", idx)
						}
						if !st.textStarted {
							st.textStarted = true
							s.Send(llm.StreamEvent{Type: llm.StreamEventTextStart, TextID: st.textID})
						}
					case "tool_use":
						st.toolID, _ = cb["id"].(string)
						st.toolName, _ = cb["name"].(string)
						if inAny, ok := cb["input"]; ok && inAny != nil && st.toolArgs.Len() == 0 {
							if b, err := json.Marshal(inAny); err == nil && len(b) > 0 && string(b) != "null" {
								st.toolArgs.Write(b)
								st.toolArgsSrc.fromStart = true
							}
						}
						if !st.toolStarted && strings.TrimSpace(st.toolID) != "" {
							st.toolStarted = true
							tc := llm.ToolCallData{ID: st.toolID, Name: st.toolName, Type: "function"}
							s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &tc})
							if st.toolArgs.Len() > 0 {
								tc2 := llm.ToolCallData{ID: st.toolID, Name: st.toolName, Arguments: []byte(st.toolArgs.String()), Type: "function"}
								s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallDelta, ToolCall: &tc2})
							}
						}
					case "thinking", "redacted_thinking":
						st.redacted = (typ == "redacted_thinking")
						if sig, _ := cb["signature"].(string); sig != "" && st.signature.Len() == 0 {
							st.signature.WriteString(sig)
						}
						if !st.thinkingStarted {
							st.thinkingStarted = true
							s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningStart})
						}
						// Some implementations may include initial thinking in the start block.
						t, _ := cb["thinking"].(string)
						if t == "" {
							t, _ = cb["text"].(string)
						}
						if t == "" {
							t, _ = cb["data"].(string)
						}
						if t != "" {
							st.thinking.WriteString(t)
							s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningDelta, ReasoningDelta: t})
						}
					}
				}
			case "content_block_delta":
				idx := getInt(payload["index"])
				st := getBlock(idx)
				if d, ok := payload["delta"].(map[string]any); ok {
					switch typ, _ := d["type"].(string); typ {
					case "text_delta":
						if delta, _ := d["text"].(string); delta != "" {
							if st.textID == "" {
								st.textID = fmt.Sprintf("text_%d", idx)
							}
							if !st.textStarted {
								st.textStarted = true
								s.Send(llm.StreamEvent{Type: llm.StreamEventTextStart, TextID: st.textID})
							}
							st.text.WriteString(delta)
							s.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, TextID: st.textID, Delta: delta})
						}
					case "input_json_delta":
						if delta, _ := d["partial_json"].(string); delta != "" {
							// Some providers emit tool_use.input in content_block_start and then stream
							// a canonical JSON payload via input_json_delta. Treat the first delta as
							// authoritative to avoid concatenating two top-level JSON values.
							if st.toolArgsSrc.fromStart && !st.toolArgsSrc.fromDelta {
								st.toolArgs.Reset()
								st.toolArgsSrc.fromStart = false
							}
							st.toolArgs.WriteString(delta)
							st.toolArgsSrc.fromDelta = true
							if !st.toolStarted && strings.TrimSpace(st.toolID) != "" {
								st.toolStarted = true
								tc := llm.ToolCallData{ID: st.toolID, Name: st.toolName, Type: "function"}
								s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &tc})
							}
							if strings.TrimSpace(st.toolID) != "" {
								tc := llm.ToolCallData{ID: st.toolID, Name: st.toolName, Arguments: []byte(st.toolArgs.String()), Type: "function"}
								s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallDelta, ToolCall: &tc})
							}
						}
					case "thinking_delta":
						delta, _ := d["thinking"].(string)
						if delta == "" {
							delta, _ = d["text"].(string)
						}
						if delta != "" {
							if !st.thinkingStarted {
								st.thinkingStarted = true
								s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningStart})
							}
							st.thinking.WriteString(delta)
							s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningDelta, ReasoningDelta: delta})
						}
					case "signature_delta":
						if delta, _ := d["signature"].(string); delta != "" {
							st.signature.WriteString(delta)
						}
					}
				}
			case "content_block_stop":
				idx := getInt(payload["index"])
				st := blocks[idx]
				if st == nil {
					return nil
				}
				switch st.typ {
				case "text":
					if st.textStarted {
						if st.textID == "" {
							st.textID = fmt.Sprintf("text_%d", idx)
						}
						s.Send(llm.StreamEvent{Type: llm.StreamEventTextEnd, TextID: st.textID})
						st.textStarted = false
					}
				case "tool_use":
					if strings.TrimSpace(st.toolID) != "" {
						if !st.toolStarted {
							st.toolStarted = true
							tc := llm.ToolCallData{ID: st.toolID, Name: st.toolName, Type: "function"}
							s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &tc})
						}
						tc := llm.ToolCallData{ID: st.toolID, Name: st.toolName, Arguments: []byte(st.toolArgs.String()), Type: "function"}
						s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &tc})
						st.toolStarted = false
					}
				case "thinking", "redacted_thinking":
					if st.thinkingStarted {
						s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningEnd})
						st.thinkingStarted = false
					}
				}
			case "message_delta":
				if sr, _ := payload["stop_reason"].(string); sr != "" {
					finish = llm.NormalizeFinishReason("anthropic", sr)
				}
				if u, ok := payload["usage"].(map[string]any); ok {
					u2 := parseUsage(u)
					if u2.OutputTokens > 0 {
						usage.OutputTokens = u2.OutputTokens
					}
					if u2.InputTokens > 0 {
						usage.InputTokens = u2.InputTokens
					}
				}
			case "message_stop":
				var parts []llm.ContentPart
				for i := 0; i <= maxIdx; i++ {
					st := blocks[i]
					if st == nil {
						continue
					}
					switch st.typ {
					case "text":
						if st.text.Len() > 0 {
							parts = append(parts, llm.ContentPart{Kind: llm.ContentText, Text: st.text.String()})
						}
					case "tool_use":
						if strings.TrimSpace(st.toolID) != "" {
							args := st.toolArgs.String()
							parts = append(parts, llm.ContentPart{
								Kind: llm.ContentToolCall,
								ToolCall: &llm.ToolCallData{
									ID:        st.toolID,
									Name:      st.toolName,
									Arguments: json.RawMessage(args),
									Type:      "function",
								},
							})
						}
					case "thinking":
						if st.thinking.Len() > 0 {
							parts = append(parts, llm.ContentPart{
								Kind: llm.ContentThinking,
								Thinking: &llm.ThinkingData{
									Text:      st.thinking.String(),
									Signature: st.signature.String(),
									Redacted:  false,
								},
							})
						}
					case "redacted_thinking":
						if st.thinking.Len() > 0 {
							parts = append(parts, llm.ContentPart{
								Kind: llm.ContentRedThinking,
								Thinking: &llm.ThinkingData{
									Text:     st.thinking.String(),
									Redacted: true,
								},
							})
						}
					}
				}

				msg := llm.Message{Role: llm.RoleAssistant, Content: parts}
				r := llm.Response{
					Provider: a.Name(),
					Model:    req.Model,
					Message:  msg,
					Finish:   finish,
					Usage:    usage,
				}
				if len(r.ToolCalls()) > 0 {
					r.Finish = llm.FinishReason{Reason: "tool_calls", Raw: "tool_use"}
				}
				rp := r
				s.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &r.Finish, Usage: &r.Usage, Response: &rp})
				finished = true
				cancel()
			default:
				s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: payload})
			}
			return nil
		})

		if !finished {
			if err := sctx.Err(); err != nil {
				s.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: llm.WrapContextError(a.Name(), err)})
			}
		}
	}()

	return s, nil
}

func applyAnthropicResponseFormat(system string, rf *llm.ResponseFormat) (string, error) {
	if rf == nil {
		return system, nil
	}
	typ := strings.ToLower(strings.TrimSpace(rf.Type))
	switch typ {
	case "", "text":
		return system, nil
	case "json":
		return strings.TrimSpace(system + "\n\nOutput only valid JSON. Do not include any extra text."), nil
	case "json_schema":
		if rf.JSONSchema == nil {
			return system, nil
		}
		b, err := json.Marshal(rf.JSONSchema)
		if err != nil {
			return "", err
		}
		inst := "Output only valid JSON that matches this JSON Schema. Do not include any extra text.\n\nJSON Schema:\n" + string(b)
		return strings.TrimSpace(system + "\n\n" + inst), nil
	default:
		return system, nil
	}
}

func betaHeaderFromProviderOptions(opts map[string]any) string {
	if opts == nil {
		return ""
	}
	aAny, ok := opts["anthropic"]
	if !ok {
		return ""
	}
	m, ok := aAny.(map[string]any)
	if !ok {
		return ""
	}
	v, ok := m["beta_headers"]
	if !ok {
		return ""
	}
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case []string:
		return strings.Join(x, ",")
	case []any:
		var parts []string
		for _, it := range x {
			if s, ok := it.(string); ok && strings.TrimSpace(s) != "" {
				parts = append(parts, strings.TrimSpace(s))
			}
		}
		return strings.Join(parts, ",")
	default:
		return ""
	}
}

func toAnthropicTools(tools []llm.ToolDefinition) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		params := t.Parameters
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, map[string]any{
			"name":         t.Name,
			"description":  t.Description,
			"input_schema": params,
		})
	}
	return out
}

func toAnthropicMessages(msgs []llm.Message) (system string, messages []map[string]any, _ error) {
	var sysParts []string
	appendMessage := func(role string, content []map[string]any) {
		if len(content) == 0 {
			return
		}
		// Anthropic requires user/assistant alternation; merge same-role neighbors.
		if len(messages) > 0 {
			last := messages[len(messages)-1]
			if lastRole, _ := last["role"].(string); lastRole == role {
				if lastContent, ok := last["content"].([]map[string]any); ok {
					last["content"] = append(lastContent, content...)
					return
				}
			}
		}
		messages = append(messages, map[string]any{
			"role":    role,
			"content": content,
		})
	}

	for _, m := range msgs {
		switch m.Role {
		case llm.RoleSystem, llm.RoleDeveloper:
			if t := strings.TrimSpace(m.Text()); t != "" {
				sysParts = append(sysParts, t)
			}
		case llm.RoleUser:
			var blocks []map[string]any
			for _, p := range m.Content {
				switch p.Kind {
				case llm.ContentText:
					if strings.TrimSpace(p.Text) != "" {
						blocks = append(blocks, map[string]any{"type": "text", "text": p.Text})
					}
				case llm.ContentImage:
					if p.Image == nil {
						continue
					}
					u := strings.TrimSpace(p.Image.URL)
					if len(p.Image.Data) > 0 || llm.IsLocalPath(u) {
						var b []byte
						var err error
						mt := strings.TrimSpace(p.Image.MediaType)
						if len(p.Image.Data) > 0 {
							b = p.Image.Data
							if mt == "" {
								mt = "image/png"
							}
						} else {
							path := llm.ExpandTilde(u)
							b, err = os.ReadFile(path)
							if err != nil {
								return "", nil, err
							}
							if mt == "" {
								mt = llm.InferMimeTypeFromPath(path)
							}
							if mt == "" {
								mt = "image/png"
							}
						}
						blocks = append(blocks, map[string]any{
							"type": "image",
							"source": map[string]any{
								"type":       "base64",
								"media_type": mt,
								"data":       base64.StdEncoding.EncodeToString(b),
							},
						})
					} else if u != "" {
						blocks = append(blocks, map[string]any{
							"type": "image",
							"source": map[string]any{
								"type": "url",
								"url":  u,
							},
						})
					}
				case llm.ContentAudio, llm.ContentDocument:
					return "", nil, &llm.ConfigurationError{Message: fmt.Sprintf("unsupported content kind for anthropic: %s", p.Kind)}
				default:
					// ignore
				}
			}
			appendMessage("user", blocks)
		case llm.RoleAssistant:
			var blocks []map[string]any
			for _, p := range m.Content {
				switch p.Kind {
				case llm.ContentText:
					if strings.TrimSpace(p.Text) != "" {
						blocks = append(blocks, map[string]any{"type": "text", "text": p.Text})
					}
				case llm.ContentImage:
					if p.Image == nil {
						continue
					}
					u := strings.TrimSpace(p.Image.URL)
					if len(p.Image.Data) > 0 || llm.IsLocalPath(u) {
						var b []byte
						var err error
						mt := strings.TrimSpace(p.Image.MediaType)
						if len(p.Image.Data) > 0 {
							b = p.Image.Data
							if mt == "" {
								mt = "image/png"
							}
						} else {
							path := llm.ExpandTilde(u)
							b, err = os.ReadFile(path)
							if err != nil {
								return "", nil, err
							}
							if mt == "" {
								mt = llm.InferMimeTypeFromPath(path)
							}
							if mt == "" {
								mt = "image/png"
							}
						}
						blocks = append(blocks, map[string]any{
							"type": "image",
							"source": map[string]any{
								"type":       "base64",
								"media_type": mt,
								"data":       base64.StdEncoding.EncodeToString(b),
							},
						})
					} else if u != "" {
						blocks = append(blocks, map[string]any{
							"type": "image",
							"source": map[string]any{
								"type": "url",
								"url":  u,
							},
						})
					}
				case llm.ContentToolCall:
					if p.ToolCall == nil {
						continue
					}
					var in any
					if len(p.ToolCall.Arguments) > 0 {
						_ = json.Unmarshal(p.ToolCall.Arguments, &in)
					}
					blocks = append(blocks, map[string]any{
						"type":  "tool_use",
						"id":    p.ToolCall.ID,
						"name":  p.ToolCall.Name,
						"input": in,
					})
				case llm.ContentThinking:
					if p.Thinking == nil {
						continue
					}
					blocks = append(blocks, map[string]any{
						"type":      "thinking",
						"thinking":  p.Thinking.Text,
						"signature": p.Thinking.Signature,
					})
				case llm.ContentRedThinking:
					if p.Thinking == nil {
						continue
					}
					blocks = append(blocks, map[string]any{
						"type": "redacted_thinking",
						"data": p.Thinking.Text,
					})
				case llm.ContentAudio, llm.ContentDocument:
					return "", nil, &llm.ConfigurationError{Message: fmt.Sprintf("unsupported content kind for anthropic: %s", p.Kind)}
				default:
					// ignore
				}
			}
			appendMessage("assistant", blocks)
		case llm.RoleTool:
			// Tool results are provided as user messages with tool_result blocks.
			var blocks []map[string]any
			for _, p := range m.Content {
				if p.Kind != llm.ContentToolResult || p.ToolResult == nil {
					continue
				}
				blocks = append(blocks, map[string]any{
					"type":        "tool_result",
					"tool_use_id": p.ToolResult.ToolCallID,
					"content":     fmt.Sprint(p.ToolResult.Content),
					"is_error":    p.ToolResult.IsError,
				})
			}
			appendMessage("user", blocks)
		default:
			// ignore
		}
	}

	return strings.Join(sysParts, "\n\n"), messages, nil
}

func fromAnthropicResponse(provider string, raw map[string]any, requestedModel string) llm.Response {
	r := llm.Response{
		Provider: provider,
		Model:    requestedModel,
		Raw:      raw,
	}
	if id, _ := raw["id"].(string); id != "" {
		r.ID = id
	}
	if m, _ := raw["model"].(string); m != "" {
		r.Model = m
	}

	msg := llm.Message{Role: llm.RoleAssistant}
	if content, ok := raw["content"].([]any); ok {
		for _, itAny := range content {
			it, ok := itAny.(map[string]any)
			if !ok {
				continue
			}
			typ, _ := it["type"].(string)
			switch typ {
			case "text":
				if t, _ := it["text"].(string); t != "" {
					msg.Content = append(msg.Content, llm.ContentPart{Kind: llm.ContentText, Text: t})
				}
			case "tool_use":
				id, _ := it["id"].(string)
				name, _ := it["name"].(string)
				argsAny := it["input"]
				argsRaw, _ := json.Marshal(argsAny)
				msg.Content = append(msg.Content, llm.ContentPart{
					Kind: llm.ContentToolCall,
					ToolCall: &llm.ToolCallData{
						ID:        id,
						Name:      name,
						Arguments: argsRaw,
						Type:      "function",
					},
				})
			case "thinking":
				t, _ := it["text"].(string)
				if t == "" {
					t, _ = it["thinking"].(string)
				}
				if t != "" {
					sig, _ := it["signature"].(string)
					msg.Content = append(msg.Content, llm.ContentPart{
						Kind: llm.ContentThinking,
						Thinking: &llm.ThinkingData{
							Text:      t,
							Signature: sig,
							Redacted:  false,
						},
					})
				}
			case "redacted_thinking":
				if d, _ := it["data"].(string); d != "" {
					msg.Content = append(msg.Content, llm.ContentPart{
						Kind: llm.ContentRedThinking,
						Thinking: &llm.ThinkingData{
							Text:     d,
							Redacted: true,
						},
					})
				}
			default:
				// ignore
			}
		}
	}

	r.Message = msg
	if len(r.ToolCalls()) > 0 {
		r.Finish = llm.FinishReason{Reason: "tool_calls", Raw: "tool_use"}
	} else {
		sr, _ := raw["stop_reason"].(string)
		r.Finish = llm.NormalizeFinishReason("anthropic", sr)
	}

	if u, ok := raw["usage"].(map[string]any); ok {
		r.Usage = parseUsage(u)
	}
	return r
}

func anthropicAutoCacheEnabled(provider string, opts map[string]any) bool {
	defaultEnabled := providerspec.CanonicalProviderKey(provider) == "anthropic"
	if opts == nil {
		return defaultEnabled
	}
	aAny, ok := opts["anthropic"]
	if !ok {
		return defaultEnabled
	}
	m, ok := aAny.(map[string]any)
	if !ok {
		return defaultEnabled
	}
	vAny, ok := m["auto_cache"]
	if !ok {
		return defaultEnabled
	}
	v, ok := vAny.(bool)
	if !ok {
		return defaultEnabled
	}
	return v
}

func appendBetaHeader(existing, add string) string {
	existing = strings.TrimSpace(existing)
	add = strings.TrimSpace(add)
	if add == "" {
		return existing
	}

	seen := map[string]struct{}{}
	var parts []string
	for _, p := range strings.Split(existing, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		parts = append(parts, p)
	}
	if _, ok := seen[add]; !ok {
		parts = append(parts, add)
	}
	return strings.Join(parts, ",")
}

func addCacheControlBreakpoint(messages []map[string]any) {
	// Heuristic: cache everything up to (but excluding) the last user message,
	// since that last user message is typically the only new content per turn.
	lastUser := -1
	for i := len(messages) - 1; i >= 0; i-- {
		role, _ := messages[i]["role"].(string)
		if role == "user" {
			lastUser = i
			break
		}
	}
	if lastUser <= 0 {
		return
	}

	target := lastUser - 1
	contentAny, ok := messages[target]["content"]
	if !ok {
		return
	}

	setOnBlocks := func(blocks []map[string]any) {
		if len(blocks) == 0 {
			return
		}
		// Prefer a text block so we don't attach cache_control to tool/image blocks
		// unless we have to.
		idx := len(blocks) - 1
		for i := len(blocks) - 1; i >= 0; i-- {
			if typ, _ := blocks[i]["type"].(string); typ == "text" {
				idx = i
				break
			}
		}
		if _, exists := blocks[idx]["cache_control"]; exists {
			return
		}
		blocks[idx]["cache_control"] = map[string]any{"type": "ephemeral"}
	}

	switch c := contentAny.(type) {
	case []map[string]any:
		setOnBlocks(c)
	case []any:
		var blocks []map[string]any
		for _, it := range c {
			bm, ok := it.(map[string]any)
			if !ok {
				continue
			}
			blocks = append(blocks, bm)
		}
		setOnBlocks(blocks)
	default:
		return
	}
}

func addToolCacheControlBreakpoint(tools []map[string]any) {
	if len(tools) == 0 {
		return
	}
	// Anthropic allows at most four cache_control blocks per request.
	// One checkpoint on the last tool caches the entire tool-definition prefix
	// without exhausting the limit on large toolsets.
	idx := len(tools) - 1
	if _, exists := tools[idx]["cache_control"]; exists {
		return
	}
	tools[idx]["cache_control"] = map[string]any{"type": "ephemeral"}
}

func parseUsage(u map[string]any) llm.Usage {
	getInt := func(v any) int {
		switch x := v.(type) {
		case float64:
			return int(x)
		case int:
			return x
		default:
			return 0
		}
	}
	usage := llm.Usage{
		InputTokens:  getInt(u["input_tokens"]),
		OutputTokens: getInt(u["output_tokens"]),
		TotalTokens:  getInt(u["input_tokens"]) + getInt(u["output_tokens"]),
		Raw:          map[string]any{},
	}
	if vAny, ok := u["cache_read_input_tokens"]; ok {
		v := getInt(vAny)
		usage.CacheReadTokens = &v
	}
	if vAny, ok := u["cache_creation_input_tokens"]; ok {
		v := getInt(vAny)
		usage.CacheWriteTokens = &v
	}
	return usage
}

// nativeModelID translates OpenRouter-format Anthropic model IDs (dots in version
// numbers, e.g. "claude-sonnet-4.5") to the native API format (dashes, e.g.
// "claude-sonnet-4-5"). IDs already in native format pass through unchanged.
func nativeModelID(id string) string {
	return strings.ReplaceAll(id, ".", "-")
}
