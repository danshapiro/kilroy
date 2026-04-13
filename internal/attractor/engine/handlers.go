package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/danshapiro/kilroy/internal/attractor/browsergate"
	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

type Execution struct {
	Graph       *model.Graph
	Context     *runtime.Context
	LogsRoot    string
	WorktreeDir string
	Engine      *Engine
	Artifacts   *ArtifactStore // spec §5.5: per-run artifact store
}

type Handler interface {
	Execute(ctx context.Context, exec *Execution, node *model.Node) (runtime.Outcome, error)
}

// FidelityAwareHandler is an optional interface that handlers implement to
// declare they use fidelity/thread resolution (e.g., LLM session continuity).
// The engine resolves fidelity and thread keys only for handlers that
// implement this interface, avoiding hardcoded handler-type checks.
type FidelityAwareHandler interface {
	Handler
	UsesFidelity() bool
}

// SingleExecutionHandler is an optional interface that handlers implement to
// declare they should bypass retry logic (execute exactly once). Conditional
// pass-through nodes are the canonical example: retrying a routing point
// burns retry budget without useful work.
type SingleExecutionHandler interface {
	Handler
	SkipRetry() bool
}

// ProviderRequiringHandler is an optional interface that handlers implement
// to declare they require an LLM provider. The engine uses this during
// preflight to gather provider requirements instead of checking node shapes.
type ProviderRequiringHandler interface {
	Handler
	RequiresProvider() bool
}

type HandlerRegistry struct {
	handlers       map[string]Handler
	defaultHandler Handler
}

// NewCoreRegistry returns a registry with only Layer 0 (graph runner) handlers.
// Use this when composing layers explicitly via cmd/kilroy/ startup.
func NewCoreRegistry() *HandlerRegistry {
	reg := &HandlerRegistry{
		handlers: map[string]Handler{},
	}
	reg.Register("start", &StartHandler{})
	reg.Register("exit", &ExitHandler{})
	reg.Register("conditional", &ConditionalHandler{})
	reg.Register("parallel", &ParallelHandler{})
	reg.Register("parallel.fan_in", &FanInHandler{})
	reg.Register("tool", &ToolHandler{})
	reg.Register("loop.begin", &LoopBeginHandler{})
	reg.Register("loop.end", &LoopEndHandler{})
	reg.Register("concurrent.split", &ConcurrentSplitHandler{})
	reg.Register("concurrent.join", &ConcurrentJoinHandler{})
	return reg
}

// NewDefaultRegistry returns a registry with all built-in handlers registered.
// Retained for backward compatibility with tests and single-package usage.
func NewDefaultRegistry() *HandlerRegistry {
	reg := NewCoreRegistry()
	// wait.human is registered by cmd/kilroy/ from workflows/ (Layer 2).
	// Not included in core registry — human-in-the-loop is opt-in.
	reg.Register("stack.manager_loop", &ManagerLoopHandler{})
	reg.defaultHandler = &CodergenHandler{}
	reg.Register("agent", reg.defaultHandler)
	return reg
}

// SetDefault sets the handler used when no registered handler matches a node.
func (r *HandlerRegistry) SetDefault(h Handler) {
	r.defaultHandler = h
}

func (r *HandlerRegistry) Register(typeString string, h Handler) {
	if r.handlers == nil {
		r.handlers = map[string]Handler{}
	}
	r.handlers[typeString] = h
}

// KnownTypes returns the list of registered handler type strings.
// Used by the validate package's TypeKnownRule to check node type overrides.
func (r *HandlerRegistry) KnownTypes() []string {
	if r == nil || r.handlers == nil {
		return nil
	}
	types := make([]string, 0, len(r.handlers))
	for t := range r.handlers {
		types = append(types, t)
	}
	return types
}

func (r *HandlerRegistry) Resolve(n *model.Node) Handler {
	if n == nil {
		return r.defaultHandler
	}
	if t := strings.TrimSpace(n.TypeOverride()); t != "" {
		if h, ok := r.handlers[t]; ok {
			return h
		}
	}
	handlerType := shapeToType(n.Shape())
	if h, ok := r.handlers[handlerType]; ok {
		return h
	}
	return r.defaultHandler
}

func shapeToType(shape string) string {
	switch shape {
	case "Mdiamond", "circle":
		return "start"
	case "Msquare", "doublecircle":
		return "exit"
	case "box":
		return "agent"
	case "hexagon":
		return "wait.human"
	case "diamond":
		return "conditional"
	case "component":
		return "parallel"
	case "tripleoctagon":
		return "parallel.fan_in"
	case "parallelogram":
		return "tool"
	case "house":
		return "stack.manager_loop"
	case "trapezium":
		return "loop.begin"
	case "invtrapezium":
		return "loop.end"
	case "pentagon":
		return "concurrent.split"
	case "cylinder":
		return "concurrent.join"
	default:
		return "agent"
	}
}

type StartHandler struct{}

func (h *StartHandler) Execute(ctx context.Context, exec *Execution, node *model.Node) (runtime.Outcome, error) {
	return runtime.Outcome{Status: runtime.StatusSuccess, Notes: "start"}, nil
}

type ExitHandler struct{}

func (h *ExitHandler) Execute(ctx context.Context, exec *Execution, node *model.Node) (runtime.Outcome, error) {
	return runtime.Outcome{Status: runtime.StatusSuccess, Notes: "exit"}, nil
}

// LoopBeginHandler marks the entry of a multi-node loop scope. Pass-through —
// iteration state is tracked in engine context and managed by the engine's
// main loop when it reaches the paired LoopEnd node.
type LoopBeginHandler struct{}

// SkipRetry: loop_begin is a routing sentinel, not a work node; retrying it
// would burn retry budget for nothing.
func (h *LoopBeginHandler) SkipRetry() bool { return true }

func (h *LoopBeginHandler) Execute(ctx context.Context, exec *Execution, node *model.Node) (runtime.Outcome, error) {
	return runtime.Outcome{Status: runtime.StatusSuccess, Notes: "loop_begin"}, nil
}

// LoopEndHandler marks the exit of a multi-node loop scope. Pass-through —
// the engine's main loop inspects the node, evaluates termination conditions
// via shouldContinueLoop, and either follows the forward edge or jumps back
// to the paired loop_begin.
type LoopEndHandler struct{}

