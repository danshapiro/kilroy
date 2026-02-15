package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/engine"
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

	if req.DotSource == "" && req.DotSourcePath == "" {
		writeError(w, http.StatusBadRequest, "dot_source or dot_source_path is required")
		return
	}
	if req.DotSource != "" && req.DotSourcePath != "" {
		writeError(w, http.StatusBadRequest, "provide dot_source or dot_source_path, not both")
		return
	}
	if req.ConfigPath == "" {
		writeError(w, http.StatusBadRequest, "config_path is required")
		return
	}

	// Resolve DOT source.
	var dotSource []byte
	if req.DotSource != "" {
		dotSource = []byte(req.DotSource)
	} else {
		var err error
		dotSource, err = os.ReadFile(req.DotSourcePath)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("cannot read dot file: %v", err))
			return
		}
	}

	// Load config.
	cfg, err := engine.LoadRunConfigFile(req.ConfigPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid config: %v", err))
		return
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

	// Create pipeline components.
	broadcaster := NewBroadcaster()
	interviewer := NewWebInterviewer(0) // default timeout
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
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	// Launch pipeline in a background goroutine.
	go func() {
		defer broadcaster.Close()

		overrides := engine.RunOptions{
			RunID:         runID,
			AllowTestShim: req.AllowTestShim,
			ForceModels:   req.ForceModels,
			ProgressSink:  broadcaster.Send,
			Interviewer:   interviewer,
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

	ps, ok := s.registry.Get(runID)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("pipeline %s not found", runID))
		return
	}

	writeJSON(w, http.StatusOK, ps.Status())
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

	ps, ok := s.registry.Get(runID)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("pipeline %s not found", runID))
		return
	}

	writeJSON(w, http.StatusOK, ps.ContextValues())
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

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ErrorResponse{Error: msg})
}
