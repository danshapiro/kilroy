package codexappserver

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/danshapiro/kilroy/internal/llm"
)

type codexTransport interface {
	Initialize(ctx context.Context) error
	Close() error
	Complete(ctx context.Context, payload map[string]any) (map[string]any, error)
	Stream(ctx context.Context, payload map[string]any) (*NotificationStream, error)
	ListModels(ctx context.Context, params map[string]any) (modelListResponse, error)
}

type AdapterOptions struct {
	Provider          string
	Transport         codexTransport
	TransportOptions  TransportOptions
	TranslateRequest  func(request llm.Request, streaming bool) (translateRequestResult, error)
	TranslateResponse func(body map[string]any) (llm.Response, error)
	TranslateStream   func(events <-chan map[string]any) <-chan llm.StreamEvent
}

type Adapter struct {
	provider          string
	transportProvided codexTransport
	transportOptions  TransportOptions

	translateRequestFn  func(request llm.Request, streaming bool) (translateRequestResult, error)
	translateResponseFn func(body map[string]any) (llm.Response, error)
	translateStreamFn   func(events <-chan map[string]any) <-chan llm.StreamEvent

	transportMu sync.Mutex
	transport   codexTransport

	modelListMu sync.Mutex
	modelList   *modelListResponse
}

func init() {
	llm.RegisterEnvAdapterFactory(func() (llm.ProviderAdapter, bool, error) {
		opts, ok := transportOptionsFromEnv()
		if !ok {
			return nil, false, nil
		}
		return NewAdapter(AdapterOptions{TransportOptions: opts}), true, nil
	})
}

func NewAdapter(options AdapterOptions) *Adapter {
	provider := strings.TrimSpace(options.Provider)
	if provider == "" {
		provider = providerName
	}
	return &Adapter{
		provider:          provider,
		transportProvided: options.Transport,
		transportOptions:  options.TransportOptions,
		translateRequestFn: func(request llm.Request, streaming bool) (translateRequestResult, error) {
			if options.TranslateRequest != nil {
				return options.TranslateRequest(request, streaming)
			}
			return translateRequest(request, streaming)
		},
		translateResponseFn: func(body map[string]any) (llm.Response, error) {
			if options.TranslateResponse != nil {
				return options.TranslateResponse(body)
			}
			return translateResponse(body)
		},
		translateStreamFn: func(events <-chan map[string]any) <-chan llm.StreamEvent {
			if options.TranslateStream != nil {
				return options.TranslateStream(events)
			}
			return translateStream(events)
		},
	}
}

func NewFromEnv() (*Adapter, error) {
	opts, _ := transportOptionsFromEnv()
	return NewAdapter(AdapterOptions{TransportOptions: opts}), nil
}

func (a *Adapter) Name() string { return a.provider }

func (a *Adapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	resolved, err := resolveFileImages(req)
	if err != nil {
		return llm.Response{}, mapCodexError(err, a.provider, "complete")
	}

	translated, err := a.translateRequestFn(resolved, false)
	if err != nil {
		return llm.Response{}, mapCodexError(err, a.provider, "complete")
	}

	transport, err := a.getTransport()
	if err != nil {
		return llm.Response{}, mapCodexError(err, a.provider, "complete")
	}

	result, err := transport.Complete(ctx, translated.Payload)
	if err != nil {
		return llm.Response{}, mapCodexError(err, a.provider, "complete")
	}
	if embedded := extractTurnError(result); embedded != nil {
		return llm.Response{}, mapCodexError(embedded, a.provider, "complete")
	}

	response, err := a.translateResponseFn(result)
	if err != nil {
		return llm.Response{}, mapCodexError(err, a.provider, "complete")
	}
	response.Provider = a.provider
	if len(translated.Warnings) > 0 {
		response.Warnings = append(response.Warnings, translated.Warnings...)
	}
	return response, nil
}

