package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/strongdm/kilroy/internal/llm"
)

type Adapter struct {
	APIKey  string
	BaseURL string
	Client  *http.Client
}

func init() {
	llm.RegisterEnvAdapterFactory(func() (llm.ProviderAdapter, bool, error) {
		if strings.TrimSpace(os.Getenv("OPENAI_API_KEY")) == "" {
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
	key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if key == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY is required")
	}
	base := strings.TrimSpace(os.Getenv("OPENAI_BASE_URL"))
	if base == "" {
		base = "https://api.openai.com"
	}
	return &Adapter{
		APIKey:  key,
		BaseURL: strings.TrimRight(base, "/"),
		// Avoid short client-level timeouts; rely on request context deadlines instead.
		Client: &http.Client{Timeout: 0},
	}, nil
}

func (a *Adapter) Name() string { return "openai" }

func (a *Adapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if a.Client == nil {
		// Avoid short client-level timeouts; rely on request context deadlines instead.
		a.Client = &http.Client{Timeout: 0}
	}

	instructions, inputItems, err := toResponsesInput(req.Messages)
	if err != nil {
		return llm.Response{}, err
	}

	body := map[string]any{
		"model":               req.Model,
		"instructions":        instructions,
		"input":               inputItems,
		"parallel_tool_calls": false, // safer default; can be overridden later per profile
		"store":               false, // local-first logging is handled by Kilroy; don't retain server-side by default
	}

	if len(req.Tools) > 0 {
		body["tools"] = toResponsesTools(req.Tools)
	}
	if req.ToolChoice != nil {
		tc, err := toResponsesToolChoice(*req.ToolChoice)
		if err != nil {
			return llm.Response{}, err
		}
		body["tool_choice"] = tc
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if req.MaxTokens != nil {
		body["max_output_tokens"] = *req.MaxTokens
	}
	if len(req.Metadata) > 0 {
		body["metadata"] = req.Metadata
	}
	if req.ReasoningEffort != nil {
		body["reasoning"] = map[string]any{"effort": *req.ReasoningEffort}
	}
	if req.ResponseFormat != nil {
		if rf := toResponsesResponseFormat(*req.ResponseFormat); rf != nil {
			body["response_format"] = rf
		}
	}
	// provider_options escape hatch (unified-llm spec).
	if req.ProviderOptions != nil {
		if ov, ok := req.ProviderOptions["openai"].(map[string]any); ok {
			for k, v := range ov {
				body[k] = v
			}
		}
	}

	b, err := json.Marshal(body)
	if err != nil {
		return llm.Response{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.BaseURL+"/v1/responses", bytes.NewReader(b))
	if err != nil {
		return llm.Response{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+a.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := a.Client.Do(httpReq)
	if err != nil {
		return llm.Response{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	var raw map[string]any
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return llm.Response{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		ra := llm.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		msg := fmt.Sprintf("responses.create failed: %v", raw)
		return llm.Response{}, llm.ErrorFromHTTPStatus("openai", resp.StatusCode, msg, raw, ra)
	}

	return fromResponses(raw, req.Model), nil
}

func (a *Adapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	if a.Client == nil {
		a.Client = &http.Client{Timeout: 0} // streaming uses request context for cancellation
	}
	sctx, cancel := context.WithCancel(ctx)

	instructions, inputItems, err := toResponsesInput(req.Messages)
	if err != nil {
		cancel()
		return nil, err
	}

	body := map[string]any{
		"model":               req.Model,
		"instructions":        instructions,
		"input":               inputItems,
		"parallel_tool_calls": false,
		"store":               false,
		"stream":              true,
	}
	if len(req.Tools) > 0 {
		body["tools"] = toResponsesTools(req.Tools)
	}
	if req.ToolChoice != nil {
		tc, err := toResponsesToolChoice(*req.ToolChoice)
		if err != nil {
			cancel()
			return nil, err
		}
		body["tool_choice"] = tc
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if req.MaxTokens != nil {
		body["max_output_tokens"] = *req.MaxTokens
	}
	if len(req.Metadata) > 0 {
		body["metadata"] = req.Metadata
	}
	if req.ReasoningEffort != nil {
		body["reasoning"] = map[string]any{"effort": *req.ReasoningEffort}
	}
	if req.ResponseFormat != nil {
		if rf := toResponsesResponseFormat(*req.ResponseFormat); rf != nil {
			body["response_format"] = rf
		}
	}
	if req.ProviderOptions != nil {
		if ov, ok := req.ProviderOptions["openai"].(map[string]any); ok {
			for k, v := range ov {
				body[k] = v
			}
		}
	}

	b, err := json.Marshal(body)
	if err != nil {
		cancel()
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(sctx, http.MethodPost, a.BaseURL+"/v1/responses", bytes.NewReader(b))
	if err != nil {
		cancel()
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+a.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := a.Client.Do(httpReq)
	if err != nil {
		cancel()
		return nil, llm.WrapContextError("openai", err)
	}

	// Handle non-2xx immediately.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		var raw map[string]any
		dec := json.NewDecoder(resp.Body)
		dec.UseNumber()
		_ = dec.Decode(&raw)
		ra := llm.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		msg := fmt.Sprintf("responses.create(stream) failed: %v", raw)
		cancel()
		return nil, llm.ErrorFromHTTPStatus("openai", resp.StatusCode, msg, raw, ra)
	}

	s := llm.NewChanStream(cancel)
	// STREAM_START
	s.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart})

	go func() {
		defer func() {
			_ = resp.Body.Close()
			s.CloseSend()
		}()

		textID := "text_1"
		textStarted := false
		finished := false
		type toolState struct {
			id      string
			name    string
			started bool
			args    strings.Builder
		}
		toolStates := map[string]*toolState{}

		_ = llm.ParseSSE(sctx, resp.Body, func(ev llm.SSEEvent) error {
			if len(ev.Data) == 0 {
				return nil
			}
			var payload map[string]any
			dec := json.NewDecoder(bytes.NewReader(ev.Data))
			dec.UseNumber()
			if err := dec.Decode(&payload); err != nil {
				// Emit raw passthrough and continue.
				s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: map[string]any{"event": ev.Event, "data": string(ev.Data)}})
				return nil
			}
			typ, _ := payload["type"].(string)
			if typ == "" {
				typ = ev.Event
			}

			switch typ {
			case "response.output_text.delta":
				delta, _ := payload["delta"].(string)
				if delta == "" {
					delta, _ = payload["text"].(string)
				}
				if delta == "" {
					return nil
				}
				if !textStarted {
					textStarted = true
					s.Send(llm.StreamEvent{Type: llm.StreamEventTextStart, TextID: textID})
				}
				s.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, TextID: textID, Delta: delta})
			case "response.function_call_arguments.delta":
				delta, _ := payload["delta"].(string)
				if delta == "" {
					delta, _ = payload["arguments"].(string)
				}
				callID, _ := payload["call_id"].(string)
				if callID == "" {
					callID, _ = payload["item_id"].(string)
				}
				if callID == "" {
					callID, _ = payload["id"].(string)
				}
				name, _ := payload["name"].(string)
				if callID == "" || (delta == "" && name == "") {
					// Can't map reliably; pass through.
					s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: payload})
					return nil
				}

				st := toolStates[callID]
				if st == nil {
					st = &toolState{id: callID, name: name}
					toolStates[callID] = st
				}
				if st.name == "" && name != "" {
					st.name = name
				}
				if delta != "" {
					st.args.WriteString(delta)
				}
				if !st.started {
					st.started = true
					tc := llm.ToolCallData{ID: st.id, Name: st.name, Type: "function"}
					s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &tc})
				}
				tc := llm.ToolCallData{ID: st.id, Name: st.name, Arguments: []byte(st.args.String()), Type: "function"}
				s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallDelta, ToolCall: &tc})
			case "response.output_item.done":
				itemAny, _ := payload["item"]
				if itemAny == nil {
					itemAny, _ = payload["output_item"]
				}
				if item, ok := itemAny.(map[string]any); ok {
					it, _ := item["type"].(string)
					switch it {
					case "function_call":
						callID, _ := item["call_id"].(string)
						name, _ := item["name"].(string)
						argsStr, _ := item["arguments"].(string)
						if callID != "" {
							st := toolStates[callID]
							if st == nil {
								st = &toolState{id: callID, name: name}
								toolStates[callID] = st
							}
							if st.name == "" && name != "" {
								st.name = name
							}
							if argsStr != "" && st.args.Len() == 0 {
								st.args.WriteString(argsStr)
							}
							if argsStr == "" {
								argsStr = st.args.String()
							}
							if !st.started {
								st.started = true
								tc := llm.ToolCallData{ID: st.id, Name: st.name, Type: "function"}
								s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &tc})
							}
							tc := llm.ToolCallData{ID: st.id, Name: st.name, Arguments: json.RawMessage(argsStr), Type: "function"}
							s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &tc})
						} else {
							s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: payload})
						}
					default:
						// Best-effort: treat as end-of-text.
						if textStarted {
							s.Send(llm.StreamEvent{Type: llm.StreamEventTextEnd, TextID: textID})
							textStarted = false
						}
					}
				} else {
					// Best-effort: treat as end-of-text.
					if textStarted {
						s.Send(llm.StreamEvent{Type: llm.StreamEventTextEnd, TextID: textID})
						textStarted = false
					}
				}
			case "response.completed":
				// Response object may be nested under "response" or be the payload itself.
				rawResp, _ := payload["response"].(map[string]any)
				if rawResp == nil {
					rawResp = payload
				}
				r := fromResponses(rawResp, req.Model)
				// Ensure text segment is closed.
				if textStarted {
					s.Send(llm.StreamEvent{Type: llm.StreamEventTextEnd, TextID: textID})
					textStarted = false
				}
				rp := r
				s.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &r.Finish, Usage: &r.Usage, Response: &rp})
				// Stop parsing after finish.
				finished = true
				cancel()
			default:
				s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: payload})
			}
			return nil
		})

		if !finished {
			if err := sctx.Err(); err != nil {
				s.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: llm.WrapContextError("openai", err)})
			}
		}
	}()

	return s, nil
}

