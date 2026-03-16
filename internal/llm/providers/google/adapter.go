package google

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/danshapiro/kilroy/internal/llm"
	"github.com/danshapiro/kilroy/internal/providerspec"
)

type Adapter struct {
	Provider string
	APIKey   string
	BaseURL  string
	Client   *http.Client
}

func init() {
	llm.RegisterEnvAdapterFactory(func() (llm.ProviderAdapter, bool, error) {
		if strings.TrimSpace(os.Getenv("GEMINI_API_KEY")) == "" && strings.TrimSpace(os.Getenv("GOOGLE_API_KEY")) == "" {
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
	key := strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	if key == "" {
		// Common alias.
		key = strings.TrimSpace(os.Getenv("GOOGLE_API_KEY"))
	}
	if key == "" {
		return nil, fmt.Errorf("GEMINI_API_KEY is required")
	}
	return NewWithProvider("google", key, os.Getenv("GEMINI_BASE_URL")), nil
}

func NewWithProvider(provider, apiKey, baseURL string) *Adapter {
	p := providerspec.CanonicalProviderKey(provider)
	if p == "" {
		p = "google"
	}
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = "https://generativelanguage.googleapis.com"
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
	return "google"
}

func (a *Adapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if a.Client == nil {
		// Avoid short client-level timeouts; rely on request context deadlines instead.
		a.Client = &http.Client{Timeout: 0}
	}

	system, contents, err := toGeminiContents(req.Messages)
	if err != nil {
		return llm.Response{}, err
	}

	genCfg := map[string]any{}
	if req.Temperature != nil {
		genCfg["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		genCfg["topP"] = *req.TopP
	}
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		genCfg["maxOutputTokens"] = *req.MaxTokens
	} else {
		genCfg["maxOutputTokens"] = 2048
	}
	if len(req.StopSequences) > 0 {
		genCfg["stopSequences"] = req.StopSequences
	}
	if req.ResponseFormat != nil {
		switch strings.ToLower(strings.TrimSpace(req.ResponseFormat.Type)) {
		case "json":
			genCfg["responseMimeType"] = "application/json"
		case "json_schema":
			genCfg["responseMimeType"] = "application/json"
			if req.ResponseFormat.JSONSchema != nil {
				// Gemini's Schema is a restricted subset; strip JSON-schema-only fields
				// (e.g., additionalProperties) so requests don't fail validation.
				genCfg["responseSchema"] = sanitizeGeminiSchema(req.ResponseFormat.JSONSchema)
			}
		}
	}

	body := map[string]any{
		"contents":         contents,
		"generationConfig": genCfg,
	}
	if strings.TrimSpace(system) != "" {
		body["systemInstruction"] = map[string]any{
			"parts": []map[string]any{{"text": system}},
		}
	}
	if len(req.Tools) > 0 {
		body["tools"] = []map[string]any{{
			"functionDeclarations": toGeminiFunctionDecls(req.Tools),
		}}
	}
	if req.ToolChoice != nil {
		mode := strings.ToLower(strings.TrimSpace(req.ToolChoice.Mode))
		cfg := map[string]any{}
		switch mode {
		case "", "auto":
			cfg["mode"] = "AUTO"
		case "none":
			cfg["mode"] = "NONE"
		case "required":
			cfg["mode"] = "ANY"
		case "named":
			if strings.TrimSpace(req.ToolChoice.Name) == "" {
				return llm.Response{}, &llm.ConfigurationError{Message: "tool_choice mode=named requires name"}
			}
			cfg["mode"] = "ANY"
			cfg["allowedFunctionNames"] = []string{req.ToolChoice.Name}
		default:
			return llm.Response{}, llm.NewUnsupportedToolChoiceError("google", req.ToolChoice.Mode)
		}
		body["toolConfig"] = map[string]any{"functionCallingConfig": cfg}
	}
	// provider_options escape hatch (unified-llm spec).
	if req.ProviderOptions != nil {
		if ov, ok := req.ProviderOptions["google"].(map[string]any); ok {
			for k, v := range ov {
				body[k] = v
			}
		}
		if ov, ok := req.ProviderOptions["gemini"].(map[string]any); ok {
			for k, v := range ov {
				body[k] = v
			}
		}
	}

	b, err := json.Marshal(body)
	if err != nil {
		return llm.Response{}, err
	}

	endpoint := fmt.Sprintf("%s/v1beta/models/%s:generateContent", a.BaseURL, url.PathEscape(req.Model))
	u, err := url.Parse(endpoint)
	if err != nil {
		return llm.Response{}, err
	}
	q := u.Query()
	q.Set("key", a.APIKey)
	u.RawQuery = q.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(b))
	if err != nil {
		return llm.Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

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
		msg := fmt.Sprintf("generateContent failed: %s", strings.TrimSpace(string(rawBytes)))
		return llm.Response{}, llm.ErrorFromHTTPStatus(a.Name(), resp.StatusCode, msg, raw, ra)
	}

	return fromGeminiResponse(a.Name(), raw, req.Model), nil
}

func (a *Adapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	if a.Client == nil {
		a.Client = &http.Client{Timeout: 0}
	}
	sctx, cancel := context.WithCancel(ctx)

	system, contents, err := toGeminiContents(req.Messages)
	if err != nil {
		cancel()
		return nil, err
	}

	genCfg := map[string]any{}
	if req.Temperature != nil {
		genCfg["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		genCfg["topP"] = *req.TopP
	}
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		genCfg["maxOutputTokens"] = *req.MaxTokens
	} else {
		genCfg["maxOutputTokens"] = 2048
	}
	if len(req.StopSequences) > 0 {
		genCfg["stopSequences"] = req.StopSequences
	}
	if req.ResponseFormat != nil {
		switch strings.ToLower(strings.TrimSpace(req.ResponseFormat.Type)) {
		case "json":
			genCfg["responseMimeType"] = "application/json"
		case "json_schema":
			genCfg["responseMimeType"] = "application/json"
			if req.ResponseFormat.JSONSchema != nil {
				// Gemini's Schema is a restricted subset; strip JSON-schema-only fields
				// (e.g., additionalProperties) so requests don't fail validation.
				genCfg["responseSchema"] = sanitizeGeminiSchema(req.ResponseFormat.JSONSchema)
			}
		}
	}

	body := map[string]any{
		"contents":         contents,
		"generationConfig": genCfg,
	}
	if strings.TrimSpace(system) != "" {
		body["systemInstruction"] = map[string]any{
			"parts": []map[string]any{{"text": system}},
		}
	}
	if len(req.Tools) > 0 {
		body["tools"] = []map[string]any{{
			"functionDeclarations": toGeminiFunctionDecls(req.Tools),
		}}
	}
	if req.ToolChoice != nil {
		mode := strings.ToLower(strings.TrimSpace(req.ToolChoice.Mode))
		cfg := map[string]any{}
		switch mode {
		case "", "auto":
			cfg["mode"] = "AUTO"
		case "none":
			cfg["mode"] = "NONE"
		case "required":
			cfg["mode"] = "ANY"
		case "named":
			if strings.TrimSpace(req.ToolChoice.Name) == "" {
				cancel()
				return nil, &llm.ConfigurationError{Message: "tool_choice mode=named requires name"}
			}
			cfg["mode"] = "ANY"
			cfg["allowedFunctionNames"] = []string{req.ToolChoice.Name}
		default:
			cancel()
			return nil, llm.NewUnsupportedToolChoiceError("google", req.ToolChoice.Mode)
		}
		body["toolConfig"] = map[string]any{"functionCallingConfig": cfg}
	}
	if req.ProviderOptions != nil {
		if ov, ok := req.ProviderOptions["google"].(map[string]any); ok {
			for k, v := range ov {
				body[k] = v
			}
		}
		if ov, ok := req.ProviderOptions["gemini"].(map[string]any); ok {
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

	endpoint := fmt.Sprintf("%s/v1beta/models/%s:streamGenerateContent", a.BaseURL, url.PathEscape(req.Model))
	u, err := url.Parse(endpoint)
	if err != nil {
		cancel()
		return nil, err
	}
	q := u.Query()
	q.Set("key", a.APIKey)
	q.Set("alt", "sse")
	u.RawQuery = q.Encode()

	httpReq, err := http.NewRequestWithContext(sctx, http.MethodPost, u.String(), bytes.NewReader(b))
	if err != nil {
		cancel()
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

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
		msg := fmt.Sprintf("streamGenerateContent failed: %s", strings.TrimSpace(string(rawBytes)))
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

		textID := "text_1"
		textStarted := false
		finished := false
		var textBuf strings.Builder
		var contentParts []llm.ContentPart
		var usage llm.Usage
		finish := llm.FinishReason{Reason: "stop"}

		flushTextPart := func() {
			if textBuf.Len() == 0 {
				return
			}
			contentParts = append(contentParts, llm.ContentPart{Kind: llm.ContentText, Text: textBuf.String()})
			textBuf.Reset()
		}

		_ = llm.ParseSSE(sctx, resp.Body, func(ev llm.SSEEvent) error {
			if len(ev.Data) == 0 {
				return nil
			}
			var raw map[string]any
			dec := json.NewDecoder(bytes.NewReader(ev.Data))
			dec.UseNumber()
			if err := dec.Decode(&raw); err != nil {
				s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: map[string]any{"event": ev.Event, "data": string(ev.Data)}})
				return nil
			}

			// candidates[0].content.parts
			if cands, ok := raw["candidates"].([]any); ok && len(cands) > 0 {
				if c0, ok := cands[0].(map[string]any); ok {
					if content, ok := c0["content"].(map[string]any); ok {
						if parts, ok := content["parts"].([]any); ok {
							for _, pAny := range parts {
								p, ok := pAny.(map[string]any)
								if !ok {
									continue
								}
								if t, _ := p["text"].(string); t != "" {
									if !textStarted {
										textStarted = true
										s.Send(llm.StreamEvent{Type: llm.StreamEventTextStart, TextID: textID})
									}
									textBuf.WriteString(t)
									s.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, TextID: textID, Delta: t})
									continue
								}
								if fc, ok := p["functionCall"].(map[string]any); ok {
									name, _ := fc["name"].(string)
									argsAny := normalizeJSONNumbers(fc["args"])
									argsRaw, _ := json.Marshal(argsAny)
									thoughtSig := geminiThoughtSignature(p, fc)
									flushTextPart()

									id := "call_" + ulid.Make().String() // synthetic per spec
									tc := llm.ToolCallData{
										ID:               id,
										Name:             name,
										Type:             "function",
										ThoughtSignature: thoughtSig,
									}
									s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &tc})
									tcEnd := llm.ToolCallData{
										ID:               id,
										Name:             name,
										Arguments:        argsRaw,
										Type:             "function",
										ThoughtSignature: thoughtSig,
									}
									s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &tcEnd})

									// Preserve tool call in the accumulated response.
									contentParts = append(contentParts, llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &tcEnd})
								}
							}
						}
					}
					if fr, _ := c0["finishReason"].(string); fr != "" {
						finish = llm.NormalizeFinishReason(a.Name(), fr)
						if textStarted {
							s.Send(llm.StreamEvent{Type: llm.StreamEventTextEnd, TextID: textID})
							textStarted = false
						}
						// Finish on explicit finishReason chunk.
						flushTextPart()
						msg := llm.Message{Role: llm.RoleAssistant, Content: contentParts}
						r := llm.Response{
							Provider: a.Name(),
							Model:    req.Model,
							Message:  msg,
							Finish:   finish,
							Usage:    usage,
							Raw:      raw,
						}
						if r.Finish.Reason == "" {
							if len(r.ToolCalls()) > 0 {
								r.Finish = llm.FinishReason{Reason: "tool_calls"}
							} else {
								r.Finish = llm.FinishReason{Reason: "stop"}
							}
						}
						rp := r
						s.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &r.Finish, Usage: &r.Usage, Response: &rp})
						finished = true
						cancel()
						return nil
					}
				}
			}

			if um, ok := raw["usageMetadata"].(map[string]any); ok {
				usage = parseUsage(um)
			}

			s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: raw})
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

