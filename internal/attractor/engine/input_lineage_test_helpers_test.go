package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func branchIsolationDOTForRunID(runID string) []byte {
	runID = strings.TrimSpace(runID)
	return []byte(fmt.Sprintf(`
digraph P {
  graph [goal="lineage branch isolation"]
  start [shape=Mdiamond]
  par [shape=component]
  a [shape=parallelogram, tool_command="mkdir -p .ai/runs/%s && echo a > .ai/runs/%s/branch_a_only.md"]
  b [shape=parallelogram, tool_command="test ! -f .ai/runs/%s/branch_a_only.md"]
  join [shape=tripleoctagon]
  exit [shape=Msquare]
  start -> par
  par -> a
  par -> b
  a -> join
  b -> join
  join -> exit
}
`, runID, runID, runID))
}

func resumeSeedDOTForRunID(runID string) []byte {
	runID = strings.TrimSpace(runID)
	return []byte(fmt.Sprintf(`
digraph R {
  graph [goal="lineage resume seed"]
  start [shape=Mdiamond]
  write [shape=parallelogram, tool_command="mkdir -p .ai/runs/%s && echo seeded > .ai/runs/%s/postmortem_latest.md"]
  exit [shape=Msquare]
  start -> write -> exit
}
`, runID, runID))
}

func mustLoadParallelResults(t *testing.T, path string) []parallelBranchResult {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read parallel_results.json: %v", err)
	}
	var results []parallelBranchResult
	if err := json.Unmarshal(b, &results); err != nil {
		t.Fatalf("unmarshal parallel_results.json: %v", err)
	}
	return results
}

func mustLoadInputManifest(t *testing.T, path string) *InputManifest {
	t.Helper()
	manifest, err := loadInputManifest(path)
	if err != nil {
		t.Fatalf("load input manifest %s: %v", path, err)
	}
	return manifest
}

func mustParallelResultByStartNode(t *testing.T, results []parallelBranchResult, nodeID string) parallelBranchResult {
	t.Helper()
	for _, r := range results {
		if strings.TrimSpace(r.StartNodeID) == strings.TrimSpace(nodeID) {
			return r
		}
	}
	t.Fatalf("missing parallel branch result for node %q", nodeID)
	return parallelBranchResult{}
}

func resumeFromCheckpointForTest(ctx context.Context, logsRoot string) error {
	_, err := Resume(ctx, logsRoot)
	return err
}

func runScopedPath(worktreeDir string, runID string, rel string) string {
	return filepath.Join(worktreeDir, ".ai", "runs", runID, filepath.FromSlash(rel))
}