func (h *LoopEndHandler) SkipRetry() bool { return true }

func (h *LoopEndHandler) Execute(ctx context.Context, exec *Execution, node *model.Node) (runtime.Outcome, error) {
	return runtime.Outcome{Status: runtime.StatusSuccess, Notes: "loop_end"}, nil
}

// ConcurrentSplitHandler marks a fan-out point for concurrent execution.
// Pass-through — the engine's main loop detects the split, finds the paired
// concurrent.join, and dispatches each outgoing branch as a goroutine running
// in the shared workspace. No isolation, no winner selection; branches are
// expected to be independent.
type ConcurrentSplitHandler struct{}

func (h *ConcurrentSplitHandler) SkipRetry() bool { return true }

func (h *ConcurrentSplitHandler) Execute(ctx context.Context, exec *Execution, node *model.Node) (runtime.Outcome, error) {
	return runtime.Outcome{Status: runtime.StatusSuccess, Notes: "concurrent_split"}, nil
}

// ConcurrentJoinHandler marks the barrier where concurrent branches converge.
// Pass-through — the engine treats the join as the termination point for each
// branch goroutine. When all branches complete, the main loop resumes at the
// join and follows its outgoing edges normally.
type ConcurrentJoinHandler struct{}

func (h *ConcurrentJoinHandler) SkipRetry() bool { return true }

func (h *ConcurrentJoinHandler) Execute(ctx context.Context, exec *Execution, node *model.Node) (runtime.Outcome, error) {
	return runtime.Outcome{Status: runtime.StatusSuccess, Notes: "concurrent_join"}, nil
}

type ConditionalHandler struct{}

// SkipRetry implements SingleExecutionHandler. Conditional nodes are
// pass-through routing points — retrying them burns retry budget without
// useful work.
func (h *ConditionalHandler) SkipRetry() bool { return true }

func (h *ConditionalHandler) Execute(ctx context.Context, exec *Execution, node *model.Node) (runtime.Outcome, error) {
	_ = ctx
	_ = node

	// Spec: conditional nodes are pass-through routing points. They should not overwrite
	// the prior stage's outcome/preferred_label, since edge conditions frequently depend
	// on those values.
	prevStatus := runtime.StatusSuccess
	prevPreferred := ""
	prevFailure := ""
	prevFailureClass := ""
	if exec != nil && exec.Context != nil {
		if st, err := runtime.ParseStageStatus(exec.Context.GetString("outcome", "")); err == nil && st != "" {
			prevStatus = st
		}
		prevPreferred = exec.Context.GetString("preferred_label", "")
		prevFailure = exec.Context.GetString("failure_reason", "")
		prevFailureClass = exec.Context.GetString("failure_class", "")
	}
	var contextUpdates map[string]any
	if cls := strings.TrimSpace(prevFailureClass); cls != "" && cls != "<nil>" {
		contextUpdates = map[string]any{
			"failure_class": cls,
		}
	}

	return runtime.Outcome{
		Status:         prevStatus,
		PreferredLabel: prevPreferred,
		FailureReason:  prevFailure,
		Notes:          "conditional pass-through",
		ContextUpdates: contextUpdates,
	}, nil
}

type AgentBackend interface {
	Run(ctx context.Context, exec *Execution, node *model.Node, prompt string) (string, *runtime.Outcome, error)
}

type SimulatedAgentBackend struct{}

func (b *SimulatedAgentBackend) Run(ctx context.Context, exec *Execution, node *model.Node, prompt string) (string, *runtime.Outcome, error) {
	_ = ctx
	_ = exec
	_ = prompt
	out := runtime.Outcome{Status: runtime.StatusSuccess, Notes: "simulated agent completed"}
	return "[Simulated] Response for stage: " + node.ID, &out, nil
}

type CodergenHandler struct{}

// UsesFidelity implements FidelityAwareHandler. LLM nodes need fidelity/thread
// resolution for context management and session reuse.
func (h *CodergenHandler) UsesFidelity() bool { return true }

// RequiresProvider implements ProviderRequiringHandler. LLM nodes require an
// LLM provider to be configured.
func (h *CodergenHandler) RequiresProvider() bool { return true }

type StatusSource string

const (
	StatusSourceNone      StatusSource = ""
	StatusSourceCanonical StatusSource = "canonical"
	StatusSourceWorktree  StatusSource = "worktree"
	StatusSourceDotAI     StatusSource = "dot_ai"
)

type FallbackStatusPath struct {
	Path   string
	Source StatusSource
}

type fallbackStatusFailureMode string

const (
	fallbackFailureModeNone           fallbackStatusFailureMode = ""
	fallbackFailureModeMissing        fallbackStatusFailureMode = "missing"
	fallbackFailureModeUnreadable     fallbackStatusFailureMode = "unreadable"
	fallbackFailureModeCorrupt        fallbackStatusFailureMode = "corrupt"
	fallbackFailureModeInvalidPayload fallbackStatusFailureMode = "invalid_payload"
)

const (
	fallbackStatusDecodeMaxAttempts = 3
	fallbackStatusDecodeBaseDelay   = 25 * time.Millisecond
)

func CopyFirstValidFallbackStatus(stageStatusPath string, fallbackPaths []FallbackStatusPath) (StatusSource, string, error) {
	if _, err := os.Stat(stageStatusPath); err == nil {
		return StatusSourceCanonical, "", nil
	}
	issues := make([]string, 0, len(fallbackPaths))
	for _, fallback := range fallbackPaths {
		b, mode, err := readAndDecodeFallbackStatusWithRetry(fallback.Path)
		if err != nil {
			issues = append(issues, formatFallbackStatusIssue(fallback, mode, err))
			continue
		}
		if err := runtime.WriteFileAtomic(stageStatusPath, b); err != nil {
			return StatusSourceNone, strings.Join(issues, "; "), err
		}
		_ = os.Remove(fallback.Path)
		return fallback.Source, "", nil
	}
	return StatusSourceNone, strings.Join(issues, "; "), nil
}