func toGeminiFunctionDecls(tools []llm.ToolDefinition) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		params := t.Parameters
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			// Gemini's Schema is a restricted subset; strip JSON-schema-only fields
			// (e.g., additionalProperties) so requests don't fail validation.
			"parameters": sanitizeGeminiSchema(params),
		})
	}
	return out
}

func sanitizeGeminiSchema(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			// The Gemini Schema proto does not accept JSON Schema's additionalProperties field.
			// Omitting it preserves compatibility while keeping the rest of the schema useful.
			if k == "additionalProperties" {
				continue
			}
			out[k] = sanitizeGeminiSchema(vv)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = sanitizeGeminiSchema(x[i])
		}
		return out
	default:
		return v
	}
}

func toGeminiContents(msgs []llm.Message) (system string, contents []map[string]any, _ error) {
	var sysParts []string
	appendContent := func(role string, parts []map[string]any) {
		if len(parts) == 0 {
			return
		}
		contents = append(contents, map[string]any{
			"role":  role,
			"parts": parts,
		})
	}

	for _, m := range msgs {
		switch m.Role {
		case llm.RoleSystem, llm.RoleDeveloper:
			if t := strings.TrimSpace(m.Text()); t != "" {
				sysParts = append(sysParts, t)
			}
		case llm.RoleUser:
			var parts []map[string]any
			for _, p := range m.Content {
				switch p.Kind {
				case llm.ContentText:
					if strings.TrimSpace(p.Text) != "" {
						parts = append(parts, map[string]any{"text": p.Text})
					}
				case llm.ContentImage:
					if p.Image == nil {
						continue
					}
					u := strings.TrimSpace(p.Image.URL)
					mt := strings.TrimSpace(p.Image.MediaType)
					if len(p.Image.Data) > 0 || llm.IsLocalPath(u) {
						var b []byte
						var err error
						if len(p.Image.Data) > 0 {
							b = p.Image.Data
						} else {
							path := llm.ExpandTilde(u)
							b, err = os.ReadFile(path)
							if err != nil {
								return "", nil, err
							}
							if mt == "" {
								mt = llm.InferMimeTypeFromPath(path)
							}
						}
						if mt == "" {
							mt = "image/png"
						}
						parts = append(parts, map[string]any{
							"inlineData": map[string]any{
								"mimeType": mt,
								"data":     base64.StdEncoding.EncodeToString(b),
							},
						})
					} else if u != "" {
						if mt == "" {
							mt = "image/png"
						}
						parts = append(parts, map[string]any{
							"fileData": map[string]any{
								"mimeType": mt,
								"fileUri":  u,
							},
						})
					}
				case llm.ContentAudio, llm.ContentDocument:
					return "", nil, &llm.ConfigurationError{Message: fmt.Sprintf("unsupported content kind for google: %s", p.Kind)}
				default:
					// ignore
				}
			}
			appendContent("user", parts)
		case llm.RoleAssistant:
			var parts []map[string]any
			for _, p := range m.Content {
				switch p.Kind {
				case llm.ContentText:
					if strings.TrimSpace(p.Text) != "" {
						parts = append(parts, map[string]any{"text": p.Text})
					}
				case llm.ContentImage:
					if p.Image == nil {
						continue
					}
					u := strings.TrimSpace(p.Image.URL)
					mt := strings.TrimSpace(p.Image.MediaType)
					if len(p.Image.Data) > 0 || llm.IsLocalPath(u) {
						var b []byte
						var err error
						if len(p.Image.Data) > 0 {
							b = p.Image.Data
						} else {
							path := llm.ExpandTilde(u)
							b, err = os.ReadFile(path)
							if err != nil {
								return "", nil, err
							}
							if mt == "" {
								mt = llm.InferMimeTypeFromPath(path)
							}
						}
						if mt == "" {
							mt = "image/png"
						}
						parts = append(parts, map[string]any{
							"inlineData": map[string]any{
								"mimeType": mt,
								"data":     base64.StdEncoding.EncodeToString(b),
							},
						})
					} else if u != "" {
						if mt == "" {
							mt = "image/png"
						}
						parts = append(parts, map[string]any{
							"fileData": map[string]any{
								"mimeType": mt,
								"fileUri":  u,
							},
						})
					}
				case llm.ContentToolCall:
					if p.ToolCall == nil {
						continue
					}
					var args any
					if len(p.ToolCall.Arguments) > 0 {
						_ = json.Unmarshal(p.ToolCall.Arguments, &args)
					}
					part := map[string]any{
						"functionCall": map[string]any{
							"name": p.ToolCall.Name,
							"args": args,
						},
					}
					if sig := strings.TrimSpace(p.ToolCall.ThoughtSignature); sig != "" {
						// Gemini requires replaying the thought signature that accompanied prior tool calls.
						part["thoughtSignature"] = sig
					}
					parts = append(parts, part)
				case llm.ContentAudio, llm.ContentDocument:
					return "", nil, &llm.ConfigurationError{Message: fmt.Sprintf("unsupported content kind for google: %s", p.Kind)}
				default:
					// ignore
				}
			}
			appendContent("model", parts)
		case llm.RoleTool:
			var parts []map[string]any
			for _, p := range m.Content {
				if p.Kind != llm.ContentToolResult || p.ToolResult == nil {
					continue
				}
				name := strings.TrimSpace(p.ToolResult.Name)
				if name == "" {
					name = toolNameFromCallID(msgs, p.ToolResult.ToolCallID)
				}
				if name == "" {
					name = p.ToolResult.ToolCallID
				}
				// Gemini expects a functionResponse part.
				var respObj map[string]any
				switch v := p.ToolResult.Content.(type) {
				case map[string]any:
					respObj = v
				case string:
					respObj = map[string]any{"result": v}
				default:
					b, _ := json.Marshal(v)
					respObj = map[string]any{"result": string(b)}
				}
				parts = append(parts, map[string]any{
					"functionResponse": map[string]any{
						"name":     name,
						"response": respObj,
					},
				})
			}
			appendContent("user", parts)
		default:
			// ignore
		}
	}
	return strings.Join(sysParts, "\n\n"), contents, nil
}