func toResponsesResponseFormat(rf llm.ResponseFormat) any {
	switch strings.ToLower(strings.TrimSpace(rf.Type)) {
	case "", "text":
		return nil
	case "json":
		return map[string]any{"type": "json"}
	case "json_schema":
		return map[string]any{
			"type":        "json_schema",
			"json_schema": rf.JSONSchema,
			"strict":      rf.Strict,
		}
	default:
		return nil
	}
}

func toResponsesTools(tools []llm.ToolDefinition) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		params := t.Parameters
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		// OpenAI strict mode requires a fully-specified JSON Schema:
		// - object schemas must set additionalProperties=false
		// - required must include every key in properties (even for "optional" fields)
		// See API validation errors like:
		// "Invalid schema for function 'read_file': ... 'required' ... Missing 'limit'."
		params = strictifyJSONSchema(params)
		out = append(out, map[string]any{
			"type":        "function",
			"name":        t.Name,
			"description": t.Description,
			"parameters":  params,
			// Structured Outputs strict-by-default.
			"strict": true,
		})
	}
	return out
}

func strictifyJSONSchema(in map[string]any) map[string]any {
	// Best-effort deep copy + strictification for OpenAI tool schemas.
	// This intentionally handles only the constructs we emit (object/array) and is safe to
	// apply repeatedly (idempotent for our shapes).
	cp := deepCopyAny(in).(map[string]any)
	strictifyJSONSchemaInPlace(cp)
	return cp
}

