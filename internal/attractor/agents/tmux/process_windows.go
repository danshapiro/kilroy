// Process tree cleanup stub for Windows.
//go:build windows

package tmux

// cleanupProcessTree is a no-op on Windows (tmux not natively supported).
func cleanupProcessTree(pid int) {}
