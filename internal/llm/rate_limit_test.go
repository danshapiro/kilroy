package llm

import (
	"net/http"
	"testing"
	"time"
)

func TestParseRateLimitInfo_ProviderHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("x-ratelimit-remaining-requests", "9")
	h.Set("x-ratelimit-limit-requests", "10")
	h.Set("x-ratelimit-remaining-tokens", "90")
	h.Set("x-ratelimit-limit-tokens", "100")
	h.Set("x-ratelimit-reset-requests", "Wed, 01 Jan 2025 00:00:10 GMT")

	got := ParseRateLimitInfo(h, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
	if got == nil {
		t.Fatalf("expected rate limit info")
	}
	if got.RequestsRemaining == nil || *got.RequestsRemaining != 9 {
		t.Fatalf("requests_remaining: %#v", got.RequestsRemaining)
	}
	if got.RequestsLimit == nil || *got.RequestsLimit != 10 {
		t.Fatalf("requests_limit: %#v", got.RequestsLimit)
	}
	if got.TokensRemaining == nil || *got.TokensRemaining != 90 {
		t.Fatalf("tokens_remaining: %#v", got.TokensRemaining)
	}
	if got.TokensLimit == nil || *got.TokensLimit != 100 {
		t.Fatalf("tokens_limit: %#v", got.TokensLimit)
	}
	if got.ResetAt != "2025-01-01T00:00:10Z" {
		t.Fatalf("reset_at: %q", got.ResetAt)
	}
}

func TestParseRateLimitInfo_FallbackHeaders(t *testing.T) {
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	h := http.Header{}
	h.Set("x-ratelimit-remaining", "5")
	h.Set("x-ratelimit-limit", "8")
	h.Set("ratelimit-reset", "5")

	got := ParseRateLimitInfo(h, now)
	if got == nil {
		t.Fatalf("expected rate limit info")
	}
	if got.RequestsRemaining == nil || *got.RequestsRemaining != 5 {
		t.Fatalf("requests_remaining: %#v", got.RequestsRemaining)
	}
	if got.RequestsLimit == nil || *got.RequestsLimit != 8 {
		t.Fatalf("requests_limit: %#v", got.RequestsLimit)
	}
	if got.TokensRemaining != nil {
		t.Fatalf("tokens_remaining: %#v", got.TokensRemaining)
	}
	if got.TokensLimit != nil {
		t.Fatalf("tokens_limit: %#v", got.TokensLimit)
	}
	if got.ResetAt != "2025-01-01T00:00:05Z" {
		t.Fatalf("reset_at: %q", got.ResetAt)
	}
}

func TestParseRateLimitInfo_ResetFormats(t *testing.T) {
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		value  string
		expect string
	}{
		{name: "duration", value: "2s", expect: "2025-01-01T00:00:02Z"},
		{name: "epoch_seconds", value: "1735689610", expect: "2025-01-01T00:00:10Z"},
		{name: "epoch_millis", value: "1735689610000", expect: "2025-01-01T00:00:10Z"},
		{name: "relative_seconds_float", value: "1.5", expect: "2025-01-01T00:00:01Z"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			h.Set("x-ratelimit-reset", tc.value)
			got := ParseRateLimitInfo(h, now)
			if got == nil {
				t.Fatalf("expected rate limit info")
			}
			if got.ResetAt != tc.expect {
				t.Fatalf("reset_at: got %q want %q", got.ResetAt, tc.expect)
			}
		})
	}
}

func TestParseRateLimitInfo_Empty(t *testing.T) {
	got := ParseRateLimitInfo(http.Header{}, time.Now())
	if got != nil {
		t.Fatalf("expected nil, got %#v", got)
	}
}