func strictifyJSONSchemaInPlace(m map[string]any) {
	if m == nil {
		return
	}
	typ, _ := m["type"].(string)
	switch typ {
	case "object":
		// OpenAI strict mode requires this be present and false.
		m["additionalProperties"] = false

		props, _ := m["properties"].(map[string]any)
		if props == nil {
			props = map[string]any{}
			m["properties"] = props
		}
		// Required must include all properties keys (even for "optional" fields).
		keys := make([]string, 0, len(props))
		for k := range props {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		m["required"] = keys

		// Recurse into property schemas.
		for _, k := range keys {
			if child, ok := props[k].(map[string]any); ok {
				strictifyJSONSchemaInPlace(child)
			}
		}
	case "array":
		if items, ok := m["items"].(map[string]any); ok {
			strictifyJSONSchemaInPlace(items)
		}
	}

	// If the schema uses combinators, strictify any subschemas we can find.
	for _, comb := range []string{"anyOf", "oneOf", "allOf"} {
		raw, ok := m[comb]
		if !ok || raw == nil {
			continue
		}
		arr, ok := raw.([]any)
		if !ok {
			continue
		}
		for _, it := range arr {
			if child, ok := it.(map[string]any); ok {
				strictifyJSONSchemaInPlace(child)
			}
		}
	}
}

func deepCopyAny(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[k] = deepCopyAny(vv)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = deepCopyAny(x[i])
		}
		return out
	default:
		return v
	}
}

