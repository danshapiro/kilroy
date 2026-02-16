package gitutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddAllWithExcludes_DoesNotStageExcludedUntrackedPaths(t *testing.T) {
	dir := initTestRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, ".cargo_target_local", "obj"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".cargo_target_local", "obj", "a.bin"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := AddAllWithExcludes(dir, []string{"**/.cargo_target*/**"}); err != nil {
		t.Fatalf("AddAllWithExcludes: %v", err)
	}

	staged := stagedFiles(t, dir)
	if !contains(staged, "keep.txt") {
		t.Fatalf("expected keep.txt staged, got %v", staged)
	}
	if contains(staged, ".cargo_target_local/obj/a.bin") {
		t.Fatalf("excluded file was staged: %v", staged)
	}
}

func TestAddAllWithExcludes_DoesNotStageExcludedTrackedModifications(t *testing.T) {
	dir := initTestRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, ".cargo_target_local", "obj"), 0o755); err != nil {
		t.Fatal(err)
	}
	excluded := filepath.Join(dir, ".cargo_target_local", "obj", "tracked.bin")
	if err := os.WriteFile(excluded, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	normal := filepath.Join(dir, "normal.txt")
	if err := os.WriteFile(normal, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	commitAll(t, dir, "seed tracked files")

	if err := os.WriteFile(excluded, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(normal, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := AddAllWithExcludes(dir, []string{"**/.cargo_target*/**"}); err != nil {
		t.Fatalf("AddAllWithExcludes: %v", err)
	}

	staged := stagedFiles(t, dir)
	if !contains(staged, "normal.txt") {
		t.Fatalf("expected normal.txt staged, got %v", staged)
	}
	if contains(staged, ".cargo_target_local/obj/tracked.bin") {
		t.Fatalf("excluded tracked file modification was staged: %v", staged)
	}
}

func stagedFiles(t *testing.T, dir string) []string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "diff", "--cached", "--name-only")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git diff --cached --name-only: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 1 && strings.TrimSpace(lines[0]) == "" {
		return nil
	}
	return lines
}

func commitAll(t *testing.T, dir string, msg string) {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "add", "-A")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add -A: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "-C", dir, "commit", "-m", msg)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@test",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@test",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit -m %q: %v\n%s", msg, err, out)
	}
}

func contains(ss []string, target string) bool {
	for _, s := range ss {
		if strings.TrimSpace(s) == target {
			return true
		}
	}
	return false
}