func readAndDecodeFallbackStatusWithRetry(path string) ([]byte, fallbackStatusFailureMode, error) {
	var lastErr error
	mode := fallbackFailureModeMissing
	for attempt := 1; attempt <= fallbackStatusDecodeMaxAttempts; attempt++ {
		b, err := os.ReadFile(path)
		if err != nil {
			lastErr = err
			if os.IsNotExist(err) {
				mode = fallbackFailureModeMissing
			} else {
				mode = fallbackFailureModeUnreadable
			}
			if attempt < fallbackStatusDecodeMaxAttempts && shouldRetryFallbackRead(mode, err) {
				time.Sleep(backoffDelay(attempt))
				continue
			}
			return nil, mode, lastErr
		}
		if _, err := runtime.DecodeOutcomeJSON(b); err != nil {
			lastErr = err
			mode = classifyFallbackDecodeError(b, err)
			if attempt < fallbackStatusDecodeMaxAttempts && shouldRetryFallbackDecode(mode, b, err) {
				time.Sleep(backoffDelay(attempt))
				continue
			}
			return nil, mode, lastErr
		}
		return b, fallbackFailureModeNone, nil
	}
	return nil, mode, lastErr
}

func classifyFallbackDecodeError(raw []byte, err error) fallbackStatusFailureMode {
	if isCorruptFallbackPayload(raw, err) {
		return fallbackFailureModeCorrupt
	}
	return fallbackFailureModeInvalidPayload
}

func shouldRetryFallbackRead(mode fallbackStatusFailureMode, err error) bool {
	if mode == fallbackFailureModeMissing {
		// Missing fallback files are expected in normal flows; retrying them
		// just adds latency before trying the next candidate path.
		return false
	}
	return errors.Is(err, io.ErrUnexpectedEOF)
}

func shouldRetryFallbackDecode(mode fallbackStatusFailureMode, raw []byte, err error) bool {
	return mode == fallbackFailureModeCorrupt && isCorruptFallbackPayload(raw, err)
}

func isCorruptFallbackPayload(raw []byte, err error) bool {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return true
	}
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(msg, "unexpected end of json input") || strings.Contains(msg, "invalid character")
}

func backoffDelay(attempt int) time.Duration {
	if attempt <= 0 {
		attempt = 1
	}
	return time.Duration(attempt) * fallbackStatusDecodeBaseDelay
}

func formatFallbackStatusIssue(fallback FallbackStatusPath, mode fallbackStatusFailureMode, err error) string {
	switch mode {
	case fallbackFailureModeMissing:
		return fmt.Sprintf("fallback[%s] missing status artifact: %s", fallback.Source, fallback.Path)
	case fallbackFailureModeUnreadable:
		return fmt.Sprintf("fallback[%s] unreadable status artifact: %s (%v)", fallback.Source, fallback.Path, err)
	case fallbackFailureModeCorrupt:
		return fmt.Sprintf("fallback[%s] corrupt status artifact: %s (%v)", fallback.Source, fallback.Path, err)
	case fallbackFailureModeInvalidPayload:
		return fmt.Sprintf("fallback[%s] invalid status payload: %s (%v)", fallback.Source, fallback.Path, err)
	default:
		return fmt.Sprintf("fallback[%s] status ingestion error: %s (%v)", fallback.Source, fallback.Path, err)
	}
}

func BuildManualBoxFanInPromptPreamble(exec *Execution, node *model.Node) string {
	if exec == nil || exec.Context == nil || exec.Graph == nil || node == nil {
		return ""
	}
	joinNodeID := strings.TrimSpace(exec.Context.GetString("parallel.join_node", ""))
	if joinNodeID == "" || joinNodeID != strings.TrimSpace(node.ID) {
		return ""
	}
	mergeMode := strings.TrimSpace(exec.Context.GetString(parallelMergeModeContextKey, ""))
	if mergeMode == "" {
		mergeMode = classifyJoinMergeMode(exec.Graph, joinNodeID)
	}
	if mergeMode != parallelMergeModeManualBox {
		return ""
	}
	raw, ok := exec.Context.Get("parallel.results")
	if !ok || raw == nil {
		return ""
	}
	results, err := decodeParallelResults(raw)
	if err != nil || len(results) == 0 {
		return ""
	}
	currentWorktree := strings.TrimSpace(exec.WorktreeDir)
	var b strings.Builder
	b.WriteString("Manual parallel fan-in handoff:\n")
	b.WriteString("- This node is a convergence box. The engine does NOT auto-merge branch commits — you must manually merge all branch outputs into the current worktree.\n")
	if currentWorktree != "" {
		b.WriteString(fmt.Sprintf("- Current worktree (write your merged result here): %s\n", currentWorktree))
	}
	b.WriteString("- Branch outputs (merge ALL of these unless your task prompt specifies a different strategy):\n")
	for _, r := range results {
		b.WriteString(fmt.Sprintf("  - branch_key=%s status=%s head_sha=%s worktree_dir=%s logs_root=%s\n",
			strings.TrimSpace(r.BranchKey),
			strings.TrimSpace(string(r.Outcome.Status)),
			strings.TrimSpace(r.HeadSHA),
			strings.TrimSpace(r.WorktreeDir),
			strings.TrimSpace(r.LogsRoot),
		))
	}
	b.WriteString("- DEFAULT MERGE STRATEGY (use this unless your task prompt says otherwise):\n")
	b.WriteString("  1. For each branch above, run `git merge --no-ff <head_sha>` in the current worktree.\n")
	b.WriteString("  2. If the merge succeeds cleanly, continue to the next branch.\n")
	b.WriteString("  3. If there is a merge conflict, resolve it however you see fit, then `git add` the resolved\n")
	b.WriteString("     files and run `git merge --continue` (or `git commit`) to complete the merge.\n")
	b.WriteString("  4. Do NOT read files manually or copy them by hand unless git merge itself is unavailable.\n")
	b.WriteString("- Node run artifacts (response.md, status.json, etc.) are under <logs_root>/<node_id>/.\n")
	return strings.TrimSpace(b.String())
}

