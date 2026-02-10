package engine

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/strongdm/kilroy/internal/llm"
	"github.com/strongdm/kilroy/internal/providerspec"
)

func TestClassifyProviderCLIError_AnthropicStreamJSONRequiresVerbose(t *testing.T) {
	got := classifyProviderCLIError(
		"anthropic",
		"error: --output-format stream-json requires --verbose",
		errors.New("exit status 2"),
	)

	if got.FailureClass != failureClassDeterministic {
		t.Fatalf("FailureClass: got %q want %q", got.FailureClass, failureClassDeterministic)
	}
	if !strings.HasPrefix(got.FailureSignature, "provider_contract|anthropic|") {
		t.Fatalf("FailureSignature: got %q", got.FailureSignature)
	}
}

func TestClassifyProviderCLIError_GeminiModelNotFound(t *testing.T) {
	got := classifyProviderCLIError(
		"google",
		"Error: model gemini-2.5-pro was not found",
		errors.New("exit status 1"),
	)

	if got.FailureClass != failureClassDeterministic {
		t.Fatalf("FailureClass: got %q want %q", got.FailureClass, failureClassDeterministic)
	}
	if !strings.HasPrefix(got.FailureSignature, "provider_model_unavailable|google|") {
		t.Fatalf("FailureSignature: got %q", got.FailureSignature)
	}
}

func TestClassifyProviderCLIError_CodexIdleTimeout_RunErrSignal(t *testing.T) {
	got := classifyProviderCLIError(
		"openai",
		"",
		errors.New("codex cli idle timeout after 2m0s with no output activity"),
	)

	if got.FailureClass != failureClassTransientInfra {
		t.Fatalf("FailureClass: got %q want %q", got.FailureClass, failureClassTransientInfra)
	}
	if !strings.HasPrefix(got.FailureSignature, "provider_timeout|openai|") {
		t.Fatalf("FailureSignature: got %q", got.FailureSignature)
	}
}

func TestClassifyProviderCLIError_UnknownFallbackIsDeterministic(t *testing.T) {
	got := classifyProviderCLIError(
		"anthropic",
		"fatal: unexpected failure",
		errors.New("exit status 1"),
	)

	if got.FailureClass != failureClassDeterministic {
		t.Fatalf("FailureClass: got %q want %q", got.FailureClass, failureClassDeterministic)
	}
	if !strings.HasPrefix(got.FailureSignature, "provider_failure|anthropic|unknown") {
		t.Fatalf("FailureSignature: got %q", got.FailureSignature)
	}
}

func TestClassifyProviderCLIErrorWithContract_ExecutableMissing(t *testing.T) {
	got := classifyProviderCLIErrorWithContract(
		"openai",
		nil,
		"",
		&exec.Error{Name: "codex", Err: errors.New("not found")},
	)
	if got.Kind != providerCLIErrorKindExecutableMissing {
		t.Fatalf("Kind: got %q want %q", got.Kind, providerCLIErrorKindExecutableMissing)
	}
}

func TestClassifyProviderCLIErrorWithContract_CapabilityMissing(t *testing.T) {
	spec := &providerspec.CLISpec{
		CapabilityAll: []string{"--json", "--sandbox"},
	}
	got := classifyProviderCLIErrorWithContract(
		"openai",
		spec,
		"error: unknown option --foo",
		errors.New("exit status 2"),
	)
	if got.Kind != providerCLIErrorKindCapabilityMissing {
		t.Fatalf("Kind: got %q want %q", got.Kind, providerCLIErrorKindCapabilityMissing)
	}
}

func TestLooksLikeStreamDisconnect(t *testing.T) {
	cases := []struct {
		name   string
		stdout string
		want   bool
	}{
		{
			name:   "empty",
			stdout: "",
			want:   false,
		},
		{
			name:   "normal_codex_output",
			stdout: `{"type":"item.completed","item":{"id":"item_1","type":"command_execution"}}`,
			want:   false,
		},
		{
			name:   "stream_disconnected",
			stdout: `{"type":"error","message":"Reconnecting... 5/5 (stream disconnected before completion: stream closed before response.completed)"}`,
			want:   true,
		},
		{
			name:   "turn_failed_stream_closed",
			stdout: `{"type":"turn.failed","error":{"message":"stream closed before response.completed"}}`,
			want:   true,
		},
		{
			name:   "stream_disconnected_among_normal_events",
			stdout: `{"type":"item.completed","item":{"id":"item_1"}}
{"type":"item.completed","item":{"id":"item_2"}}
{"type":"error","message":"Reconnecting... 1/5 (stream disconnected before completion)"}
{"type":"turn.failed","error":{"message":"stream disconnected before completion"}}`,
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := looksLikeStreamDisconnect(tc.stdout)
			if got != tc.want {
				t.Fatalf("looksLikeStreamDisconnect: got %v want %v", got, tc.want)
			}
		})
	}
}