func (a *Adapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	resolved, err := resolveFileImages(req)
	if err != nil {
		return nil, mapCodexError(err, a.provider, "stream")
	}
	translated, err := a.translateRequestFn(resolved, true)
	if err != nil {
		return nil, mapCodexError(err, a.provider, "stream")
	}

	transport, err := a.getTransport()
	if err != nil {
		return nil, mapCodexError(err, a.provider, "stream")
	}

	sctx, cancel := context.WithCancel(ctx)
	stream, err := transport.Stream(sctx, translated.Payload)
	if err != nil {
		cancel()
		return nil, mapCodexError(err, a.provider, "stream")
	}

	out := llm.NewChanStream(cancel)
	go func() {
		defer cancel()
		defer stream.Close()
		defer out.CloseSend()

		translatedEvents := a.translateStreamFn(stream.Notifications)
		warningsAttached := false
		for event := range translatedEvents {
			if !warningsAttached && event.Type == llm.StreamEventStreamStart {
				warningsAttached = true
				if len(translated.Warnings) > 0 {
					event.Warnings = append(event.Warnings, translated.Warnings...)
				}
				out.Send(event)
				continue
			}
			out.Send(event)
		}
		if !warningsAttached && len(translated.Warnings) > 0 {
			out.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart, Warnings: translated.Warnings})
		}
		if stream.Err != nil {
			if streamErr, ok := <-stream.Err; ok && streamErr != nil {
				out.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: mapCodexError(streamErr, a.provider, "stream")})
			}
		}
	}()

	return out, nil
}

func (a *Adapter) Initialize(ctx context.Context) error {
	transport, err := a.getTransport()
	if err != nil {
		return mapCodexError(err, a.provider, "complete")
	}
	if err := transport.Initialize(ctx); err != nil {
		return mapCodexError(err, a.provider, "complete")
	}
	return nil
}

func (a *Adapter) ListModels(ctx context.Context, params map[string]any) (modelListResponse, error) {
	a.modelListMu.Lock()
	if a.modelList != nil {
		cached := *a.modelList
		a.modelListMu.Unlock()
		return cached, nil
	}
	a.modelListMu.Unlock()

	transport, err := a.getTransport()
	if err != nil {
		return modelListResponse{}, mapCodexError(err, a.provider, "complete")
	}
	resp, err := transport.ListModels(ctx, params)
	if err != nil {
		return modelListResponse{}, mapCodexError(err, a.provider, "complete")
	}

	a.modelListMu.Lock()
	cp := resp
	a.modelList = &cp
	a.modelListMu.Unlock()
	return resp, nil
}

func (a *Adapter) GetDefaultModel(ctx context.Context) (*modelEntry, error) {
	resp, err := a.ListModels(ctx, nil)
	if err != nil {
		return nil, err
	}
	for idx := range resp.Data {
		if resp.Data[idx].IsDefault {
			entry := resp.Data[idx]
			return &entry, nil
		}
	}
	return nil, nil
}

func (a *Adapter) Close() error {
	a.transportMu.Lock()
	transport := a.transport
	a.transportMu.Unlock()
	if transport == nil {
		return nil
	}
	if err := transport.Close(); err != nil {
		return mapCodexError(err, a.provider, "complete")
	}
	return nil
}

func (a *Adapter) getTransport() (codexTransport, error) {
	if a.transportProvided != nil {
		return a.transportProvided, nil
	}
	a.transportMu.Lock()
	defer a.transportMu.Unlock()
	if a.transport != nil {
		return a.transport, nil
	}
	opts := a.transportOptions
	if envOpts, ok := transportOptionsFromEnv(); ok {
		if strings.TrimSpace(opts.Command) == "" {
			opts.Command = envOpts.Command
		}
		if len(opts.Args) == 0 {
			opts.Args = append([]string{}, envOpts.Args...)
		}
	}
	a.transport = NewTransport(opts)
	return a.transport, nil
}

func resolveFileImages(req llm.Request) (llm.Request, error) {
	resolved := req
	resolved.Messages = make([]llm.Message, len(req.Messages))
	for mi, message := range req.Messages {
		copyMessage := message
		copyMessage.Content = make([]llm.ContentPart, len(message.Content))
		for pi, part := range message.Content {
			copyPart := part
			if part.Kind == llm.ContentImage && part.Image != nil && len(part.Image.Data) == 0 {
				url := strings.TrimSpace(part.Image.URL)
				if isResolvableImagePath(url) {
					path := resolveImagePath(url)
					bytes, err := os.ReadFile(path)
					if err != nil {
						return llm.Request{}, err
					}
					mediaType := strings.TrimSpace(part.Image.MediaType)
					if mediaType == "" {
						mediaType = llm.InferMimeTypeFromPath(path)
					}
					if mediaType == "" {
						mediaType = "image/png"
					}
					copyPart.Image = &llm.ImageData{
						Data:      bytes,
						MediaType: mediaType,
						Detail:    part.Image.Detail,
					}
				}
			}
			copyMessage.Content[pi] = copyPart
		}
		resolved.Messages[mi] = copyMessage
	}
	return resolved, nil
}

