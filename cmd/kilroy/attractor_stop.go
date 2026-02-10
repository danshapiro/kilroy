package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/strongdm/kilroy/internal/attractor/procutil"
	"github.com/strongdm/kilroy/internal/attractor/runstate"
)

type verifiedProcess struct {
	PID            int
	StartTime      uint64
	StartTimeKnown bool
}

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
	if snapshot.State != runstate.StateRunning {
		fmt.Fprintf(stderr, "run state is %q (expected %q); refusing to stop\n", snapshot.State, runstate.StateRunning)
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
	verified, err := verifyAttractorRunPID(snapshot.PID, logsRoot, snapshot.RunID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	proc, err := os.FindProcess(verified.PID)
	if err != nil {
		fmt.Fprintf(stderr, "find pid %d: %v\n", verified.PID, err)
		return 1
	}
	// Best-effort identity check immediately before signaling. A small race
	// remains without pidfd-based signaling, but start-time verification greatly
	// reduces accidental PID-reuse targeting.
	if err := verifyProcessIdentity(verified); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		fmt.Fprintf(stderr, "send SIGTERM to pid %d: %v\n", verified.PID, err)
		return 1
	}

	if waitForPIDExit(verified, grace) {
		fmt.Fprintf(stdout, "pid=%d\nstopped=graceful\n", verified.PID)
		return 0
	}

	if !force {
		fmt.Fprintf(stderr, "pid %d did not exit within %s\n", verified.PID, grace)
		return 1
	}
	if err := verifyProcessIdentity(verified); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	if err := proc.Signal(syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		fmt.Fprintf(stderr, "send SIGKILL to pid %d: %v\n", verified.PID, err)
		return 1
	}
	forceWait := grace
	if forceWait < time.Second {
		// Give SIGKILL at least one second to be observed by wait/poll loops.
		forceWait = time.Second
	}
	if forceWait > 10*time.Second {
		// SIGKILL should terminate quickly; avoid waiting an operator-specified
		// long grace window after we've already forced termination.
		forceWait = 10 * time.Second
	}
	if !waitForPIDExit(verified, forceWait) {
		fmt.Fprintf(stderr, "pid %d did not exit after SIGKILL\n", verified.PID)
		return 1
	}
	fmt.Fprintf(stdout, "pid=%d\nstopped=forced\n", verified.PID)
	return 0
}

