package engine

import (
	"fmt"
	"strings"
)

func normalizePromptProbeTransport(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "complete", "responses", "response":
		return "complete"
	case "stream", "streaming":
		return "stream"
	default:
		return ""
	}
}

func normalizePromptProbeTransports(raw []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		normalized := normalizePromptProbeTransport(v)
		if normalized == "" {
			return nil, fmt.Errorf("invalid preflight.prompt_probes.transports value %q (want complete|stream)", strings.TrimSpace(v))
		}
		if seen[normalized] {
			continue
		}
		seen[normalized] = true
		out = append(out, normalized)
	}
	return out, nil
}
