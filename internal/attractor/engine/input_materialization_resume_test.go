package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestInputMaterializationResume_RestoresInputsFromSnapshotWithoutSourceWorkspace(t *testing.T) {
	sourceRepo := t.TempDir()
	worktree := t.TempDir()
	logsRoot := t.TempDir()

	mustWriteInputFile(t, filepath.Join(sourceRepo, ".ai", "definition_of_done.md"), "See [tests](../tests.md)")
	mustWriteInputFile(t, filepath.Join(sourceRepo, "tests.md"), "tests")

	eng := &Engine{
		Options:     RunOptions{RepoPath: sourceRepo, RunID: "resume-materialization-test"},
		WorktreeDir: worktree,
		LogsRoot:    logsRoot,
		InputMaterializationPolicy: InputMaterializationPolicy{
			Enabled:          true,
			Include:          nil,
			DefaultInclude:   []string{".ai/**"},
			FollowReferences: true,
			InferWithLLM:     false,
		},
		InputInferenceCache:  map[string][]InferredReference{},
		InputSourceTargetMap: map[string]string{},
	}

	if err := eng.materializeRunStartupInputs(context.Background()); err != nil {
		t.Fatalf("materializeRunStartupInputs: %v", err)
	}
	assertExists(t, filepath.Join(worktree, ".ai", "definition_of_done.md"))
	assertExists(t, filepath.Join(worktree, "tests.md"))
	assertExists(t, inputRunManifestPath(logsRoot))
	assertExists(t, inputSnapshotFilesRoot(logsRoot))

	if err := os.RemoveAll(filepath.Join(sourceRepo, ".ai")); err != nil {
		t.Fatalf("remove source .ai: %v", err)
	}
	if err := os.Remove(filepath.Join(sourceRepo, "tests.md")); err != nil {
		t.Fatalf("remove source tests.md: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(worktree, ".ai")); err != nil {
		t.Fatalf("remove worktree .ai: %v", err)
	}
	if err := os.Remove(filepath.Join(worktree, "tests.md")); err != nil {
		t.Fatalf("remove worktree tests.md: %v", err)
	}

	if err := eng.materializeResumeStartupInputs(context.Background()); err != nil {
		t.Fatalf("materializeResumeStartupInputs: %v", err)
	}
	assertExists(t, filepath.Join(worktree, ".ai", "definition_of_done.md"))
	assertExists(t, filepath.Join(worktree, "tests.md"))
}

func TestInputMaterializationRunStartup_IncludeMissingFailsFast(t *testing.T) {
	sourceRepo := t.TempDir()
	worktree := t.TempDir()
	logsRoot := t.TempDir()

	eng := &Engine{
		Options:     RunOptions{RepoPath: sourceRepo, RunID: "startup-include-missing"},
		WorktreeDir: worktree,
		LogsRoot:    logsRoot,
		InputMaterializationPolicy: InputMaterializationPolicy{
			Enabled:          true,
			Include:          []string{"missing/**/*.md"},
			DefaultInclude:   nil,
			FollowReferences: true,
		},
		InputInferenceCache:  map[string][]InferredReference{},
		InputSourceTargetMap: map[string]string{},
	}

	err := eng.materializeRunStartupInputs(context.Background())
	if err == nil {
		t.Fatal("expected include-missing error")
	}
	if _, ok := err.(*inputIncludeMissingError); !ok {
		t.Fatalf("expected *inputIncludeMissingError, got %T (%v)", err, err)
	}
}

func TestInputMaterializationResume_LineageHydratesRunScopedWithoutWorkspaceAI(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	cfg := newInputMaterializationRunConfigForTest(t, repo)
	runID := "lineage-resume"

	res, err := RunWithConfig(context.Background(), resumeSeedDOTForRunID(runID), cfg, RunOptions{
		RunID:       runID,
		LogsRoot:    logsRoot,
		DisableCXDB: true,
	})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}

	runScoped := runScopedPath(res.WorktreeDir, res.RunID, "postmortem_latest.md")
	if err := os.MkdirAll(filepath.Dir(runScoped), 0o755); err != nil {
		t.Fatalf("mkdir run scoped: %v", err)
	}
	if err := os.WriteFile(runScoped, []byte("lineage-only-content"), 0o644); err != nil {
		t.Fatalf("write run scoped: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(repo, ".ai")); err != nil {
		t.Fatalf("remove source .ai: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(res.WorktreeDir, ".ai")); err != nil {
		t.Fatalf("remove worktree .ai: %v", err)
	}

	if err := resumeFromCheckpointForTest(context.Background(), logsRoot); err != nil {
		t.Fatalf("resumeFromCheckpointForTest: %v", err)
	}

	resumedRunScoped := runScopedPath(filepath.Join(logsRoot, "worktree"), res.RunID, "postmortem_latest.md")
	if !fileExists(resumedRunScoped) {
		t.Fatalf("resume failed to hydrate run-scoped file from persisted lineage: %s", resumedRunScoped)
	}
}
