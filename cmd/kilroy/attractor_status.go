package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/strongdm/kilroy/internal/attractor/runstate"
)

func attractorStatus(args []string) {
	os.Exit(runAttractorStatus(args, os.Stdout, os.Stderr))
}

func runAttractorStatus(args []string, stdout io.Writer, stderr io.Writer) int {
	var logsRoot string
	asJSON := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--logs-root":
			i++
			if i >= len(args) {
				fmt.Fprintln(stderr, "--logs-root requires a value")
				return 1
			}
			logsRoot = args[i]
		case "--json":
			asJSON = true
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
	return 0
}
