// HTTP handlers for the /runs and /workflows APIs backed by the RunDB.
package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
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
		Sort:      r.URL.Query().Get("sort"),
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

func (s *Server) handleDownloadOutput(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "output name is required")
		return
	}

	// Resolve logs_root from DB.
	var logsRoot string
	db, err := rundb.Open(rundb.DefaultPath())
	if err == nil {
		defer db.Close()
		run, err := db.GetRun(id)
		if err == nil && run != nil {
			logsRoot = run.LogsRoot
		}
	}
	if logsRoot == "" {
		if p, ok := s.registry.Get(id); ok && p != nil {
			logsRoot = p.LogsRoot
		}
	}
	if logsRoot == "" {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}

	// Serve from outputs/ directory. Sanitize the name to prevent traversal.
	clean := filepath.Clean(name)
	if strings.Contains(clean, "..") {
		writeError(w, http.StatusBadRequest, "invalid output name")
		return
	}

	outputPath := filepath.Join(logsRoot, "outputs", clean)
	data, err := os.ReadFile(outputPath)
	if err != nil {
		writeError(w, http.StatusNotFound, "output not found: "+name)
		return
	}

	// Detect content type.
	if strings.HasSuffix(name, ".json") {
		w.Header().Set("Content-Type", "application/json")
	} else if strings.HasSuffix(name, ".md") {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

func (s *Server) handleListWorkflows(w http.ResponseWriter, r *http.Request) {
	// Scan known workflow package directories.
	searchDirs := []string{"workflows"}

	// Also check the working directory.
	if cwd, err := os.Getwd(); err == nil {
		candidate := filepath.Join(cwd, "workflows")
		if candidate != "workflows" {
			searchDirs = append(searchDirs, candidate)
		}
	}

	type workflowInfo struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Version     string   `json:"version"`
		Dir         string   `json:"dir"`
		Inputs      []any    `json:"inputs,omitempty"`
		Outputs     []string `json:"outputs,omitempty"`
	}

	var workflows []workflowInfo
	seen := map[string]bool{}

	for _, dir := range searchDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || seen[e.Name()] {
				continue
			}
			pkgDir := filepath.Join(dir, e.Name())
			// Must have a graph.dot to be a workflow package.
			if _, err := os.Stat(filepath.Join(pkgDir, "graph.dot")); err != nil {
				continue
			}
			seen[e.Name()] = true

			wf := workflowInfo{Name: e.Name(), Dir: pkgDir}

			// Parse workflow.toml if present.
			tomlPath := filepath.Join(pkgDir, "workflow.toml")
			if data, err := os.ReadFile(tomlPath); err == nil {
				var manifest struct {
					Name        string `toml:"name"`
					Description string `toml:"description"`
					Version     string `toml:"version"`
					Inputs      []any  `toml:"inputs"`
					Outputs     []string `toml:"outputs"`
				}
				if err := toml.Unmarshal(data, &manifest); err == nil {
					if manifest.Name != "" {
						wf.Name = manifest.Name
					}
					wf.Description = manifest.Description
					wf.Version = manifest.Version
					wf.Inputs = manifest.Inputs
					wf.Outputs = manifest.Outputs
				}
			}
			workflows = append(workflows, wf)
		}
	}

	if workflows == nil {
		workflows = []workflowInfo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"workflows": workflows,
		"count":     len(workflows),
	})
}
