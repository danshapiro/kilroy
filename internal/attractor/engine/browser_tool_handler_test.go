package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

func TestToolHandler_CollectsBrowserArtifacts_OnSuccess(t *testing.T) {
	out, logsRoot, _, nodeID := runToolHandler(t, "verify_browser", "Verify Browser", "mkdir -p test-results && echo ok > test-results/result.txt", nil)
	if out.Status != runtime.StatusSuccess {
		t.Fatalf("status: got %q want %q", out.Status, runtime.StatusSuccess)
	}

	assertExists(t, filepath.Join(logsRoot, nodeID, browserArtifactsDirName, "test-results", "result.txt"))
}

func TestToolHandler_CollectsBrowserArtifacts_OnFailure(t *testing.T) {
	out, logsRoot, _, nodeID := runToolHandler(t, "verify_browser", "Verify Browser", "mkdir -p playwright-report && echo failed > playwright-report/index.html; echo boom >&2; exit 1", nil)
	if out.Status != runtime.StatusFail {
		t.Fatalf("status: got %q want %q", out.Status, runtime.StatusFail)
	}

	assertExists(t, filepath.Join(logsRoot, nodeID, browserArtifactsDirName, "playwright-report", "index.html"))
}

func TestToolHandler_BrowserArtifactCollectionFailure_IsNonFatal(t *testing.T) {
	origCollect := collectBrowserArtifactsFunc
	origSnapshot := snapshotBrowserArtifactsFunc
	t.Cleanup(func() {
		collectBrowserArtifactsFunc = origCollect
		snapshotBrowserArtifactsFunc = origSnapshot
	})

	snapshotBrowserArtifactsFunc = func(_ string) (map[string]artifactFingerprint, error) {
		return map[string]artifactFingerprint{}, nil
	}
	collectBrowserArtifactsFunc = func(_ string, _ string, _ map[string]artifactFingerprint, _ time.Time) (browserArtifactSummary, error) {
		return browserArtifactSummary{}, os.ErrPermission
	}

	out, _, _, _ := runToolHandler(t, "verify_browser", "Verify Browser", "echo ok", nil)
	if out.Status != runtime.StatusSuccess {
		t.Fatalf("status: got %q want %q", out.Status, runtime.StatusSuccess)
	}
}

func TestToolHandler_BrowserArtifactSummary_EmitsProgressEvent(t *testing.T) {
	_, logsRoot, _, _ := runToolHandler(t, "verify_browser", "Verify Browser", "mkdir -p test-results && echo ok > test-results/result.txt", nil)
	progressPath := filepath.Join(logsRoot, "progress.ndjson")
	events := mustReadProgressEvents(t, progressPath)
	found := false
	for _, ev := range events {
		if fmt.Sprint(ev["event"]) != "tool_browser_artifacts" {
			continue
		}
		if fmt.Sprint(ev["copied_files"]) == "0" {
			t.Fatalf("tool_browser_artifacts event found with copied_files=0: %#v", ev)
		}
		found = true
		break
	}
	if !found {
		t.Fatalf("expected tool_browser_artifacts event in %s", progressPath)
	}
}

func TestToolHandler_BrowserFailureReasonUsesStderrExcerpt_WhenCommandFails(t *testing.T) {
	out, _, _, _ := runToolHandler(t, "verify_browser", "Verify Browser", "echo 'page.goto failed: net::ERR_INTERNET_DISCONNECTED' >&2; exit 1", nil)
	if out.Status != runtime.StatusFail {
		t.Fatalf("status: got %q want %q", out.Status, runtime.StatusFail)
	}
	if !strings.Contains(strings.ToLower(out.FailureReason), "net::err_internet_disconnected") {
		t.Fatalf("failure_reason missing stderr excerpt: %q", out.FailureReason)
	}
}

func TestToolHandler_BrowserFailureReasonFallsBackToStdout_WhenStderrEmpty(t *testing.T) {
	out, _, _, _ := runToolHandler(t, "verify_browser", "Verify Browser", "echo 'page.goto failed: net::ERR_INTERNET_DISCONNECTED'; exit 1", nil)
	if out.Status != runtime.StatusFail {
		t.Fatalf("status: got %q want %q", out.Status, runtime.StatusFail)
	}
	if !strings.Contains(strings.ToLower(out.FailureReason), "net::err_internet_disconnected") {
		t.Fatalf("failure_reason missing stdout fallback excerpt: %q", out.FailureReason)
	}
}

func TestToolHandler_NonBrowserFailureReason_RemainsExitStatus(t *testing.T) {
	out, _, _, _ := runToolHandler(t, "compile_project", "Compile", "echo 'compile error' >&2; exit 1", nil)
	if out.Status != runtime.StatusFail {
		t.Fatalf("status: got %q want %q", out.Status, runtime.StatusFail)
	}
	if !strings.Contains(strings.ToLower(out.FailureReason), "exit status") {
		t.Fatalf("expected exit-status failure reason for non-browser node, got: %q", out.FailureReason)
	}
}

func runToolHandler(t *testing.T, nodeID string, label string, cmd string, attrs map[string]string) (runtime.Outcome, string, string, string) {
	t.Helper()

	logsRoot := t.TempDir()
	worktree := t.TempDir()

	nodeAttrs := map[string]string{
		"shape":        "parallelogram",
		"label":        label,
		"tool_command": cmd,
	}
	for k, v := range attrs {
		nodeAttrs[k] = v
	}
	node := &model.Node{
		ID:    nodeID,
		Attrs: nodeAttrs,
	}

	execCtx := &Execution{
		Context:     runtime.NewContext(),
		LogsRoot:    logsRoot,
		WorktreeDir: worktree,
		Engine: &Engine{
			LogsRoot: logsRoot,
			Options:  RunOptions{RunID: "tool-handler-test"},
		},
	}

	out, err := (&ToolHandler{}).Execute(context.Background(), execCtx, node)
	if err != nil {
		t.Fatalf("ToolHandler.Execute returned error: %v", err)
	}
	return out, logsRoot, worktree, nodeID
}
