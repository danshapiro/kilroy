// HTTP handlers for the /runs and /workflows APIs backed by the RunDB.
package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/danshapiro/kilroy/internal/attractor/gitutil"
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

func (s *Server) handleGetNodeTurns(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	nodeId := r.PathValue("nodeId")
	if id == "" || nodeId == "" {
		writeError(w, http.StatusBadRequest, "run_id and nodeId are required")
		return
	}

	// Resolve logs_root.
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

	stageDir := filepath.Join(logsRoot, nodeId)
	if _, err := os.Stat(stageDir); err != nil {
		writeError(w, http.StatusNotFound, "node directory not found: "+nodeId)
		return
	}

	result := map[string]any{
		"node_id": nodeId,
		"run_id":  id,
	}

	// Read prompt.
	if data, err := os.ReadFile(filepath.Join(stageDir, "prompt.md")); err == nil {
		result["prompt"] = string(data)
	}

	// Read response (agent output).
	if data, err := os.ReadFile(filepath.Join(stageDir, "response.md")); err == nil {
		result["response"] = string(data)
	}

	// Read status.json for outcome details.
	if data, err := os.ReadFile(filepath.Join(stageDir, "status.json")); err == nil {
		var status map[string]any
		if json.Unmarshal(data, &status) == nil {
			result["status"] = status
		}
	}

	// Read stdout/stderr for tool nodes.
	if data, err := os.ReadFile(filepath.Join(stageDir, "stdout.log")); err == nil {
		result["stdout"] = string(data)
	}
	if data, err := os.ReadFile(filepath.Join(stageDir, "stderr.log")); err == nil {
		result["stderr"] = string(data)
	}

	// Read tool_timing.json if present.
	if data, err := os.ReadFile(filepath.Join(stageDir, "tool_timing.json")); err == nil {
		var timing map[string]any
		if json.Unmarshal(data, &timing) == nil {
			result["timing"] = timing
		}
	}

	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleGetNodeDiff(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	nodeId := r.PathValue("nodeId")
	if id == "" || nodeId == "" {
		writeError(w, http.StatusBadRequest, "run_id and nodeId are required")
		return
	}

	db, err := rundb.Open(rundb.DefaultPath())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database unavailable: "+err.Error())
		return
	}
	defer db.Close()

	// Parse optional attempt query param (default: latest).
	attempt := 0
	if a := r.URL.Query().Get("attempt"); a != "" {
		if v, err := strconv.Atoi(a); err == nil && v > 0 {
			attempt = v
		}
	}

	diff, err := db.GetNodeDiff(id, nodeId, attempt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query node diff: "+err.Error())
		return
	}
	if diff == nil {
		writeError(w, http.StatusNotFound, "no diff data for node "+nodeId)
		return
	}

	result := map[string]any{
		"node_id":    diff.NodeID,
		"attempt":    diff.Attempt,
		"before_sha": diff.BeforeSHA,
		"after_sha":  diff.AfterSHA,
		"summary": map[string]any{
			"files_changed": diff.FilesChanged,
			"insertions":    diff.Insertions,
			"deletions":     diff.Deletions,
		},
	}

	// Try to get the full diff from the git repo.
	run, _ := db.GetRun(id)
	if run != nil && run.WorktreeDir != "" {
		if fullDiff, err := gitDiff(run.WorktreeDir, diff.BeforeSHA, diff.AfterSHA); err == nil {
			result["diff"] = fullDiff
		}
		if fileList, err := gitDiffFileList(run.WorktreeDir, diff.BeforeSHA, diff.AfterSHA); err == nil {
			result["files"] = fileList
		}
	}

	writeJSON(w, http.StatusOK, result)
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

// gitDiff returns the full unified diff between two commits.
func gitDiff(dir, fromSHA, toSHA string) (string, error) {
	return gitutil.Diff(dir, fromSHA, toSHA)
}

// gitDiffFileList returns per-file diff info between two commits.
type diffFileEntry struct {
	Path       string `json:"path"`
	Status     string `json:"status"`
	Insertions int    `json:"insertions"`
	Deletions  int    `json:"deletions"`
}

func gitDiffFileList(dir, fromSHA, toSHA string) ([]diffFileEntry, error) {
	raw, err := gitutil.DiffFileList(dir, fromSHA, toSHA)
	if err != nil {
		return nil, err
	}
	var entries []diffFileEntry
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		ins, _ := strconv.Atoi(parts[0])
		del, _ := strconv.Atoi(parts[1])
		path := parts[2]
		status := "modified"
		if ins > 0 && del == 0 {
			status = "added"
		}
		entries = append(entries, diffFileEntry{
			Path:       path,
			Status:     status,
			Insertions: ins,
			Deletions:  del,
		})
	}
	return entries, nil
}
