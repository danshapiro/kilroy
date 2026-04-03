package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/engine"
	"github.com/danshapiro/kilroy/internal/attractor/rundb"
)

func attractorRuns(args []string) {
	if len(args) < 1 {
		runsUsage()
		os.Exit(1)
	}
	switch args[0] {
	case "list":
		attractorRunsList(args[1:])
	case "prune":
		attractorRunsPrune(args[1:])
	default:
		runsUsage()
		os.Exit(1)
	}
}

func runsUsage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  kilroy attractor runs list [--json]")
	fmt.Fprintln(os.Stderr, "  kilroy attractor runs prune [--before YYYY-MM-DD] [--graph PATTERN] [--label KEY=VALUE] [--orphans] [--dry-run | --yes]")
}

// runManifest is the subset of manifest.json fields we care about for list/prune.
type runManifest struct {
	RunID     string            `json:"run_id"`
	GraphName string            `json:"graph_name"`
	Goal      string            `json:"goal"`
	StartedAt string            `json:"started_at"`
	LogsRoot  string            `json:"logs_root"`
	RepoPat   string            `json:"repo_path"`
	Labels    map[string]string `json:"labels"`
}

// runRecord is a fully resolved run entry (manifest + final status).
type runRecord struct {
	RunID       string
	GraphName   string
	Goal        string
	StartedAt   time.Time
	LogsRoot    string
	Labels      map[string]string
	FinalStatus string
	Duration    string
}

func loadRunRecords(baseDir string) ([]runRecord, error) {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var records []runRecord
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(baseDir, e.Name())
		raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
		if err != nil {
			// No manifest.json — include as an orphan using dir mtime for date.
			var startedAt time.Time
			if info, statErr := os.Stat(dir); statErr == nil {
				startedAt = info.ModTime()
			}
			records = append(records, runRecord{
				RunID:       e.Name(),
				GraphName:   "[no manifest]",
				StartedAt:   startedAt,
				LogsRoot:    dir,
				FinalStatus: readFinalStatus(dir),
			})
			continue
		}
		var m runManifest
		if err := json.Unmarshal(raw, &m); err != nil {
			continue
		}
		// Parse started_at; fall back to dir mtime on failure.
		startedAt, err := time.Parse(time.RFC3339Nano, m.StartedAt)
		if err != nil {
			if info, statErr := os.Stat(dir); statErr == nil {
				startedAt = info.ModTime()
			}
		}
		finalStatus := readFinalStatus(dir)
		records = append(records, runRecord{
			RunID:       m.RunID,
			GraphName:   m.GraphName,
			Goal:        m.Goal,
			StartedAt:   startedAt,
			LogsRoot:    dir,
			Labels:      m.Labels,
			FinalStatus: finalStatus,
		})
	}
	return records, nil
}

func readFinalStatus(logsRoot string) string {
	raw, err := os.ReadFile(filepath.Join(logsRoot, "final.json"))
	if err != nil {
		// Check for a live run (no final.json yet).
		if _, err2 := os.Stat(filepath.Join(logsRoot, "run.pid")); err2 == nil {
			return "running"
		}
		return "unknown"
	}
	var f struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &f); err != nil || f.Status == "" {
		return "unknown"
	}
	return f.Status
}

// --- list ---

func attractorRunsList(args []string) {
	asJSON := false
	for _, a := range args {
		switch a {
		case "--json":
			asJSON = true
		default:
			fmt.Fprintf(os.Stderr, "unknown arg: %s\n", a)
			runsUsage()
			os.Exit(1)
		}
	}

	// Try RunDB first, fall back to filesystem scan.
	if records := listRunsFromDB(); len(records) > 0 {
		printRunRecords(records, asJSON, "run database")
		return
	}

	baseDir := engine.DefaultRunsBaseDir()
	records, err := loadRunRecords(baseDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	printRunRecords(records, asJSON, baseDir)
}

func listRunsFromDB() []runRecord {
	db, err := rundb.Open(rundb.DefaultPath())
	if err != nil {
		return nil
	}
	defer db.Close()

	runs, err := db.ListRuns(rundb.ListFilter{})
	if err != nil {
		return nil
	}
	records := make([]runRecord, 0, len(runs))
	for _, r := range runs {
		var dur string
		if r.DurationMS != nil {
			dur = fmt.Sprintf("%dms", *r.DurationMS)
		}
		records = append(records, runRecord{
			RunID:       r.RunID,
			GraphName:   r.GraphName,
			Goal:        r.Goal,
			StartedAt:   r.StartedAt,
			LogsRoot:    r.LogsRoot,
			Labels:      r.Labels,
			FinalStatus: r.Status,
			Duration:    dur,
		})
	}
	return records
}

func printRunRecords(records []runRecord, asJSON bool, source string) {
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(records)
		return
	}
	if len(records) == 0 {
		fmt.Printf("no runs found in %s\n", source)
		return
	}
	fmt.Printf("%-26s  %-20s  %-12s  %-20s  %-10s  %s\n", "RUN ID", "GRAPH", "STATUS", "STARTED", "DURATION", "LABELS")
	fmt.Println(strings.Repeat("-", 110))
	for _, r := range records {
		labels := formatLabels(r.Labels)
		started := r.StartedAt.Local().Format("2006-01-02 15:04")
		graph := r.GraphName
		if len(graph) > 20 {
			graph = graph[:17] + "..."
		}
		dur := r.Duration
		if dur == "" {
			dur = "-"
		}
		fmt.Printf("%-26s  %-20s  %-12s  %-20s  %-10s  %s\n", r.RunID, graph, r.FinalStatus, started, dur, labels)
	}
	fmt.Printf("\n%d run(s)\n", len(records))
}

func formatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	var parts []string
	for k, v := range labels {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, " ")
}

// --- prune ---

func attractorRunsPrune(args []string) {
	var beforeStr string
	var graphPattern string
	var labelFilter string
	var orphansOnly bool
	dryRun := true

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--orphans":
			orphansOnly = true
		case "--before":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "--before requires a value (YYYY-MM-DD)")
				os.Exit(1)
			}
			beforeStr = args[i]
		case "--graph":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "--graph requires a value")
				os.Exit(1)
			}
			graphPattern = args[i]
		case "--label":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "--label requires KEY=VALUE")
				os.Exit(1)
			}
			labelFilter = args[i]
		case "--dry-run":
			dryRun = true
		case "--yes":
			dryRun = false
		default:
			fmt.Fprintf(os.Stderr, "unknown arg: %s\n", args[i])
			runsUsage()
			os.Exit(1)
		}
	}

	// Parse --before date (YYYY-MM-DD or "YYYY-MM-DD HH:MM").
	var beforeTime time.Time
	if beforeStr != "" {
		var err error
		for _, layout := range []string{"2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02"} {
			beforeTime, err = time.ParseInLocation(layout, beforeStr, time.Local)
			if err == nil {
				break
			}
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "--before %q: expected YYYY-MM-DD or \"YYYY-MM-DD HH:MM\"\n", beforeStr)
			os.Exit(1)
		}
	}

	// Parse --label KEY=VALUE.
	var labelKey, labelVal string
	if labelFilter != "" {
		parts := strings.SplitN(labelFilter, "=", 2)
		if len(parts) != 2 {
			fmt.Fprintf(os.Stderr, "--label %q: expected KEY=VALUE format\n", labelFilter)
			os.Exit(1)
		}
		labelKey = parts[0]
		labelVal = parts[1]
	}

	// Try RunDB-based prune first.
	if pruneFromDB(beforeTime, graphPattern, labelKey, labelVal, orphansOnly, dryRun) {
		return
	}

	// Fall back to filesystem-based prune.
	baseDir := engine.DefaultRunsBaseDir()
	records, err := loadRunRecords(baseDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// Filter.
	var targets []runRecord
	for _, r := range records {
		if orphansOnly && r.GraphName != "[no manifest]" {
			continue
		}
		if !beforeTime.IsZero() && !r.StartedAt.Before(beforeTime) {
			continue
		}
		if graphPattern != "" && !strings.Contains(r.GraphName, graphPattern) {
			continue
		}
		if labelKey != "" {
			v, ok := r.Labels[labelKey]
			if !ok || v != labelVal {
				continue
			}
		}
		targets = append(targets, r)
	}

	if len(targets) == 0 {
		fmt.Println("no matching runs found")
		return
	}

	verb := "Would delete"
	if !dryRun {
		verb = "Deleting"
	}
	for _, r := range targets {
		labels := formatLabels(r.Labels)
		started := r.StartedAt.Local().Format("2006-01-02 15:04")
		fmt.Printf("%s  %s  graph=%-20s  status=%-12s  started=%s  labels=%s\n",
			verb, r.RunID, r.GraphName, r.FinalStatus, started, labels)
		if !dryRun {
			if err := os.RemoveAll(r.LogsRoot); err != nil {
				fmt.Fprintf(os.Stderr, "  error removing %s: %v\n", r.LogsRoot, err)
			}
		}
	}

	if dryRun {
		fmt.Printf("\n%d run(s) matched. Re-run with --yes to delete.\n", len(targets))
	} else {
		fmt.Printf("\n%d run(s) deleted.\n", len(targets))
	}
}

func pruneFromDB(beforeTime time.Time, graphPattern, labelKey, labelVal string, orphansOnly, dryRun bool) bool {
	db, err := rundb.Open(rundb.DefaultPath())
	if err != nil {
		return false
	}
	defer db.Close()

	filter := rundb.PruneFilter{
		Orphans:   orphansOnly,
		GraphName: graphPattern,
	}
	if !beforeTime.IsZero() {
		filter.Before = &beforeTime
	}
	if labelKey != "" {
		filter.Labels = map[string]string{labelKey: labelVal}
	}

	if dryRun {
		// For dry run, list matching runs instead of deleting.
		listFilter := rundb.ListFilter{GraphName: graphPattern}
		if labelKey != "" {
			listFilter.Labels = map[string]string{labelKey: labelVal}
		}
		runs, err := db.ListRuns(listFilter)
		if err != nil {
			return false
		}
		var count int
		for _, r := range runs {
			if !beforeTime.IsZero() && !r.StartedAt.Before(beforeTime) {
				continue
			}
			count++
			started := r.StartedAt.Local().Format("2006-01-02 15:04")
			fmt.Printf("Would delete  %s  graph=%-20s  status=%-12s  started=%s\n",
				r.RunID, r.GraphName, r.Status, started)
		}
		if count == 0 {
			fmt.Println("no matching runs found")
		} else {
			fmt.Printf("\n%d run(s) matched. Re-run with --yes to delete.\n", count)
		}
		return true
	}

	n, err := db.PruneRuns(filter)
	if err != nil {
		fmt.Fprintf(os.Stderr, "prune error: %v\n", err)
		return true
	}
	fmt.Printf("%d run(s) pruned from database.\n", n)
	return true
}