func (h *CodergenHandler) Execute(ctx context.Context, exec *Execution, node *model.Node) (runtime.Outcome, error) {
	stageDir := filepath.Join(exec.LogsRoot, node.ID)
	stageStatusPath := filepath.Join(stageDir, "status.json")
	contract := StageStatusContract{}
	if exec != nil {
		contract = BuildStageStatusContract(exec.WorktreeDir)
	}
	worktreeStatusPaths := contract.Fallbacks
	// Clear stale files from prior stages so we don't accidentally attribute them.
	for _, statusPath := range worktreeStatusPaths {
		_ = os.Remove(statusPath.Path)
	}

	basePrompt := strings.TrimSpace(node.Prompt())
	if basePrompt == "" {
		basePrompt = node.Label()
	}

	// Fidelity preamble (attractor-spec context fidelity): when fidelity is not `full`, synthesize
	// a context carryover preamble at execution time.
	fidelity := "compact"
	if exec != nil && exec.Engine != nil && strings.TrimSpace(exec.Engine.lastResolvedFidelity) != "" {
		fidelity = strings.TrimSpace(exec.Engine.lastResolvedFidelity)
	}
	promptText := basePrompt
	if fidelity != "full" {
		runID := ""
		if exec != nil && exec.Engine != nil {
			runID = exec.Engine.Options.RunID
		}
		goal := ""
		if exec != nil && exec.Context != nil {
			goal = exec.Context.GetString("graph.goal", "")
		}
		if strings.TrimSpace(goal) == "" && exec != nil && exec.Graph != nil {
			goal = exec.Graph.Attrs["goal"]
		}
		prevNode := ""
		if exec != nil && exec.Context != nil {
			prevNode = exec.Context.GetString("previous_node", "")
		}
		preamble := BuildFidelityPreamble(exec.Context, runID, goal, fidelity, prevNode, DecodeCompletedNodes(exec.Context))
		promptText = strings.TrimSpace(preamble) + "\n\n" + basePrompt
	}
	if preamble := strings.TrimSpace(contract.PromptPreamble); preamble != "" {
		if strings.TrimSpace(promptText) == "" {
			promptText = preamble
		} else {
			promptText = preamble + "\n\n" + strings.TrimSpace(promptText)
		}
	}
	if env := BuildStageRuntimeEnv(exec, node.ID); len(env) > 0 {
		if manifestPath := strings.TrimSpace(env[inputsManifestEnvKey]); manifestPath != "" {
			preamble := strings.TrimSpace(MustRenderInputMaterializationPromptPreamble(manifestPath))
			if preamble != "" {
				if strings.TrimSpace(promptText) == "" {
					promptText = preamble
				} else {
					promptText = preamble + "\n\n" + strings.TrimSpace(promptText)
				}
			}
		}
	}
	if exec != nil && exec.Context != nil {
		dossierPath := strings.TrimSpace(exec.Context.GetString(failureDossierContextPathKey, ""))
		if dossierPath != "" {
			logsPath := strings.TrimSpace(exec.Context.GetString(failureDossierContextLogsPathKey, ""))
			if logsPath == "" {
				logsPath = dossierPath
			}
			preamble := strings.TrimSpace(MustRenderFailureDossierPromptPreamble(dossierPath, logsPath))
			if preamble != "" {
				if strings.TrimSpace(promptText) == "" {
					promptText = preamble
				} else {
					promptText = preamble + "\n\n" + strings.TrimSpace(promptText)
				}
			}
		}
	}
	if preamble := strings.TrimSpace(BuildManualBoxFanInPromptPreamble(exec, node)); preamble != "" {
		if strings.TrimSpace(promptText) == "" {
			promptText = preamble
		} else {
			promptText = preamble + "\n\n" + strings.TrimSpace(promptText)
		}
		if exec != nil && exec.Engine != nil {
			exec.Engine.appendProgress(map[string]any{
				"event":   "manual_box_fan_in_handoff",
				"node_id": node.ID,
			})
		}
		// Copy git-ignored files from each branch worktree into the run
		// worktree so the merging agent has access to .env / secrets /
		// build artifacts produced by branch workers.
		if exec != nil && exec.Engine != nil && exec.Engine.GitOps != nil {
			if raw, ok := exec.Context.Get("parallel.results"); ok && raw != nil {
				if brResults, err := decodeParallelResults(raw); err == nil {
					for _, br := range brResults {
						if strings.TrimSpace(br.WorktreeDir) == "" {
							continue
						}
						if cerr := exec.Engine.GitOps.CopyIgnoredFiles(br.WorktreeDir, exec.WorktreeDir, ".ai/runs/"); cerr != nil {
							exec.Engine.appendProgress(map[string]any{
								"event":      "manual_box_fan_in_ignored_files_warning",
								"node_id":    node.ID,
								"branch_key": br.BranchKey,
								"warning":    cerr.Error(),
							})
						}
					}
				}
			}
		}
	}
	if exec != nil && exec.Engine != nil && strings.TrimSpace(contract.PrimaryPath) != "" {
		exec.Engine.appendProgress(map[string]any{
			"event":                "status_contract",
			"node_id":              node.ID,
			"status_path":          contract.PrimaryPath,
			"status_fallback_path": contract.FallbackPath,
		})
	}

	if err := os.WriteFile(filepath.Join(stageDir, "prompt.md"), []byte(promptText), 0o644); err != nil {
		return runtime.Outcome{Status: runtime.StatusFail, FailureReason: err.Error()}, err
	}
	if exec.Engine != nil {
		exec.Engine.cxdbPrompt(ctx, node.ID, promptText)
	}

	backend := exec.Engine.AgentBackend
	if backend == nil {
		backend = &SimulatedAgentBackend{}
	}
	resp, out, err := backend.Run(ctx, exec, node, promptText)
	if err != nil {
		fc, sig := ClassifyAPIError(err)
		// Spec §4.5: set semantically correct status based on failure classification.
		// Deterministic errors (auth, bad request, etc.) are FAIL — retrying won't help.
		// Transient errors (rate limits, timeouts, server errors) are RETRY — worth retrying.
		status := runtime.StatusFail
		if fc == failureClassTransientInfra {
			status = runtime.StatusRetry
		}
		return runtime.Outcome{
			Status:         status,
			FailureReason:  err.Error(),
			Meta:           map[string]any{"failure_class": fc, "failure_signature": sig},
			ContextUpdates: map[string]any{"failure_class": fc},
		}, nil
	}
	if strings.TrimSpace(resp) != "" {
		_ = os.WriteFile(filepath.Join(stageDir, "response.md"), []byte(resp), 0o644)
	}

	// If the backend/agent wrote a worktree status.json, surface it to the engine by
	// copying it into the authoritative stage directory location.
	source := StatusSourceNone
	ingestionDiagnostic := ""
	if len(worktreeStatusPaths) > 0 {
		var err error
		source, ingestionDiagnostic, err = CopyFirstValidFallbackStatus(stageStatusPath, worktreeStatusPaths)
		if err != nil {
			reason := err.Error()
			if strings.TrimSpace(ingestionDiagnostic) != "" {
				reason = reason + "; " + strings.TrimSpace(ingestionDiagnostic)
			}
			return runtime.Outcome{Status: runtime.StatusFail, FailureReason: reason}, nil
		}
	}
	if exec != nil && exec.Engine != nil {
		progress := map[string]any{
			"event":   "status_ingestion_decision",
			"node_id": node.ID,
			"source":  string(source),
			"copied":  source == StatusSourceWorktree || source == StatusSourceDotAI,
		}
		if strings.TrimSpace(ingestionDiagnostic) != "" {
			progress["diagnostic"] = strings.TrimSpace(ingestionDiagnostic)
		}
		exec.Engine.appendProgress(progress)
	}

	if out != nil {
		// Spec §5.1: always set last_stage/last_response on handler completion.
		if out.ContextUpdates == nil {
			out.ContextUpdates = map[string]any{}
		}
		if _, ok := out.ContextUpdates["last_stage"]; !ok {
			out.ContextUpdates["last_stage"] = node.ID
		}
		if _, ok := out.ContextUpdates["last_response"]; !ok {
			out.ContextUpdates["last_response"] = Truncate(resp, 200)
		}
		return *out, nil
	}

	// If the backend didn't return an explicit outcome, require a status.json signal unless
	// auto_status is explicitly enabled.
	if _, err := os.Stat(stageStatusPath); err == nil {
		// The engine will parse the status.json after the handler returns.
		// Spec §5.1: always set last_stage/last_response on handler completion.
		return runtime.Outcome{
			Status: runtime.StatusSuccess,
			Notes:  "agent completed (status.json written)",
			ContextUpdates: map[string]any{
				"last_stage":    node.ID,
				"last_response": Truncate(resp, 200),
			},
		}, nil
	}
	autoStatus := strings.EqualFold(node.Attr("auto_status", "false"), "true")
	if autoStatus {
		return runtime.Outcome{
			Status: runtime.StatusSuccess,
			Notes:  "auto-status: handler completed without writing status",
			ContextUpdates: map[string]any{
				"last_stage":    node.ID,
				"last_response": Truncate(resp, 200),
			},
		}, nil
	}
	reason := "missing status.json (auto_status=false)"
	if strings.TrimSpace(ingestionDiagnostic) != "" {
		reason = reason + "; " + strings.TrimSpace(ingestionDiagnostic)
	}
	return runtime.Outcome{
		Status:        runtime.StatusFail,
		FailureReason: reason,
		Notes:         "agent completed without an outcome or status.json",
		ContextUpdates: map[string]any{
			"last_stage":    node.ID,
			"last_response": Truncate(resp, 200),
		},
	}, nil
}

