// Output capture and pattern matching for tmux panes.
package tmux

import (
	"fmt"
	"strings"
)

// CaptureOutput captures the last N lines from a session's pane.
// If lines <= 0, captures the entire scrollback.
func (m *Manager) CaptureOutput(session string, lines int) (string, error) {
	startArg := fmt.Sprintf("-%d", lines)
	if lines <= 0 {
		startArg = "-" // entire scrollback
	}
	out, err := m.run("capture-pane", "-p", "-t", session, "-S", startArg)
	if err != nil {
		return "", err
	}
	return out, nil
}

// CaptureLines captures the last N lines and returns them as a slice.
func (m *Manager) CaptureLines(session string, lines int) ([]string, error) {
	out, err := m.CaptureOutput(session, lines)
	if err != nil {
		return nil, err
	}
	return strings.Split(out, "\n"), nil
}

// normalizeForMatching replaces NBSP (U+00A0) with regular space.
// Claude Code uses NBSP after its prompt character.
func normalizeForMatching(s string) string {
	return strings.ReplaceAll(s, "\u00a0", " ")
}

// hasPromptPrefix checks if any line starts with the given prefix.
func hasPromptPrefix(lines []string, prefix string) bool {
	for _, line := range lines {
		normalized := normalizeForMatching(strings.TrimSpace(line))
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}

// hasBusyIndicator checks if any line contains a busy indicator string.
func hasBusyIndicator(lines []string, indicators []string) bool {
	for _, line := range lines {
		normalized := normalizeForMatching(line)
		for _, ind := range indicators {
			if strings.Contains(normalized, ind) {
				return true
			}
		}
	}
	return false
}
