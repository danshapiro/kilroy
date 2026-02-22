package main

import (
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/danshapiro/kilroy/internal/attractor/runstate"
)

func attractorStatus(args []string) {
	os.Exit(runAttractorStatus(args, os.Stdout, os.Stderr))
}

// loadSnapshot wraps runstate.LoadSnapshot for reuse.
func loadSnapshot(logsRoot string) (*runstate.Snapshot, error) {
	return runstate.LoadSnapshot(logsRoot)
}

func runAttractorStatus(args []string, stdout io.Writer, stderr io.Writer) int {
	var logsRoot string
	var asJSON bool
	var follow bool
	var raw bool
	var watch bool
	var latest bool
	var useCXDB bool
	var verbose bool
	intervalSec := 2

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
		case "--follow", "-f":
			follow = true
		case "--raw":
			raw = true
		case "--watch":
			watch = true
		case "--latest":
			latest = true
		case "--cxdb":
			useCXDB = true
		case "--verbose", "-v":
			verbose = true
		case "--interval":
			i++
			if i >= len(args) {
				fmt.Fprintln(stderr, "--interval requires a value")
				return 1
			}
			n, err := strconv.Atoi(args[i])
			if err != nil || n <= 0 {
				fmt.Fprintln(stderr, "--interval must be a positive integer")
				return 1
			}
			intervalSec = n
		default:
			fmt.Fprintf(stderr, "unknown arg: %s\n", args[i])
			return 1
		}
	}

	// Resolve --latest to logs-root.
	if latest {
		if logsRoot != "" {
			fmt.Fprintln(stderr, "--latest and --logs-root are mutually exclusive")
			return 1
		}
		root, err := latestRunLogsRoot()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		logsRoot = root
		fmt.Fprintf(stderr, "logs_root=%s\n", logsRoot)
	}

	if logsRoot == "" {
		fmt.Fprintln(stderr, "--logs-root or --latest is required")
		return 1
	}

	// Mutually exclusive modes.
	if follow && watch {
		fmt.Fprintln(stderr, "--follow and --watch are mutually exclusive")
		return 1
	}

	if follow {
		if useCXDB {
			return runFollowCXDB(logsRoot, stdout, raw)
		}
		// Auto-detect: if manifest.json has CXDB config, try CXDB first.
		if m, err := loadCXDBManifest(logsRoot); err == nil && m.CXDB.HTTPBaseURL != "" {
			return runFollowCXDB(logsRoot, stdout, raw)
		}
		return runFollowProgress(logsRoot, stdout, raw)
	}

	if watch {
		return runWatchStatus(logsRoot, stdout, stderr, asJSON, verbose, intervalSec)
	}

	// Default: one-shot snapshot.
	return printSnapshot(logsRoot, stdout, stderr, asJSON, verbose)
}
