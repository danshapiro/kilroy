// RunDB lifecycle hooks. Called at run/node/edge lifecycle points.
// All operations are best-effort: errors produce warnings, never block execution.
package engine

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

func (e *Engine) rundbRecordRunStart() {
	if e == nil || e.RunDB == nil {
		return
	}
	goal := ""
	if e.Graph != nil {
		goal = e.Graph.Attrs["goal"]
	}
	graphName := ""
	if e.Graph != nil {
		graphName = e.Graph.Name
	}
	var configMap map[string]any
	if e.RunConfig != nil {
		if b, err := json.Marshal(e.RunConfig); err == nil {
			_ = json.Unmarshal(b, &configMap)
		}
	}
	if err := e.RunDB.RecordRunStart(
		e.Options.RunID, graphName, goal, "running",
		e.LogsRoot, e.WorktreeDir, e.RunBranch, e.Options.RepoPath,
		string(e.DotSource), e.Options.Inputs, e.Options.Labels,
		e.Options.Invocation, configMap,
	); err != nil {
		e.Warn("rundb: record run start: " + err.Error())
	}
}

func (e *Engine) rundbRecordRunComplete(status runtime.FinalStatus, failureReason, finalSHA string) {
	if e == nil || e.RunDB == nil {
		return
	}
	if err := e.RunDB.RecordRunComplete(
		e.Options.RunID, string(status), failureReason, finalSHA, e.warningsCopy(),
	); err != nil {
		e.Warn("rundb: record run complete: " + err.Error())
	}
}

func (e *Engine) rundbRecordNodeStart(nodeID string, attempt int, handlerType string) int64 {
	if e == nil || e.RunDB == nil {
		return 0
	}
	id, err := e.RunDB.RecordNodeStart(e.Options.RunID, nodeID, attempt, handlerType)
	if err != nil {
		e.Warn("rundb: record node start: " + err.Error())
		return 0
	}
	return id
}

func (e *Engine) rundbRecordNodeComplete(dbID int64, out runtime.Outcome) {
	if e == nil || e.RunDB == nil || dbID == 0 {
		return
	}
	failureClass := ""
	if meta, ok := out.Meta["failure_class"]; ok {
		if s, ok := meta.(string); ok {
			failureClass = s
		}
	}
	if err := e.RunDB.RecordNodeComplete(
		dbID, string(out.Status), out.FailureReason, failureClass,
		out.PreferredLabel, out.Notes, out.ContextUpdates,
	); err != nil {
		e.Warn("rundb: record node complete: " + err.Error())
	}
}

// artifactCaptureList enumerates the files captured from a stage directory
// after each node attempt. Each entry pairs a filename with a content type hint.
var artifactCaptureList = []struct {
	name        string
	contentType string
}{
	{"prompt.md", "text/markdown"},
	{"response.md", "text/markdown"},
	{"agent_output.jsonl", "application/x-ndjson"},
	{"events.ndjson", "application/x-ndjson"},
	{"events.json", "application/json"},
	{"status.json", "application/json"},
	{"stdout.log", "text/plain"},
	{"stderr.log", "text/plain"},
	{"tool_timing.json", "application/json"},
	{"tool_invocation.json", "application/json"},
	{"tmux_command.txt", "text/plain"},
	{"inputs_manifest.json", "application/json"},
	{"provider_used.json", "application/json"},
	{"panic.txt", "text/plain"},
}

// maxCapturedArtifactBytes caps a single captured file. Files larger than this
// are stored truncated with the truncated flag set.
const maxCapturedArtifactBytes = 10 * 1024 * 1024 // 10 MB