func waitForPIDExit(proc verifiedProcess, grace time.Duration) bool {
	if !procutil.PIDAlive(proc.PID) || !processIdentityMatches(proc) {
		return true
	}
	deadline := time.Now().Add(grace)
	poll := adaptiveGracePoll(grace)
	for time.Now().Before(deadline) {
		time.Sleep(poll)
		if !procutil.PIDAlive(proc.PID) || !processIdentityMatches(proc) {
			return true
		}
	}
	return !procutil.PIDAlive(proc.PID) || !processIdentityMatches(proc)
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

func verifyAttractorRunPID(pid int, logsRoot string, runID string) (verifiedProcess, error) {
	if err := verifyPIDExecutableMatchesSelf(pid); err != nil {
		return verifiedProcess{}, err
	}

	args, err := readPIDCmdline(pid)
	if err != nil {
		return verifiedProcess{}, fmt.Errorf("refusing to signal pid %d: cannot read process command line: %w", pid, err)
	}
	if len(args) == 0 {
		return verifiedProcess{}, fmt.Errorf("refusing to signal pid %d: empty process command line", pid)
	}

	attractorIdx := -1
	for i, arg := range args {
		if strings.TrimSpace(arg) == "attractor" {
			attractorIdx = i
			break
		}
	}
	if attractorIdx < 0 || attractorIdx+1 >= len(args) {
		return verifiedProcess{}, fmt.Errorf("refusing to signal pid %d: process is not an attractor run/resume command", pid)
	}
	sub := strings.TrimSpace(args[attractorIdx+1])
	if sub != "run" && sub != "resume" {
		return verifiedProcess{}, fmt.Errorf("refusing to signal pid %d: process is attractor %q, not run/resume", pid, sub)
	}

	expectedRunID := resolveExpectedRunID(runID, logsRoot)
	pidRunID, hasRunID := cmdlineRunID(args)
	pidLogsRoot, hasLogsRoot := cmdlineLogsRoot(args)

	if hasRunID && expectedRunID != "" {
		if strings.TrimSpace(pidRunID) != expectedRunID {
			return verifiedProcess{}, fmt.Errorf("refusing to signal pid %d: --run-id mismatch (pid=%q expected=%q)", pid, pidRunID, expectedRunID)
		}
		return captureVerifiedProcess(pid)
	}
	if hasLogsRoot {
		if !samePath(pidLogsRoot, logsRoot) {
			return verifiedProcess{}, fmt.Errorf("refusing to signal pid %d: --logs-root mismatch (pid=%q requested=%q)", pid, pidLogsRoot, logsRoot)
		}
		return captureVerifiedProcess(pid)
	}
	if hasRunID {
		// Fallback: we confirmed this is a local kilroy attractor run/resume process
		// and it carries --run-id, but we have no expected run-id materialized yet
		// (early startup before manifest/live events). Preserve operability here.
		return captureVerifiedProcess(pid)
	}
	return verifiedProcess{}, fmt.Errorf("refusing to signal pid %d: process command line has no --logs-root/--run-id", pid)
}

func resolveExpectedRunID(snapshotRunID string, logsRoot string) string {
	expected := strings.TrimSpace(snapshotRunID)
	if expected != "" {
		return expected
	}
	// Early-stage runs can have run.pid before manifest/live artifacts are
	// populated. Best-effort fallback to manifest run_id when available.
	manifestRunID, err := readManifestRunID(logsRoot)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(manifestRunID)
}

func verifyPIDExecutableMatchesSelf(pid int) error {
	if !procutil.ProcFSAvailable() {
		return nil
	}
	selfExe, err := readProcessExePath("self")
	if err != nil {
		return fmt.Errorf("refusing to signal pid %d: cannot resolve current executable: %w", pid, err)
	}
	targetExe, err := readProcessExePath(strconv.Itoa(pid))
	if err != nil {
		return fmt.Errorf("refusing to signal pid %d: cannot resolve target executable: %w", pid, err)
	}
	if !samePath(selfExe, targetExe) {
		return fmt.Errorf("refusing to signal pid %d: executable mismatch (target=%q current=%q)", pid, targetExe, selfExe)
	}
	return nil
}

func readProcessExePath(pidToken string) (string, error) {
	linkPath := filepath.Join("/proc", pidToken, "exe")
	resolved, err := os.Readlink(linkPath)
	if err != nil {
		return "", err
	}
	if abs, err := filepath.Abs(resolved); err == nil {
		resolved = abs
	}
	if eval, err := filepath.EvalSymlinks(resolved); err == nil {
		resolved = eval
	}
	return resolved, nil
}

func readManifestRunID(logsRoot string) (string, error) {
	path := filepath.Join(logsRoot, "manifest.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		return "", err
	}
	rawRunID, ok := doc["run_id"]
	if !ok || rawRunID == nil {
		return "", fmt.Errorf("manifest run_id is empty")
	}
	runID, ok := rawRunID.(string)
	if !ok {
		return "", fmt.Errorf("manifest run_id is not a string")
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return "", fmt.Errorf("manifest run_id is empty")
	}
	return runID, nil
}

func captureVerifiedProcess(pid int) (verifiedProcess, error) {
	if !procutil.ProcFSAvailable() {
		return verifiedProcess{PID: pid}, nil
	}
	start, err := procutil.ReadPIDStartTime(pid)
	if err != nil {
		return verifiedProcess{}, fmt.Errorf("refusing to signal pid %d: cannot read process start time: %w", pid, err)
	}
	return verifiedProcess{PID: pid, StartTime: start, StartTimeKnown: true}, nil
}

func verifyProcessIdentity(proc verifiedProcess) error {
	if !procutil.PIDAlive(proc.PID) {
		return fmt.Errorf("refusing to signal pid %d: process is no longer running", proc.PID)
	}
	if !proc.StartTimeKnown {
		return nil
	}
	start, err := procutil.ReadPIDStartTime(proc.PID)
	if err != nil {
		return fmt.Errorf("refusing to signal pid %d: cannot read process start time: %w", proc.PID, err)
	}
	if start != proc.StartTime {
		return fmt.Errorf("refusing to signal pid %d: process identity changed (pid was reused)", proc.PID)
	}
	return nil
}

func processIdentityMatches(proc verifiedProcess) bool {
	if !proc.StartTimeKnown {
		return true
	}
	start, err := procutil.ReadPIDStartTime(proc.PID)
	if err != nil {
		return false
	}
	return start == proc.StartTime
}

func readPIDCmdline(pid int) ([]string, error) {
	if !procutil.ProcFSAvailable() {
		return readPIDCmdlineFromPS(pid)
	}
	path := filepath.Join("/proc", strconv.Itoa(pid), "cmdline")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseCmdlineParts(string(b), "\x00"), nil
}

func readPIDCmdlineFromPS(pid int) ([]string, error) {
	out, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return nil, err
	}
	cmdline := strings.TrimSpace(string(out))
	if cmdline == "" {
		return nil, fmt.Errorf("empty command line")
	}
	return parseCmdlineParts(cmdline, " "), nil
}

func parseCmdlineParts(raw string, sep string) []string {
	parts := strings.Split(raw, sep)
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if s := strings.TrimSpace(part); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func cmdlineLogsRoot(args []string) (string, bool) {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--logs-root" && i+1 < len(args):
			return strings.TrimSpace(args[i+1]), true
		case strings.HasPrefix(args[i], "--logs-root="):
			return strings.TrimSpace(strings.TrimPrefix(args[i], "--logs-root=")), true
		}
	}
	return "", false
}

func cmdlineRunID(args []string) (string, bool) {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--run-id" && i+1 < len(args):
			return strings.TrimSpace(args[i+1]), true
		case strings.HasPrefix(args[i], "--run-id="):
			return strings.TrimSpace(strings.TrimPrefix(args[i], "--run-id=")), true
		}
	}
	return "", false
}

func samePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return false
	}
	return filepath.Clean(absA) == filepath.Clean(absB)
}
