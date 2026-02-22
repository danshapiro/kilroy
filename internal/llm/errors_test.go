package llm

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestParseRetryAfter_Seconds(t *testing.T) {
	now := time.Date(2026, 2, 7, 0, 0, 0, 0, time.UTC)
	d := ParseRetryAfter("12", now)
	if d == nil || *d != 12*time.Second {
		t.Fatalf("got %v want 12s", d)
	}
}

func TestParseRetryAfter_HTTPDate(t *testing.T) {
	now := time.Date(2026, 2, 7, 0, 0, 0, 0, time.UTC)
	d := ParseRetryAfter("Sat, 07 Feb 2026 00:00:10 GMT", now)
	if d == nil || *d != 10*time.Second {
		t.Fatalf("got %v want 10s", d)
	}
}

func TestErrorFromHTTPStatus_MappingAndRetryable(t *testing.T) {
	cases := []struct {
		status    int
		wantType  any
		retryable bool
	}{
		{status: 400, wantType: &InvalidRequestError{}, retryable: false},
		{status: 401, wantType: &AuthenticationError{}, retryable: false},
		{status: 403, wantType: &AccessDeniedError{}, retryable: false},
		{status: 404, wantType: &NotFoundError{}, retryable: false},
		{status: 408, wantType: &RequestTimeoutError{}, retryable: true},
		{status: 413, wantType: &ContextLengthError{}, retryable: false},
		{status: 422, wantType: &InvalidRequestError{}, retryable: false},
		{status: 429, wantType: &RateLimitError{}, retryable: true},
		{status: 500, wantType: &ServerError{}, retryable: true},
		{status: 503, wantType: &ServerError{}, retryable: true},
		{status: 599, wantType: &UnknownHTTPError{}, retryable: true},
	}
	for _, tc := range cases {
		err := ErrorFromHTTPStatus("p", tc.status, "msg", nil, nil)
		switch tc.wantType.(type) {
		case *InvalidRequestError:
			if _, ok := err.(*InvalidRequestError); !ok {
				t.Fatalf("status %d: got %T", tc.status, err)
			}
		case *AuthenticationError:
			if _, ok := err.(*AuthenticationError); !ok {
				t.Fatalf("status %d: got %T", tc.status, err)
			}
		case *AccessDeniedError:
			if _, ok := err.(*AccessDeniedError); !ok {
				t.Fatalf("status %d: got %T", tc.status, err)
			}
		case *NotFoundError:
			if _, ok := err.(*NotFoundError); !ok {
				t.Fatalf("status %d: got %T", tc.status, err)
			}
		case *RequestTimeoutError:
			if _, ok := err.(*RequestTimeoutError); !ok {
				t.Fatalf("status %d: got %T", tc.status, err)
			}
		case *ContextLengthError:
			if _, ok := err.(*ContextLengthError); !ok {
				t.Fatalf("status %d: got %T", tc.status, err)
			}
		case *RateLimitError:
			if _, ok := err.(*RateLimitError); !ok {
				t.Fatalf("status %d: got %T", tc.status, err)
			}
		case *ServerError:
			if _, ok := err.(*ServerError); !ok {
				t.Fatalf("status %d: got %T", tc.status, err)
			}
		case *UnknownHTTPError:
			if _, ok := err.(*UnknownHTTPError); !ok {
				t.Fatalf("status %d: got %T", tc.status, err)
			}
		}
		e, ok := err.(Error)
		if !ok {
			t.Fatalf("status %d: not an llm.Error (%T)", tc.status, err)
		}
		if e.Retryable() != tc.retryable {
			t.Fatalf("status %d: retryable=%t want %t", tc.status, e.Retryable(), tc.retryable)
		}
	}
}

func TestContentFilterError_ImplementsErrorInterface(t *testing.T) {
	err := &ContentFilterError{httpErrorBase{provider: "test", statusCode: 400, message: "blocked", retryable: false}}
	var llmErr Error
	if !errors.As(err, &llmErr) {
		t.Fatalf("ContentFilterError does not implement Error interface")
	}
	if llmErr.Provider() != "test" {
		t.Fatalf("Provider: %q", llmErr.Provider())
	}
	if llmErr.Retryable() {
		t.Fatalf("expected non-retryable")
	}
}

func TestQuotaExceededError_ImplementsErrorInterface(t *testing.T) {
	err := &QuotaExceededError{httpErrorBase{provider: "test", statusCode: 429, message: "quota exceeded", retryable: false}}
	var llmErr Error
	if !errors.As(err, &llmErr) {
		t.Fatalf("QuotaExceededError does not implement Error interface")
	}
	if llmErr.Provider() != "test" {
		t.Fatalf("Provider: %q", llmErr.Provider())
	}
	if llmErr.Retryable() {
		t.Fatalf("expected non-retryable")
	}
}

func TestErrorFromHTTPStatus_ToolUseMismatch_IsRetryable(t *testing.T) {
	err := ErrorFromHTTPStatus("anthropic", 400, "tool_use ids were found without tool_result blocks immediately after", nil, nil)
	e, ok := err.(Error)
	if !ok {
		t.Fatalf("not an llm.Error: %T", err)
	}
	if !e.Retryable() {
		t.Fatalf("expected retryable, got non-retryable")
	}
	if _, ok := err.(*ServerError); !ok {
		t.Fatalf("expected *ServerError, got %T", err)
	}
}

func TestErrorFromHTTPStatus_MessageBasedClassification(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		message string
		want    string
	}{
		{"400 content filter", 400, "content filter policy violated", "*llm.ContentFilterError"},
		{"400 safety", 400, "blocked by safety settings", "*llm.ContentFilterError"},
		{"400 context length", 400, "context length exceeded", "*llm.ContextLengthError"},
		{"400 too many tokens", 400, "too many tokens in request", "*llm.ContextLengthError"},
		{"400 quota", 400, "quota exceeded for billing account", "*llm.QuotaExceededError"},
		{"400 billing", 400, "billing issue on account", "*llm.QuotaExceededError"},
		{"400 not found", 400, "model does not exist", "*llm.NotFoundError"},
		{"400 unauthorized", 400, "invalid key", "*llm.AuthenticationError"},
		{"400 plain", 400, "bad request", "*llm.InvalidRequestError"},
		{"422 content filter", 422, "this violates safety policy", "*llm.ContentFilterError"},
		{"422 plain", 422, "invalid field", "*llm.InvalidRequestError"},
		{"401 always auth", 401, "content filter something", "*llm.AuthenticationError"},
		{"429 always rate", 429, "quota exceeded", "*llm.RateLimitError"},
		{"404 always notfound", 404, "quota exceeded", "*llm.NotFoundError"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ErrorFromHTTPStatus("p", tc.status, tc.message, nil, nil)
			if got := fmt.Sprintf("%T", err); got != tc.want {
				t.Fatalf("ErrorFromHTTPStatus(%d, %q) = %s, want %s", tc.status, tc.message, got, tc.want)
			}
		})
	}
}