// rundbCaptureNodeArtifacts reads the files in a node's stage directory and
// stores them against the node execution record. Called after CompleteNode so
// that iteration/retry history is preserved in the DB even when filesystem
// stage dirs are reused or cleaned up.
func (e *Engine) rundbCaptureNodeArtifacts(dbID int64, nodeID string) {
	if e == nil || e.RunDB == nil || dbID == 0 || e.LogsRoot == "" {
		return
	}
	stageDir := filepath.Join(e.LogsRoot, nodeID)
	for _, entry := range artifactCaptureList {
		path := filepath.Join(stageDir, entry.name)
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		truncated := false
		size := info.Size()
		readLimit := int64(maxCapturedArtifactBytes)
		if size > readLimit {
			truncated = true
		}
		data, err := readCapped(path, readLimit)
		if err != nil {
			e.Warn(fmt.Sprintf("rundb: capture artifact %s/%s: %v", nodeID, entry.name, err))
			continue
		}
		if err := e.RunDB.RecordNodeArtifact(dbID, entry.name, entry.contentType, data, truncated); err != nil {
			e.Warn(fmt.Sprintf("rundb: record artifact %s/%s: %v", nodeID, entry.name, err))
		}
	}
}

// readCapped reads up to limit bytes from path.
func readCapped(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, limit)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, err
	}
	return buf[:n], nil
}

func (e *Engine) rundbRecordEdgeDecision(fromNode, toNode, edgeLabel, condition, reason string) {
	if e == nil || e.RunDB == nil {
		return
	}
	if err := e.RunDB.RecordEdgeDecision(
		e.Options.RunID, fromNode, toNode, edgeLabel, condition, reason,
	); err != nil {
		e.Warn("rundb: record edge decision: " + err.Error())
	}
}

func (e *Engine) rundbRecordProviderIfAgent(nodeID string, attempt int) {
	if e == nil || e.RunDB == nil || e.Graph == nil {
		return
	}
	node := e.Graph.Nodes[nodeID]
	if node == nil {
		return
	}
	provider := node.Attrs["llm_provider"]
	model := node.Attrs["llm_model"]
	agentTool := node.Attrs["agent_tool"]
	if provider == "" && model == "" && agentTool == "" {
		return
	}
	backend := agentTool
	if backend == "" {
		backend = node.Attrs["backend"]
	}
	if backend == "" {
		backend = "cli"
	}
	if err := e.RunDB.RecordProviderSelection(
		e.Options.RunID, nodeID, attempt, provider, model, backend,
	); err != nil {
		e.Warn("rundb: record provider selection: " + err.Error())
	}
}

func (e *Engine) recordNodeDiff(nodeID string, attempt int, beforeSHA, afterSHA string) {
	if e == nil || e.RunDB == nil || e.GitOps == nil {
		return
	}
	beforeSHA = strings.TrimSpace(beforeSHA)
	afterSHA = strings.TrimSpace(afterSHA)
	if beforeSHA == "" || afterSHA == "" || beforeSHA == afterSHA {
		return
	}
	filesChanged, insertions, deletions, err := e.GitOps.DiffStat(e.WorktreeDir, beforeSHA, afterSHA)
	if err != nil {
		e.Warn("rundb: diffstat for node " + nodeID + ": " + err.Error())
	}
	if err := e.RunDB.RecordNodeDiff(e.Options.RunID, nodeID, attempt, beforeSHA, afterSHA, filesChanged, insertions, deletions); err != nil {
		e.Warn("rundb: record node diff: " + err.Error())
	}
	if e.RunLog != nil && filesChanged > 0 {
		e.RunLog.Info("git", nodeID, "commit", fmt.Sprintf("%d files changed (+%d/-%d) %s", filesChanged, insertions, deletions, afterSHA[:minInt(8, len(afterSHA))]), map[string]any{
			"before_sha":    beforeSHA,
			"after_sha":     afterSHA,
			"files_changed": filesChanged,
			"insertions":    insertions,
			"deletions":     deletions,
		})
	}
}

// resolvedHandlerTypeName returns the handler type string for a node.
func resolvedHandlerTypeName(e *Engine, nodeID string) string {
	if e == nil || e.Graph == nil || e.Registry == nil {
		return ""
	}
	node := e.Graph.Nodes[nodeID]
	if node == nil {
		return ""
	}
	if t := strings.TrimSpace(node.TypeOverride()); t != "" {
		return t
	}
	return shapeToType(node.Shape())
}
