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
	// Parse repeatable ?label=KEY=VALUE query params.
	if labelParams := r.URL.Query()["label"]; len(labelParams) > 0 {
		filter.Labels = map[string]string{}
		for _, spec := range labelParams {
			parts := strings.SplitN(spec, "=", 2)
			if len(parts) == 2 {
				filter.Labels[parts[0]] = parts[1]
			}
		}
	}
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
			filter.Limit = n
		}
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

	// Optional ?attempt=N selects a specific attempt (for loop iteration
	// history). When absent, returns the latest attempt.
	requestedAttempt := 0
	if s := strings.TrimSpace(r.URL.Query().Get("attempt")); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			requestedAttempt = n
		}
	}

	// Resolve the full run via DB (handles prefix IDs) and attempt DB-first artifact read.
	db, err := rundb.Open(rundb.DefaultPath())
	var run *rundb.RunSummary
	var resolvedID string
	if err == nil {
		defer db.Close()
		run, _ = db.GetRun(id)
	}
	if run != nil {
		resolvedID = run.RunID
	} else {
		resolvedID = id
	}

	result := map[string]any{
		"node_id": nodeId,
		"run_id":  resolvedID,
		"attempt": requestedAttempt,
	}

	// DB-first: read captured artifacts for the requested attempt of this node
	// (or the latest when no attempt was requested).
	dbServed := false
	if db != nil {
		var artifacts []rundb.NodeArtifactSummary
		if requestedAttempt > 0 {
			artifacts, _ = db.GetNodeArtifactsForAttempt(resolvedID, nodeId, requestedAttempt)
		} else {
			artifacts, _ = db.GetNodeArtifactsForRunNode(resolvedID, nodeId)
		}
		if len(artifacts) > 0 {
			dbServed = true
			assignArtifactsToTurnsResult(result, artifacts)
		}
	}

	// Filesystem fallback: legacy runs (pre-artifact-capture) or running nodes
	// whose completion hook hasn't fired yet.
	if !dbServed {
		var logsRoot string
		if run != nil {
			logsRoot = run.LogsRoot
		} else if p, ok := s.registry.Get(resolvedID); ok && p != nil {
			logsRoot = p.LogsRoot
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
		readFilesystemTurns(result, stageDir)
	}

	writeJSON(w, http.StatusOK, result)
}

// handleGetNodeAttempts returns all attempts for a node in a run, letting
// the UI render a visit/iteration picker. Each entry includes attempt number,
// status, timing, and failure details. Sorted by auto-increment id so the
// order matches execution order (which for loop iterations is the same as
// iteration order).
func (s *Server) handleGetNodeAttempts(w http.ResponseWriter, r *http.Request) {
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

	// Resolve full ID via prefix lookup.
	resolvedID := id
	if run, err := db.GetRun(id); err == nil && run != nil {
		resolvedID = run.RunID
	}

	attempts, err := db.GetNodeAttempts(resolvedID, nodeId)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query attempts: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"run_id":   resolvedID,
		"node_id":  nodeId,
		"attempts": attempts,
		"count":    len(attempts),
	})
}

// assignArtifactsToTurnsResult maps captured DB artifacts into the response
// shape the UI expects (prompt, response, agent_log, stdout, stderr, status).
func assignArtifactsToTurnsResult(result map[string]any, artifacts []rundb.NodeArtifactSummary) {
	scripts := []map[string]any{}
	for _, a := range artifacts {
		switch {
		case a.Name == "prompt.md":
			result["prompt"] = string(a.Content)
		case a.Name == "response.md":
			result["response"] = string(a.Content)
		case a.Name == "agent_output.jsonl":
			result["agent_log"] = string(a.Content)
			result["agent_log_format"] = "claude-stream-jsonl"
		case a.Name == "events.ndjson":
			result["agent_log"] = string(a.Content)
			result["agent_log_format"] = "kilroy-events-ndjson"
		case a.Name == "status.json":
			var status map[string]any
			if json.Unmarshal(a.Content, &status) == nil {
				result["status"] = status
			}
		case a.Name == "stdout.log":
			result["stdout"] = string(a.Content)
		case a.Name == "stderr.log":
			result["stderr"] = string(a.Content)
		case a.Name == "tool_timing.json":
			var timing map[string]any
			if json.Unmarshal(a.Content, &timing) == nil {
				result["timing"] = timing
			}
		case a.Name == "tool_invocation.json":
			var inv map[string]any
			if json.Unmarshal(a.Content, &inv) == nil {
				result["tool_invocation"] = inv
			}
		case strings.HasPrefix(a.Name, "tool_script:"):
			scripts = append(scripts, map[string]any{
				"name":         strings.TrimPrefix(a.Name, "tool_script:"),
				"content":      string(a.Content),
				"content_type": a.ContentType,
				"truncated":    a.Truncated,
			})
		}
		if a.Truncated {
			trunc, _ := result["truncated"].([]string)
			result["truncated"] = append(trunc, a.Name)
		}
	}
	if len(scripts) > 0 {
		result["scripts"] = scripts
	}
	result["source"] = "db"
}

// readFilesystemTurns is the legacy path: reads stage files directly from disk
// for runs that predate artifact capture or are still in flight.
func readFilesystemTurns(result map[string]any, stageDir string) {
	if data, err := os.ReadFile(filepath.Join(stageDir, "prompt.md")); err == nil {
		result["prompt"] = string(data)
	}
	if data, err := os.ReadFile(filepath.Join(stageDir, "response.md")); err == nil {
		result["response"] = string(data)
	}
	if data, err := os.ReadFile(filepath.Join(stageDir, "agent_output.jsonl")); err == nil {
		result["agent_log"] = string(data)
		result["agent_log_format"] = "claude-stream-jsonl"
	} else if data, err := os.ReadFile(filepath.Join(stageDir, "events.ndjson")); err == nil {
		result["agent_log"] = string(data)
		result["agent_log_format"] = "kilroy-events-ndjson"
	}
	if data, err := os.ReadFile(filepath.Join(stageDir, "status.json")); err == nil {
		var status map[string]any
		if json.Unmarshal(data, &status) == nil {
			result["status"] = status
		}
	}
	if data, err := os.ReadFile(filepath.Join(stageDir, "stdout.log")); err == nil {
		result["stdout"] = string(data)
	}
	if data, err := os.ReadFile(filepath.Join(stageDir, "stderr.log")); err == nil {
		result["stderr"] = string(data)
	}
	if data, err := os.ReadFile(filepath.Join(stageDir, "tool_timing.json")); err == nil {
		var timing map[string]any
		if json.Unmarshal(data, &timing) == nil {
			result["timing"] = timing
		}
	}
	result["source"] = "filesystem"
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
