package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// appendProgress writes compact, machine-readable progress events under logs_root.
//
// Files:
// - progress.ndjson: append-only stream (one JSON object per line)
// - live.json: last event (overwritten)
//
// This is best-effort: progress logging must never block or fail a run.
func (e *Engine) appendProgress(ev map[string]any) {
	if e == nil {
		return
	}
	logsRoot := strings.TrimSpace(e.LogsRoot)
	if logsRoot == "" {
		return
	}
	if ev == nil {
		ev = map[string]any{}
	}
	now := time.Now().UTC()
	if _, ok := ev["ts"]; !ok {
		ev["ts"] = now.Format(time.RFC3339Nano)
	}
	if _, ok := ev["run_id"]; !ok && strings.TrimSpace(e.Options.RunID) != "" {
		ev["run_id"] = e.Options.RunID
	}

	b, err := json.Marshal(ev)
	if err != nil {
		return
	}

	e.progressMu.Lock()
	defer e.progressMu.Unlock()
	e.lastProgressAt = now

	// Append to progress.ndjson.
	// Intentionally open/close on each event so writes are immediately flushed
	// and resilient to abrupt process termination.
	if f, err := os.OpenFile(filepath.Join(logsRoot, "progress.ndjson"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		_, _ = f.Write(append(b, '\n'))
		_ = f.Close()
	}

	// Overwrite live.json with the last event.
	_ = os.WriteFile(filepath.Join(logsRoot, "live.json"), append(b, '\n'), 0o644)
}

func (e *Engine) setLastProgressTime(ts time.Time) {
	if e == nil {
		return
	}
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	e.progressMu.Lock()
	e.lastProgressAt = ts
	e.progressMu.Unlock()
}

func (e *Engine) lastProgressTime() time.Time {
	if e == nil {
		return time.Time{}
	}
	e.progressMu.Lock()
	defer e.progressMu.Unlock()
	return e.lastProgressAt
}
