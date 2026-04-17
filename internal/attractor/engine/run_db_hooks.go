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
// stage dirs are reused or cleaned up. Also captures any script files that
// the tool_command referenced so reruns/debugging can see exactly what ran.
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
	// Capture any script files referenced by tool_invocation.json. This gives
	// debuggers the exact script content that ran even if the package is
	// updated or deleted later.
	e.captureReferencedScripts(dbID, stageDir)
}

// captureReferencedScripts inspects tool_invocation.json and captures any
// file-path tokens that exist in the worktree as tool_script artifacts.
// Tokens are extracted from both argv entries and the joined command string
// (split on whitespace) to handle the common `bash -c "sh scripts/foo.sh"`
// pattern where the script path is embedded inside a single argv entry.
func (e *Engine) captureReferencedScripts(dbID int64, stageDir string) {
	invocationPath := filepath.Join(stageDir, "tool_invocation.json")
	raw, err := os.ReadFile(invocationPath)
	if err != nil {
		return
	}
	var inv struct {
		Argv    []string `json:"argv"`
		Command string   `json:"command"`
	}
	if json.Unmarshal(raw, &inv) != nil {
		return
	}
	// Collect tokens from argv entries plus the command string itself.
	var tokens []string
	for _, arg := range inv.Argv {
		tokens = append(tokens, strings.Fields(arg)...)
	}
	if inv.Command != "" {
		tokens = append(tokens, strings.Fields(inv.Command)...)
	}
	seen := map[string]bool{}
	for _, token := range tokens {
		candidate := extractScriptPath(token)
		if candidate == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true
		path := candidate
		if !filepath.IsAbs(path) && e.WorktreeDir != "" {
			path = filepath.Join(e.WorktreeDir, candidate)
		}
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		data, err := readCapped(path, maxCapturedArtifactBytes)
		if err != nil {
			continue
		}
		truncated := info.Size() > maxCapturedArtifactBytes
		artifactName := "tool_script:" + filepath.Base(candidate)
		contentType := scriptContentType(candidate)
		if err := e.RunDB.RecordNodeArtifact(dbID, artifactName, contentType, data, truncated); err != nil {
			e.Warn(fmt.Sprintf("rundb: record script %s: %v", artifactName, err))
		}
	}
}

// extractScriptPath returns the argument path if it looks like a script file
// (ends with a known script extension or lives under a scripts/ directory).
// Returns empty string for non-script args.
func extractScriptPath(arg string) string {
	trimmed := strings.TrimSpace(arg)
	if trimmed == "" {
		return ""
	}
	lower := strings.ToLower(trimmed)
	// Recognize common script extensions.
	extensions := []string{".sh", ".py", ".js", ".mjs", ".ts", ".rb", ".pl", ".bash", ".zsh", ".fish", ".ps1"}
	hasExt := false
	for _, ext := range extensions {
		if strings.HasSuffix(lower, ext) {
			hasExt = true
			break
		}
	}
	// Also recognize paths inside .kilroy/package/scripts/ even without an extension.
	isInScriptsDir := strings.Contains(trimmed, "/scripts/") || strings.Contains(trimmed, ".kilroy/package/")
	if !hasExt && !isInScriptsDir {
		return ""
	}
	// Strip any shell redirection or flags.
	if strings.ContainsAny(trimmed, "<>|;&") {
		return ""
	}
	return trimmed
}

// scriptContentType returns a reasonable content-type for a script path.
func scriptContentType(path string) string {
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".py"):
		return "text/x-python"
	case strings.HasSuffix(lower, ".js"), strings.HasSuffix(lower, ".mjs"):
		return "application/javascript"
	case strings.HasSuffix(lower, ".ts"):
		return "application/typescript"
	case strings.HasSuffix(lower, ".rb"):
		return "text/x-ruby"
	case strings.HasSuffix(lower, ".pl"):
		return "text/x-perl"
	default:
		return "text/x-shellscript"
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
