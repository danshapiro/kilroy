package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/strongdm/kilroy/internal/llm"
)

type Config struct {
	Provider     string
	APIKey       string
	BaseURL      string
	Path         string
	OptionsKey   string
	ExtraHeaders map[string]string
}

type Adapter struct {
	cfg    Config
	client *http.Client
}

const defaultRequestTimeout = 10 * time.Minute

func NewAdapter(cfg Config) *Adapter {
	cfg.Provider = strings.ToLower(strings.TrimSpace(cfg.Provider))
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if strings.TrimSpace(cfg.Path) == "" {
		cfg.Path = "/v1/chat/completions"
	}
	if strings.TrimSpace(cfg.OptionsKey) == "" {
		cfg.OptionsKey = strings.TrimSpace(cfg.Provider)
	}
	if cfg.Provider == "" {
		cfg.Provider = cfg.OptionsKey
	}
	return &Adapter{
		cfg:    cfg,
		client: &http.Client{Timeout: 0},
	}
}

func (a *Adapter) Name() string { return a.cfg.Provider }

func (a *Adapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	requestCtx, cancel := withDefaultRequestDeadline(ctx)
	defer cancel()

	body, err := toChatCompletionsBody(req, a.cfg.OptionsKey, chatCompletionsBodyOptions{})
	if err != nil {
		return llm.Response{}, err
	}

	httpReq, err := http.NewRequestWithContext(requestCtx, http.MethodPost, a.cfg.BaseURL+a.cfg.Path, bytes.NewReader(body))
	if err != nil {
		return llm.Response{}, llm.WrapContextError(a.cfg.Provider, err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+a.cfg.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")
	for k, v := range a.cfg.ExtraHeaders {
		httpReq.Header.Set(k, v)
	}

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return llm.Response{}, llm.WrapContextError(a.cfg.Provider, err)
	}
	defer resp.Body.Close()

	return parseChatCompletionsResponse(a.cfg.Provider, req.Model, resp)
}

func (a *Adapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	baseCtx, baseCancel := withDefaultRequestDeadline(ctx)
	sctx, cancel := context.WithCancel(baseCtx)
	cancelAll := func() {
		cancel()
		baseCancel()
	}
	body, err := toChatCompletionsBody(req, a.cfg.OptionsKey, chatCompletionsBodyOptions{
		Stream:       true,
		IncludeUsage: true,
	})
	if err != nil {
		cancelAll()
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(sctx, http.MethodPost, a.cfg.BaseURL+a.cfg.Path, bytes.NewReader(body))
	if err != nil {
		cancelAll()
		return nil, llm.WrapContextError(a.cfg.Provider, err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+a.cfg.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")
	for k, v := range a.cfg.ExtraHeaders {
		httpReq.Header.Set(k, v)
	}

	resp, err := a.client.Do(httpReq)
	if err != nil {
		cancelAll()
		return nil, llm.WrapContextError(a.cfg.Provider, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		cancelAll()
		_, perr := parseChatCompletionsResponse(a.cfg.Provider, req.Model, resp)
		return nil, perr
	}

	s := llm.NewChanStream(cancelAll)
	go func() {
		defer cancelAll()
		defer resp.Body.Close()
		defer s.CloseSend()

		s.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart})
		state := &chatStreamState{
			Provider: a.cfg.Provider,
			Model:    req.Model,
			TextID:   "assistant_text",
		}

		err := llm.ParseSSE(sctx, resp.Body, func(ev llm.SSEEvent) error {
			payload := strings.TrimSpace(string(ev.Data))
			if payload == "" {
				return nil
			}
			if payload == "[DONE]" {
				if state.TextOpen {
					s.Send(llm.StreamEvent{Type: llm.StreamEventTextEnd, TextID: state.TextID})
					state.TextOpen = false
				}
				state.closeOpenToolCalls(s)
				final := state.FinalResponse()
				s.Send(llm.StreamEvent{
					Type:         llm.StreamEventFinish,
					FinishReason: &final.Finish,
					Usage:        &final.Usage,
					Response:     &final,
				})
				return nil
			}

			var chunk map[string]any
			dec := json.NewDecoder(strings.NewReader(payload))
			dec.UseNumber()
			if err := dec.Decode(&chunk); err != nil {
				return err
			}
			emitChatCompletionsChunkEvents(s, state, chunk)
			return nil
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			s.Send(llm.StreamEvent{
				Type: llm.StreamEventError,
				Err:  llm.NewStreamError(a.cfg.Provider, err.Error()),
			})
		}
	}()
	return s, nil
}

type chatCompletionsBodyOptions struct {
	Stream       bool
	IncludeUsage bool
}

func toChatCompletionsBody(req llm.Request, optionsKey string, opts chatCompletionsBodyOptions) ([]byte, error) {
	body := map[string]any{
		"model":    req.Model,
		"messages": toChatCompletionsMessages(req.Messages),
	}
	if len(req.Tools) > 0 {
		body["tools"] = toChatCompletionsTools(req.Tools)
	}
	if req.ToolChoice != nil {
		body["tool_choice"] = toChatCompletionsToolChoice(*req.ToolChoice)
	}
	if req.ProviderOptions != nil {
		if ov, ok := req.ProviderOptions[optionsKey].(map[string]any); ok {
			for k, v := range ov {
				body[k] = v
			}
		}
	}
	if opts.Stream {
		body["stream"] = true
		if opts.IncludeUsage {
			body["stream_options"] = map[string]any{"include_usage": true}
		}
	}
	return json.Marshal(body)
}

func parseChatCompletionsResponse(provider, model string, resp *http.Response) (llm.Response, error) {
	rawBytes, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return llm.Response{}, llm.WrapContextError(provider, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw := map[string]any{}
		dec := json.NewDecoder(bytes.NewReader(rawBytes))
		dec.UseNumber()
		if err := dec.Decode(&raw); err != nil {
			raw["raw_body"] = string(rawBytes)
		}
		ra := llm.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		return llm.Response{}, llm.ErrorFromHTTPStatus(provider, resp.StatusCode, "chat.completions failed", raw, ra)
	}
	var raw map[string]any
	dec := json.NewDecoder(bytes.NewReader(rawBytes))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return llm.Response{}, llm.WrapContextError(provider, err)
	}
	return fromChatCompletions(provider, model, raw)
}

func toChatCompletionsMessages(msgs []llm.Message) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		entry := map[string]any{"role": string(m.Role)}
		textParts := []string{}
		toolCalls := []map[string]any{}
		for _, p := range m.Content {
			switch p.Kind {
			case llm.ContentText:
				if strings.TrimSpace(p.Text) != "" {
					textParts = append(textParts, p.Text)
				}
			case llm.ContentToolCall:
				if p.ToolCall != nil {
					toolCalls = append(toolCalls, map[string]any{
						"id":   p.ToolCall.ID,
						"type": "function",
						"function": map[string]any{
							"name":      p.ToolCall.Name,
							"arguments": string(p.ToolCall.Arguments),
						},
					})
				}
			case llm.ContentToolResult:
				if p.ToolResult != nil {
					entry["role"] = "tool"
					entry["tool_call_id"] = p.ToolResult.ToolCallID
					entry["content"] = renderAnyAsText(p.ToolResult.Content)
				}
			}
		}
		if _, ok := entry["content"]; !ok {
			entry["content"] = strings.Join(textParts, "\n")
		}
		if len(toolCalls) > 0 {
			entry["tool_calls"] = toolCalls
		}
		out = append(out, entry)
	}
	return out
}

func toChatCompletionsTools(tools []llm.ToolDefinition) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, td := range tools {
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        td.Name,
				"description": td.Description,
				"parameters":  td.Parameters,
			},
		})
	}
	return out
}