type WaitHumanHandler struct{}

func (h *WaitHumanHandler) Execute(ctx context.Context, exec *Execution, node *model.Node) (runtime.Outcome, error) {
	edges := exec.Graph.Outgoing(node.ID)
	if len(edges) == 0 {
		return runtime.Outcome{Status: runtime.StatusFail, FailureReason: "no outgoing edges for human gate"}, nil
	}

	options := make([]Option, 0, len(edges))
	used := map[string]bool{}
	for i, e := range edges {
		if e == nil {
			continue
		}
		label := strings.TrimSpace(e.Label())
		if label == "" {
			label = e.To
		}
		key := AcceleratorKey(label)
		if key == "" || used[key] {
			// Provide a stable fallback key when accelerator extraction is ambiguous.
			key = fmt.Sprintf("%d", i+1)
		}
		used[key] = true
		options = append(options, Option{
			Key:   key,
			Label: label,
			To:    e.To,
		})
	}

	q := Question{
		Type:    QuestionSingleSelect,
		Text:    node.Attr("question", node.Label()),
		Options: options,
		Stage:   node.ID,
	}
	interviewer := exec.Engine.Interviewer
	if interviewer == nil {
		interviewer = &AutoApproveInterviewer{}
	}
	// Spec §9.6: emit InterviewStarted CXDB event.
	interviewStart := time.Now()
	exec.Engine.cxdbInterviewStarted(ctx, node.ID, q.Text, string(q.Type))

	ans := interviewer.Ask(q)
	interviewDurationMS := time.Since(interviewStart).Milliseconds()

	if ans.TimedOut {
		// Spec §9.6: emit InterviewTimeout CXDB event.
		exec.Engine.cxdbInterviewTimeout(ctx, node.ID, q.Text, interviewDurationMS)
		// §4.6: On timeout, check for a default choice before returning RETRY.
		if dc := strings.TrimSpace(node.Attr("human.default_choice", "")); dc != "" {
			for _, o := range options {
				if strings.EqualFold(o.Key, dc) || strings.EqualFold(o.To, dc) {
					return runtime.Outcome{
						Status:           runtime.StatusSuccess,
						SuggestedNextIDs: []string{o.To},
						PreferredLabel:   o.Label,
						ContextUpdates: map[string]any{
							"human.gate.selected": o.To,
							"human.gate.label":    o.Label,
						},
						Notes: "human gate timeout, used default choice",
					}, nil
				}
			}
		}
		return runtime.Outcome{Status: runtime.StatusRetry, FailureReason: "human gate timeout, no default"}, nil
	}
	if ans.Skipped {
		return runtime.Outcome{Status: runtime.StatusFail, FailureReason: "human gate skipped interaction"}, nil
	}

	selected := options[0]
	if want := strings.TrimSpace(ans.Value); want != "" {
		for _, o := range options {
			if strings.EqualFold(o.Key, want) || strings.EqualFold(o.To, want) {
				selected = o
				break
			}
		}
	}

	// Spec §9.6: emit InterviewCompleted CXDB event.
	exec.Engine.cxdbInterviewCompleted(ctx, node.ID, ans.Value, interviewDurationMS)

	return runtime.Outcome{
		Status:           runtime.StatusSuccess,
		SuggestedNextIDs: []string{selected.To},
		PreferredLabel:   selected.Label,
		ContextUpdates: map[string]any{
			"human.gate.selected": selected.To,
			"human.gate.label":    selected.Label,
		},
		Notes: "human gate selected",
	}, nil
}

