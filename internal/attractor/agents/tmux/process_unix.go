// Process tree discovery and cleanup for Unix systems.
//go:build !windows

package tmux

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// cleanupProcessTree kills all descendants of a process, then the process itself.
// Uses SIGTERM first, then SIGKILL after a grace period.
func cleanupProcessTree(pid int) {
	descendants := getDescendants(pid)
	allPids := append(descendants, pid)

	// SIGTERM all (graceful shutdown).
	for _, p := range allPids {
		_ = syscall.Kill(p, syscall.SIGTERM)
	}

	// Grace period for graceful shutdown.
	time.Sleep(2 * time.Second)

	// SIGKILL survivors.
	for _, p := range allPids {
		_ = syscall.Kill(p, syscall.SIGKILL)
	}
}

// getDescendants returns all descendant PIDs of the given process,
// deepest-first so leaf processes are killed before parents.
func getDescendants(pid int) []int {
	out, err := exec.Command("pgrep", "-P", strconv.Itoa(pid)).Output()
	if err != nil {
		return nil
	}
	var children []int
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if p, err := strconv.Atoi(strings.TrimSpace(line)); err == nil && p > 0 {
			children = append(children, p)
		}
	}
	// Recurse: get grandchildren first (deepest-first order).
	var result []int
	for _, child := range children {
		result = append(result, getDescendants(child)...)
	}
	result = append(result, children...)
	return result
}
