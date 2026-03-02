package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

// --- Unit tests for archiveAttemptDir ---

func TestArchiveAttemptDir_CopiesFiles(t *testing.T) {
	stageDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stageDir, "response.md"), []byte("hello response"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "status.json"), []byte(`{"status":"fail"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	archiveAttemptDir(stageDir, 1)

	wantDir := filepath.Join(stageDir, "attempt_1")
	if _, err := os.Stat(wantDir); err != nil {
		t.Fatalf("attempt_1 dir not created: %v", err)
	}
	for _, name := range []string{"response.md", "status.json"} {
		src, _ := os.ReadFile(filepath.Join(stageDir, name))
		dst, err := os.ReadFile(filepath.Join(wantDir, name))
		if err != nil {
			t.Fatalf("attempt_1/%s not copied: %v", name, err)
		}
		if string(src) != string(dst) {
			t.Fatalf("attempt_1/%s content mismatch: got %q want %q", name, dst, src)
		}
	}
}

func TestArchiveAttemptDir_OriginalsUnchanged(t *testing.T) {
	stageDir := t.TempDir()
	content := []byte("original content")
	if err := os.WriteFile(filepath.Join(stageDir, "response.md"), content, 0o644); err != nil {
		t.Fatal(err)
	}

	archiveAttemptDir(stageDir, 1)

	got, err := os.ReadFile(filepath.Join(stageDir, "response.md"))
	if err != nil {
		t.Fatalf("original file missing after archive: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("original file modified: got %q want %q", got, content)
	}
}

func TestArchiveAttemptDir_SkipsSubdirectories(t *testing.T) {
	stageDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stageDir, "response.md"), []byte("current"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Simulate a prior archive that already exists.
	_ = os.MkdirAll(filepath.Join(stageDir, "attempt_1"), 0o755)
	if err := os.WriteFile(filepath.Join(stageDir, "attempt_1", "response.md"), []byte("prior"), 0o644); err != nil {
		t.Fatal(err)
	}

	archiveAttemptDir(stageDir, 2)

	// attempt_1 should be untouched.
	data, err := os.ReadFile(filepath.Join(stageDir, "attempt_1", "response.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "prior" {
		t.Fatalf("attempt_1/response.md overwritten: got %q", data)
	}
	// attempt_2 should have the current flat file.
	data2, err := os.ReadFile(filepath.Join(stageDir, "attempt_2", "response.md"))
	if err != nil {
		t.Fatalf("attempt_2/response.md not created: %v", err)
	}
	if string(data2) != "current" {
		t.Fatalf("attempt_2/response.md: got %q want %q", data2, "current")
	}
	// attempt_2 must not contain a nested attempt_1 subdir.
	entries, _ := os.ReadDir(filepath.Join(stageDir, "attempt_2"))
	for _, e := range entries {
		if e.IsDir() {
			t.Fatalf("attempt_2 contains unexpected subdir %q", e.Name())
		}
	}
}

func TestArchiveAttemptDir_EmptyDir(t *testing.T) {
	stageDir := t.TempDir()
	// Should not panic on empty directory.
	archiveAttemptDir(stageDir, 1)

	entries, err := os.ReadDir(filepath.Join(stageDir, "attempt_1"))
	if err != nil {
		t.Fatalf("attempt_1 not created: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty attempt_1, got %d entries", len(entries))
	}
}

func TestArchiveAttemptDir_PreservesBrowserArtifactDirectories(t *testing.T) {
	stageDir := t.TempDir()
	artifactPath := filepath.Join(stageDir, browserArtifactsDirName, "playwright-report", "index.html")
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifactPath, []byte("artifact"), 0o644); err != nil {
		t.Fatal(err)
	}

	archiveAttemptDir(stageDir, 1)

	assertExists(t, filepath.Join(stageDir, "attempt_1", browserArtifactsDirName, "playwright-report", "index.html"))
}

// --- Integration test: engine archives prior attempt artifacts on retry ---

// retryWithContentHandler writes a unique response.md on each call and fails
// on the first two attempts, succeeding on the third.
type retryWithContentHandler struct {
	callCount int
}

func (h *retryWithContentHandler) Execute(_ context.Context, exec *Execution, node *model.Node) (runtime.Outcome, error) {
	h.callCount++
	stageDir := filepath.Join(exec.LogsRoot, node.ID)
	_ = os.MkdirAll(stageDir, 0o755)
	_ = os.WriteFile(filepath.Join(stageDir, "response.md"),
		[]byte(fmt.Sprintf("response from call %d", h.callCount)), 0o644)
	if h.callCount < 3 {
		return runtime.Outcome{Status: runtime.StatusRetry, FailureReason: "transient: try again"}, nil
	}
	return runtime.Outcome{Status: runtime.StatusSuccess}, nil
}

func TestRun_AttemptArchivesPreservedOnRetry(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	logsRoot := t.TempDir()

	g, _, err := Prepare([]byte(`
digraph G {
  start [shape=Mdiamond]
  n [shape=diamond, type="retry_with_content", max_retries=3]
  exit  [shape=Msquare]
  start -> n -> exit
}
`))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	handler := &retryWithContentHandler{}
	opts := RunOptions{RepoPath: repo, RunID: "archivetest", LogsRoot: logsRoot}
	if err := opts.applyDefaults(); err != nil {
		t.Fatalf("applyDefaults: %v", err)
	}
	eng := &Engine{
		Graph:           g,
		Options:         opts,
		DotSource:       []byte(""),
		LogsRoot:        opts.LogsRoot,
		WorktreeDir:     opts.WorktreeDir,
		Context:         runtime.NewContext(),
		Registry:        NewDefaultRegistry(),
		Interviewer:     &AutoApproveInterviewer{},
		CodergenBackend: &SimulatedCodergenBackend{},
	}
	eng.Registry.Register("retry_with_content", handler)
	eng.RunBranch = "attractor/run/" + opts.RunID

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := eng.run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}

	if handler.callCount != 3 {
		t.Fatalf("expected 3 handler calls, got %d", handler.callCount)
	}

	nodeDir := filepath.Join(logsRoot, "n")

	// attempt_1/ and attempt_2/ must exist and contain each attempt's response.
	for _, tc := range []struct {
		attempt int
		want    string
	}{
		{1, "response from call 1"},
		{2, "response from call 2"},
	} {
		path := filepath.Join(nodeDir, fmt.Sprintf("attempt_%d", tc.attempt), "response.md")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("attempt_%d/response.md: %v", tc.attempt, err)
		}
		if string(data) != tc.want {
			t.Fatalf("attempt_%d/response.md: got %q want %q", tc.attempt, data, tc.want)
		}
	}

	// Flat response.md must hold the final (successful) attempt's content.
	data, err := os.ReadFile(filepath.Join(nodeDir, "response.md"))
	if err != nil {
		t.Fatalf("response.md: %v", err)
	}
	if string(data) != "response from call 3" {
		t.Fatalf("response.md: got %q want %q", data, "response from call 3")
	}

	// attempt_1/status.json must exist and reflect the retry outcome.
	raw, err := os.ReadFile(filepath.Join(nodeDir, "attempt_1", "status.json"))
	if err != nil {
		t.Fatalf("attempt_1/status.json: %v", err)
	}
	var s map[string]any
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("attempt_1/status.json parse: %v", err)
	}
	if s["status"] != "retry" {
		t.Fatalf("attempt_1/status.json status: got %q want \"retry\"", s["status"])
	}

	// attempt_3/ must not exist — the last attempt is always in the flat dir.
	if _, err := os.Stat(filepath.Join(nodeDir, "attempt_3")); err == nil {
		t.Fatalf("attempt_3/ should not exist; final attempt stays in flat dir")
	}
}
