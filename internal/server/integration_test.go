package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/engine"
)

// newTestServer creates a Server and wraps its mux in httptest.Server.
func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	srv := New(Config{Addr: ":0"})
	ts := httptest.NewServer(srv.httpSrv.Handler)
	t.Cleanup(func() {
		ts.Close()
		srv.Shutdown()
	})
	return srv, ts
}

// registerTestPipeline creates a pipeline state with a broadcaster and
// interviewer, registers it, and returns the components for test manipulation.
func registerTestPipeline(t *testing.T, srv *Server, runID string) (*PipelineState, *Broadcaster, *WebInterviewer) {
	t.Helper()
	b := NewBroadcaster()
	wi := NewWebInterviewer(5 * time.Second)
	_, cancel := context.WithCancelCause(context.Background())
	ps := &PipelineState{
		RunID:       runID,
		Broadcaster: b,
		Interviewer: wi,
		Cancel:      cancel,
		StartedAt:   time.Now().UTC(),
	}
	if err := srv.registry.Register(runID, ps); err != nil {
		t.Fatalf("register pipeline: %v", err)
	}
	return ps, b, wi
}

func TestIntegration_HealthEndpoint(t *testing.T) {
	_, ts := newTestServer(t)

	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("expected status=ok, got %v", body["status"])
	}
}

