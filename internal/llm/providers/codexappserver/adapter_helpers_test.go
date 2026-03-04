package codexappserver

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/llm"
)

func TestAdapterHelpers_PathAndTurnExtraction(t *testing.T) {
	if isResolvableImagePath("") {
		t.Fatalf("empty image path should not be resolvable")
	}
	if !isResolvableImagePath("file:///tmp/image.png") {
		t.Fatalf("file:// image path should be resolvable")
	}
	if !strings.HasSuffix(resolveImagePath("file:///tmp/image.png"), "/tmp/image.png") {
		t.Fatalf("resolved file path mismatch: %q", resolveImagePath("file:///tmp/image.png"))
	}

	if got := extractTurnError(nil); got != nil {
		t.Fatalf("nil map should not produce turn error, got %#v", got)
	}
	turnErr := map[string]any{"message": "bad turn"}
	if got := asMap(extractTurnError(map[string]any{"turnError": turnErr})); got["message"] != "bad turn" {
		t.Fatalf("turnError precedence mismatch: %#v", got)
	}
	rootErr := map[string]any{"message": "bad root"}
	if got := asMap(extractTurnError(map[string]any{"error": rootErr})); got["message"] != "bad root" {
		t.Fatalf("root error extraction mismatch: %#v", got)
	}
	turn := map[string]any{"id": "turn_1", "status": "failed"}
	if got := asMap(extractTurnError(map[string]any{"turn": turn})); got["id"] != "turn_1" {
		t.Fatalf("failed turn should be treated as error payload: %#v", got)
	}
}

func TestAdapterHelpers_NormalizeAndClassifyErrorInfo(t *testing.T) {
	info := normalizeErrorInfo(map[string]any{
		"error": map[string]any{
			"message":    `{"detail":"wrapped detail"}`,
			"status":     429,
			"code":       "RATE_LIMIT",
			"retryAfter": 7,
		},
	})
	if info.Message != "wrapped detail" {
		t.Fatalf("message: got %q want %q", info.Message, "wrapped detail")
	}
	if !info.HasStatus || info.Status != 429 {
		t.Fatalf("status: got has=%v status=%d", info.HasStatus, info.Status)
	}
	if info.Code != "RATE_LIMIT" {
		t.Fatalf("code: got %q", info.Code)
	}
	if info.RetryAfter == nil || *info.RetryAfter != 7*time.Second {
		t.Fatalf("retryAfter: got %#v", info.RetryAfter)
	}

	if got := unwrapJSONMessage("plain text"); got != "plain text" {
		t.Fatalf("unwrap plain text: got %q", got)
	}
	if got := unwrapJSONMessage(`{"message":"msg fallback"}`); got != "msg fallback" {
		t.Fatalf("unwrap json message fallback: got %q", got)
	}
	if !isTransportFailure("ECONNREFUSED", "ignored") {
		t.Fatalf("expected transport failure by code")
	}
	if !isTransportFailure("", "broken pipe from child process") {
		t.Fatalf("expected transport failure by message")
	}
	if isTransportFailure("INVALID_REQUEST", "input is malformed") {
		t.Fatalf("did not expect invalid request to be transport failure")
	}

	if got := classifyByCode("INVALID_REQUEST"); got != "invalid_request" {
		t.Fatalf("classify invalid_request: got %q", got)
	}
	if got := classifyByCode("UNAUTHENTICATED"); got != "auth" {
		t.Fatalf("classify auth: got %q", got)
	}
	if got := classifyByCode("RESOURCE_EXHAUSTED"); got != "rate_limit" {
		t.Fatalf("classify rate_limit: got %q", got)
	}
	if got := classifyByCode("SERVER_ERROR"); got != "server" {
		t.Fatalf("classify server: got %q", got)
	}
	if got := classifyByCode("SOMETHING_ELSE"); got != "" {
		t.Fatalf("unexpected classification: %q", got)
	}

	var target *llm.AuthenticationError
	if errorsAs(nil, &target) {
		t.Fatalf("errorsAs should return false for nil error")
	}
	authErr := llm.ErrorFromHTTPStatus("codex-app-server", 401, "bad key", nil, nil)
	if !errorsAs(authErr, &target) {
		t.Fatalf("expected errorsAs to match authentication error")
	}
}