func toChatCompletionsToolChoice(tc llm.ToolChoice) any {
	mode := strings.ToLower(strings.TrimSpace(tc.Mode))
	switch mode {
	case "", "auto":
		return "auto"
	case "none":
		return "none"
	case "required":
		return "required"
	case "named":
		return map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": tc.Name,
			},
		}
	default:
		return "auto"
	}
}

func fromChatCompletions(provider, model string, raw map[string]any) (llm.Response, error) {
	choicesAny, ok := raw["choices"].([]any)
	if !ok || len(choicesAny) == 0 {
		return llm.Response{}, fmt.Errorf("chat.completions response missing choices")
	}
	choice, ok := choicesAny[0].(map[string]any)
	if !ok {
		return llm.Response{}, fmt.Errorf("chat.completions first choice malformed")
	}
	msgMap, _ := choice["message"].(map[string]any)
	msg := llm.Assistant(asString(msgMap["content"]))

	if callsAny, ok := msgMap["tool_calls"].([]any); ok {
		for _, c := range callsAny {
			cm, _ := c.(map[string]any)
			fn, _ := cm["function"].(map[string]any)
			msg.Content = append(msg.Content, llm.ContentPart{
				Kind: llm.ContentToolCall,
				ToolCall: &llm.ToolCallData{
					ID:        asString(cm["id"]),
					Type:      asString(cm["type"]),
					Name:      asString(fn["name"]),
					Arguments: json.RawMessage(renderAnyAsText(fn["arguments"])),
				},
			})
		}
	}

	usageMap, _ := raw["usage"].(map[string]any)
	return llm.Response{
		ID:       asString(raw["id"]),
		Model:    firstNonEmpty(model, asString(raw["model"])),
		Provider: provider,
		Message:  msg,
		Finish: llm.FinishReason{
			Reason: normalizeFinishReason(asString(choice["finish_reason"])),
			Raw:    asString(choice["finish_reason"]),
		},
		Usage: llm.Usage{
			InputTokens:  intFromAny(usageMap["prompt_tokens"]),
			OutputTokens: intFromAny(usageMap["completion_tokens"]),
			TotalTokens:  intFromAny(usageMap["total_tokens"]),
		},
		Raw: raw,
	}, nil
}

