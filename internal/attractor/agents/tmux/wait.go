// Waiting and readiness detection for tmux sessions.
package tmux

import (
	"context"
	"fmt"
	"time"
)

// WaitConfig controls how completion is detected.
type WaitConfig struct {
	PromptPrefix    string        // prompt prefix to detect (e.g. "❯")
	BusyIndicators  []string      // strings that indicate the agent is busy
	ConsecutiveIdle int           // required consecutive idle polls (default 2)
	PollInterval    time.Duration // interval between polls (default 200ms)
}

func (c WaitConfig) withDefaults() WaitConfig {
	if c.ConsecutiveIdle <= 0 {
		c.ConsecutiveIdle = 2
	}
	if c.PollInterval <= 0 {
		c.PollInterval = 200 * time.Millisecond
	}
	return c
}

// WaitForReady polls until the prompt prefix appears in the pane output.
// Used during startup to detect when the agent is ready for input.
func (m *Manager) WaitForReady(ctx context.Context, session, promptPrefix string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		lines, _ := m.CaptureLines(session, 10)
		if hasPromptPrefix(lines, promptPrefix) {
			return nil
		}
		// Check if pane died.
		if m.CheckHealth(session) != Healthy {
			return fmt.Errorf("session died while waiting for ready")
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for prompt prefix %q", promptPrefix)
}

// WaitForIdle polls until the agent shows the prompt prefix for consecutive
// polls with no busy indicators. This is the primary completion detection
// mechanism for interactive agent sessions.
func (m *Manager) WaitForIdle(ctx context.Context, session string, cfg WaitConfig, timeout time.Duration) error {
	cfg = cfg.withDefaults()
	deadline := time.Now().Add(timeout)
	consecutiveIdle := 0

	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		health := m.CheckHealth(session)
		if health == Dead {
			return fmt.Errorf("session died while waiting for idle")
		}
		if health == PaneDead {
			return nil // agent exited, treat as completed
		}

		lines, _ := m.CaptureLines(session, 10)
		if hasBusyIndicator(lines, cfg.BusyIndicators) {
			consecutiveIdle = 0
			time.Sleep(cfg.PollInterval)
			continue
		}
		if cfg.PromptPrefix != "" && hasPromptPrefix(lines, cfg.PromptPrefix) {
			consecutiveIdle++
			if consecutiveIdle >= cfg.ConsecutiveIdle {
				return nil
			}
		} else if cfg.PromptPrefix == "" {
			// No prompt prefix configured — rely solely on busy indicators.
			consecutiveIdle++
			if consecutiveIdle >= cfg.ConsecutiveIdle {
				return nil
			}
		} else {
			consecutiveIdle = 0
		}
		time.Sleep(cfg.PollInterval)
	}
	return fmt.Errorf("timeout waiting for idle (prompt=%q)", cfg.PromptPrefix)
}

// WaitForExit blocks until the session's pane process exits.
// Returns the exit status if available.
func (m *Manager) WaitForExit(ctx context.Context, session string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		health := m.CheckHealth(session)
		if health == Dead || health == PaneDead {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for session exit")
}