var toolCommandAbsPathRE = regexp.MustCompile(`cd\s+/`)

type ToolHandler struct{}

var (
	snapshotBrowserArtifactsFunc = snapshotBrowserArtifacts
	collectBrowserArtifactsFunc  = collectBrowserArtifacts
)

var toolActionableLineHints = []string{
	"error",
	"fail",
	"failed",
	"failure",
	"exception",
	"panic",
	"fatal",
	"timeout",
	"timed out",
	"net::",
	"err_",
	"missing",
	"not found",
	"cannot",
	"can't",
	"refused",
	"disconnected",
	"assert",
}

func (h *ToolHandler) Execute(ctx context.Context, execCtx *Execution, node *model.Node) (runtime.Outcome, error) {
	stageDir := filepath.Join(execCtx.LogsRoot, node.ID)
	cmdStr := strings.TrimSpace(node.Attr("tool_command", ""))
	if cmdStr == "" {
		return runtime.Outcome{Status: runtime.StatusFail, FailureReason: "no tool_command specified"}, nil
	}
	if toolCommandAbsPathRE.MatchString(cmdStr) {
		WarnEngine(execCtx, fmt.Sprintf("tool_command for node %q contains 'cd /…' which overrides worktree CWD %q", node.ID, execCtx.WorktreeDir))
	}
	timeout := parseDuration(node.Attr("timeout", ""), 0)
	if timeout <= 0 {
		timeout = 600 * time.Second
	}
	isBrowserVerifyNode := browsergate.IsBrowserVerificationNode(cmdStr, node.ID, node.Label(), node.Attrs)
	var baseline map[string]artifactFingerprint
	var startedAt time.Time
	if isBrowserVerifyNode {
		baselineRun, err := snapshotBrowserArtifactsFunc(execCtx.WorktreeDir)
		if err != nil {
			WarnEngine(execCtx, fmt.Sprintf("snapshot browser artifacts: %v", err))
		} else {
			baseline = baselineRun
		}
		startedAt = time.Now()
	}

	callID := ulid.Make().String()
	if execCtx != nil && execCtx.Engine != nil && execCtx.Engine.CXDB != nil {
		argsJSON, _ := json.Marshal(map[string]any{
			"command": cmdStr,
			"timeout": timeout.String(),
		})
		if _, _, err := execCtx.Engine.CXDB.Append(ctx, "com.kilroy.attractor.ToolCall", 1, map[string]any{
			"run_id":         execCtx.Engine.Options.RunID,
			"node_id":        node.ID,
			"tool_name":      "shell",
			"call_id":        callID,
			"arguments_json": string(argsJSON),
		}); err != nil {
			execCtx.Engine.Warn(fmt.Sprintf("cxdb append ToolCall failed (node=%s call_id=%s): %v", node.ID, callID, err))
		}
	}

	shellPath := resolveToolShellPath()
	if err := writeJSON(filepath.Join(stageDir, toolInvocationFileName), map[string]any{
		"tool": filepath.Base(shellPath),
		// Use a non-login, non-interactive shell to avoid sourcing user dotfiles.
		"argv":        []string{shellPath, "-c", cmdStr},
		"command":     cmdStr,
		"working_dir": execCtx.WorktreeDir,
		"timeout_ms":  timeout.Milliseconds(),
		"env_mode":    "base",
	}); err != nil {
		WarnEngine(execCtx, fmt.Sprintf("write tool_invocation.json: %v", err))
	}

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, shellPath, "-c", cmdStr)
	cmd.Dir = execCtx.WorktreeDir
	cmd.Env = mergeEnvWithOverrides(
		buildBaseNodeEnv(artifactPolicyFromExecution(execCtx)),
		BuildStageRuntimeEnv(execCtx, node.ID),
	)
	// Avoid hanging on interactive reads; tool_command doesn't provide a way to supply stdin.
	cmd.Stdin = strings.NewReader("")
	stdoutPath := filepath.Join(stageDir, "stdout.log")
	stderrPath := filepath.Join(stageDir, toolStderrFileName)
	stdoutFile, err := os.Create(stdoutPath)
	if err != nil {
		return runtime.Outcome{Status: runtime.StatusFail, FailureReason: err.Error()}, nil
	}
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		_ = stdoutFile.Close()
		return runtime.Outcome{Status: runtime.StatusFail, FailureReason: err.Error()}, nil
	}
	defer func() { _ = stdoutFile.Close(); _ = stderrFile.Close() }()

	// Tee stdout/stderr through RunLog for line-by-line streaming.
	var rl *RunLog
	if execCtx != nil && execCtx.Engine != nil {
		rl = execCtx.Engine.RunLog
	}
	stdoutWriter := NewLineWriter(stdoutFile, rl, node.ID, "stdout")
	stderrWriter := NewLineWriter(stderrFile, rl, node.ID, "stderr")
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter

	commandStart := time.Now()
	runErr := cmd.Run()
	stdoutWriter.Flush()
	stderrWriter.Flush()
	dur := time.Since(commandStart)
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	if rl != nil {
		rl.Info("tool", node.ID, "exit", fmt.Sprintf("exit %d (%dms)", exitCode, dur.Milliseconds()), map[string]any{
			"exit_code":   exitCode,
			"duration_ms": dur.Milliseconds(),
		})
	}
	if cctx.Err() == context.DeadlineExceeded {
		if err := writeJSON(filepath.Join(stageDir, toolTimingFileName), map[string]any{
			"duration_ms": dur.Milliseconds(),
			"exit_code":   exitCode,
			"timed_out":   true,
		}); err != nil {
			WarnEngine(execCtx, fmt.Sprintf("write tool_timing.json: %v", err))
		}
		_ = writeDiffPatch(stageDir, execCtx.WorktreeDir)
		emitBrowserArtifactCollection(execCtx, node, stageDir, isBrowserVerifyNode, baseline, startedAt)
		return runtime.Outcome{
			Status:        runtime.StatusFail,
			FailureReason: fmt.Sprintf("tool_command timed out after %s", timeout),
		}, nil
	}

	if err := writeJSON(filepath.Join(stageDir, toolTimingFileName), map[string]any{
		"duration_ms": dur.Milliseconds(),
		"exit_code":   exitCode,
		"timed_out":   false,
	}); err != nil {
		WarnEngine(execCtx, fmt.Sprintf("write tool_timing.json: %v", err))
	}

	// Capture diff for debug-by-default. This is stable because we checkpoint after each node.
	_ = writeDiffPatch(stageDir, execCtx.WorktreeDir)

	stdoutBytes, rerr := os.ReadFile(stdoutPath)
	if rerr != nil {
		WarnEngine(execCtx, fmt.Sprintf("read stdout.log: %v", rerr))
	}
	stderrBytes, rerr := os.ReadFile(stderrPath)
	if rerr != nil {
		WarnEngine(execCtx, fmt.Sprintf("read stderr.log: %v", rerr))
	}
	emitBrowserArtifactCollection(execCtx, node, stageDir, isBrowserVerifyNode, baseline, startedAt)

	combined := append(append([]byte{}, stdoutBytes...), stderrBytes...)
	combinedStr := string(combined)
	if runErr != nil {
		rawExitStatus := strings.TrimSpace(runErr.Error())
		failureReason := rawExitStatus
		if isBrowserVerifyNode {
			if line := firstActionableToolOutputLine(stderrBytes); line != "" {
				failureReason = line
			} else if line := firstActionableToolOutputLine(stdoutBytes); line != "" {
				failureReason = line
			}
		}
		if failureReason == "" {
			failureReason = "tool command failed"
		}
		if hint := worktreeNotFoundHint(stderrBytes, cmdStr, execCtx); hint != "" {
			failureReason += "\n  hint: " + hint
		}
		if execCtx != nil && execCtx.Engine != nil && execCtx.Engine.CXDB != nil {
			if _, _, err := execCtx.Engine.CXDB.Append(ctx, "com.kilroy.attractor.ToolResult", 1, map[string]any{
				"run_id":    execCtx.Engine.Options.RunID,
				"node_id":   node.ID,
				"tool_name": "shell",
				"call_id":   callID,
				"output":    Truncate(combinedStr, 8_000),
				"is_error":  true,
			}); err != nil {
				execCtx.Engine.Warn(fmt.Sprintf("cxdb append ToolResult failed (node=%s call_id=%s): %v", node.ID, callID, err))
			}
		}
		return runtime.Outcome{
			Status:        runtime.StatusFail,
			FailureReason: failureReason,
			ContextUpdates: map[string]any{
				"tool.output":      Truncate(combinedStr, 8_000),
				"tool.exit_status": rawExitStatus,
			},
		}, nil
	}
	if execCtx != nil && execCtx.Engine != nil && execCtx.Engine.CXDB != nil {
		if _, _, err := execCtx.Engine.CXDB.Append(ctx, "com.kilroy.attractor.ToolResult", 1, map[string]any{
			"run_id":    execCtx.Engine.Options.RunID,
			"node_id":   node.ID,
			"tool_name": "shell",
			"call_id":   callID,
			"output":    Truncate(combinedStr, 8_000),
			"is_error":  false,
		}); err != nil {
			execCtx.Engine.Warn(fmt.Sprintf("cxdb append ToolResult failed (node=%s call_id=%s): %v", node.ID, callID, err))
		}
	}
	return runtime.Outcome{
		Status: runtime.StatusSuccess,
		ContextUpdates: map[string]any{
			"tool.output": Truncate(combinedStr, 8_000),
		},
		Notes: "tool completed",
	}, nil
}

