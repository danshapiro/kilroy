package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/procutil"
	"github.com/danshapiro/kilroy/internal/attractor/runstate"
)

// runFollowProgress tails progress.ndjson with formatted output until the run
// completes (final.json appears) or the process dies (PID no longer alive).
// When raw is true, events are printed as-is (NDJSON passthrough).
func runFollowProgress(logsRoot string, w io.Writer, raw bool) int {
	ndjsonPath := filepath.Join(logsRoot, "progress.ndjson")
	finalPath := filepath.Join(logsRoot, "final.json")
	pidPath := filepath.Join(logsRoot, "run.pid")

	// Check if already finished.
	if isTerminal(finalPath) {
		// Catch up on all events, then exit.
		printAllEvents(ndjsonPath, w, raw)
		printFinalSummary(finalPath, w)
		return 0
	}

	// Open ndjson for tailing. File may not exist yet if the run just started.
	var offset int64
	offset, _ = printAllEvents(ndjsonPath, w, raw)

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		// Read new events.
		offset = tailEvents(ndjsonPath, offset, w, raw)

		// Check for terminal state.
		if isTerminal(finalPath) {
			// Drain any remaining events.
			tailEvents(ndjsonPath, offset, w, raw)
			printFinalSummary(finalPath, w)
			return 0
		}

		// Check if PID is still alive.
		if pid := readPID(pidPath); pid > 0 && !procutil.PIDAlive(pid) {
			// Drain any remaining events.
			tailEvents(ndjsonPath, offset, w, raw)
			fmt.Fprintf(w, "\nrun process (pid %d) is no longer alive\n", pid)
			return 1
		}
	}
	return 0
}

// printAllEvents reads all existing events from ndjson and prints them.
// Returns the file offset after reading and any error.
func printAllEvents(ndjsonPath string, w io.Writer, raw bool) (int64, error) {
	f, err := os.Open(ndjsonPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		printEvent(w, line, raw)
	}

	offset, _ := f.Seek(0, io.SeekCurrent)
	return offset, scanner.Err()
}

// tailEvents reads new events from ndjsonPath starting at the given offset.
// Returns the new offset.
func tailEvents(ndjsonPath string, offset int64, w io.Writer, raw bool) int64 {
	f, err := os.Open(ndjsonPath)
	if err != nil {
		return offset
	}
	defer f.Close()

	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return offset
		}
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		printEvent(w, line, raw)
	}

	newOffset, _ := f.Seek(0, io.SeekCurrent)
	return newOffset
}

func printEvent(w io.Writer, line string, raw bool) {
	if raw {
		fmt.Fprintln(w, line)
		return
	}
	var ev map[string]any
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		fmt.Fprintln(w, line)
		return
	}
	formatted := formatProgressEvent(ev)
	if strings.TrimSpace(formatted) == "" {
		return
	}
	fmt.Fprintln(w, formatted)
}

func formatProgressEvent(ev map[string]any) string {
	ts := formatEventTime(ev)
	event := evStr(ev, "event")
	nodeID := evStr(ev, "node_id")

	switch event {
	case "stage_attempt_start":
		return fmt.Sprintf("%s | %-24s | %s (attempt %s/%s)",
			ts, event, nodeID,
			evVal(ev, "attempt"), evVal(ev, "max"))

	case "stage_attempt_end":
		status := evStr(ev, "status")
		line := fmt.Sprintf("%s | %-24s | %s | %s",
			ts, event, nodeID, status)
		if reason := evStr(ev, "failure_reason"); reason != "" {
			line += " | " + reason
		}
		return line

	case "edge_selected":
		return fmt.Sprintf("%s | %-24s | %s -> %s",
			ts, event,
			evStr(ev, "from_node"),
			evStr(ev, "to_node"))

	case "warning":
		return fmt.Sprintf("%s | %-24s | %s",
			ts, "WARNING", evStr(ev, "message"))

	case "stage_heartbeat":
		return fmt.Sprintf("%s | %-24s | %s | elapsed=%ss",
			ts, event, nodeID,
			evVal(ev, "elapsed_s"))

	case "branch_progress":
		branchKey := evStr(ev, "branch_key")
		branchEvent := evStr(ev, "branch_event")
		branchNode := evStr(ev, "branch_node_id")
		line := fmt.Sprintf("%s | %-24s | %s | %s", ts, event, branchKey, branchEvent)
		if branchNode != "" {
			line += " | node=" + branchNode
		}
		if status := evStr(ev, "branch_status"); status != "" {
			line += " | status=" + status
		}
		if reason := evStr(ev, "branch_failure_reason"); reason != "" {
			line += " | " + reason
		}
		return line

	case "branch_heartbeat":
		// Keep default follow output focused on meaningful progress; heartbeats
		// are still available with --raw.
		return ""

	case "branch_stale_warning":
		return fmt.Sprintf("%s | %-24s | %s | idle=%sms | last=%s",
			ts, event,
			evStr(ev, "branch_key"),
			evVal(ev, "branch_idle_ms"),
			evStr(ev, "branch_last_event"))

	case "loop_restart":
		return fmt.Sprintf("%s | %-24s | restarting pipeline",
			ts, event)

	case "stage_retry_blocked":
		return fmt.Sprintf("%s | %-24s | %s | %s (%s)",
			ts, event, nodeID,
			evStr(ev, "failure_class"),
			evStr(ev, "failure_reason"))

	case "retry_attempt":
		return fmt.Sprintf("%s | %-24s | %s (attempt %s/%s)",
			ts, event, nodeID,
			evVal(ev, "attempt"), evVal(ev, "max"))

	default:
		if nodeID != "" {
			return fmt.Sprintf("%s | %-24s | %s", ts, event, nodeID)
		}
		// For events with a message field (like warnings without node_id)
		if msg := evStr(ev, "message"); msg != "" {
			return fmt.Sprintf("%s | %-24s | %s", ts, event, msg)
		}
		return fmt.Sprintf("%s | %-24s |", ts, event)
	}
}

