package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"syscall"
	"time"

	"github.com/strongdm/kilroy/internal/attractor/runstate"
)

func attractorStop(args []string) {
	os.Exit(runAttractorStop(args, os.Stdout, os.Stderr))
}

func runAttractorStop(args []string, stdout io.Writer, stderr io.Writer) int {
	var logsRoot string
	grace := 5 * time.Second
	force := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--logs-root":
			i++
			if i >= len(args) {
				fmt.Fprintln(stderr, "--logs-root requires a value")
				return 1
			}
			logsRoot = args[i]
		case "--grace-ms":
			i++
			if i >= len(args) {
				fmt.Fprintln(stderr, "--grace-ms requires a value")
				return 1
			}
			ms, err := strconv.Atoi(args[i])
			if err != nil || ms < 0 {
				fmt.Fprintf(stderr, "invalid --grace-ms value: %q\n", args[i])
				return 1
			}
			grace = time.Duration(ms) * time.Millisecond
		case "--force":
			force = true
		default:
			fmt.Fprintf(stderr, "unknown arg: %s\n", args[i])
			return 1
		}
	}

	if logsRoot == "" {
		fmt.Fprintln(stderr, "--logs-root is required")
		return 1
	}

	snapshot, err := runstate.LoadSnapshot(logsRoot)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if snapshot.PID <= 0 {
		fmt.Fprintln(stderr, "run pid is not available (run.pid missing or invalid)")
		return 1
	}
	if !snapshot.PIDAlive {
		fmt.Fprintf(stderr, "pid %d is not running\n", snapshot.PID)
		return 1
	}

	proc, err := os.FindProcess(snapshot.PID)
	if err != nil {
		fmt.Fprintf(stderr, "find pid %d: %v\n", snapshot.PID, err)
		return 1
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		fmt.Fprintf(stderr, "send SIGTERM to pid %d: %v\n", snapshot.PID, err)
		return 1
	}

	if waitForPIDExit(snapshot.PID, grace) {
		fmt.Fprintf(stdout, "pid=%d\nstopped=graceful\n", snapshot.PID)
		return 0
	}

	if !force {
		fmt.Fprintf(stderr, "pid %d did not exit within %s\n", snapshot.PID, grace)
		return 1
	}

	if err := proc.Signal(syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		fmt.Fprintf(stderr, "send SIGKILL to pid %d: %v\n", snapshot.PID, err)
		return 1
	}
	forceWait := grace
	if forceWait < time.Second {
		forceWait = time.Second
	}
	if !waitForPIDExit(snapshot.PID, forceWait) {
		fmt.Fprintf(stderr, "pid %d did not exit after SIGKILL\n", snapshot.PID)
		return 1
	}
	fmt.Fprintf(stdout, "pid=%d\nstopped=forced\n", snapshot.PID)
	return 0
}

func waitForPIDExit(pid int, grace time.Duration) bool {
	if !pidRunning(pid) {
		return true
	}
	deadline := time.Now().Add(grace)
	poll := adaptiveGracePoll(grace)
	for time.Now().Before(deadline) {
		time.Sleep(poll)
		if !pidRunning(pid) {
			return true
		}
	}
	return !pidRunning(pid)
}

func adaptiveGracePoll(grace time.Duration) time.Duration {
	poll := grace / 5
	if poll < 10*time.Millisecond {
		poll = 10 * time.Millisecond
	}
	if poll > 100*time.Millisecond {
		poll = 100 * time.Millisecond
	}
	return poll
}

func pidRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	return errors.Is(err, syscall.EPERM)
}
