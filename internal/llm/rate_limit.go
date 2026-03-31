package llm

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var firstIntRe = regexp.MustCompile(`[-+]?\d+`)

// ParseRateLimitInfo extracts informational rate limit metadata from response headers.
// The result is best-effort and intended for observability, not proactive throttling.
func ParseRateLimitInfo(headers http.Header, now time.Time) *RateLimitInfo {
	if headers == nil {
		return nil
	}

	reqRemaining := parseHeaderInt(headers,
		"x-ratelimit-remaining-requests",
		"x-ratelimit-remaining-request",
	)
	reqLimit := parseHeaderInt(headers,
		"x-ratelimit-limit-requests",
		"x-ratelimit-limit-request",
	)
	tokRemaining := parseHeaderInt(headers,
		"x-ratelimit-remaining-tokens",
		"x-ratelimit-remaining-token",
	)
	tokLimit := parseHeaderInt(headers,
		"x-ratelimit-limit-tokens",
		"x-ratelimit-limit-token",
	)

	// Fallback headers that do not distinguish requests vs tokens.
	if reqRemaining == nil && tokRemaining == nil {
		reqRemaining = parseHeaderInt(headers,
			"x-ratelimit-remaining",
			"ratelimit-remaining",
		)
	}
	if reqLimit == nil && tokLimit == nil {
		reqLimit = parseHeaderInt(headers,
			"x-ratelimit-limit",
			"ratelimit-limit",
		)
	}

	resetRaw := firstHeaderValue(headers,
		"x-ratelimit-reset-requests",
		"x-ratelimit-reset-request",
		"x-ratelimit-reset-tokens",
		"x-ratelimit-reset-token",
		"x-ratelimit-reset",
		"ratelimit-reset",
	)
	resetAt := parseRateLimitReset(resetRaw, now)

	if reqRemaining == nil && reqLimit == nil && tokRemaining == nil && tokLimit == nil && resetAt == "" {
		return nil
	}
	return &RateLimitInfo{
		RequestsRemaining: reqRemaining,
		RequestsLimit:     reqLimit,
		TokensRemaining:   tokRemaining,
		TokensLimit:       tokLimit,
		ResetAt:           resetAt,
	}
}

func firstHeaderValue(headers http.Header, keys ...string) string {
	for _, key := range keys {
		v := strings.TrimSpace(headers.Get(key))
		if v != "" {
			return v
		}
	}
	return ""
}

func parseHeaderInt(headers http.Header, keys ...string) *int {
	for _, key := range keys {
		if n, ok := parseIntLikeHeaderValue(headers.Get(key)); ok {
			return &n
		}
	}
	return nil
}

func parseIntLikeHeaderValue(v string) (int, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if i, err := strconv.Atoi(v); err == nil {
		return i, true
	}
	token := v
	if idx := strings.IndexAny(token, ",;"); idx >= 0 {
		token = strings.TrimSpace(token[:idx])
		if i, err := strconv.Atoi(token); err == nil {
			return i, true
		}
	}
	if f, err := strconv.ParseFloat(token, 64); err == nil {
		return int(f), true
	}
	if m := firstIntRe.FindString(v); m != "" {
		if i, err := strconv.Atoi(m); err == nil {
			return i, true
		}
	}
	return 0, false
}

func parseRateLimitReset(v string, now time.Time) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if t, err := http.ParseTime(v); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	if d, err := time.ParseDuration(v); err == nil {
		if d < 0 {
			d = 0
		}
		return now.Add(d).UTC().Format(time.RFC3339)
	}
	if f, ok := parseFloatLikeHeaderValue(v); ok {
		switch {
		case f >= 1e12:
			// Unix epoch in milliseconds.
			return time.UnixMilli(int64(f)).UTC().Format(time.RFC3339)
		case f >= 1e9:
			// Unix epoch in seconds.
			return time.Unix(int64(f), 0).UTC().Format(time.RFC3339)
		case f >= 0:
			// Relative seconds.
			return now.Add(time.Duration(f * float64(time.Second))).UTC().Format(time.RFC3339)
		}
	}
	return ""
}

func parseFloatLikeHeaderValue(v string) (float64, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		return f, true
	}
	token := v
	if idx := strings.IndexAny(token, ",;"); idx >= 0 {
		token = strings.TrimSpace(token[:idx])
		if f, err := strconv.ParseFloat(token, 64); err == nil {
			return f, true
		}
	}
	return 0, false
}