func TestAdapterHelpers_MapCodexError_Branches(t *testing.T) {
	if err := mapCodexError(nil, providerName, "complete"); err != nil {
		t.Fatalf("nil error should stay nil, got %v", err)
	}

	timeoutErr := mapCodexError(context.DeadlineExceeded, providerName, "complete")
	var requestTimeout *llm.RequestTimeoutError
	if !errors.As(timeoutErr, &requestTimeout) {
		t.Fatalf("expected RequestTimeoutError from wrapped context deadline, got %T (%v)", timeoutErr, timeoutErr)
	}

	transportErr := mapCodexError(map[string]any{
		"error": map[string]any{
			"code":    "EPIPE",
			"message": "broken pipe",
		},
	}, providerName, "stream")
	var streamErr *llm.StreamError
	if !errors.As(transportErr, &streamErr) {
		t.Fatalf("expected StreamError for stream transport failures, got %T (%v)", transportErr, transportErr)
	}

	statusErr := mapCodexError(map[string]any{
		"error": map[string]any{
			"status":  404,
			"message": "not found",
		},
	}, providerName, "complete")
	var notFound *llm.NotFoundError
	if !errors.As(statusErr, &notFound) {
		t.Fatalf("expected NotFoundError from explicit status, got %T (%v)", statusErr, statusErr)
	}

	classifiedErr := mapCodexError(map[string]any{
		"error": map[string]any{
			"code":    "RATE_LIMIT",
			"message": "too many requests",
		},
	}, providerName, "complete")
	var rateLimit *llm.RateLimitError
	if !errors.As(classifiedErr, &rateLimit) {
		t.Fatalf("expected RateLimitError from classified code, got %T (%v)", classifiedErr, classifiedErr)
	}

	messageHintErr := mapCodexError(map[string]any{
		"message": "model not supported for this endpoint",
	}, providerName, "complete")
	var invalidRequest *llm.InvalidRequestError
	if !errors.As(messageHintErr, &invalidRequest) {
		t.Fatalf("expected InvalidRequestError from message hint, got %T (%v)", messageHintErr, messageHintErr)
	}

	fallbackErr := mapCodexError(map[string]any{
		"message": "completely unknown failure",
	}, providerName, "complete")
	var networkErr *llm.NetworkError
	if !errors.As(fallbackErr, &networkErr) {
		t.Fatalf("expected NetworkError fallback for unknown complete errors, got %T (%v)", fallbackErr, fallbackErr)
	}
}

func TestAdapterHelpers_BasicLifecycleAndModelSelection(t *testing.T) {
	t.Setenv(envCommand, "codex-test")
	adapterFromEnv, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	if adapterFromEnv == nil {
		t.Fatalf("expected adapter from env")
	}
	if adapterFromEnv.Name() != providerName {
		t.Fatalf("Name: got %q want %q", adapterFromEnv.Name(), providerName)
	}
	if adapterFromEnv.transportOptions.Command != "codex-test" {
		t.Fatalf("transport command from env: got %q", adapterFromEnv.transportOptions.Command)
	}

	initCalls := 0
	closeCalls := 0
	adapter := NewAdapter(AdapterOptions{
		Transport: &fakeTransport{
			initializeFn: func(ctx context.Context) error {
				initCalls++
				return nil
			},
			closeFn: func() error {
				closeCalls++
				return nil
			},
			listFn: func(ctx context.Context, params map[string]any) (modelListResponse, error) {
				return modelListResponse{
					Data: []modelEntry{
						{ID: "model_a", Model: "codex-mini"},
						{ID: "model_b", Model: "codex-pro", IsDefault: true},
					},
				}, nil
			},
		},
	})

	if err := adapter.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if initCalls != 1 {
		t.Fatalf("initialize calls: got %d want 1", initCalls)
	}

	def, err := adapter.GetDefaultModel(context.Background())
	if err != nil {
		t.Fatalf("GetDefaultModel: %v", err)
	}
	if def == nil || def.ID != "model_b" {
		t.Fatalf("default model mismatch: %#v", def)
	}

	adapter.transport = &fakeTransport{
		closeFn: func() error {
			closeCalls++
			return nil
		},
	}
	if err := adapter.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if closeCalls != 1 {
		t.Fatalf("close calls: got %d want 1", closeCalls)
	}

	adapterNoTransport := NewAdapter(AdapterOptions{})
	if err := adapterNoTransport.Close(); err != nil {
		t.Fatalf("Close without transport should succeed, got %v", err)
	}
}

func TestAdapterHelpers_ProviderOverride(t *testing.T) {
	adapter := NewAdapter(AdapterOptions{Provider: "custom-codex-provider"})
	if got := adapter.Name(); got != "custom-codex-provider" {
		t.Fatalf("Name: got %q want %q", got, "custom-codex-provider")
	}
}

func TestAdapterHelpers_GetTransport_CachesAndRespectsProvidedTransport(t *testing.T) {
	provided := &fakeTransport{}
	adapterWithProvided := NewAdapter(AdapterOptions{Transport: provided})
	got, err := adapterWithProvided.getTransport()
	if err != nil {
		t.Fatalf("getTransport provided: %v", err)
	}
	if got != provided {
		t.Fatalf("expected provided transport to be returned")
	}

	t.Setenv(envCommand, "")
	t.Setenv(envArgs, "")
	t.Setenv(envCommandArgs, "")
	_ = os.Unsetenv(envCommand)

	adapter := NewAdapter(AdapterOptions{
		TransportOptions: TransportOptions{
			Command: "codex-custom",
			Args:    []string{"app-server", "--listen", "stdio://"},
		},
	})
	first, err := adapter.getTransport()
	if err != nil {
		t.Fatalf("getTransport first: %v", err)
	}
	second, err := adapter.getTransport()
	if err != nil {
		t.Fatalf("getTransport second: %v", err)
	}
	if first != second {
		t.Fatalf("expected getTransport to cache created transport instance")
	}
}
