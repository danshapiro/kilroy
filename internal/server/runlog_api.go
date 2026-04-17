// HTTP handler for serving the canonical run.log activity log.
package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/rundb"
)

func (s *Server) handleGetRunLog(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	logsRoot := s.resolveLogsRoot(id)
	if logsRoot == "" {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}

	logPath := filepath.Join(logsRoot, "run.log")
	if _, err := os.Stat(logPath); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"events":  []any{},
			"message": "no run.log found",
		})
		return
	}

	// Parse query filters.
	nodeFilter := r.URL.Query().Get("node")
	sourceFilter := r.URL.Query().Get("source")
	eventFilter := r.URL.Query().Get("event")
	sinceFilter := r.URL.Query().Get("since")
	tailParam := r.URL.Query().Get("tail")
	stream := r.URL.Query().Get("stream") == "true"

	var sinceTime time.Time
	if sinceFilter != "" {
		if t, err := time.Parse(time.RFC3339, sinceFilter); err == nil {
			sinceTime = t
		}
	}
	tailN := 0
	if tailParam != "" {
		if n, err := strconv.Atoi(tailParam); err == nil && n > 0 {
			tailN = n
		}
	}

	if stream {
		s.streamRunLog(w, r, logPath, nodeFilter, sourceFilter, eventFilter, sinceTime)
		return
	}

	// Read and filter the log file.
	events, err := readFilteredRunLog(logPath, nodeFilter, sourceFilter, eventFilter, sinceTime, tailN)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read run.log: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"events": events,
		"count":  len(events),
	})
}

// resolveLogsRoot finds the logs_root directory for a run ID.
func (s *Server) resolveLogsRoot(id string) string {
	db, err := rundb.Open(rundb.DefaultPath())
	if err == nil {
		defer db.Close()
		run, err := db.GetRun(id)
		if err == nil && run != nil && run.LogsRoot != "" {
			return run.LogsRoot
		}
	}
	if p, ok := s.registry.Get(id); ok && p != nil {
		return p.LogsRoot
	}
	return ""
}

// readFilteredRunLog reads run.log and applies filters.
func readFilteredRunLog(path, node, source, event string, since time.Time, tail int) ([]json.RawMessage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var events []json.RawMessage
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		if matchesFilters(line, node, source, event, since) {
			events = append(events, json.RawMessage(append([]byte{}, line...)))
		}
	}

	if tail > 0 && len(events) > tail {
		events = events[len(events)-tail:]
	}
	return events, scanner.Err()
}

// matchesFilters checks if a log line passes all query filters.
func matchesFilters(line []byte, node, source, event string, since time.Time) bool {
	if node == "" && source == "" && event == "" && since.IsZero() {
		return true
	}
	var ev struct {
		Ts     string `json:"ts"`
		Source string `json:"source"`
		Node   string `json:"node"`
		Event  string `json:"event"`
	}
	if err := json.Unmarshal(line, &ev); err != nil {
		return false
	}
	if node != "" && ev.Node != node {
		return false
	}
	if source != "" && ev.Source != source {
		return false
	}
	if event != "" && !strings.HasPrefix(ev.Event, event) {
		return false
	}
	if !since.IsZero() {
		if t, err := time.Parse("2006-01-02T15:04:05.000Z", ev.Ts); err == nil {
			if t.Before(since) {
				return false
			}
		}
	}
	return true
}

// streamRunLog implements SSE streaming of run.log events using polling.
func (s *Server) streamRunLog(w http.ResponseWriter, r *http.Request, logPath, node, source, event string, since time.Time) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Send all existing events.
	f, err := os.Open(logPath)
	if err != nil {
		fmt.Fprintf(w, "event: error\ndata: %q\n\n", err.Error())
		flusher.Flush()
		return
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		if matchesFilters(line, node, source, event, since) {
			fmt.Fprintf(w, "data: %s\n\n", line)
		}
	}
	offset, _ := f.Seek(0, 1)
	f.Close()
	flusher.Flush()

	// Poll for new events.
	ctx := r.Context()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			newOffset := tailNewLogEvents(w, flusher, logPath, offset, node, source, event, since)
			if newOffset > offset {
				offset = newOffset
			}
		}
	}
}

// tailNewLogEvents reads new lines from logPath starting at offset and writes them as SSE events.
func tailNewLogEvents(w http.ResponseWriter, flusher http.Flusher, logPath string, offset int64, node, source, event string, since time.Time) int64 {
	f, err := os.Open(logPath)
	if err != nil {
		return offset
	}
	defer f.Close()
	if _, err := f.Seek(offset, 0); err != nil {
		return offset
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	wrote := false
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		if matchesFilters(line, node, source, event, since) {
			fmt.Fprintf(w, "data: %s\n\n", line)
			wrote = true
		}
	}
	newOffset, _ := f.Seek(0, 1)
	if wrote {
		flusher.Flush()
	}
	return newOffset
}
