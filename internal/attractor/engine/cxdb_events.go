package engine

import (
	"context"
	"strings"

	"github.com/strongdm/kilroy/internal/attractor/model"
	"github.com/strongdm/kilroy/internal/attractor/runtime"
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
	return err
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
}

func (e *Engine) cxdbCheckpointSaved(ctx context.Context, nodeID string, status runtime.StageStatus, sha string) {
	// Checkpoint bookkeeping is kilroy plumbing — files are persisted to disk
	// but not emitted to the CXDB turn chain.
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
