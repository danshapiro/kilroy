// Session lifecycle: create, destroy, health checks.
package tmux

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Session represents an active tmux session managed by kilroy.
type Session struct {
	Name    string
	PaneID  string
	WorkDir string
}

// CreateSession creates a tmux session using the two-step pattern:
// create with shell, then replace via respawn-pane with the actual command.
func (m *Manager) CreateSession(name, workDir, command string, env map[string]string) (*Session, error) {
	if !validSessionName.MatchString(name) {
		return nil, fmt.Errorf("invalid session name: %q", name)
	}

	// Build args with sorted env vars for determinism.
	args := []string{"new-session", "-d", "-s", name, "-c", workDir, "-x", "200", "-y", "50"}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, env[k]))
	}

	if _, err := m.run(args...); err != nil {
		return nil, fmt.Errorf("new-session: %w", err)
	}

	// Set window-size=latest (tmux 3.3+ defaults to manual = 80x24).
	m.run("set-option", "-wt", name, "window-size", "latest")
	// Remain-on-exit so we can inspect the pane after command exits.
	m.run("set-option", "-t", name, "remain-on-exit", "on")

	// Replace the default shell with the actual command.
	if command != "" {
		if _, err := m.run("respawn-pane", "-k", "-t", name, command); err != nil {
			m.run("kill-session", "-t", name)
			return nil, fmt.Errorf("respawn-pane: %w", err)
		}
	}

	// Health check: did the command crash immediately?
	time.Sleep(50 * time.Millisecond)
	if dead, _ := m.displayMessage(name, "#{pane_dead}"); dead == "1" {
		// Capture last output for diagnostics.
		lastOutput, _ := m.CaptureOutput(name, 10)
		m.run("kill-session", "-t", name)
		return nil, fmt.Errorf("command exited immediately: %s", lastOutput)
	}

	paneID, _ := m.displayMessage(name, "#{pane_id}")
	return &Session{Name: name, PaneID: paneID, WorkDir: workDir}, nil
}

// DestroySession kills a session and its entire process tree.
func (m *Manager) DestroySession(name string) error {
	if !m.HasSession(name) {
		return nil
	}

	// Get pane PID for process tree cleanup.
	pidStr, err := m.displayMessage(name, "#{pane_pid}")
	if err == nil && pidStr != "" {
		if pid, err := strconv.Atoi(pidStr); err == nil && pid > 0 {
			cleanupProcessTree(pid)
		}
	}

	_, err = m.run("kill-session", "-t", name)
	return err
}

// SessionHealth describes the health state of a session.
type SessionHealth int

const (
	Healthy  SessionHealth = iota // Session and agent process running
	Dead                          // Session doesn't exist
	PaneDead                      // Session exists but pane process exited
)

// CheckHealth returns the health state of a session.
func (m *Manager) CheckHealth(name string) SessionHealth {
	if !m.HasSession(name) {
		return Dead
	}
	if dead, _ := m.displayMessage(name, "#{pane_dead}"); dead == "1" {
		return PaneDead
	}
	return Healthy
}

// PanePID returns the PID of the process running in the session's pane.
func (m *Manager) PanePID(name string) (int, error) {
	pidStr, err := m.displayMessage(name, "#{pane_pid}")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(pidStr)
}

// PaneExitStatus returns the exit code of a dead pane's process.
// Returns -1 if the pane is still alive or the status can't be read.
func (m *Manager) PaneExitStatus(name string) int {
	s, err := m.displayMessage(name, "#{pane_dead_status}")
	if err != nil {
		return -1
	}
	code, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return -1
	}
	return code
}
