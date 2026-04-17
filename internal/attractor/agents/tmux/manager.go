// Package tmux provides a Go wrapper for tmux session management.
// Used by the agent handler to spawn and manage CLI agent sessions.
package tmux

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync"
)

// Manager wraps tmux operations on a dedicated socket.
type Manager struct {
	socket string
	mu     sync.Mutex
	locks  sync.Map // per-session input locks
}

// NewManager creates a manager using the given tmux socket name.
// An isolated socket prevents collision with the user's interactive tmux.
func NewManager(socket string) *Manager {
	return &Manager{socket: socket}
}

var validSessionName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

// run executes a tmux command and returns stdout. Always passes -u for UTF-8.
func (m *Manager) run(args ...string) (string, error) {
	allArgs := []string{"-u"} // UTF-8 mode
	if m.socket != "" {
		allArgs = append(allArgs, "-L", m.socket)
	}
	allArgs = append(allArgs, args...)
	cmd := exec.Command("tmux", allArgs...)
	out, err := cmd.CombinedOutput()
	result := strings.TrimRight(string(out), "\n")
	if err != nil {
		if result != "" {
			return result, fmt.Errorf("tmux %s: %w: %s", args[0], err, result)
		}
		return "", fmt.Errorf("tmux %s: %w", args[0], err)
	}
	return result, nil
}

// displayMessage runs tmux display-message to query a format variable.
func (m *Manager) displayMessage(target, format string) (string, error) {
	return m.run("display-message", "-t", target, "-p", format)
}

// HasSession checks if a named session exists.
func (m *Manager) HasSession(name string) bool {
	_, err := m.run("has-session", "-t", name)
	return err == nil
}

// ListSessions returns all session names on this socket.
func (m *Manager) ListSessions() ([]string, error) {
	out, err := m.run("list-sessions", "-F", "#{session_name}")
	if err != nil {
		// No server running = no sessions.
		if strings.Contains(err.Error(), "no server") || strings.Contains(err.Error(), "no sessions") {
			return nil, nil
		}
		return nil, err
	}
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// SetEnvironment stores a key-value pair in the session environment.
func (m *Manager) SetEnvironment(session, key, value string) error {
	_, err := m.run("set-environment", "-t", session, key, value)
	return err
}

// GetEnvironment retrieves a value from the session environment.
func (m *Manager) GetEnvironment(session, key string) (string, error) {
	out, err := m.run("show-environment", "-t", session, key)
	if err != nil {
		return "", err
	}
	// Output format: "KEY=VALUE"
	if idx := strings.IndexByte(out, '='); idx >= 0 {
		return out[idx+1:], nil
	}
	return out, nil
}

// SendKeys sends raw key sequences (Enter, Down, Escape, etc.) to a session.
// Unlike SendInput, this does NOT use literal mode — key names are interpreted.
func (m *Manager) SendKeys(session string, keys ...string) error {
	args := []string{"send-keys", "-t", session}
	args = append(args, keys...)
	_, err := m.run(args...)
	return err
}

// acquireInputLock serializes input delivery to a session.
func (m *Manager) acquireInputLock(session string) {
	ch, _ := m.locks.LoadOrStore(session, make(chan struct{}, 1))
	ch.(chan struct{}) <- struct{}{}
}

// releaseInputLock releases the input lock for a session.
func (m *Manager) releaseInputLock(session string) {
	if ch, ok := m.locks.Load(session); ok {
		<-ch.(chan struct{})
	}
}
