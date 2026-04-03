package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

const (
	failureDossierFileName = "failure_dossier.json"
	toolInvocationFileName = "tool_invocation.json"
	toolTimingFileName     = "tool_timing.json"
	toolStderrFileName     = "stderr.log"

	failureDossierContextPathKey         = "context.failure_dossier.path"
	failureDossierContextLogsPathKey     = "context.failure_dossier.logs_path"
	failureDossierContextFailedNodeKey   = "context.failure_dossier.failed_node"
	failureDossierContextFailureClassKey = "context.failure_dossier.failure_class"
	failureDossierContextSummaryKey      = "context.failure_dossier.summary"
)

var (
	toolMissingPathPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?mi)\bcd:\s+([^:\r\n]+):\s+No such file or directory`),
		regexp.MustCompile(`(?mi)\btest:\s+([^:\r\n]+):\s+No such file or directory`),
		regexp.MustCompile(`(?mi)\bcannot (?:stat|open)\s+'([^'\r\n]+)'`),
		regexp.MustCompile(`(?mi)\bopen\s+([^:\r\n]+):\s+no such file or directory`),
	}
	toolMissingExecutablePattern = regexp.MustCompile(`(?mi)^(?:.*?:\s+)?([A-Za-z0-9._+\-/]+):\s+command not found$`)
)

type failureDossier struct {
	Version            int                      `json:"version"`
	GeneratedAt        string                   `json:"generated_at"`
	FailedNodeID       string                   `json:"failed_node_id"`
	HandlerType        string                   `json:"handler_type"`
	Status             string                   `json:"status"`
	FailureClass       string                   `json:"failure_class"`
	FailureReason      string                   `json:"failure_reason"`
	AttemptsUsed       int                      `json:"attempts_used"`
	MaxAttempts        int                      `json:"max_attempts"`
	StageDir           string                   `json:"stage_dir"`
	MissingPaths       []failureDossierPathFact `json:"missing_paths,omitempty"`
	MissingExecutables []string                 `json:"missing_executables,omitempty"`
	Tool               *failureDossierTool      `json:"tool,omitempty"`
	Summary            string                   `json:"summary"`
}

type failureDossierTool struct {
	Command       string `json:"command,omitempty"`
	WorkingDir    string `json:"working_dir,omitempty"`
	ExitCode      int    `json:"exit_code"`
	StderrExcerpt string `json:"stderr_excerpt,omitempty"`
}

type failureDossierPathFact struct {
	Path                  string `json:"path"`
	AbsolutePath          string `json:"absolute_path,omitempty"`
	InsideRepo            bool   `json:"inside_repo"`
	ExistsNow             bool   `json:"exists_now"`
	ExistsInInputSnapshot bool   `json:"exists_in_input_snapshot"`
}

func (e *Engine) updateFailureDossierContext(node *model.Node, out runtime.Outcome, failureClass string, retries map[string]int) {
	if e == nil || e.Context == nil {
		return
	}
	if !isFailureLoopRestartOutcome(out) {
		e.clearFailureDossierContext()
		return
	}

	dossier := e.buildFailureDossier(node, out, failureClass, retries)
	// Latest failure wins: this file is intentionally overwritten on each
	// fail/retry outcome so the next agent stage reads current evidence.
	logsPath := filepath.Join(e.LogsRoot, failureDossierFileName)
	if err := writeJSON(logsPath, dossier); err != nil {
		e.Warn(fmt.Sprintf("failure dossier write (%s): %v", logsPath, err))
		e.clearFailureDossierContext()
		return
	}

	contextPath := logsPath
	if strings.TrimSpace(e.WorktreeDir) != "" {
		worktreeRelPath := failureDossierRunScopedRelativePath(e.Options.RunID)
		wtPath := filepath.Join(e.WorktreeDir, filepath.FromSlash(worktreeRelPath))
		if err := os.MkdirAll(filepath.Dir(wtPath), 0o755); err != nil {
			e.Warn(fmt.Sprintf("failure dossier mkdir (%s): %v", filepath.Dir(wtPath), err))
		} else if err := writeJSON(wtPath, dossier); err != nil {
			e.Warn(fmt.Sprintf("failure dossier write (%s): %v", wtPath, err))
		} else {
			contextPath = worktreeRelPath
		}
	}

	e.Context.Set(failureDossierContextPathKey, contextPath)
	e.Context.Set(failureDossierContextLogsPathKey, logsPath)
	e.Context.Set(failureDossierContextFailedNodeKey, dossier.FailedNodeID)
	e.Context.Set(failureDossierContextFailureClassKey, dossier.FailureClass)
	e.Context.Set(failureDossierContextSummaryKey, dossier.Summary)
	e.appendProgress(map[string]any{
		"event":          "failure_dossier_updated",
		"failed_node_id": dossier.FailedNodeID,
		"failure_class":  dossier.FailureClass,
		"path":           contextPath,
	})
}

