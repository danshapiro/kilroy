// HTTP handlers for the /runs API backed by the RunDB.
package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"github.com/danshapiro/kilroy/internal/attractor/rundb"
)

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	db, err := rundb.Open(rundb.DefaultPath())
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"runs":    []any{},
			"warning": "run database unavailable: " + err.Error(),
		})
		return
	}
	defer db.Close()

	filter := rundb.ListFilter{
		Status:    r.URL.Query().Get("status"),
		GraphName: r.URL.Query().Get("graph"),
	}

	runs, err := db.ListRuns(filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query runs: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"runs":  runs,
		"count": len(runs),
	})
}

func (s *Server) handleGetRunOutputs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Try to find the run's logs_root from the RunDB.
	var logsRoot string
	db, err := rundb.Open(rundb.DefaultPath())
	if err == nil {
		defer db.Close()
		run, err := db.GetRun(id)
		if err == nil && run != nil {
			logsRoot = run.LogsRoot
		}
	}

	// Also check if we have the run in the live pipeline registry.
	if logsRoot == "" {
		if p, ok := s.registry.Get(id); ok && p != nil {
			logsRoot = p.LogsRoot
		}
	}

	if logsRoot == "" {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}

	outputsPath := filepath.Join(logsRoot, "outputs.json")
	data, err := os.ReadFile(outputsPath)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"outputs": []any{},
			"message": "no outputs declared or collected",
		})
		return
	}

	var outputs []any
	_ = json.Unmarshal(data, &outputs)
	writeJSON(w, http.StatusOK, map[string]any{
		"outputs": outputs,
	})
}