func emitBrowserArtifactCollection(execCtx *Execution, node *model.Node, stageDir string, isBrowserVerifyNode bool, baseline map[string]artifactFingerprint, startedAt time.Time) {
	if !isBrowserVerifyNode {
		return
	}
	summary, err := collectBrowserArtifactsFunc(stageDir, execCtx.WorktreeDir, baseline, startedAt)
	if err != nil {
		WarnEngine(execCtx, fmt.Sprintf("collect browser artifacts: %v", err))
	}

	if execCtx == nil || execCtx.Engine == nil {
		return
	}
	event := map[string]any{
		"event":   "tool_browser_artifacts",
		"node_id": node.ID,
	}
	for k, v := range summary.toProgressFields() {
		event[k] = v
	}
	if err != nil {
		event["collection_error"] = err.Error()
	}
	execCtx.Engine.appendProgress(event)
}

func firstActionableToolOutputLine(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	firstNonEmpty := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if firstNonEmpty == "" {
			firstNonEmpty = trimToRunes(trimmed, 4000)
		}
		if looksActionableToolOutputLine(trimmed) {
			return trimToRunes(trimmed, 4000)
		}
	}
	return firstNonEmpty
}

func looksActionableToolOutputLine(line string) bool {
	line = strings.ToLower(strings.TrimSpace(line))
	if line == "" {
		return false
	}
	for _, hint := range toolActionableLineHints {
		if strings.Contains(line, hint) {
			return true
		}
	}
	return false
}

// worktreeNotFoundHint returns a hint when a tool command fails because a
// referenced file doesn't exist in the worktree but does exist in the source
// repo. Returns "" when no hint applies.
func worktreeNotFoundHint(stderr []byte, cmdStr string, execCtx *Execution) string {
	stderrStr := strings.ToLower(string(stderr))
	if !strings.Contains(stderrStr, "not found") && !strings.Contains(stderrStr, "no such file") {
		return ""
	}
	repoPath := ""
	if execCtx != nil && execCtx.Engine != nil {
		repoPath = execCtx.Engine.Options.RepoPath
	}

	// Try to extract a script path from the command (first token of the command).
	scriptPath := extractLeadingPath(cmdStr)
	if scriptPath == "" {
		return "file not found — if this script is not committed to git, run 'git add <file> && git commit' in the source repo"
	}

	worktreeDir := ""
	if execCtx != nil {
		worktreeDir = execCtx.WorktreeDir
	}

	// Check if the file exists in the source repo but not in the worktree.
	inWorktree := worktreeDir != "" && pathExists(filepath.Join(worktreeDir, scriptPath))
	inRepo := repoPath != "" && pathExists(filepath.Join(repoPath, scriptPath))
	if !inWorktree && inRepo {
		return fmt.Sprintf("file %q exists in the source repo but not in the worktree — it may not be committed to git; run 'git add %s && git commit'", scriptPath, scriptPath)
	}
	if !inWorktree && !inRepo {
		return fmt.Sprintf("file %q not found in worktree or source repo — check the path in tool_command", scriptPath)
	}
	return ""
}