func failureDossierRunScopedRelativePath(runID string) string {
	id := strings.TrimSpace(runID)
	if id == "" {
		id = "unknown_run"
	}
	return filepath.ToSlash(filepath.Join(".ai", "runs", id, failureDossierFileName))
}

func (e *Engine) clearFailureDossierContext() {
	if e == nil || e.Context == nil {
		return
	}
	e.Context.Set(failureDossierContextPathKey, "")
	e.Context.Set(failureDossierContextLogsPathKey, "")
	e.Context.Set(failureDossierContextFailedNodeKey, "")
	e.Context.Set(failureDossierContextFailureClassKey, "")
	e.Context.Set(failureDossierContextSummaryKey, "")
}

func (e *Engine) buildFailureDossier(node *model.Node, out runtime.Outcome, failureClass string, retries map[string]int) failureDossier {
	failedNode := ""
	handlerType := ""
	stageDir := ""
	attempts := 1
	maxAttempts := 1
	if node != nil {
		failedNode = strings.TrimSpace(node.ID)
		t := strings.TrimSpace(node.TypeOverride())
		if t == "" {
			t = shapeToType(node.Shape())
		}
		handlerType = t
		stageDir = filepath.Join(e.LogsRoot, node.ID)
		maxAttempts = maxAttemptsForNode(node, e.Graph)
		if retries != nil {
			if n, ok := retries[node.ID]; ok && n >= 0 {
				attempts = n + 1
			}
		}
	}
	if attempts < 1 {
		attempts = 1
	}
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	fc := normalizedFailureClassOrDefault(failureClass)
	dossier := failureDossier{
		Version:       1,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339Nano),
		FailedNodeID:  failedNode,
		HandlerType:   handlerType,
		Status:        strings.ToLower(strings.TrimSpace(string(out.Status))),
		FailureClass:  fc,
		FailureReason: strings.TrimSpace(out.FailureReason),
		AttemptsUsed:  attempts,
		MaxAttempts:   maxAttempts,
		StageDir:      stageDir,
	}

	dossier.Tool = buildFailureDossierTool(stageDir, out)
	searchText := collectFailureDossierSearchText(dossier, out)
	dossier.MissingPaths = e.extractMissingPaths(searchText, dossier.Tool)
	dossier.MissingExecutables = extractMissingExecutables(searchText)
	dossier.Summary = buildFailureDossierSummary(dossier)
	return dossier
}

func maxAttemptsForNode(node *model.Node, g *model.Graph) int {
	if node == nil {
		return 1
	}
	maxRetries := parseInt(node.Attr("max_retries", ""), -1)
	if maxRetries < 0 && g != nil {
		maxRetries = parseInt(g.Attrs["default_max_retry"], -1)
	}
	if maxRetries < 0 {
		maxRetries = 3
	}
	return maxRetries + 1
}

func buildFailureDossierTool(stageDir string, out runtime.Outcome) *failureDossierTool {
	if strings.TrimSpace(stageDir) == "" {
		return nil
	}
	ti := readToolInvocation(filepath.Join(stageDir, toolInvocationFileName))
	tt := readToolTiming(filepath.Join(stageDir, toolTimingFileName))
	stderr := readFileExcerpt(filepath.Join(stageDir, toolStderrFileName), 4000)

	if strings.TrimSpace(ti.Command) == "" && strings.TrimSpace(stderr) == "" {
		if v, ok := out.ContextUpdates["tool.output"]; ok {
			stderr = trimToRunes(strings.TrimSpace(fmt.Sprint(v)), 4000)
		}
	}

	if strings.TrimSpace(ti.Command) == "" && strings.TrimSpace(stderr) == "" && tt.ExitCode == 0 {
		return nil
	}
	return &failureDossierTool{
		Command:       strings.TrimSpace(ti.Command),
		WorkingDir:    strings.TrimSpace(ti.WorkingDir),
		ExitCode:      tt.ExitCode,
		StderrExcerpt: strings.TrimSpace(stderr),
	}
}

type toolInvocationRecord struct {
	Command    string `json:"command"`
	WorkingDir string `json:"working_dir"`
}

type toolTimingRecord struct {
	ExitCode int `json:"exit_code"`
}

func readToolInvocation(path string) toolInvocationRecord {
	var out toolInvocationRecord
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return out
	}
	_ = json.Unmarshal(b, &out)
	return out
}

func readToolTiming(path string) toolTimingRecord {
	var out toolTimingRecord
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return out
	}
	_ = json.Unmarshal(b, &out)
	return out
}