func formatEventTime(ev map[string]any) string {
	raw := evStr(ev, "ts")
	if raw == "" {
		return "          "
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t.Format("15:04:05")
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.Format("15:04:05")
	}
	return raw[:min(10, len(raw))]
}

func evStr(ev map[string]any, key string) string {
	v, ok := ev[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func evVal(ev map[string]any, key string) string {
	v, ok := ev[key]
	if !ok || v == nil {
		return "?"
	}
	switch t := v.(type) {
	case float64:
		if t == float64(int(t)) {
			return fmt.Sprintf("%d", int(t))
		}
		return fmt.Sprintf("%.1f", t)
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}

func isTerminal(finalPath string) bool {
	_, err := os.Stat(finalPath)
	return err == nil
}

func readPID(pidPath string) int {
	b, err := os.ReadFile(pidPath)
	if err != nil {
		return 0
	}
	raw := strings.TrimSpace(string(b))
	if raw == "" {
		return 0
	}
	var pid int
	if _, err := fmt.Sscanf(raw, "%d", &pid); err != nil || pid <= 0 {
		return 0
	}
	return pid
}

func printFinalSummary(finalPath string, w io.Writer) {
	b, err := os.ReadFile(finalPath)
	if err != nil {
		return
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		return
	}
	status := evStr(doc, "status")
	fmt.Fprintf(w, "\nrun completed: %s\n", status)
	if reason := evStr(doc, "failure_reason"); reason != "" {
		fmt.Fprintf(w, "failure_reason: %s\n", reason)
	}
}

// latestRunLogsRoot finds the most recently modified run directory under the
// default XDG state path.
func latestRunLogsRoot() (string, error) {
	stateHome := strings.TrimSpace(os.Getenv("XDG_STATE_HOME"))
	if stateHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		stateHome = filepath.Join(home, ".local", "state")
	}
	runsDir := filepath.Join(stateHome, "kilroy", "attractor", "runs")

	entries, err := os.ReadDir(runsDir)
	if err != nil {
		return "", fmt.Errorf("no runs found in %s: %w", runsDir, err)
	}
	if len(entries) == 0 {
		return "", fmt.Errorf("no runs found in %s", runsDir)
	}

	type dirEntry struct {
		name    string
		modTime time.Time
	}
	var dirs []dirEntry
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		dirs = append(dirs, dirEntry{name: e.Name(), modTime: info.ModTime()})
	}
	if len(dirs) == 0 {
		return "", fmt.Errorf("no run directories found in %s", runsDir)
	}

	sort.Slice(dirs, func(i, j int) bool {
		return dirs[i].modTime.After(dirs[j].modTime)
	})

	return filepath.Join(runsDir, dirs[0].name), nil
}

// runWatchStatus polls the snapshot every interval and reprints it with
// screen clearing. Exits when the run reaches a terminal state.
func runWatchStatus(logsRoot string, stdout io.Writer, stderr io.Writer, asJSON bool, verbose bool, intervalSec int) int {
	if intervalSec <= 0 {
		intervalSec = 2
	}
	interval := time.Duration(intervalSec) * time.Second

	for {
		// Clear screen (ANSI escape).
		fmt.Fprint(stdout, "\033[2J\033[H")

		code := printSnapshot(logsRoot, stdout, stderr, asJSON, verbose)
		if code != 0 {
			return code
		}

		fmt.Fprintf(stdout, "\nrefreshing every %ds (ctrl-c to stop)\n", intervalSec)

		// Check if terminal.
		finalPath := filepath.Join(logsRoot, "final.json")
		if isTerminal(finalPath) {
			return 0
		}

		time.Sleep(interval)
	}
}

// printSnapshot loads and prints the current snapshot. Same as the one-shot
// path in runAttractorStatus but extracted for reuse.
func printSnapshot(logsRoot string, stdout io.Writer, stderr io.Writer, asJSON bool, verbose bool) int {
	snapshot, err := loadSnapshot(logsRoot)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	if verbose {
		if err := runstate.ApplyVerbose(snapshot); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}

	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(snapshot); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}

	fmt.Fprintf(stdout, "state=%s\n", snapshot.State)
	fmt.Fprintf(stdout, "run_id=%s\n", snapshot.RunID)
	fmt.Fprintf(stdout, "node=%s\n", snapshot.CurrentNodeID)
	fmt.Fprintf(stdout, "event=%s\n", snapshot.LastEvent)
	fmt.Fprintf(stdout, "pid=%d\n", snapshot.PID)
	fmt.Fprintf(stdout, "pid_alive=%t\n", snapshot.PIDAlive)
	if !snapshot.LastEventAt.IsZero() {
		fmt.Fprintf(stdout, "last_event_at=%s\n", snapshot.LastEventAt.UTC().Format(time.RFC3339Nano))
	}
	if snapshot.FailureReason != "" {
		fmt.Fprintf(stdout, "failure_reason=%s\n", snapshot.FailureReason)
	}

	if verbose {
		printVerboseSnapshot(stdout, snapshot)
	}
	return 0
}