// extractLeadingPath pulls the first token from a shell command, stripping
// common prefixes like "bash", "sh", "./" etc. Returns "" if no plausible
// file path is found.
func extractLeadingPath(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	// Strip leading "bash -c", "sh -c", etc.
	for _, prefix := range []string{"bash -c ", "sh -c "} {
		if strings.HasPrefix(cmd, prefix) {
			cmd = strings.TrimSpace(cmd[len(prefix):])
			// Remove surrounding quotes from the remaining command.
			if len(cmd) >= 2 && (cmd[0] == '\'' || cmd[0] == '"') && cmd[len(cmd)-1] == cmd[0] {
				cmd = cmd[1 : len(cmd)-1]
			}
			break
		}
	}
	// Take the first whitespace-delimited token.
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return ""
	}
	candidate := fields[0]
	// Skip if it looks like a bare command (no path separator and no extension).
	if !strings.Contains(candidate, "/") && !strings.Contains(candidate, ".") {
		return ""
	}
	return candidate
}

func resolveToolShellPath() string {
	return resolveToolShellPathWith(goruntime.GOOS, exec.LookPath, pathExists)
}

func resolveToolShellPathWith(goos string, lookPath func(string) (string, error), exists func(string) bool) string {
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if exists == nil {
		exists = pathExists
	}
	if path, err := lookPath("bash"); err == nil && strings.TrimSpace(path) != "" {
		if strings.EqualFold(goos, "windows") && isWindowsBashShim(path) {
			if preferred := preferredWindowsBashPath(lookPath, exists); preferred != "" {
				return preferred
			}
		}
		return path
	}
	if strings.EqualFold(goos, "windows") {
		if preferred := preferredWindowsBashPath(lookPath, exists); preferred != "" {
			return preferred
		}
	}
	return "bash"
}

func preferredWindowsBashPath(lookPath func(string) (string, error), exists func(string) bool) string {
	candidates := []string{
		filepath.Clean(`C:\Program Files\Git\bin\bash.exe`),
		filepath.Clean(`C:\Program Files\Git\usr\bin\bash.exe`),
	}
	if lookPath != nil {
		if gitPath, err := lookPath("git"); err == nil && strings.TrimSpace(gitPath) != "" {
			gitDir := filepath.Dir(gitPath)
			candidates = append(candidates,
				filepath.Clean(filepath.Join(gitDir, "bash.exe")),
				filepath.Clean(filepath.Join(gitDir, "..", "usr", "bin", "bash.exe")),
			)
		}
	}
	for _, candidate := range candidates {
		if exists != nil && exists(candidate) {
			return candidate
		}
	}
	return ""
}

func isWindowsBashShim(path string) bool {
	clean := strings.ToLower(filepath.Clean(strings.TrimSpace(path)))
	return strings.HasSuffix(clean, `\windows\system32\bash.exe`) || strings.HasSuffix(clean, `\windows\sysnative\bash.exe`)
}

func pathExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func Truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n]
}

func writeDiffPatch(stageDir string, worktreeDir string) error {
	// Best-effort debug artifact: never block the run on diff generation.
	cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", "diff", "--patch")
	cmd.Dir = worktreeDir
	cmd.Stdin = strings.NewReader("")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	_ = cmd.Run()
	if cctx.Err() == context.DeadlineExceeded {
		return nil
	}
	if buf.Len() == 0 {
		return nil
	}
	return os.WriteFile(filepath.Join(stageDir, "diff.patch"), buf.Bytes(), 0o644)
}

func parseDuration(s string, def time.Duration) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	// DOT durations are like "900s", "15m", "250ms", "2h", "1d".
	// Support 'd' as 24h.
	if strings.HasSuffix(s, "d") {
		base, ok := parseIntPrefix(strings.TrimSuffix(s, "d"))
		if ok {
			return time.Duration(base) * 24 * time.Hour
		}
	}
	// Common shorthand in DOT specs: bare integers mean seconds.
	if base, ok := parseIntPrefix(s); ok {
		return time.Duration(base) * time.Second
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return def
	}
	return d
}

func parseIntPrefix(s string) (int, bool) {
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	if err != nil {
		return 0, false
	}
	return n, true
}

type Interviewer interface {
	Ask(question Question) Answer
	AskMultiple(questions []Question) []Answer
	Inform(message string, stage string)
}

type QuestionType string

const (
	QuestionSingleSelect QuestionType = "SINGLE_SELECT"
	QuestionMultiSelect  QuestionType = "MULTI_SELECT"
	QuestionFreeText     QuestionType = "FREE_TEXT"
	QuestionConfirm      QuestionType = "CONFIRM"
	QuestionYesNo        QuestionType = "YES_NO" // binary yes/no; semantically distinct from CONFIRM
)

type Question struct {
	Type           QuestionType
	Text           string
	Options        []Option
	Default        *Answer // default answer if timeout/skip (nil = no default)
	TimeoutSeconds float64 // max wait time; 0 means no timeout
	Stage          string
	Metadata       map[string]any // arbitrary key-value pairs for frontend use
}

type Option struct {
	Key   string
	Label string
	To    string
}

type Answer struct {
	Value          string
	Values         []string
	SelectedOption *Option // the full selected option (for SINGLE_SELECT); nil if not applicable
	Text           string
	TimedOut       bool
	Skipped        bool
}

type AutoApproveInterviewer struct{}

func (i *AutoApproveInterviewer) Ask(q Question) Answer {
	if len(q.Options) > 0 {
		return Answer{Value: q.Options[0].Key}
	}
	return Answer{Value: "YES"}
}

func (i *AutoApproveInterviewer) AskMultiple(questions []Question) []Answer {
	answers := make([]Answer, len(questions))
	for idx, q := range questions {
		answers[idx] = i.Ask(q)
	}
	return answers
}

func (i *AutoApproveInterviewer) Inform(message string, stage string) {
	// No-op for auto-approve.
}