func toResponsesToolChoice(tc llm.ToolChoice) (any, error) {
	switch strings.ToLower(strings.TrimSpace(tc.Mode)) {
	case "", "auto":
		return "auto", nil
	case "none":
		return "none", nil
	case "required":
		return "required", nil
	case "named":
		if strings.TrimSpace(tc.Name) == "" {
			return nil, &llm.ConfigurationError{Message: "tool_choice mode=named requires name"}
		}
		return map[string]any{
			"type":     "function",
			"function": map[string]any{"name": tc.Name},
		}, nil
	default:
		// Backward-compatible: some callers may have used an unspecified mode to force
		// a particular tool. Prefer explicit mode="named".
		if strings.TrimSpace(tc.Name) != "" {
			return map[string]any{
				"type":     "function",
				"function": map[string]any{"name": tc.Name},
			}, nil
		}
		return nil, llm.NewUnsupportedToolChoiceError("openai", tc.Mode)
	}
}

func toResponsesInput(msgs []llm.Message) (instructions string, items []any, _ error) {
	var instrParts []string
	for _, m := range msgs {
		switch m.Role {
		case llm.RoleSystem, llm.RoleDeveloper:
			if t := strings.TrimSpace(m.Text()); t != "" {
				instrParts = append(instrParts, t)
			}
		}
	}
	instructions = strings.Join(instrParts, "\n\n")

	for _, m := range msgs {
		switch m.Role {
		case llm.RoleSystem, llm.RoleDeveloper:
			continue
		case llm.RoleUser, llm.RoleAssistant:
			content := make([]any, 0, len(m.Content))
			for _, p := range m.Content {
				switch p.Kind {
				case llm.ContentText:
					if strings.TrimSpace(p.Text) == "" {
						continue
					}
					typ := "input_text"
					if m.Role == llm.RoleAssistant {
						typ = "output_text"
					}
					content = append(content, map[string]any{
						"type": typ,
						"text": p.Text,
					})
				case llm.ContentImage:
					if p.Image == nil {
						continue
					}
					url := strings.TrimSpace(p.Image.URL)
					if len(p.Image.Data) > 0 {
						mt := strings.TrimSpace(p.Image.MediaType)
						if mt == "" {
							mt = "image/png"
						}
						url = llm.DataURI(mt, p.Image.Data)
					} else if llm.IsLocalPath(url) {
						path := llm.ExpandTilde(url)
						b, err := os.ReadFile(path)
						if err != nil {
							return "", nil, err
						}
						mt := strings.TrimSpace(p.Image.MediaType)
						if mt == "" {
							mt = llm.InferMimeTypeFromPath(path)
						}
						if mt == "" {
							mt = "image/png"
						}
						url = llm.DataURI(mt, b)
					}
					if url != "" {
						content = append(content, map[string]any{
							"type":      "input_image",
							"image_url": url,
						})
					}
				case llm.ContentAudio, llm.ContentDocument:
					return "", nil, &llm.ConfigurationError{Message: fmt.Sprintf("unsupported content kind for openai: %s", p.Kind)}
				default:
					// ignore (tool calls are top-level items)
				}
			}
			if len(content) > 0 {
				items = append(items, map[string]any{
					"type":    "message",
					"role":    string(m.Role),
					"content": content,
				})
			}
			for _, p := range m.Content {
				if p.Kind == llm.ContentToolCall && p.ToolCall != nil {
					items = append(items, map[string]any{
						"type":      "function_call",
						"call_id":   p.ToolCall.ID,
						"name":      p.ToolCall.Name,
						"arguments": string(p.ToolCall.Arguments),
					})
				}
			}
		case llm.RoleTool:
			for _, p := range m.Content {
				if p.Kind != llm.ContentToolResult || p.ToolResult == nil {
					continue
				}
				outStr := ""
				switch v := p.ToolResult.Content.(type) {
				case string:
					outStr = v
				default:
					b, _ := json.Marshal(v)
					outStr = string(b)
				}
				items = append(items, map[string]any{
					"type":    "function_call_output",
					"call_id": p.ToolResult.ToolCallID,
					"output":  outStr,
				})
			}
		default:
			// ignore unknown roles
		}
	}
	return instructions, items, nil
}