func TestIntegration_PipelineNotFound(t *testing.T) {
	_, ts := newTestServer(t)

	resp, err := http.Get(ts.URL + "/pipelines/nonexistent")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestIntegration_PipelineLifecycle(t *testing.T) {
	srv, ts := newTestServer(t)
	runID := "test-run-001"
	ps, broadcaster, _ := registerTestPipeline(t, srv, runID)

	// Send some progress events.
	broadcaster.Send(map[string]any{
		"event":   "node_start",
		"node_id": "fetch_data",
		"ts":      time.Now().UTC().Format(time.RFC3339Nano),
	})
	broadcaster.Send(map[string]any{
		"event":   "node_complete",
		"node_id": "fetch_data",
		"ts":      time.Now().UTC().Format(time.RFC3339Nano),
	})

	// GET /pipelines/{id} — should be running with current_node_id from history.
	resp, err := http.Get(ts.URL + "/pipelines/" + runID)
	if err != nil {
		t.Fatalf("GET pipeline: %v", err)
	}
	defer resp.Body.Close()

	var status PipelineStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if status.State != "running" {
		t.Errorf("expected state=running, got %q", status.State)
	}
	if status.RunID != runID {
		t.Errorf("expected run_id=%s, got %s", runID, status.RunID)
	}
	if status.CurrentNodeID != "fetch_data" {
		t.Errorf("expected current_node_id=fetch_data, got %q", status.CurrentNodeID)
	}
	if status.LastEvent != "node_complete" {
		t.Errorf("expected last_event=node_complete, got %q", status.LastEvent)
	}

	// Mark pipeline as done with success.
	ps.SetResult(&engine.Result{FinalStatus: "success", FinalCommitSHA: "abc123"}, nil)

	// GET again — should show success.
	resp2, err := http.Get(ts.URL + "/pipelines/" + runID)
	if err != nil {
		t.Fatalf("GET pipeline (done): %v", err)
	}
	defer resp2.Body.Close()

	var status2 PipelineStatus
	if err := json.NewDecoder(resp2.Body).Decode(&status2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if status2.State != "success" {
		t.Errorf("expected state=success, got %q", status2.State)
	}
	if status2.FinalCommit != "abc123" {
		t.Errorf("expected final_commit=abc123, got %q", status2.FinalCommit)
	}
}

func TestIntegration_SSEEvents(t *testing.T) {
	srv, ts := newTestServer(t)
	runID := "test-sse-001"
	_, broadcaster, _ := registerTestPipeline(t, srv, runID)

	// Send an event before subscribing (tests history replay).
	broadcaster.Send(map[string]any{
		"event":   "node_start",
		"node_id": "step1",
	})

	// Subscribe to SSE in a goroutine.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/pipelines/"+runID+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	defer resp.Body.Close()

	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("expected text/event-stream, got %q", resp.Header.Get("Content-Type"))
	}

	scanner := bufio.NewScanner(resp.Body)
	events := make(chan string, 10)

	go func() {
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				events <- strings.TrimPrefix(line, "data: ")
			} else if strings.HasPrefix(line, "event: done") {
				events <- "DONE"
			}
		}
		close(events)
	}()

	// First event should be the replayed history.
	select {
	case ev := <-events:
		var data map[string]any
		if err := json.Unmarshal([]byte(ev), &data); err != nil {
			t.Fatalf("unmarshal replayed event: %v", err)
		}
		if data["node_id"] != "step1" {
			t.Errorf("expected node_id=step1, got %v", data["node_id"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for replayed event")
	}

	// Send a live event.
	broadcaster.Send(map[string]any{
		"event":   "node_complete",
		"node_id": "step1",
	})

	select {
	case ev := <-events:
		var data map[string]any
		if err := json.Unmarshal([]byte(ev), &data); err != nil {
			t.Fatalf("unmarshal live event: %v", err)
		}
		if data["event"] != "node_complete" {
			t.Errorf("expected event=node_complete, got %v", data["event"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for live event")
	}

	// Close broadcaster to signal pipeline done.
	broadcaster.Close()

	select {
	case ev := <-events:
		if ev != "DONE" {
			// Might get the done data line — that's fine too.
			t.Logf("got event before DONE: %s", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for done event")
	}
}

func TestIntegration_CancelPipeline(t *testing.T) {
	srv, ts := newTestServer(t)
	runID := "test-cancel-001"
	registerTestPipeline(t, srv, runID)

	req, _ := http.NewRequest("POST", ts.URL+"/pipelines/"+runID+"/cancel", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST cancel: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	if body["status"] != "canceling" {
		t.Errorf("expected status=canceling, got %q", body["status"])
	}
}

func TestIntegration_QuestionsAndAnswers(t *testing.T) {
	srv, ts := newTestServer(t)
	runID := "test-qa-001"
	_, _, interviewer := registerTestPipeline(t, srv, runID)

	// Initially no pending questions.
	resp, err := http.Get(ts.URL + "/pipelines/" + runID + "/questions")
	if err != nil {
		t.Fatalf("GET questions: %v", err)
	}
	defer resp.Body.Close()

	var questions []PendingQuestion
	json.NewDecoder(resp.Body).Decode(&questions)
	if len(questions) != 0 {
		t.Errorf("expected 0 questions, got %d", len(questions))
	}

	// Ask a question in a goroutine (blocks until answered).
	answerCh := make(chan engine.Answer, 1)
	go func() {
		ans := interviewer.Ask(engine.Question{
			Type:  "confirm",
			Text:  "Deploy to production?",
			Stage: "deploy",
			Options: []engine.Option{
				{Key: "yes", Label: "Yes"},
				{Key: "no", Label: "No"},
			},
		})
		answerCh <- ans
	}()

	// Wait for the question to be pending.
	var pending []PendingQuestion
	for i := 0; i < 20; i++ {
		time.Sleep(50 * time.Millisecond)
		r, _ := http.Get(ts.URL + "/pipelines/" + runID + "/questions")
		json.NewDecoder(r.Body).Decode(&pending)
		r.Body.Close()
		if len(pending) > 0 {
			break
		}
	}
	if len(pending) == 0 {
		t.Fatal("no pending question appeared")
	}

	pq := pending[0]
	if pq.Text != "Deploy to production?" {
		t.Errorf("expected question text, got %q", pq.Text)
	}
	if pq.Type != "confirm" {
		t.Errorf("expected type=confirm, got %q", pq.Type)
	}
	if len(pq.Options) != 2 {
		t.Errorf("expected 2 options, got %d", len(pq.Options))
	}

	// Answer the question.
	answerBody := `{"value":"yes"}`
	answerReq, _ := http.NewRequest("POST",
		fmt.Sprintf("%s/pipelines/%s/questions/%s/answer", ts.URL, runID, pq.QuestionID),
		strings.NewReader(answerBody))
	answerReq.Header.Set("Content-Type", "application/json")
	ansResp, err := http.DefaultClient.Do(answerReq)
	if err != nil {
		t.Fatalf("POST answer: %v", err)
	}
	defer ansResp.Body.Close()

	if ansResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", ansResp.StatusCode)
	}

	// Verify the engine goroutine received the answer.
	select {
	case ans := <-answerCh:
		if ans.Value != "yes" {
			t.Errorf("expected answer value=yes, got %q", ans.Value)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for answer delivery")
	}

	// No more pending questions.
	r, _ := http.Get(ts.URL + "/pipelines/" + runID + "/questions")
	var empty []PendingQuestion
	json.NewDecoder(r.Body).Decode(&empty)
	r.Body.Close()
	if len(empty) != 0 {
		t.Errorf("expected 0 pending questions after answering, got %d", len(empty))
	}
}

func TestIntegration_ContextEndpoint(t *testing.T) {
	srv, ts := newTestServer(t)
	runID := "test-ctx-001"
	registerTestPipeline(t, srv, runID)

	// No engine set — should return empty map.
	resp, err := http.Get(ts.URL + "/pipelines/" + runID + "/context")
	if err != nil {
		t.Fatalf("GET context: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var ctx map[string]any
	json.NewDecoder(resp.Body).Decode(&ctx)
	if len(ctx) != 0 {
		t.Errorf("expected empty context map, got %v", ctx)
	}
}

func TestIntegration_SubmitValidation(t *testing.T) {
	_, ts := newTestServer(t)

	tests := []struct {
		name   string
		body   string
		expect int
	}{
		{
			name:   "empty body",
			body:   `{}`,
			expect: http.StatusBadRequest,
		},
		{
			name:   "missing config_path",
			body:   `{"dot_source":"digraph{}"}`,
			expect: http.StatusBadRequest,
		},
		{
			name:   "invalid json",
			body:   `{not json`,
			expect: http.StatusBadRequest,
		},
		{
			name:   "both dot_source and dot_source_path",
			body:   `{"dot_source":"digraph{}","dot_source_path":"/tmp/test.dot","config_path":"/tmp/run.yaml"}`,
			expect: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := http.Post(ts.URL+"/pipelines", "application/json", strings.NewReader(tt.body))
			if err != nil {
				t.Fatalf("POST: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tt.expect {
				t.Errorf("expected %d, got %d", tt.expect, resp.StatusCode)
			}
		})
	}
}

func TestIntegration_HealthReflectsPipelineCount(t *testing.T) {
	srv, ts := newTestServer(t)

	// Initially 0.
	resp, _ := http.Get(ts.URL + "/health")
	var h1 map[string]any
	json.NewDecoder(resp.Body).Decode(&h1)
	resp.Body.Close()

	if h1["pipelines"].(float64) != 0 {
		t.Errorf("expected 0 pipelines, got %v", h1["pipelines"])
	}

	// Register a pipeline.
	registerTestPipeline(t, srv, "p1")

	resp2, _ := http.Get(ts.URL + "/health")
	var h2 map[string]any
	json.NewDecoder(resp2.Body).Decode(&h2)
	resp2.Body.Close()

	if h2["pipelines"].(float64) != 1 {
		t.Errorf("expected 1 pipeline, got %v", h2["pipelines"])
	}
}

func TestIntegration_AnswerWrongQuestion(t *testing.T) {
	srv, ts := newTestServer(t)
	runID := "test-wrongq-001"
	registerTestPipeline(t, srv, runID)

	// Answer with a nonexistent question ID.
	body := `{"value":"yes"}`
	req, _ := http.NewRequest("POST",
		fmt.Sprintf("%s/pipelines/%s/questions/q-999/answer", ts.URL, runID),
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST answer: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestIntegration_CSRFBlocksCrossOrigin(t *testing.T) {
	_, ts := newTestServer(t)

	// POST with cross-origin Origin header should be blocked.
	req, _ := http.NewRequest("POST", ts.URL+"/pipelines", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.com")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for cross-origin POST, got %d", resp.StatusCode)
	}
}

func TestIntegration_CSRFAllowsNoOrigin(t *testing.T) {
	_, ts := newTestServer(t)

	// POST without Origin header should pass through (programmatic caller).
	// Will fail at validation (empty body), but NOT at CSRF.
	req, _ := http.NewRequest("POST", ts.URL+"/pipelines", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	// No Origin header set.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	// Should get 400 (validation error), not 403 (CSRF block).
	if resp.StatusCode == http.StatusForbidden {
		t.Fatal("expected CSRF to allow requests without Origin header")
	}
}

func TestIntegration_CSRFAllowsLocalhostOrigin(t *testing.T) {
	_, ts := newTestServer(t)

	// POST with localhost Origin should be allowed.
	req, _ := http.NewRequest("POST", ts.URL+"/pipelines", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", ts.URL) // httptest uses 127.0.0.1:PORT
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	// Should get 400 (validation), not 403 (CSRF).
	if resp.StatusCode == http.StatusForbidden {
		t.Fatal("expected CSRF to allow same-origin localhost requests")
	}
}

func TestIntegration_RunIDPathTraversal(t *testing.T) {
	_, ts := newTestServer(t)

	tests := []struct {
		name  string
		runID string
	}{
		{"path traversal", "../../../etc/passwd"},
		{"absolute path", "/tmp/evil"},
		{"dot segment", ".."},
		{"slash in id", "foo/bar"},
		{"empty after trim", "  "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"dot_source":"digraph{}","config_path":"/tmp/run.yaml","run_id":%q}`, tt.runID)
			resp, err := http.Post(ts.URL+"/pipelines", "application/json", strings.NewReader(body))
			if err != nil {
				t.Fatalf("POST: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("expected 400 for run_id=%q, got %d", tt.runID, resp.StatusCode)
			}
		})
	}
}

func TestIntegration_FailedPipelineStatus(t *testing.T) {
	srv, ts := newTestServer(t)
	runID := "test-fail-001"
	ps, _, _ := registerTestPipeline(t, srv, runID)

	// Mark as failed.
	ps.SetResult(nil, fmt.Errorf("node X exploded"))

	resp, _ := http.Get(ts.URL + "/pipelines/" + runID)
	defer resp.Body.Close()

	var status PipelineStatus
	json.NewDecoder(resp.Body).Decode(&status)
	if status.State != "fail" {
		t.Errorf("expected state=fail, got %q", status.State)
	}
	if status.FailureReason != "node X exploded" {
		t.Errorf("expected failure reason, got %q", status.FailureReason)
	}
}