func TestClassifyAPIError(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantClass string
	}{
		// Typed LLM errors — deterministic
		{
			name:      "InvalidRequestError_400",
			err:       llm.ErrorFromHTTPStatus("openai", 400, "model not found", nil, nil),
			wantClass: failureClassDeterministic,
		},
		{
			name:      "AuthenticationError_401",
			err:       llm.ErrorFromHTTPStatus("kimi", 401, "invalid api key", nil, nil),
			wantClass: failureClassDeterministic,
		},
		{
			name:      "AccessDeniedError_403",
			err:       llm.ErrorFromHTTPStatus("openai", 403, "forbidden", nil, nil),
			wantClass: failureClassDeterministic,
		},
		{
			name:      "NotFoundError_404",
			err:       llm.ErrorFromHTTPStatus("openai", 404, "resource not found", nil, nil),
			wantClass: failureClassDeterministic,
		},
		{
			name:      "ContextLengthError_413",
			err:       llm.ErrorFromHTTPStatus("anthropic", 413, "context too long", nil, nil),
			wantClass: failureClassDeterministic,
		},
		{
			name:      "ContentFilterError_400",
			err:       llm.ErrorFromHTTPStatus("openai", 400, "blocked by content filter policy", nil, nil),
			wantClass: failureClassDeterministic,
		},
		{
			name:      "QuotaExceededError_400",
			err:       llm.ErrorFromHTTPStatus("openai", 400, "quota exceeded for account", nil, nil),
			wantClass: failureClassDeterministic,
		},
		{
			name:      "InvalidRequestError_422",
			err:       llm.ErrorFromHTTPStatus("openai", 422, "unprocessable", nil, nil),
			wantClass: failureClassDeterministic,
		},
		// Typed LLM errors — transient
		{
			name:      "RateLimitError_429",
			err:       llm.ErrorFromHTTPStatus("kimi", 429, "rate limited", nil, nil),
			wantClass: failureClassTransientInfra,
		},
		{
			name:      "ServerError_500",
			err:       llm.ErrorFromHTTPStatus("openai", 500, "internal error", nil, nil),
			wantClass: failureClassTransientInfra,
		},
		{
			name:      "ServerError_502",
			err:       llm.ErrorFromHTTPStatus("openai", 502, "bad gateway", nil, nil),
			wantClass: failureClassTransientInfra,
		},
		{
			name:      "ServerError_503",
			err:       llm.ErrorFromHTTPStatus("openai", 503, "service unavailable", nil, nil),
			wantClass: failureClassTransientInfra,
		},
		{
			name:      "RequestTimeoutError_408",
			err:       llm.ErrorFromHTTPStatus("openai", 408, "request timeout", nil, nil),
			wantClass: failureClassTransientInfra,
		},
		{
			name:      "UnknownHTTPError_599",
			err:       llm.ErrorFromHTTPStatus("openai", 599, "weird error", nil, nil),
			wantClass: failureClassTransientInfra,
		},
		// SDK errors — non-HTTP
		{
			name:      "NetworkError_transient",
			err:       llm.NewNetworkError("openai", "connection reset"),
			wantClass: failureClassTransientInfra,
		},
		{
			name:      "StreamError_transient",
			err:       llm.NewStreamError("openai", "stream interrupted"),
			wantClass: failureClassTransientInfra,
		},
		{
			name:      "AbortError_canceled",
			err:       llm.NewAbortError("user cancelled"),
			wantClass: failureClassCanceled,
		},
		{
			name:      "InvalidToolCallError_deterministic",
			err:       llm.NewInvalidToolCallError("bad tool"),
			wantClass: failureClassDeterministic,
		},
		{
			name:      "ConfigurationError_deterministic",
			err:       &llm.ConfigurationError{Message: "missing api key"},
			wantClass: failureClassDeterministic,
		},
		// Non-LLM errors — heuristic fallback
		{
			name:      "plain_connection_refused",
			err:       fmt.Errorf("dial tcp: connection refused"),
			wantClass: failureClassTransientInfra,
		},
		{
			name:      "plain_rate_limit",
			err:       fmt.Errorf("rate limit exceeded"),
			wantClass: failureClassTransientInfra,
		},
		{
			name:      "plain_timeout",
			err:       fmt.Errorf("context deadline exceeded"),
			wantClass: failureClassTransientInfra,
		},
		{
			name:      "plain_unknown",
			err:       fmt.Errorf("something unknown failed"),
			wantClass: failureClassDeterministic,
		},
		{
			name:      "nil_error",
			err:       nil,
			wantClass: failureClassDeterministic,
		},
		// Wrapped errors — errors.As unwraps through fmt.Errorf chains
		{
			name:      "wrapped_llm_error_400",
			err:       fmt.Errorf("agent loop: %w", llm.ErrorFromHTTPStatus("openai", 400, "model not found", nil, nil)),
			wantClass: failureClassDeterministic,
		},
		{
			name:      "wrapped_llm_error_429",
			err:       fmt.Errorf("agent loop: %w", llm.ErrorFromHTTPStatus("kimi", 429, "rate limited", nil, nil)),
			wantClass: failureClassTransientInfra,
		},
		// SDK errors that also occur in agent_loop path
		{
			name:      "NoObjectGeneratedError_deterministic",
			err:       llm.NewNoObjectGeneratedError("no output", "raw"),
			wantClass: failureClassDeterministic,
		},
		{
			name:      "UnsupportedToolChoiceError_deterministic",
			err:       llm.NewUnsupportedToolChoiceError("openai", "required"),
			wantClass: failureClassDeterministic,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotClass, gotSig := classifyAPIError(tc.err)
			if gotClass != tc.wantClass {
				t.Fatalf("classifyAPIError(%v): class=%q want %q", tc.err, gotClass, tc.wantClass)
			}
			if gotSig == "" {
				t.Fatalf("classifyAPIError(%v): signature is empty", tc.err)
			}
		})
	}
}

func TestClassifyAPIError_AbortErrorMapsToCanceledClass(t *testing.T) {
	cls, _ := classifyAPIError(llm.NewAbortError("operator canceled"))
	if cls != failureClassCanceled {
		t.Fatalf("class=%q want %q", cls, failureClassCanceled)
	}
}