func readFileExcerpt(path string, maxRunes int) string {
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return ""
	}
	return trimToRunes(string(b), maxRunes)
}

func trimToRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= maxRunes {
		return string(r)
	}
	return string(r[:maxRunes])
}

func collectFailureDossierSearchText(d failureDossier, out runtime.Outcome) string {
	parts := []string{
		d.FailureReason,
	}
	if d.Tool != nil {
		parts = append(parts, d.Tool.StderrExcerpt, d.Tool.Command)
	}
	if out.ContextUpdates != nil {
		if v, ok := out.ContextUpdates["tool.output"]; ok {
			parts = append(parts, fmt.Sprint(v))
		}
	}
	return strings.Join(parts, "\n")
}

func (e *Engine) extractMissingPaths(text string, tool *failureDossierTool) []failureDossierPathFact {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	seen := map[string]struct{}{}
	var tokens []string
	for _, re := range toolMissingPathPatterns {
		matches := re.FindAllStringSubmatch(text, -1)
		for _, m := range matches {
			if len(m) < 2 {
				continue
			}
			token := strings.TrimSpace(strings.Trim(m[1], `"'`))
			if token == "" || token == "." {
				continue
			}
			if _, ok := seen[token]; ok {
				continue
			}
			seen[token] = struct{}{}
			tokens = append(tokens, token)
		}
	}
	if len(tokens) == 0 {
		return nil
	}

	facts := make([]failureDossierPathFact, 0, len(tokens))
	for _, token := range tokens {
		facts = append(facts, e.pathFact(token, tool))
	}
	slices.SortFunc(facts, func(a, b failureDossierPathFact) int {
		return strings.Compare(a.Path, b.Path)
	})
	return facts
}

func (e *Engine) pathFact(token string, tool *failureDossierTool) failureDossierPathFact {
	abs := resolveToolPath(token, e.WorktreeDir, tool)
	insideRepo := pathWithin(abs, e.WorktreeDir)
	existsNow := false
	if strings.TrimSpace(abs) != "" {
		if _, err := os.Stat(abs); err == nil {
			existsNow = true
		}
	}

	existsInSnapshot := false
	if insideRepo && strings.TrimSpace(e.LogsRoot) != "" && strings.TrimSpace(e.WorktreeDir) != "" {
		rel, err := filepath.Rel(e.WorktreeDir, abs)
		if err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != ".." {
			snapshotPath := filepath.Join(e.LogsRoot, "input_snapshot", "files", rel)
			if _, err := os.Stat(snapshotPath); err == nil {
				existsInSnapshot = true
			}
		}
	}

	return failureDossierPathFact{
		Path:                  token,
		AbsolutePath:          abs,
		InsideRepo:            insideRepo,
		ExistsNow:             existsNow,
		ExistsInInputSnapshot: existsInSnapshot,
	}
}

func resolveToolPath(token, worktreeDir string, tool *failureDossierTool) string {
	p := strings.TrimSpace(token)
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	base := strings.TrimSpace(worktreeDir)
	if tool != nil && strings.TrimSpace(tool.WorkingDir) != "" {
		base = strings.TrimSpace(tool.WorkingDir)
	}
	if base == "" {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(base, p))
}

func pathWithin(path, root string) bool {
	p := strings.TrimSpace(path)
	r := strings.TrimSpace(root)
	if p == "" || r == "" {
		return false
	}
	p = filepath.Clean(p)
	r = filepath.Clean(r)
	rel, err := filepath.Rel(r, p)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != "..")
}

func extractMissingExecutables(text string) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	matches := toolMissingExecutablePattern.FindAllStringSubmatch(text, -1)
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		name := strings.TrimSpace(strings.Trim(m[1], `"'`))
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

func buildFailureDossierSummary(d failureDossier) string {
	parts := []string{
		fmt.Sprintf("node=%s", d.FailedNodeID),
		fmt.Sprintf("class=%s", d.FailureClass),
		fmt.Sprintf("attempt=%d/%d", d.AttemptsUsed, d.MaxAttempts),
	}
	if len(d.MissingPaths) > 0 {
		parts = append(parts, fmt.Sprintf("missing_paths=%d", len(d.MissingPaths)))
	}
	if len(d.MissingExecutables) > 0 {
		parts = append(parts, fmt.Sprintf("missing_tools=%d", len(d.MissingExecutables)))
	}
	if strings.TrimSpace(d.FailureReason) != "" {
		parts = append(parts, fmt.Sprintf("reason=%s", trimToRunes(strings.TrimSpace(d.FailureReason), 120)))
	}
	return strings.Join(parts, "; ")
}
