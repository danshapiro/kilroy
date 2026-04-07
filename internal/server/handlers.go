package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/agents"
	"github.com/danshapiro/kilroy/internal/attractor/engine"
	"github.com/danshapiro/kilroy/internal/attractor/rundb"
	"github.com/danshapiro/kilroy/internal/attractor/workflows"
)

// validRunID matches ULIDs, UUIDs, and other safe identifiers.
// Only alphanumeric, dashes, and underscores are allowed.
var validRunID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "ok",
		"pipelines": len(s.registry.List()),
	})
}

func (s *Server) handleSubmitPipeline(w http.ResponseWriter, r *http.Request) {
	var req SubmitPipelineRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
		return
	}

	// Resolve DOT source and config from either mode.
	var dotSource []byte
	var cfg *engine.RunConfigFile
	var graphDir string
	var packageDir string
	var labels map[string]string

	if req.Workflow != "" || req.PackagePath != "" {
		// Mode 2: Workflow package.
		pkgPath := req.PackagePath
		if pkgPath == "" {
			// Resolve by name from workflows/ directory.
			candidates := []string{
				filepath.Join("workflows", req.Workflow),
			}
			if cwd, err := os.Getwd(); err == nil {
				candidates = append(candidates, filepath.Join(cwd, "workflows", req.Workflow))
			}
			for _, c := range candidates {
				if _, err := os.Stat(filepath.Join(c, "graph.dot")); err == nil {
					pkgPath = c
					break
				}
			}
			if pkgPath == "" {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("workflow %q not found", req.Workflow))
				return
			}
		}

		pkg, err := workflows.LoadPackage(pkgPath)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("package load error: %v", err))
			return
		}

		dotSource, err = os.ReadFile(pkg.GraphPath)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("cannot read graph: %v", err))
			return
		}

		graphDir = filepath.Dir(pkg.GraphPath)
		packageDir = pkg.Dir

		// Apply manifest defaults for labels.
		labels = make(map[string]string)
		if pkg.Manifest != nil {
			for k, v := range pkg.Manifest.Defaults.Labels {
				labels[k] = v
			}
		}
		for k, v := range req.Labels {
			labels[k] = v
		}

		// Build config via auto-detection (same as CLI zero-config path).
		if req.ConfigPath != "" {
			cfg, err = engine.LoadRunConfigFile(req.ConfigPath)
			if err != nil {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid config: %v", err))
				return
			}
		}
		// cfg may be nil here — will be built below.
	} else {
		// Mode 1: Legacy (dot source + config path).
		if req.DotSource == "" && req.DotSourcePath == "" {
			writeError(w, http.StatusBadRequest, "provide workflow, package_path, dot_source, or dot_source_path")
			return
		}
		if req.DotSource != "" && req.DotSourcePath != "" {
			writeError(w, http.StatusBadRequest, "provide dot_source or dot_source_path, not both")
			return
		}

		if req.DotSource != "" {
			dotSource = []byte(req.DotSource)
		} else {
			var err error
			dotSource, err = os.ReadFile(req.DotSourcePath)
			if err != nil {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("cannot read dot file: %v", err))
				return
			}
			graphDir = filepath.Dir(req.DotSourcePath)
		}

		if req.ConfigPath != "" {
			var err error
			cfg, err = engine.LoadRunConfigFile(req.ConfigPath)
			if err != nil {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid config: %v", err))
				return
			}
		}
		labels = req.Labels
	}

	// Build default config if none provided.
	if cfg == nil {
		workspace := req.Workspace
		if workspace == "" {
			workspace, _ = os.Getwd()
		}
		var err error
		cfg, err = engine.DefaultRunConfig(nil, workspace)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("build default config: %v", err))
			return
		}
	}

	// Generate run ID if not provided.
	runID := strings.TrimSpace(req.RunID)
	if runID == "" {
		id, err := engine.NewRunID()
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("generate run id: %v", err))
			return
		}
		runID = id
	}
	if !validRunID.MatchString(runID) {
		writeError(w, http.StatusBadRequest, "run_id must be alphanumeric with dashes/underscores, 1-128 chars")
		return
	}

	// Detect git integration from workspace.
	var gitOps engine.GitOps
	workspace := req.Workspace
	if workspace != "" {
		gitHook := &workflows.GitHook{}
		if gitHook.ValidateRepo(workspace, false) == nil {
			gitOps = gitHook
		}
	}

	// Open run DB.
	rdb, _ := rundb.Open(rundb.DefaultPath())

	// Create pipeline components.
	broadcaster := NewBroadcaster()
	interviewer := NewWebInterviewer(0)
	ctx, cancel := context.WithCancelCause(s.baseCtx)

	ps := &PipelineState{
		RunID:       runID,
		Broadcaster: broadcaster,
		Interviewer: interviewer,
		Cancel:      cancel,
		StartedAt:   time.Now().UTC(),
	}

	if err := s.registry.Register(runID, ps); err != nil {
		cancel(nil)
		if rdb != nil {
			rdb.Close()
		}
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	// Launch pipeline in a background goroutine.
	go func() {
		defer broadcaster.Close()
		if rdb != nil {
			defer rdb.Close()
		}

		overrides := engine.RunOptions{
			RunID:         runID,
			AllowTestShim: req.AllowTestShim,
			ForceModels:   req.ForceModels,
			ProgressSink:  broadcaster.Send,
			Interviewer:   interviewer,
			Inputs:        req.Inputs,
			Workspace:     workspace,
			GraphDir:      graphDir,
			Labels:        labels,
			GitOps:        gitOps,
			PackageDir:    packageDir,
			RunDB:         rdb,
			Registry:      newLayeredRegistry(req.Tmux),
			OnEngineReady: func(e *engine.Engine) {
				ps.SetEngine(e)
			},
		}

		res, err := engine.RunWithConfig(ctx, dotSource, cfg, overrides)
		ps.SetResult(res, err)
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{
		"run_id": runID,
		"status": "accepted",
	})
}

