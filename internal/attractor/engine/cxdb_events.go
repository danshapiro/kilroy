package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

func (e *Engine) cxdbRunStarted(ctx context.Context, baseSHA string) error {
	if e == nil || e.CXDB == nil {
		return nil
	}
	data := map[string]any{
		"run_id":                 e.Options.RunID,
		"timestamp_ms":           nowMS(),
		"repo_path":              e.Options.RepoPath,
		"base_sha":               baseSHA,
		"run_branch":             e.RunBranch,
		"logs_root":              e.LogsRoot,
		"worktree_dir":           e.WorktreeDir,
		"graph_name":             e.Graph.Name,
		"goal":                   e.Graph.Attrs["goal"],
		"modeldb_catalog_sha256": e.ModelCatalogSHA,
		"modeldb_catalog_source": e.ModelCatalogSource,
	}
	if len(e.DotSource) > 0 {
		data["graph_dot"] = string(e.DotSource)
	}
	_, _, err := e.CXDB.Append(ctx, "com.kilroy.attractor.RunStarted", 1, data)
	if err != nil {
		return err
	}
	// Required artifacts.
	_, _ = e.CXDB.PutArtifactFile(ctx, "", "manifest.json", filepath.Join(e.LogsRoot, "manifest.json"))
	if _, err := os.Stat(filepath.Join(e.LogsRoot, "run_config.json")); err == nil {
		_, _ = e.CXDB.PutArtifactFile(ctx, "", "run_config.json", filepath.Join(e.LogsRoot, "run_config.json"))
	}
	openrouterCatalogPath := filepath.Join(e.LogsRoot, "modeldb", "openrouter_models.json")
	if _, err := os.Stat(openrouterCatalogPath); err == nil {
		_, _ = e.CXDB.PutArtifactFile(ctx, "", "modeldb/openrouter_models.json", openrouterCatalogPath)
	}
	if _, err := os.Stat(filepath.Join(e.LogsRoot, "graph.dot")); err == nil {
		_, _ = e.CXDB.PutArtifactFile(ctx, "", "graph.dot", filepath.Join(e.LogsRoot, "graph.dot"))
	}
	return nil
}

func (e *Engine) cxdbPrompt(ctx context.Context, nodeID, text string) {
	if e == nil || e.CXDB == nil {
		return
	}
	_, _, _ = e.CXDB.Append(ctx, "com.kilroy.attractor.Prompt", 1, map[string]any{
		"run_id":       e.Options.RunID,
		"node_id":      nodeID,
		"text":         text,
		"timestamp_ms": nowMS(),
	})
}

func (e *Engine) cxdbStageStarted(ctx context.Context, node *model.Node) {
	if e == nil || e.CXDB == nil || node == nil {
		return
	}
	_, _, _ = e.CXDB.Append(ctx, "com.kilroy.attractor.StageStarted", 1, map[string]any{
		"run_id":       e.Options.RunID,
		"node_id":      node.ID,
		"timestamp_ms": nowMS(),
		"handler_type": resolvedHandlerType(node),
	})
}

func (e *Engine) cxdbStageFinished(ctx context.Context, node *model.Node, out runtime.Outcome) {
	if e == nil || e.CXDB == nil || node == nil {
		return
	}
	_, _, _ = e.CXDB.Append(ctx, "com.kilroy.attractor.StageFinished", 1, map[string]any{
		"run_id":             e.Options.RunID,
		"node_id":            node.ID,
		"timestamp_ms":       nowMS(),
		"status":             string(out.Status),
		"preferred_label":    out.PreferredLabel,
		"failure_reason":     out.FailureReason,
		"notes":              out.Notes,
		"suggested_next_ids": out.SuggestedNextIDs,
	})

	// Stage artifact mapping (metaspec): prompt/response/status and any additional stage files.
	stageDir := filepath.Join(e.LogsRoot, node.ID)
	// Convenience tarball (metaspec SHOULD): stage.tgz.
	stageTar := filepath.Join(stageDir, "stage.tgz")
	if _, err := os.Stat(stageTar); err != nil {
		_ = writeTarGz(stageTar, stageDir, includeInStageArchive)
	}
	if _, err := os.Stat(filepath.Join(stageDir, "prompt.md")); err == nil {
		_, _ = e.CXDB.PutArtifactFile(ctx, node.ID, "prompt.md", filepath.Join(stageDir, "prompt.md"))
	}
	if _, err := os.Stat(filepath.Join(stageDir, "response.md")); err == nil {
		_, _ = e.CXDB.PutArtifactFile(ctx, node.ID, "response.md", filepath.Join(stageDir, "response.md"))
	}
	if _, err := os.Stat(filepath.Join(stageDir, "status.json")); err == nil {
		_, _ = e.CXDB.PutArtifactFile(ctx, node.ID, "status.json", filepath.Join(stageDir, "status.json"))
	}
	if _, err := os.Stat(filepath.Join(stageDir, "parallel_results.json")); err == nil {
		_, _ = e.CXDB.PutArtifactFile(ctx, node.ID, "parallel_results.json", filepath.Join(stageDir, "parallel_results.json"))
	}
	// Backend-native traces and agent loop logs (best-effort).
	for _, name := range []string{
		"stage.tgz",
		"events.ndjson",
		"events.json",
		"stdout.log",
		"stderr.log",
		"panic.txt",
		"output.json",
		"output_schema.json",
		"diff.patch",
		"api_request.json",
		"api_response.json",
		"cli_invocation.json",
		"cli_timing.json",
		"tool_invocation.json",
		"tool_timing.json",
	} {
		if _, err := os.Stat(filepath.Join(stageDir, name)); err == nil {
			_, _ = e.CXDB.PutArtifactFile(ctx, node.ID, name, filepath.Join(stageDir, name))
		}
	}
}

