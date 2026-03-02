package llm

import (
	"strings"

	"github.com/danshapiro/kilroy/internal/modelmeta"
	"github.com/danshapiro/kilroy/internal/providerspec"
)

const kimiCodingMinMaxTokens = 16000

type ProviderExecutionPolicy struct {
	ForceStream  bool
	MinMaxTokens int
	MaxMaxTokens int
	Reason       string
}

func ExecutionPolicy(provider, model string) ProviderExecutionPolicy {
	p := ProviderExecutionPolicy{}

	switch providerspec.CanonicalProviderKey(provider) {
	case "kimi":
		p = ProviderExecutionPolicy{
			ForceStream:  true,
			MinMaxTokens: kimiCodingMinMaxTokens,
			Reason: "Kimi Coding requests must use stream=true with max_tokens>=16000 " +
				"to avoid tool-history continuation failures that often surface as misleading 429 overload errors.",
		}
	}

	if cap := modelMaxTokensCap(provider, model); cap > 0 {
		p.MaxMaxTokens = cap
	}
	return p
}

// modelMaxTokensCap returns the maximum output tokens allowed for the given
// provider+model combination, based on the provider's published model limits.
// Returns 0 if no cap is needed (model limit is at or above the provider default).
func modelMaxTokensCap(provider, model string) int {
	bare := strings.ToLower(strings.TrimSpace(modelmeta.ProviderRelativeModelID(provider, model)))
	// Strip variant suffixes like ":thinking", ":extended", etc.
	if i := strings.IndexByte(bare, ':'); i >= 0 {
		bare = bare[:i]
	}

	caps := map[string]int{
		// Anthropic models (provider default: 65,536)
		// claude-opus-4.6 has max_out=128,000 — no cap needed
		"claude-opus-4.5":   64000,
		"claude-opus-4.1":   32000,
		"claude-opus-4":     32000,
		"claude-sonnet-4.5": 64000,
		"claude-sonnet-4":   64000,
		"claude-3.7-sonnet": 64000,
		"claude-haiku-4.5":  64000,
		"claude-3.5-sonnet": 8192,
		"claude-3.5-haiku":  8192,
		"claude-3-haiku":    4096,

		// Google models (provider default: 32,768)
		// gemini-2.5-* models have max_out >= 65,535 — no cap needed
		"gemini-2.0-flash-001":      8192,
		"gemini-2.0-flash-lite-001": 8192,
	}

	return caps[bare]
}

func ApplyExecutionPolicy(req Request, policy ProviderExecutionPolicy) Request {
	// Apply minimum floor.
	if policy.MinMaxTokens > 0 {
		current := 0
		if req.MaxTokens != nil {
			current = *req.MaxTokens
		}
		if current < policy.MinMaxTokens {
			v := policy.MinMaxTokens
			req.MaxTokens = &v
		}
	}
	// Apply maximum cap. If both floor and cap are set, the floor wins
	// (provider protocol contracts like Kimi's are load-bearing).
	if policy.MaxMaxTokens > 0 {
		cap := policy.MaxMaxTokens
		if policy.MinMaxTokens > cap {
			cap = policy.MinMaxTokens
		}
		if req.MaxTokens == nil || *req.MaxTokens > cap {
			v := cap
			req.MaxTokens = &v
		}
	}
	return req
}