func renderAnyAsText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

func asString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	default:
		return ""
	}
}

func intFromAny(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case json.Number:
		i, _ := x.Int64()
		return int(i)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(x))
		return n
	default:
		return 0
	}
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return strings.TrimSpace(a)
	}
	return strings.TrimSpace(b)
}

func normalizeFinishReason(in string) string {
	switch strings.ToLower(strings.TrimSpace(in)) {
	case "tool_calls":
		return "tool_call"
	case "length":
		return "max_tokens"
	default:
		return strings.ToLower(strings.TrimSpace(in))
	}
}

type chatStreamState struct {
	Provider string
	Model    string
	TextID   string

	Text     strings.Builder
	TextOpen bool
	ToolSeq  []string
	Tools    map[string]*chatStreamToolCall
	NextID   int

	Finish llm.FinishReason
	Usage  llm.Usage
}

func (st *chatStreamState) FinalResponse() llm.Response {
	msg := llm.Assistant(st.Text.String())
	for _, key := range st.ToolSeq {
		tc := st.Tools[key]
		if tc == nil {
			continue
		}
		var args json.RawMessage
		if tc.Args.Len() > 0 {
			args = json.RawMessage(tc.Args.String())
		}
		msg.Content = append(msg.Content, llm.ContentPart{
			Kind: llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{
				ID:        st.ensureToolCallID(tc),
				Type:      firstNonEmpty(tc.Type, "function"),
				Name:      tc.Name,
				Arguments: args,
			},
		})
	}
	finish := st.Finish
	if strings.TrimSpace(finish.Reason) == "" {
		finish = llm.FinishReason{Reason: "stop", Raw: "stop"}
	}
	return llm.Response{
		Provider: st.Provider,
		Model:    st.Model,
		Message:  msg,
		Finish:   finish,
		Usage:    st.Usage,
	}
}

type chatStreamToolCall struct {
	ID      string
	Name    string
	Type    string
	Args    strings.Builder
	Started bool
	Ended   bool
}

func (st *chatStreamState) ensureToolCallID(tc *chatStreamToolCall) string {
	if tc == nil {
		return ""
	}
	if strings.TrimSpace(tc.ID) != "" {
		return strings.TrimSpace(tc.ID)
	}
	st.NextID++
	tc.ID = fmt.Sprintf("tool_call_%d", st.NextID)
	return tc.ID
}

func (st *chatStreamState) ensureToolCall(key string) *chatStreamToolCall {
	if st.Tools == nil {
		st.Tools = map[string]*chatStreamToolCall{}
	}
	tc := st.Tools[key]
	if tc != nil {
		return tc
	}
	tc = &chatStreamToolCall{}
	st.Tools[key] = tc
	st.ToolSeq = append(st.ToolSeq, key)
	return tc
}

