package engine

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/model"
)

// BackoffConfig configures retry delays. This matches the attractor-spec BackoffConfig fields.
type BackoffConfig struct {
	InitialDelayMS int
	BackoffFactor  float64
	MaxDelayMS     int
	Jitter         bool
}

func defaultBackoffConfig() BackoffConfig {
	// Spec defaults: 200ms / factor 2.0 / cap 60s / jitter on.
	// Jitter is deterministic (SHA-256 seeded from runID:nodeID:attempt), so
	// identical inputs always produce identical delays — replay determinism is preserved.
	return BackoffConfig{
		InitialDelayMS: 200,
		BackoffFactor:  2.0,
		MaxDelayMS:     60_000,
		Jitter:         true,
	}
}

func backoffConfigFor(g *model.Graph, n *model.Node) BackoffConfig {
	cfg := defaultBackoffConfig()
	get := func(key string) string {
		if n != nil {
			if v, ok := n.Attrs[key]; ok && strings.TrimSpace(v) != "" {
				return v
			}
		}
		if g != nil {
			if v, ok := g.Attrs[key]; ok && strings.TrimSpace(v) != "" {
				return v
			}
		}
		return ""
	}

	if v := strings.TrimSpace(get("retry.backoff.initial_delay_ms")); v != "" {
		cfg.InitialDelayMS = parseInt(v, cfg.InitialDelayMS)
	}
	if v := strings.TrimSpace(get("retry.backoff.backoff_factor")); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			cfg.BackoffFactor = f
		}
	}
	if v := strings.TrimSpace(get("retry.backoff.max_delay_ms")); v != "" {
		cfg.MaxDelayMS = parseInt(v, cfg.MaxDelayMS)
	}
	if v := strings.TrimSpace(get("retry.backoff.jitter")); v != "" {
		cfg.Jitter = parseBool(v, cfg.Jitter)
	}

	// Sanity.
	if cfg.InitialDelayMS < 0 {
		cfg.InitialDelayMS = 0
	}
	if cfg.MaxDelayMS < 0 {
		cfg.MaxDelayMS = 0
	}
	if cfg.BackoffFactor <= 0 {
		cfg.BackoffFactor = 1.0
	}
	return cfg
}

func DelayForAttempt(attempt int, cfg BackoffConfig, jitterSeed string) time.Duration {
	// attempt is 1-indexed: first retry is attempt=1 (attractor-spec).
	if attempt < 1 {
		attempt = 1
	}
	if cfg.InitialDelayMS <= 0 {
		return 0
	}

	// base = initial * factor^(attempt-1), capped.
	baseMS := float64(cfg.InitialDelayMS) * math.Pow(cfg.BackoffFactor, float64(attempt-1))
	if cfg.MaxDelayMS > 0 {
		baseMS = math.Min(baseMS, float64(cfg.MaxDelayMS))
	}

	// Apply jitter after capping (matches spec pseudocode).
	if cfg.Jitter {
		m := 0.5 + jitterUnit(jitterSeed) // [0.5, 1.5]
		baseMS *= m
	}

	if baseMS < 0 {
		baseMS = 0
	}
	return time.Duration(baseMS * float64(time.Millisecond))
}

func jitterUnit(seed string) float64 {
	sum := sha256.Sum256([]byte(seed))
	u := binary.BigEndian.Uint64(sum[:8])
	// Map uint64 -> [0,1]. Avoid division by zero.
	const max = float64(^uint64(0))
	if max <= 0 {
		return 0
	}
	return float64(u) / max
}

func parseBool(s string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "yes", "y":
		return true
	case "false", "0", "no", "n":
		return false
	default:
		return def
	}
}

func backoffDelayForNode(runID string, g *model.Graph, n *model.Node, attempt int) time.Duration {
	seed := fmt.Sprintf("%s:%s:%d", strings.TrimSpace(runID), func() string {
		if n == nil {
			return ""
		}
		return n.ID
	}(), attempt)
	return DelayForAttempt(attempt, backoffConfigFor(g, n), seed)
}