func (e *Engine) cxdbCheckpointSaved(ctx context.Context, nodeID string, status runtime.StageStatus, sha string) {
	if e == nil || e.CXDB == nil {
		return
	}
	_, _, _ = e.CXDB.Append(ctx, "com.kilroy.attractor.GitCheckpoint", 1, map[string]any{
		"run_id":         e.Options.RunID,
		"node_id":        nodeID,
		"status":         string(status),
		"git_commit_sha": sha,
		"timestamp_ms":   nowMS(),
	})
	cpPath := filepath.Join(e.LogsRoot, "checkpoint.json")
	if _, err := os.Stat(cpPath); err == nil {
		_, _ = e.CXDB.PutArtifactFile(ctx, "", "checkpoint.json", cpPath)
	}
	_, _, _ = e.CXDB.Append(ctx, "com.kilroy.attractor.CheckpointSaved", 1, map[string]any{
		"run_id":            e.Options.RunID,
		"node_id":           nodeID,
		"timestamp_ms":      nowMS(),
		"checkpoint_path":   cpPath,
		"cxdb_context_id":   e.CXDB.ContextID,
		"cxdb_head_turn_id": e.CXDB.HeadTurnID,
	})
}

func (e *Engine) cxdbRunCompleted(ctx context.Context, finalSHA string) (string, error) {
	if e == nil || e.CXDB == nil {
		return "", nil
	}
	turnID, _, err := e.CXDB.Append(ctx, "com.kilroy.attractor.RunCompleted", 1, map[string]any{
		"run_id":               e.Options.RunID,
		"timestamp_ms":         nowMS(),
		"final_status":         "success",
		"final_git_commit_sha": finalSHA,
		"cxdb_context_id":      e.CXDB.ContextID,
		"cxdb_head_turn_id":    e.CXDB.HeadTurnID,
	})
	return turnID, err
}

func resolvedHandlerType(n *model.Node) string {
	if n == nil {
		return ""
	}
	if t := strings.TrimSpace(n.TypeOverride()); t != "" {
		return t
	}
	return shapeToType(n.Shape())
}

// cxdbStageFailed emits a StageFailed event (spec §9.6).
func (e *Engine) cxdbStageFailed(ctx context.Context, node *model.Node, failureReason string, willRetry bool, attempt int) {
	if e == nil || e.CXDB == nil || node == nil {
		return
	}
	_, _, _ = e.CXDB.Append(ctx, "com.kilroy.attractor.StageFailed", 1, map[string]any{
		"run_id":         e.Options.RunID,
		"node_id":        node.ID,
		"timestamp_ms":   nowMS(),
		"failure_reason": failureReason,
		"will_retry":     willRetry,
		"attempt":        attempt,
	})
}

// cxdbStageRetrying emits a StageRetrying event (spec §9.6).
func (e *Engine) cxdbStageRetrying(ctx context.Context, node *model.Node, attempt int, delayMS int64) {
	if e == nil || e.CXDB == nil || node == nil {
		return
	}
	_, _, _ = e.CXDB.Append(ctx, "com.kilroy.attractor.StageRetrying", 1, map[string]any{
		"run_id":       e.Options.RunID,
		"node_id":      node.ID,
		"timestamp_ms": nowMS(),
		"attempt":      attempt,
		"delay_ms":     delayMS,
	})
}