func isResolvableImagePath(url string) bool {
	if strings.TrimSpace(url) == "" {
		return false
	}
	if strings.HasPrefix(strings.TrimSpace(url), "file://") {
		return true
	}
	return llm.IsLocalPath(url)
}

func resolveImagePath(url string) string {
	path := strings.TrimSpace(url)
	if strings.HasPrefix(path, "file://") {
		path = strings.TrimPrefix(path, "file://")
	}
	return llm.ExpandTilde(path)
}

type normalizedErrorInfo struct {
	Message    string
	Status     int
	HasStatus  bool
	Code       string
	RetryAfter *time.Duration
	Raw        any
}

func extractTurnError(value map[string]any) any {
	if value == nil {
		return nil
	}
	if turnError, ok := value["turnError"]; ok && turnError != nil {
		return turnError
	}
	if rootErr, ok := value["error"]; ok && rootErr != nil {
		return rootErr
	}
	turn := asMap(value["turn"])
	if turn == nil {
		return nil
	}
	if turnErr, ok := turn["error"]; ok && turnErr != nil {
		return turnErr
	}
	status := strings.ToLower(strings.TrimSpace(asString(turn["status"])))
	if status == "failed" || status == "error" {
		return turn
	}
	return nil
}

func mapCodexError(raw any, provider string, contextKind string) error {
	if raw == nil {
		return nil
	}
	if rawMap := asMap(raw); rawMap != nil {
		if embedded := extractTurnError(rawMap); embedded != nil {
			raw = embedded
		}
	}
	if err, ok := raw.(error); ok {
		if mapped := llm.WrapContextError(provider, err); mapped != err {
			return mapped
		}
		var llmErr llm.Error
		if errorsAs(err, &llmErr) {
			return err
		}
	}

	info := normalizeErrorInfo(raw)
	code := normalizeCode(info.Code)

	if isTransportFailure(code, info.Message) {
		if contextKind == "stream" {
			return llm.NewStreamError(provider, info.Message)
		}
		return llm.NewNetworkError(provider, info.Message)
	}

	if info.HasStatus {
		return llm.ErrorFromHTTPStatus(provider, info.Status, info.Message, info.Raw, info.RetryAfter)
	}

	if class := classifyByCode(code); class != "" {
		switch class {
		case "invalid_request":
			return llm.ErrorFromHTTPStatus(provider, 400, info.Message, info.Raw, nil)
		case "auth":
			return llm.ErrorFromHTTPStatus(provider, 401, info.Message, info.Raw, nil)
		case "rate_limit":
			return llm.ErrorFromHTTPStatus(provider, 429, info.Message, info.Raw, info.RetryAfter)
		case "server":
			return llm.ErrorFromHTTPStatus(provider, 500, info.Message, info.Raw, nil)
		}
	}

	msg := strings.ToLower(info.Message)
	switch {
	case strings.Contains(msg, "context length"), strings.Contains(msg, "too many tokens"):
		return llm.ErrorFromHTTPStatus(provider, 413, info.Message, info.Raw, nil)
	case strings.Contains(msg, "content filter"), strings.Contains(msg, "safety"):
		return llm.ErrorFromHTTPStatus(provider, 400, info.Message, info.Raw, nil)
	case strings.Contains(msg, "quota"), strings.Contains(msg, "billing"):
		return llm.ErrorFromHTTPStatus(provider, 429, info.Message, info.Raw, info.RetryAfter)
	case strings.Contains(msg, "not found"), strings.Contains(msg, "does not exist"):
		return llm.ErrorFromHTTPStatus(provider, 404, info.Message, info.Raw, nil)
	case strings.Contains(msg, "unauthorized"), strings.Contains(msg, "invalid key"):
		return llm.ErrorFromHTTPStatus(provider, 401, info.Message, info.Raw, nil)
	case strings.Contains(msg, "model") && (strings.Contains(msg, "not supported") || strings.Contains(msg, "unsupported") || strings.Contains(msg, "unknown model")):
		return llm.ErrorFromHTTPStatus(provider, 400, info.Message, info.Raw, nil)
	}

	if contextKind == "stream" {
		return llm.NewStreamError(provider, info.Message)
	}
	return llm.NewNetworkError(provider, info.Message)
}

