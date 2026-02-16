package engine

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestRunWithConfig_CheckpointExcludesConfiguredArtifacts(t *testing.T) {
	cleanupStrayEngineArtifacts(t)
	t.Cleanup(func() { cleanupStrayEngineArtifacts(t) })

	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	pinned := writePinnedCatalog(t)
	cxdbSrv := newCXDBTestServer(t)

	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.ModelDB.OpenRouterModelInfoPath = pinned
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"
	cfg.Git.CheckpointExcludeGlobs = []string{"**/.cargo_target*/**"}

	dot := []byte(`
digraph G {
  graph [goal="checkpoint exclude coverage"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  write_files [shape=parallelogram, tool_command="mkdir -p src .cargo_target_local/obj && echo ok > src/ok.txt && echo temp > .cargo_target_local/obj/a.bin"]
  start -> write_files -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "checkpoint-exclude", LogsRoot: logsRoot})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}
	if res.FinalStatus != "success" {
		t.Fatalf("final status: got %s want success", res.FinalStatus)
	}

	files := gitLsFiles(t, res.WorktreeDir)
	if !containsPath(files, "src/ok.txt") {
		t.Fatalf("expected src/ok.txt to be checkpointed, got %v", files)
	}
	if containsPath(files, ".cargo_target_local/obj/a.bin") {
		t.Fatalf("excluded artifact should not be checkpointed: %v", files)
	}
}

func gitLsFiles(t *testing.T, dir string) []string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "ls-files")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git ls-files: %v\n%s", err, out)
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "\n")
}

func containsPath(paths []string, target string) bool {
	for _, p := range paths {
		if strings.TrimSpace(p) == target {
			return true
		}
	}
	return false
}