func (st *chatStreamState) emitToolCallDelta(s *llm.ChanStream, key string, raw map[string]any) {
	tc := st.ensureToolCall(key)
	if id := strings.TrimSpace(asString(raw["id"])); id != "" && strings.TrimSpace(tc.ID) == "" {
		tc.ID = id
	}
	if kind := strings.TrimSpace(asString(raw["type"])); kind != "" {
		tc.Type = kind
	} else if strings.TrimSpace(tc.Type) == "" {
		tc.Type = "function"
	}
	if fn, ok := raw["function"].(map[string]any); ok {
		if name := strings.TrimSpace(asString(fn["name"])); name != "" {
			tc.Name = name
		}
		if argsDelta := asString(fn["arguments"]); argsDelta != "" {
			tc.Args.WriteString(argsDelta)
			if !tc.Started {
				tc.Started = true
				start := llm.ToolCallData{
					ID:   st.ensureToolCallID(tc),
					Type: firstNonEmpty(tc.Type, "function"),
					Name: tc.Name,
				}
				s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &start})
			}
			delta := llm.ToolCallData{
				ID:        st.ensureToolCallID(tc),
				Type:      firstNonEmpty(tc.Type, "function"),
				Name:      tc.Name,
				Arguments: []byte(tc.Args.String()),
			}
			s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallDelta, ToolCall: &delta})
			return
		}
	}
	if !tc.Started {
		tc.Started = true
		start := llm.ToolCallData{
			ID:   st.ensureToolCallID(tc),
			Type: firstNonEmpty(tc.Type, "function"),
			Name: tc.Name,
		}
		s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &start})
	}
}

func (st *chatStreamState) closeOpenToolCalls(s *llm.ChanStream) {
	for _, key := range st.ToolSeq {
		tc := st.Tools[key]
		if tc == nil || tc.Ended {
			continue
		}
		if !tc.Started {
			tc.Started = true
			start := llm.ToolCallData{
				ID:   st.ensureToolCallID(tc),
				Type: firstNonEmpty(tc.Type, "function"),
				Name: tc.Name,
			}
			s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &start})
		}
		end := llm.ToolCallData{
			ID:        st.ensureToolCallID(tc),
			Type:      firstNonEmpty(tc.Type, "function"),
			Name:      tc.Name,
			Arguments: []byte(tc.Args.String()),
		}
		s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &end})
		tc.Ended = true
	}
}

func streamToolCallKey(raw map[string]any, ordinal int) string {
	if idx := strings.TrimSpace(asString(raw["index"])); idx != "" {
		return "idx:" + idx
	}
	if id := strings.TrimSpace(asString(raw["id"])); id != "" {
		return "id:" + id
	}
	return fmt.Sprintf("ord:%d", ordinal)
}

func emitChatCompletionsChunkEvents(s *llm.ChanStream, st *chatStreamState, chunk map[string]any) {
	if usageMap, ok := chunk["usage"].(map[string]any); ok {
		st.Usage = llm.Usage{
			InputTokens:  intFromAny(usageMap["prompt_tokens"]),
			OutputTokens: intFromAny(usageMap["completion_tokens"]),
			TotalTokens:  intFromAny(usageMap["total_tokens"]),
		}
	}

	choices, _ := chunk["choices"].([]any)
	if len(choices) == 0 {
		return
	}
	choice, _ := choices[0].(map[string]any)
	delta, _ := choice["delta"].(map[string]any)

	if text, ok := delta["content"].(string); ok && text != "" {
		if !st.TextOpen {
			st.TextOpen = true
			s.Send(llm.StreamEvent{Type: llm.StreamEventTextStart, TextID: st.TextID})
		}
		st.Text.WriteString(text)
		s.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, TextID: st.TextID, Delta: text})
	}
	if calls, ok := delta["tool_calls"].([]any); ok {
		for i, entry := range calls {
			raw, _ := entry.(map[string]any)
			if raw == nil {
				continue
			}
			st.emitToolCallDelta(s, streamToolCallKey(raw, i), raw)
		}
	}

	if fin := strings.TrimSpace(asString(choice["finish_reason"])); fin != "" {
		st.Finish = llm.FinishReason{Reason: normalizeFinishReason(fin), Raw: fin}
		if st.TextOpen {
			s.Send(llm.StreamEvent{Type: llm.StreamEventTextEnd, TextID: st.TextID})
			st.TextOpen = false
		}
		st.closeOpenToolCalls(s)
		s.Send(llm.StreamEvent{Type: llm.StreamEventStepFinish, FinishReason: &st.Finish})
	}
}

func withDefaultRequestDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		return context.WithTimeout(context.Background(), defaultRequestTimeout)
	}
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, defaultRequestTimeout)
}