func (s *Server) handleGetPipeline(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if runID == "" {
		writeError(w, http.StatusBadRequest, "run_id is required")
		return
	}

	// Try live registry first — but only for actively running pipelines.
	// Completed runs get richer data from the DB (nodes, edges, providers).
	if ps, ok := s.registry.Get(runID); ok {
		status := ps.Status()
		if status.State == "running" {
			writeJSON(w, http.StatusOK, status)
			return
		}
	}

	// Fall back to RunDB for completed (or registry-completed) runs.
	db, err := rundb.Open(rundb.DefaultPath())
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("run %s not found", runID))
		return
	}
	defer db.Close()

	run, err := db.GetRun(runID)
	if err != nil || run == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("run %s not found", runID))
		return
	}

	nodes, _ := db.GetNodeExecutions(runID)
	edges, _ := db.GetEdgeDecisions(runID)
	providers, _ := db.GetProviderSelections(runID)

	dotSource := db.GetDotSource(runID)

	writeJSON(w, http.StatusOK, map[string]any{
		"run_id":         run.RunID,
		"graph_name":     run.GraphName,
		"goal":           run.Goal,
		"status":         run.Status,
		"started_at":     run.StartedAt,
		"completed_at":   run.CompletedAt,
		"duration_ms":    run.DurationMS,
		"logs_root":      run.LogsRoot,
		"worktree_dir":   run.WorktreeDir,
		"run_branch":     run.RunBranch,
		"repo_path":      run.RepoPath,
		"final_sha":      run.FinalSHA,
		"failure_reason": run.FailureReason,
		"labels":         run.Labels,
		"inputs":         run.Inputs,
		"warnings":       run.Warnings,
		"node_count":     run.NodeCount,
		"dot_source":     dotSource,
		"nodes":          nodes,
		"edges":          edges,
		"providers":      providers,
	})
}

func (s *Server) handlePipelineEvents(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if runID == "" {
		writeError(w, http.StatusBadRequest, "run_id is required")
		return
	}

	ps, ok := s.registry.Get(runID)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("pipeline %s not found", runID))
		return
	}

	WriteSSE(w, r, ps.Broadcaster)
}

func (s *Server) handleCancelPipeline(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if runID == "" {
		writeError(w, http.StatusBadRequest, "run_id is required")
		return
	}

	ps, ok := s.registry.Get(runID)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("pipeline %s not found", runID))
		return
	}

	ps.Cancel(fmt.Errorf("canceled via HTTP API"))
	ps.Interviewer.Cancel()
	writeJSON(w, http.StatusOK, map[string]string{"status": "canceling"})
}

func (s *Server) handleGetContext(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if runID == "" {
		writeError(w, http.StatusBadRequest, "run_id is required")
		return
	}

	// Try live registry first.
	if ps, ok := s.registry.Get(runID); ok {
		writeJSON(w, http.StatusOK, ps.ContextValues())
		return
	}

	// Fall back to DB — return node context_updates as a proxy.
	db, err := rundb.Open(rundb.DefaultPath())
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("run %s not found", runID))
		return
	}
	defer db.Close()

	nodes, _ := db.GetNodeExecutions(runID)
	if len(nodes) == 0 {
		writeError(w, http.StatusNotFound, fmt.Sprintf("run %s not found", runID))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"source": "db",
		"note":   "context snapshot from completed run node outcomes",
		"nodes":  len(nodes),
	})
}

func (s *Server) handleGetQuestions(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if runID == "" {
		writeError(w, http.StatusBadRequest, "run_id is required")
		return
	}

	ps, ok := s.registry.Get(runID)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("pipeline %s not found", runID))
		return
	}

	writeJSON(w, http.StatusOK, ps.Interviewer.Pending())
}

func (s *Server) handleAnswerQuestion(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	qid := r.PathValue("qid")
	if runID == "" || qid == "" {
		writeError(w, http.StatusBadRequest, "run_id and question_id are required")
		return
	}

	ps, ok := s.registry.Get(runID)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("pipeline %s not found", runID))
		return
	}

	var req AnswerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid body: %v", err))
		return
	}

	ans := engine.Answer{
		Value:  req.Value,
		Values: req.Values,
		Text:   req.Text,
	}

	if !ps.Interviewer.Answer(qid, ans) {
		writeError(w, http.StatusNotFound, "question not found or already answered")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "answered"})
}

// newLayeredRegistry builds a handler registry with all layers registered.
func newLayeredRegistry(useTmux bool) *engine.HandlerRegistry {
	reg := engine.NewCoreRegistry()
	if useTmux {
		agentHandler := agents.NewTmuxAgentHandler()
		reg.Register("agent", agentHandler)
		reg.SetDefault(agentHandler)
	} else {
		agentHandler := &agents.AgentHandler{}
		reg.Register("agent", agentHandler)
		reg.SetDefault(agentHandler)
	}
	reg.Register("wait.human", &workflows.HumanGateHandler{})
	reg.Register("stack.manager_loop", &workflows.ManagerLoopHandler{})
	return reg
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ErrorResponse{Error: msg})
}