func toolNameFromCallID(msgs []llm.Message, callID string) string {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return ""
	}
	for _, m := range msgs {
		for _, p := range m.Content {
			if p.Kind == llm.ContentToolCall && p.ToolCall != nil && strings.TrimSpace(p.ToolCall.ID) == callID {
				return strings.TrimSpace(p.ToolCall.Name)
			}
		}
	}
	return ""
}

func geminiThoughtSignature(part map[string]any, fc map[string]any) string {
	if v, _ := part["thoughtSignature"].(string); strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if v, _ := part["thought_signature"].(string); strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	// Defensive fallback for non-conformant payloads.
	if v, _ := fc["thoughtSignature"].(string); strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if v, _ := fc["thought_signature"].(string); strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return ""
}

func normalizeJSONNumbers(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[k] = normalizeJSONNumbers(vv)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = normalizeJSONNumbers(x[i])
		}
		return out
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return n
		}
		if f, err := x.Float64(); err == nil {
			return f
		}
		return x.String()
	default:
		return v
	}
}

func fromGeminiResponse(provider string, raw map[string]any, requestedModel string) llm.Response {
	r := llm.Response{
		Provider: provider,
		Model:    requestedModel,
		Raw:      raw,
	}

	msg := llm.Message{Role: llm.RoleAssistant}

	// candidates[0].content.parts
	if cands, ok := raw["candidates"].([]any); ok && len(cands) > 0 {
		if c0, ok := cands[0].(map[string]any); ok {
			if content, ok := c0["content"].(map[string]any); ok {
				if parts, ok := content["parts"].([]any); ok {
					for _, pAny := range parts {
						p, ok := pAny.(map[string]any)
						if !ok {
							continue
						}
						if t, _ := p["text"].(string); t != "" {
							msg.Content = append(msg.Content, llm.ContentPart{Kind: llm.ContentText, Text: t})
							continue
						}
						if fc, ok := p["functionCall"].(map[string]any); ok {
							name, _ := fc["name"].(string)
							argsAny := fc["args"]
							argsRaw, _ := json.Marshal(argsAny)
							thoughtSig := geminiThoughtSignature(p, fc)
							msg.Content = append(msg.Content, llm.ContentPart{
								Kind: llm.ContentToolCall,
								ToolCall: &llm.ToolCallData{
									ID:               "call_" + ulid.Make().String(), // synthetic per spec
									Name:             name,
									Arguments:        argsRaw,
									Type:             "function",
									ThoughtSignature: thoughtSig,
								},
							})
						}
					}
				}
			}
			if fr, _ := c0["finishReason"].(string); fr != "" {
				r.Finish = llm.NormalizeFinishReason(provider, fr)
			}
		}
	}

	r.Message = msg
	if r.Finish.Reason == "" {
		if len(r.ToolCalls()) > 0 {
			r.Finish = llm.FinishReason{Reason: "tool_calls"}
		} else {
			r.Finish = llm.FinishReason{Reason: "stop"}
		}
	}

	if um, ok := raw["usageMetadata"].(map[string]any); ok {
		r.Usage = parseUsage(um)
	}
	return r
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
		InputTokens:  getInt(u["promptTokenCount"]),
		OutputTokens: getInt(u["candidatesTokenCount"]),
		TotalTokens:  getInt(u["totalTokenCount"]),
		Raw:          map[string]any{},
	}
	if v := getInt(u["cachedContentTokenCount"]); v > 0 {
		usage.CacheReadTokens = &v
	}
	if v := getInt(u["thoughtsTokenCount"]); v > 0 {
		usage.ReasoningTokens = &v
	}
	return usage
}