func fromResponses(raw map[string]any, requestedModel string) llm.Response {
	// Best-effort mapping. OpenAI Responses output is a list of typed items.
	r := llm.Response{
		Provider: "openai",
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

	// Parse output items.
	if out, ok := raw["output"].([]any); ok {
		for _, itemAny := range out {
			item, ok := itemAny.(map[string]any)
			if !ok {
				continue
			}
			typ, _ := item["type"].(string)
			switch typ {
			case "message":
				// content: [{type:"output_text", text:"..."}]
				if content, ok := item["content"].([]any); ok {
					for _, cAny := range content {
						c, ok := cAny.(map[string]any)
						if !ok {
							continue
						}
						ct, _ := c["type"].(string)
						if ct == "output_text" {
							if text, _ := c["text"].(string); text != "" {
								msg.Content = append(msg.Content, llm.ContentPart{Kind: llm.ContentText, Text: text})
							}
						}
					}
				}
			case "function_call":
				name, _ := item["name"].(string)
				args, _ := item["arguments"].(string)
				callID, _ := item["call_id"].(string)
				msg.Content = append(msg.Content, llm.ContentPart{
					Kind: llm.ContentToolCall,
					ToolCall: &llm.ToolCallData{
						ID:        callID,
						Name:      name,
						Arguments: json.RawMessage(args),
						Type:      "function",
					},
				})
			default:
				// ignore (reasoning, web_search_call, etc.)
			}
		}
	}

	r.Message = msg
	if len(r.ToolCalls()) > 0 {
		r.Finish = llm.FinishReason{Reason: "tool_calls"}
	} else {
		r.Finish = llm.FinishReason{Reason: "stop"}
	}

	// usage
	if u, ok := raw["usage"].(map[string]any); ok {
		r.Usage = parseUsage(u)
	}
	return r
}

func parseUsage(u map[string]any) llm.Usage {
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
	usage := llm.Usage{
		InputTokens:  getInt(u["input_tokens"]),
		OutputTokens: getInt(u["output_tokens"]),
		TotalTokens:  getInt(u["total_tokens"]),
		Raw:          map[string]any{},
	}
	if outDetails, ok := u["output_tokens_details"].(map[string]any); ok {
		rt := getInt(outDetails["reasoning_tokens"])
		usage.ReasoningTokens = &rt
	}
	if inDetails, ok := u["input_tokens_details"].(map[string]any); ok {
		ct := getInt(inDetails["cached_tokens"])
		usage.CacheReadTokens = &ct
	}
	return usage
}