func normalizeErrorInfo(raw any) normalizedErrorInfo {
	info := normalizedErrorInfo{
		Message: "codex-app-server request failed",
		Raw:     raw,
	}
	root := asMap(raw)
	nested := asMap(root["error"])
	source := root
	if nested != nil {
		source = nested
	}
	if source == nil {
		source = map[string]any{}
	}

	if err, ok := raw.(error); ok {
		info.Message = err.Error()
	}
	if message := firstNonEmpty(asString(source["message"]), asString(root["message"])); message != "" {
		info.Message = unwrapJSONMessage(message)
	}

	if statusVal, ok := source["status"]; ok {
		info.Status = asInt(statusVal, 0)
		info.HasStatus = true
	} else if statusVal, ok := root["status"]; ok {
		info.Status = asInt(statusVal, 0)
		info.HasStatus = true
	}

	info.Code = firstNonEmpty(
		asString(source["code"]),
		asString(source["type"]),
		asString(root["code"]),
		asString(root["type"]),
	)

	retry := source["retryAfter"]
	if retry == nil {
		retry = source["retry_after"]
	}
	if retry == nil {
		retry = root["retryAfter"]
	}
	if retry == nil {
		retry = root["retry_after"]
	}
	if retry != nil {
		seconds := asInt(retry, -1)
		if seconds >= 0 {
			d := time.Duration(seconds) * time.Second
			info.RetryAfter = &d
		}
	}
	return info
}

func unwrapJSONMessage(message string) string {
	trimmed := strings.TrimSpace(message)
	if !strings.HasPrefix(trimmed, "{") {
		return message
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.UseNumber()
	var payload map[string]any
	if err := dec.Decode(&payload); err != nil {
		return message
	}
	if detail := firstNonEmpty(asString(payload["detail"]), asString(payload["message"])); detail != "" {
		return detail
	}
	return message
}

func isTransportFailure(code, message string) bool {
	if code != "" {
		switch code {
		case "ECONNREFUSED", "ECONNRESET", "EPIPE", "ENOTFOUND", "EAI_AGAIN", "ETIMEDOUT", "EHOSTUNREACH", "ENETUNREACH", "ECONNABORTED":
			return true
		}
	}
	lower := strings.ToLower(message)
	return strings.Contains(lower, "broken pipe") ||
		strings.Contains(lower, "econnrefused") ||
		strings.Contains(lower, "econnreset") ||
		strings.Contains(lower, "epipe") ||
		strings.Contains(lower, "spawn")
}

func classifyByCode(code string) string {
	if code == "" {
		return ""
	}
	switch {
	case strings.Contains(code, "INVALID_REQUEST"), strings.Contains(code, "BAD_REQUEST"), strings.Contains(code, "UNSUPPORTED"), strings.Contains(code, "INVALID_ARGUMENT"), strings.Contains(code, "INVALID_INPUT"):
		return "invalid_request"
	case strings.Contains(code, "UNAUTHENTICATED"), strings.Contains(code, "INVALID_API_KEY"), strings.Contains(code, "AUTHENTICATION"):
		return "auth"
	case strings.Contains(code, "RATE_LIMIT"), strings.Contains(code, "TOO_MANY_REQUESTS"), strings.Contains(code, "RESOURCE_EXHAUSTED"):
		return "rate_limit"
	case strings.Contains(code, "INTERNAL"), strings.Contains(code, "SERVER_ERROR"), strings.Contains(code, "UNAVAILABLE"):
		return "server"
	default:
		return ""
	}
}

func errorsAs(err error, target any) bool {
	if err == nil {
		return false
	}
	return errors.As(err, target)
}
