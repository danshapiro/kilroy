package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadProjectDocs_WalksFromGitRootToWorkingDir_InDepthOrder(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	initGitRepo(t, root)

	// Working directory is nested inside the repo.
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Instruction files at each level.
	_ = os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("ROOT\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "a", "AGENTS.md"), []byte("A\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "a", "b", "AGENTS.md"), []byte("B\n"), 0o644)

	env := NewLocalExecutionEnvironment(nested)
	docs, truncated := LoadProjectDocs(env, "AGENTS.md")
	if truncated {
		t.Fatalf("did not expect truncation")
	}
	if got, want := len(docs), 3; got != want {
		t.Fatalf("docs: got %d want %d (%v)", got, want, docs)
	}
	if docs[0].Path != "AGENTS.md" {
		t.Fatalf("doc0 path: %q", docs[0].Path)
	}
	if docs[1].Path != filepath.Join("a", "AGENTS.md") {
		t.Fatalf("doc1 path: %q", docs[1].Path)
	}
	if docs[2].Path != filepath.Join("a", "b", "AGENTS.md") {
		t.Fatalf("doc2 path: %q", docs[2].Path)
	}
}

func TestLoadProjectDocs_TruncatesTo32KBAndAddsMarker(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)

	huge := strings.Repeat("x", projectDocByteBudget+4096)
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(huge), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	env := NewLocalExecutionEnvironment(root)
	docs, truncated := LoadProjectDocs(env, "AGENTS.md")
	if !truncated {
		t.Fatalf("expected truncation")
	}
	if got, want := len(docs), 1; got != want {
		t.Fatalf("docs: got %d want %d", got, want)
	}
	if !strings.Contains(docs[0].Content, projectDocTruncMark) {
		t.Fatalf("expected truncation marker, got:\n%s", docs[0].Content)
	}
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, string(out))
		}
	}

	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	_ = os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644)
	run("add", "README.md")
	run("commit", "-m", "init")
}
