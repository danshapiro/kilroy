package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

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

func NewDefaultRegistry() *HandlerRegistry {
	reg := &HandlerRegistry{
		handlers: map[string]Handler{},
	}
	// Built-in handlers.
	reg.Register("start", &StartHandler{})
	reg.Register("exit", &ExitHandler{})
	reg.Register("conditional", &ConditionalHandler{})
	reg.Register("wait.human", &WaitHumanHandler{})
	reg.Register("parallel", &ParallelHandler{})
	reg.Register("parallel.fan_in", &FanInHandler{})
	reg.Register("tool", &ToolHandler{})
	reg.Register("stack.manager_loop", &ManagerLoopHandler{})
	reg.defaultHandler = &CodergenHandler{}
	reg.Register("codergen", reg.defaultHandler)
	return reg
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
		return "codergen"
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
	default:
		return "codergen"
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

type CodergenBackend interface {
	Run(ctx context.Context, exec *Execution, node *model.Node, prompt string) (string, *runtime.Outcome, error)
}

type SimulatedCodergenBackend struct{}

func (b *SimulatedCodergenBackend) Run(ctx context.Context, exec *Execution, node *model.Node, prompt string) (string, *runtime.Outcome, error) {
	_ = ctx
	_ = exec
	_ = prompt
	out := runtime.Outcome{Status: runtime.StatusSuccess, Notes: "simulated codergen completed"}
	return "[Simulated] Response for stage: " + node.ID, &out, nil
}

type CodergenHandler struct{}

// UsesFidelity implements FidelityAwareHandler. LLM nodes need fidelity/thread
// resolution for context management and session reuse.
func (h *CodergenHandler) UsesFidelity() bool { return true }

// RequiresProvider implements ProviderRequiringHandler. LLM nodes require an
// LLM provider to be configured.
func (h *CodergenHandler) RequiresProvider() bool { return true }

type statusSource string

const (
	statusSourceNone      statusSource = ""
	statusSourceCanonical statusSource = "canonical"
	statusSourceWorktree  statusSource = "worktree"
	statusSourceDotAI     statusSource = "dot_ai"
)

type fallbackStatusPath struct {
	path   string
	source statusSource
}

func copyFirstValidFallbackStatus(stageStatusPath string, fallbackPaths []fallbackStatusPath) (statusSource, error) {
	if _, err := os.Stat(stageStatusPath); err == nil {
		return statusSourceCanonical, nil
	}
	for _, fallback := range fallbackPaths {
		b, err := os.ReadFile(fallback.path)
		if err != nil {
			continue
		}
		if _, err := runtime.DecodeOutcomeJSON(b); err != nil {
			continue
		}
		if err := runtime.WriteFileAtomic(stageStatusPath, b); err != nil {
			return statusSourceNone, err
		}
		_ = os.Remove(fallback.path)
		return fallback.source, nil
	}
	return statusSourceNone, nil
}

func (h *CodergenHandler) Execute(ctx context.Context, exec *Execution, node *model.Node) (runtime.Outcome, error) {
	stageDir := filepath.Join(exec.LogsRoot, node.ID)
	stageStatusPath := filepath.Join(stageDir, "status.json")
	contract := stageStatusContract{}
	if exec != nil {
		contract = buildStageStatusContract(exec.WorktreeDir)
	}
	worktreeStatusPaths := contract.Fallbacks
	// Clear stale files from prior stages so we don't accidentally attribute them.
	for _, statusPath := range worktreeStatusPaths {
		_ = os.Remove(statusPath.path)
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
		preamble := buildFidelityPreamble(exec.Context, runID, goal, fidelity, prevNode, decodeCompletedNodes(exec.Context))
		promptText = strings.TrimSpace(preamble) + "\n\n" + basePrompt
	}
	if preamble := strings.TrimSpace(contract.PromptPreamble); preamble != "" {
		if strings.TrimSpace(promptText) == "" {
			promptText = preamble
		} else {
			promptText = preamble + "\n\n" + strings.TrimSpace(promptText)
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

	backend := exec.Engine.CodergenBackend
	if backend == nil {
		backend = &SimulatedCodergenBackend{}
	}
	resp, out, err := backend.Run(ctx, exec, node, promptText)
	if err != nil {
		fc, sig := classifyAPIError(err)
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
	source := statusSourceNone
	if len(worktreeStatusPaths) > 0 {
		var err error
		source, err = copyFirstValidFallbackStatus(stageStatusPath, worktreeStatusPaths)
		if err != nil {
			return runtime.Outcome{Status: runtime.StatusFail, FailureReason: err.Error()}, nil
		}
	}
	if exec != nil && exec.Engine != nil {
		exec.Engine.appendProgress(map[string]any{
			"event":   "status_ingestion_decision",
			"node_id": node.ID,
			"source":  string(source),
			"copied":  source == statusSourceWorktree || source == statusSourceDotAI,
		})
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
			out.ContextUpdates["last_response"] = truncate(resp, 200)
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
			Notes:  "codergen completed (status.json written)",
			ContextUpdates: map[string]any{
				"last_stage":    node.ID,
				"last_response": truncate(resp, 200),
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
				"last_response": truncate(resp, 200),
			},
		}, nil
	}
	return runtime.Outcome{
		Status:        runtime.StatusFail,
		FailureReason: "missing status.json (auto_status=false)",
		Notes:         "codergen completed without an outcome or status.json",
		ContextUpdates: map[string]any{
			"last_stage":    node.ID,
			"last_response": truncate(resp, 200),
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
		key := acceleratorKey(label)
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

type ToolHandler struct{}

func (h *ToolHandler) Execute(ctx context.Context, execCtx *Execution, node *model.Node) (runtime.Outcome, error) {
	stageDir := filepath.Join(execCtx.LogsRoot, node.ID)
	cmdStr := strings.TrimSpace(node.Attr("tool_command", ""))
	if cmdStr == "" {
		return runtime.Outcome{Status: runtime.StatusFail, FailureReason: "no tool_command specified"}, nil
	}
	timeout := parseDuration(node.Attr("timeout", ""), 0)
	if timeout <= 0 {
		timeout = 10 * time.Second
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

	if err := writeJSON(filepath.Join(stageDir, "tool_invocation.json"), map[string]any{
		"tool": "bash",
		// Use a non-login, non-interactive shell to avoid sourcing user dotfiles.
		"argv":        []string{"bash", "-c", cmdStr},
		"command":     cmdStr,
		"working_dir": execCtx.WorktreeDir,
		"timeout_ms":  timeout.Milliseconds(),
		"env_mode":    "base",
	}); err != nil {
		warnEngine(execCtx, fmt.Sprintf("write tool_invocation.json: %v", err))
	}

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "bash", "-c", cmdStr)
	cmd.Dir = execCtx.WorktreeDir
	cmd.Env = buildBaseNodeEnv(execCtx.WorktreeDir)
	// Avoid hanging on interactive reads; tool_command doesn't provide a way to supply stdin.
	cmd.Stdin = strings.NewReader("")
	stdoutPath := filepath.Join(stageDir, "stdout.log")
	stderrPath := filepath.Join(stageDir, "stderr.log")
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
	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile

	start := time.Now()
	runErr := cmd.Run()
	dur := time.Since(start)
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	if cctx.Err() == context.DeadlineExceeded {
		if err := writeJSON(filepath.Join(stageDir, "tool_timing.json"), map[string]any{
			"duration_ms": dur.Milliseconds(),
			"exit_code":   exitCode,
			"timed_out":   true,
		}); err != nil {
			warnEngine(execCtx, fmt.Sprintf("write tool_timing.json: %v", err))
		}
		_ = writeDiffPatch(stageDir, execCtx.WorktreeDir)
		return runtime.Outcome{
			Status:        runtime.StatusFail,
			FailureReason: fmt.Sprintf("tool_command timed out after %s", timeout),
		}, nil
	}

	if err := writeJSON(filepath.Join(stageDir, "tool_timing.json"), map[string]any{
		"duration_ms": dur.Milliseconds(),
		"exit_code":   exitCode,
		"timed_out":   false,
	}); err != nil {
		warnEngine(execCtx, fmt.Sprintf("write tool_timing.json: %v", err))
	}

	// Capture diff for debug-by-default. This is stable because we checkpoint after each node.
	_ = writeDiffPatch(stageDir, execCtx.WorktreeDir)

	stdoutBytes, rerr := os.ReadFile(stdoutPath)
	if rerr != nil {
		warnEngine(execCtx, fmt.Sprintf("read stdout.log: %v", rerr))
	}
	stderrBytes, rerr := os.ReadFile(stderrPath)
	if rerr != nil {
		warnEngine(execCtx, fmt.Sprintf("read stderr.log: %v", rerr))
	}
	combined := append(append([]byte{}, stdoutBytes...), stderrBytes...)
	combinedStr := string(combined)
	if runErr != nil {
		if execCtx != nil && execCtx.Engine != nil && execCtx.Engine.CXDB != nil {
			if _, _, err := execCtx.Engine.CXDB.Append(ctx, "com.kilroy.attractor.ToolResult", 1, map[string]any{
				"run_id":    execCtx.Engine.Options.RunID,
				"node_id":   node.ID,
				"tool_name": "shell",
				"call_id":   callID,
				"output":    truncate(combinedStr, 8_000),
				"is_error":  true,
			}); err != nil {
				execCtx.Engine.Warn(fmt.Sprintf("cxdb append ToolResult failed (node=%s call_id=%s): %v", node.ID, callID, err))
			}
		}
		return runtime.Outcome{
			Status:        runtime.StatusFail,
			FailureReason: runErr.Error(),
			ContextUpdates: map[string]any{
				"tool.output": truncate(combinedStr, 8_000),
			},
		}, nil
	}
	if execCtx != nil && execCtx.Engine != nil && execCtx.Engine.CXDB != nil {
		if _, _, err := execCtx.Engine.CXDB.Append(ctx, "com.kilroy.attractor.ToolResult", 1, map[string]any{
			"run_id":    execCtx.Engine.Options.RunID,
			"node_id":   node.ID,
			"tool_name": "shell",
			"call_id":   callID,
			"output":    truncate(combinedStr, 8_000),
			"is_error":  false,
		}); err != nil {
			execCtx.Engine.Warn(fmt.Sprintf("cxdb append ToolResult failed (node=%s call_id=%s): %v", node.ID, callID, err))
		}
	}
	return runtime.Outcome{
		Status: runtime.StatusSuccess,
		ContextUpdates: map[string]any{
			"tool.output": truncate(combinedStr, 8_000),
		},
		Notes: "tool completed",
	}, nil
}

func truncate(s string, n int) string {
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
	Default        *Answer            // default answer if timeout/skip (nil = no default)
	TimeoutSeconds float64            // max wait time; 0 means no timeout
	Stage          string
	Metadata       map[string]any     // arbitrary key-value pairs for frontend use
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
