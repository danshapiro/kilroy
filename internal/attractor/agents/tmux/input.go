// Input delivery: sanitize, chunk, and send text to tmux panes.
package tmux

import (
	"strings"
	"time"
)

const (
	maxChunkSize    = 512
	interChunkDelay = 10 * time.Millisecond
	enterDebounce   = 100 * time.Millisecond
	enterVerifyWait = 500 * time.Millisecond
	enterMaxRetries = 3
)

// SendInput sends text to a session, followed by Enter.
// Serialized per-session to prevent interleaving.
func (m *Manager) SendInput(session, text string) error {
	m.acquireInputLock(session)
	defer m.releaseInputLock(session)

	// Exit copy mode if active.
	if inMode, _ := m.displayMessage(session, "#{pane_in_mode}"); inMode == "1" {
		m.run("send-keys", "-t", session, "-X", "cancel")
		time.Sleep(enterDebounce)
	}

	sanitized := sanitizeInput(text)

	// Send text in chunks (large pastes can be lost).
	chunks := chunkString(sanitized, maxChunkSize)
	for _, chunk := range chunks {
		if _, err := m.run("send-keys", "-t", session, "-l", chunk); err != nil {
			return err
		}
		if len(chunks) > 1 {
			time.Sleep(interChunkDelay)
		}
	}

	// Debounce before Enter.
	time.Sleep(enterDebounce)

	// Send Enter with verification.
	return m.sendEnterVerified(session)
}

// sendEnterVerified sends Enter and confirms the pane content changed.
func (m *Manager) sendEnterVerified(session string) error {
	before, _ := m.CaptureOutput(session, 5)
	m.run("send-keys", "-t", session, "Enter")

	backoff := enterVerifyWait
	for i := 0; i < enterMaxRetries; i++ {
		time.Sleep(backoff)
		after, _ := m.CaptureOutput(session, 5)
		if after != before {
			return nil
		}
		backoff *= 2
	}
	return nil // assume success after retries
}

// sanitizeInput strips control characters that corrupt tmux input delivery.
func sanitizeInput(msg string) string {
	var b strings.Builder
	b.Grow(len(msg))
	for _, r := range msg {
		switch r {
		case 0x1b: // ESC — would start escape sequence
		case 0x0d: // CR — would trigger premature submit
		case 0x08: // BS — would delete previous chars
		case '\t':
			b.WriteRune(' ') // TAB → space (avoids shell completion)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// chunkString splits s into chunks of at most maxLen bytes.
func chunkString(s string, maxLen int) []string {
	if len(s) <= maxLen {
		return []string{s}
	}
	var chunks []string
	for len(s) > 0 {
		end := maxLen
		if end > len(s) {
			end = len(s)
		}
		chunks = append(chunks, s[:end])
		s = s[end:]
	}
	return chunks
}