// cxdbParallelStarted emits a ParallelStarted event (spec §9.6).
func (e *Engine) cxdbParallelStarted(ctx context.Context, nodeID string, branchCount int, joinPolicy string, errorPolicy string) {
	if e == nil || e.CXDB == nil {
		return
	}
	_, _, _ = e.CXDB.Append(ctx, "com.kilroy.attractor.ParallelStarted", 1, map[string]any{
		"run_id":       e.Options.RunID,
		"node_id":      nodeID,
		"timestamp_ms": nowMS(),
		"branch_count": branchCount,
		"join_policy":  joinPolicy,
		"error_policy": errorPolicy,
	})
}

// cxdbParallelBranchStarted emits a ParallelBranchStarted event (spec §9.6).
func (e *Engine) cxdbParallelBranchStarted(ctx context.Context, nodeID string, branchKey string, branchIndex int) {
	if e == nil || e.CXDB == nil {
		return
	}
	_, _, _ = e.CXDB.Append(ctx, "com.kilroy.attractor.ParallelBranchStarted", 1, map[string]any{
		"run_id":       e.Options.RunID,
		"node_id":      nodeID,
		"timestamp_ms": nowMS(),
		"branch_key":   branchKey,
		"branch_index": branchIndex,
	})
}

// cxdbParallelBranchCompleted emits a ParallelBranchCompleted event (spec §9.6).
func (e *Engine) cxdbParallelBranchCompleted(ctx context.Context, nodeID string, branchKey string, branchIndex int, status string, durationMS int64) {
	if e == nil || e.CXDB == nil {
		return
	}
	_, _, _ = e.CXDB.Append(ctx, "com.kilroy.attractor.ParallelBranchCompleted", 1, map[string]any{
		"run_id":       e.Options.RunID,
		"node_id":      nodeID,
		"timestamp_ms": nowMS(),
		"branch_key":   branchKey,
		"branch_index": branchIndex,
		"status":       status,
		"duration_ms":  durationMS,
	})
}

// cxdbParallelCompleted emits a ParallelCompleted event (spec §9.6).
func (e *Engine) cxdbParallelCompleted(ctx context.Context, nodeID string, successCount int, failureCount int, durationMS int64) {
	if e == nil || e.CXDB == nil {
		return
	}
	_, _, _ = e.CXDB.Append(ctx, "com.kilroy.attractor.ParallelCompleted", 1, map[string]any{
		"run_id":        e.Options.RunID,
		"node_id":       nodeID,
		"timestamp_ms":  nowMS(),
		"success_count": successCount,
		"failure_count": failureCount,
		"duration_ms":   durationMS,
	})
}

// cxdbInterviewStarted emits an InterviewStarted event (spec §9.6).
func (e *Engine) cxdbInterviewStarted(ctx context.Context, nodeID string, questionText string, questionType string) {
	if e == nil || e.CXDB == nil {
		return
	}
	_, _, _ = e.CXDB.Append(ctx, "com.kilroy.attractor.InterviewStarted", 1, map[string]any{
		"run_id":        e.Options.RunID,
		"node_id":       nodeID,
		"timestamp_ms":  nowMS(),
		"question_text": questionText,
		"question_type": questionType,
	})
}

// cxdbInterviewCompleted emits an InterviewCompleted event (spec §9.6).
func (e *Engine) cxdbInterviewCompleted(ctx context.Context, nodeID string, answerValue string, durationMS int64) {
	if e == nil || e.CXDB == nil {
		return
	}
	_, _, _ = e.CXDB.Append(ctx, "com.kilroy.attractor.InterviewCompleted", 1, map[string]any{
		"run_id":       e.Options.RunID,
		"node_id":      nodeID,
		"timestamp_ms": nowMS(),
		"answer_value": answerValue,
		"duration_ms":  durationMS,
	})
}

// cxdbInterviewTimeout emits an InterviewTimeout event (spec §9.6).
func (e *Engine) cxdbInterviewTimeout(ctx context.Context, nodeID string, questionText string, durationMS int64) {
	if e == nil || e.CXDB == nil {
		return
	}
	_, _, _ = e.CXDB.Append(ctx, "com.kilroy.attractor.InterviewTimeout", 1, map[string]any{
		"run_id":        e.Options.RunID,
		"node_id":       nodeID,
		"timestamp_ms":  nowMS(),
		"question_text": questionText,
		"duration_ms":   durationMS,
	})
}

func (e *Engine) cxdbRunFailed(ctx context.Context, nodeID string, sha string, reason string) (string, error) {
	if e == nil || e.CXDB == nil {
		return "", nil
	}
	turnID, _, err := e.CXDB.Append(ctx, "com.kilroy.attractor.RunFailed", 1, map[string]any{
		"run_id":         e.Options.RunID,
		"timestamp_ms":   nowMS(),
		"reason":         reason,
		"node_id":        nodeID,
		"git_commit_sha": sha,
	})
	return turnID, err
}