func printVerboseSnapshot(w io.Writer, s *runstate.Snapshot) {
	if len(s.CompletedNodes) > 0 {
		fmt.Fprintf(w, "completed_nodes=%s\n", strings.Join(s.CompletedNodes, ","))
	}
	if len(s.RetryCounts) > 0 {
		parts := make([]string, 0, len(s.RetryCounts))
		for node, count := range s.RetryCounts {
			parts = append(parts, fmt.Sprintf("%s:%d", node, count))
		}
		sort.Strings(parts)
		fmt.Fprintf(w, "retry_counts=%s\n", strings.Join(parts, ","))
	}
	if s.FinalCommitSHA != "" {
		fmt.Fprintf(w, "final_commit_sha=%s\n", s.FinalCommitSHA)
	}
	if s.CXDBContextID != "" {
		fmt.Fprintf(w, "cxdb_context_id=%s\n", s.CXDBContextID)
	}

	if len(s.StageTrace) > 0 || len(s.EdgeTrace) > 0 {
		fmt.Fprintln(w, "\n--- stage trace ---")
		si, ei := 0, 0
		for si < len(s.StageTrace) || ei < len(s.EdgeTrace) {
			if si < len(s.StageTrace) {
				sa := s.StageTrace[si]
				line := fmt.Sprintf("  %-24s %-8s attempt %d/%d", sa.NodeID, sa.Status, sa.Attempt, sa.MaxAttempts)
				if sa.FailureReason != "" {
					line += "  " + sa.FailureReason
				}
				fmt.Fprintln(w, line)
				si++
				// Print any edges that follow this stage
				for ei < len(s.EdgeTrace) && s.EdgeTrace[ei].From == sa.NodeID {
					et := s.EdgeTrace[ei]
					cond := ""
					if et.Condition != "" {
						cond = " (" + et.Condition + ")"
					}
					fmt.Fprintf(w, "    → %-20s%s\n", et.To, cond)
					ei++
				}
			} else {
				// Remaining edges
				et := s.EdgeTrace[ei]
				cond := ""
				if et.Condition != "" {
					cond = " (" + et.Condition + ")"
				}
				fmt.Fprintf(w, "    → %-20s%s\n", et.To, cond)
				ei++
			}
		}
	}

	if s.PostmortemText != "" {
		fmt.Fprintf(w, "\n--- postmortem (worktree/.ai/postmortem_latest.md) ---\n%s\n", s.PostmortemText)
	}
	if s.ReviewText != "" {
		fmt.Fprintf(w, "\n--- review (worktree/.ai/review_final.md) ---\n%s\n", s.ReviewText)
	}
}
