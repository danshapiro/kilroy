package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/engine"
	"github.com/danshapiro/kilroy/internal/attractor/rundb"
)

// registerDetachedRunInDB writes a status=running row to the run database
// before the detached child process starts. This ensures that
// `kilroy attractor runs list`, `runs show`, and `runs wait` can find the run
// immediately after the parent exits — they no longer have to wait for the child
// to reach its own RecordRunStart call inside the engine.
//
// The child's engine.RunWithConfig path will later call InsertRun
// (INSERT OR REPLACE) to overwrite this row with complete metadata (worktreeDir,
// runBranch, dotSource, etc.). This function is best-effort: failures emit a
// stderr warning but never abort the detach launch.
func registerDetachedRunInDB(runID, graphPath, logsRoot, repoPath string, labels map[string]string, inputs map[string]any, invocation []string) {
	db, err := rundb.Open(rundb.DefaultPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not open run database to pre-register run: %v\n", err)
		return
	}
	defer db.Close()

	// Read the graph file to populate graphName, goal, and dotSource.
	// Failures here are non-fatal — we write whatever we can.
	var graphName, goal, dotSrc string
	if raw, readErr := os.ReadFile(graphPath); readErr == nil {
		dotSrc = string(raw)
		if g, _, parseErr := engine.Prepare(raw); parseErr == nil && g != nil {
			graphName = g.Name
			goal = g.Attrs["goal"]
		}
	}
	// Fall back to filename if DOT parsing failed.
	if graphName == "" {
		base := filepath.Base(graphPath)
		graphName = strings.TrimSuffix(base, filepath.Ext(base))
	}

	if err := db.InsertRun(rundb.RunRecord{
		RunID:      runID,
		GraphName:  graphName,
		Goal:       goal,
		Status:     "running",
		LogsRoot:   logsRoot,
		RepoPath:   repoPath,
		DotSource:  dotSrc,
		Labels:     labels,
		Inputs:     inputs,
		Invocation: invocation,
		StartedAt:  time.Now(),
	}); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not pre-register run in database: %v\n", err)
	}
}
