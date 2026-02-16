package validate

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/danshapiro/kilroy/internal/attractor/cond"
	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
	"github.com/danshapiro/kilroy/internal/attractor/style"
)

type Severity string

const (
	SeverityError   Severity = "ERROR"
	SeverityWarning Severity = "WARNING"
	SeverityInfo    Severity = "INFO"
)

type Diagnostic struct {
	Rule     string   `json:"rule"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
	NodeID   string   `json:"node_id,omitempty"`
	EdgeFrom string   `json:"edge_from,omitempty"`
	EdgeTo   string   `json:"edge_to,omitempty"`
	Fix      string   `json:"fix,omitempty"`
}

// LintRule is the interface for custom lint rules that can be passed to
// Validate via the extra_rules parameter (spec §7.4).
type LintRule interface {
	Name() string
	Apply(g *model.Graph) []Diagnostic
}

// Validate runs all built-in lint rules and any extra rules against the graph.
// Extra rules are appended after built-in rules (spec §7.3).
func Validate(g *model.Graph, extraRules ...LintRule) []Diagnostic {
	var diags []Diagnostic
	if g == nil {
		return []Diagnostic{{Rule: "graph_nil", Severity: SeverityError, Message: "graph is nil"}}
	}

	diags = append(diags, lintStartNode(g)...)
	diags = append(diags, lintExitNode(g)...)
	diags = append(diags, lintEdgeTargetsExist(g)...)
	diags = append(diags, lintStartNoIncoming(g)...)
	diags = append(diags, lintExitNoOutgoing(g)...)
	diags = append(diags, lintReachability(g)...)
	diags = append(diags, lintConditionSyntax(g)...)
	diags = append(diags, lintStylesheetSyntax(g)...)
	diags = append(diags, lintRetryTargetsExist(g)...)
	diags = append(diags, lintGoalGateHasRetry(g)...)
	diags = append(diags, lintGoalGateExitStatusContract(g)...)
	diags = append(diags, lintGoalGatePromptStatusHint(g)...)
	diags = append(diags, lintFidelityValid(g)...)
	diags = append(diags, lintPromptOnCodergenNodes(g)...)
	diags = append(diags, lintPromptOnConditionalNodes(g)...)
	diags = append(diags, lintPromptFileConflict(g)...)
	diags = append(diags, lintLLMProviderPresent(g)...)
	diags = append(diags, lintLoopRestartFailureClassGuard(g)...)
	diags = append(diags, lintFailLoopFailureClassGuard(g)...)
	diags = append(diags, lintEscalationModelsSyntax(g)...)
	diags = append(diags, lintAllConditionalEdges(g)...)

	// Run custom lint rules (spec §7.3: extra_rules appended after built-in rules).
	for _, rule := range extraRules {
		if rule != nil {
			diags = append(diags, rule.Apply(g)...)
		}
	}
	return diags
}

func ValidateOrError(g *model.Graph, extraRules ...LintRule) error {
	diags := Validate(g, extraRules...)
	var errs []string
	for _, d := range diags {
		if d.Severity == SeverityError {
			errs = append(errs, d.Rule+": "+d.Message)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("validation failed: %s", strings.Join(errs, "; "))
	}
	return nil
}

func lintStartNode(g *model.Graph) []Diagnostic {
	var ids []string
	for id, n := range g.Nodes {
		if n == nil {
			continue
		}
		if n.Shape() == "Mdiamond" || n.Shape() == "circle" || strings.EqualFold(id, "start") {
			ids = append(ids, id)
		}
	}
	if len(ids) != 1 {
		return []Diagnostic{{
			Rule:     "start_node",
			Severity: SeverityError,
			Message:  fmt.Sprintf("pipeline must have exactly one start node (found %d: %v)", len(ids), ids),
		}}
	}
	return nil
}

func lintExitNode(g *model.Graph) []Diagnostic {
	var ids []string
	for id, n := range g.Nodes {
		if n == nil {
			continue
		}
		if n.Shape() == "Msquare" || n.Shape() == "doublecircle" || strings.EqualFold(id, "exit") || strings.EqualFold(id, "end") {
			ids = append(ids, id)
		}
	}
	// Spec §7.2: pipeline must have at least one terminal node.
	if len(ids) == 0 {
		return []Diagnostic{{
			Rule:     "terminal_node",
			Severity: SeverityError,
			Message:  "pipeline must have at least one exit node (found 0)",
		}}
	}
	return nil
}

func lintEdgeTargetsExist(g *model.Graph) []Diagnostic {
	var diags []Diagnostic
	for _, e := range g.Edges {
		if e == nil {
			continue
		}
		if _, ok := g.Nodes[e.From]; !ok {
			diags = append(diags, Diagnostic{
				Rule:     "edge_target_exists",
				Severity: SeverityError,
				Message:  "edge references missing from-node",
				EdgeFrom: e.From,
				EdgeTo:   e.To,
			})
		}
		if _, ok := g.Nodes[e.To]; !ok {
			diags = append(diags, Diagnostic{
				Rule:     "edge_target_exists",
				Severity: SeverityError,
				Message:  "edge references missing to-node",
				EdgeFrom: e.From,
				EdgeTo:   e.To,
			})
		}
	}
	return diags
}

func findStartNodeID(g *model.Graph) string {
	for id, n := range g.Nodes {
		if n != nil && (n.Shape() == "Mdiamond" || n.Shape() == "circle") {
			return id
		}
	}
	for id := range g.Nodes {
		if strings.EqualFold(id, "start") {
			return id
		}
	}
	return ""
}

func findAllStartNodeIDs(g *model.Graph) []string {
	var ids []string
	seen := map[string]bool{}
	for id, n := range g.Nodes {
		if n != nil && (n.Shape() == "Mdiamond" || n.Shape() == "circle") {
			if !seen[id] {
				ids = append(ids, id)
				seen[id] = true
			}
		}
	}
	for id := range g.Nodes {
		if strings.EqualFold(id, "start") && !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	return ids
}

func findExitNodeID(g *model.Graph) string {
	ids := findAllExitNodeIDs(g)
	if len(ids) > 0 {
		return ids[0]
	}
	return ""
}

func findAllExitNodeIDs(g *model.Graph) []string {
	var ids []string
	seen := map[string]bool{}
	for id, n := range g.Nodes {
		if n != nil && (n.Shape() == "Msquare" || n.Shape() == "doublecircle") {
			if !seen[id] {
				ids = append(ids, id)
				seen[id] = true
			}
		}
	}
	for id := range g.Nodes {
		if (strings.EqualFold(id, "exit") || strings.EqualFold(id, "end")) && !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	return ids
}

func lintStartNoIncoming(g *model.Graph) []Diagnostic {
	start := findStartNodeID(g)
	if start == "" {
		return nil
	}
	if len(g.Incoming(start)) > 0 {
		return []Diagnostic{{
			Rule:     "start_no_incoming",
			Severity: SeverityError,
			Message:  "start node must have no incoming edges",
			NodeID:   start,
		}}
	}
	return nil
}

func lintExitNoOutgoing(g *model.Graph) []Diagnostic {
	// Check ALL exit nodes, not just the first one (spec §7.2 allows multiple exit nodes).
	exitIDs := findAllExitNodeIDs(g)
	if len(exitIDs) == 0 {
		return nil
	}
	var diags []Diagnostic
	for _, exit := range exitIDs {
		if len(g.Outgoing(exit)) > 0 {
			diags = append(diags, Diagnostic{
				Rule:     "exit_no_outgoing",
				Severity: SeverityError,
				Message:  "exit node must have no outgoing edges",
				NodeID:   exit,
			})
		}
	}
	return diags
}

func lintReachability(g *model.Graph) []Diagnostic {
	start := findStartNodeID(g)
	if start == "" {
		return nil
	}
	seen := map[string]bool{start: true}
	queue := []string{start}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, e := range g.Outgoing(cur) {
			if e == nil {
				continue
			}
			if !seen[e.To] {
				seen[e.To] = true
				queue = append(queue, e.To)
			}
		}
	}
	var diags []Diagnostic
	for id := range g.Nodes {
		if !seen[id] {
			diags = append(diags, Diagnostic{
				Rule:     "reachability",
				Severity: SeverityError,
				Message:  "node is not reachable from start",
				NodeID:   id,
			})
		}
	}
	return diags
}

func lintConditionSyntax(g *model.Graph) []Diagnostic {
	var diags []Diagnostic
	for _, e := range g.Edges {
		if e == nil {
			continue
		}
		c := strings.TrimSpace(e.Condition())
		if c == "" {
			continue
		}
		if err := validateConditionSyntax(c); err != nil {
			diags = append(diags, Diagnostic{
				Rule:     "condition_syntax",
				Severity: SeverityError,
				Message:  err.Error(),
				EdgeFrom: e.From,
				EdgeTo:   e.To,
			})
			continue
		}
		// Also ensure our evaluator can process it.
		_, _ = cond.Evaluate(c, runtime.Outcome{Status: runtime.StatusSuccess}, runtime.NewContext())
	}
	return diags
}

func validateConditionSyntax(condExpr string) error {
	clauses := strings.Split(condExpr, "&&")
	for _, clause := range clauses {
		clause = strings.TrimSpace(clause)
		if clause == "" {
			continue
		}
		// Disallow known future operators so mistakes don't silently "parse" as bare keys.
		if strings.ContainsAny(clause, "<>|") {
			return fmt.Errorf("invalid condition operator in clause %q", clause)
		}
		if strings.Contains(clause, "!=") {
			parts := strings.SplitN(clause, "!=", 2)
			if len(parts) != 2 {
				return fmt.Errorf("invalid condition clause %q", clause)
			}
			if err := validateCondKey(strings.TrimSpace(parts[0])); err != nil {
				return err
			}
			if strings.TrimSpace(parts[1]) == "" {
				return fmt.Errorf("invalid condition clause %q: missing literal", clause)
			}
			continue
		}
		if strings.Contains(clause, "=") {
			parts := strings.SplitN(clause, "=", 2)
			if len(parts) != 2 {
				return fmt.Errorf("invalid condition clause %q", clause)
			}
			if err := validateCondKey(strings.TrimSpace(parts[0])); err != nil {
				return err
			}
			if strings.TrimSpace(parts[1]) == "" {
				return fmt.Errorf("invalid condition clause %q: missing literal", clause)
			}
			continue
		}
		// Bare key: allow but validate key shape.
		if err := validateCondKey(strings.TrimSpace(clause)); err != nil {
			return err
		}
	}
	return nil
}

func validateCondKey(key string) error {
	if key == "" {
		return fmt.Errorf("invalid condition: empty key")
	}
	// Allow outcome/preferred_label plus context.* dotted paths. Also allow unqualified dotted paths
	// (resolve_key supports direct context lookup).
	if key == "outcome" || key == "preferred_label" {
		return nil
	}
	key = strings.TrimPrefix(key, "context.")
	for _, part := range strings.Split(key, ".") {
		if part == "" {
			return fmt.Errorf("invalid condition key %q", key)
		}
		// [A-Za-z_][A-Za-z0-9_]*
		r0 := part[0]
		if !isAlphaUnderscore(r0) {
			return fmt.Errorf("invalid condition key %q", key)
		}
		for i := 1; i < len(part); i++ {
			ch := part[i]
			if !isAlnumUnderscore(ch) {
				return fmt.Errorf("invalid condition key %q", key)
			}
		}
	}
	return nil
}

func isAlphaUnderscore(ch byte) bool {
	return (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || ch == '_'
}

func isAlnumUnderscore(ch byte) bool {
	return isAlphaUnderscore(ch) || (ch >= '0' && ch <= '9')
}

func lintStylesheetSyntax(g *model.Graph) []Diagnostic {
	raw := strings.TrimSpace(g.Attrs["model_stylesheet"])
	if raw == "" {
		return nil
	}
	if _, err := style.ParseStylesheet(raw); err != nil {
		return []Diagnostic{{
			Rule:     "stylesheet_syntax",
			Severity: SeverityError,
			Message:  err.Error(),
		}}
	}
	return nil
}

func lintRetryTargetsExist(g *model.Graph) []Diagnostic {
	var diags []Diagnostic
	for id, n := range g.Nodes {
		if n == nil {
			continue
		}
		for _, k := range []string{"retry_target", "fallback_retry_target"} {
			t := strings.TrimSpace(n.Attr(k, ""))
			if t == "" {
				continue
			}
			if _, ok := g.Nodes[t]; !ok {
				diags = append(diags, Diagnostic{
					Rule:     "retry_target_exists",
					Severity: SeverityWarning,
					Message:  fmt.Sprintf("%s references missing node %q", k, t),
					NodeID:   id,
				})
			}
		}
	}
	return diags
}

func lintGoalGateHasRetry(g *model.Graph) []Diagnostic {
	var diags []Diagnostic
	for id, n := range g.Nodes {
		if n == nil {
			continue
		}
		if strings.EqualFold(n.Attr("goal_gate", "false"), "true") {
			if strings.TrimSpace(n.Attr("retry_target", "")) == "" && strings.TrimSpace(n.Attr("fallback_retry_target", "")) == "" &&
				strings.TrimSpace(g.Attrs["retry_target"]) == "" && strings.TrimSpace(g.Attrs["fallback_retry_target"]) == "" {
				diags = append(diags, Diagnostic{
					Rule:     "goal_gate_has_retry",
					Severity: SeverityWarning,
					Message:  "goal_gate node has no retry_target/fallback_retry_target (node or graph)",
					NodeID:   id,
				})
			}
		}
	}
	return diags
}

func lintGoalGateExitStatusContract(g *model.Graph) []Diagnostic {
	exitIDs := findAllExitNodeIDs(g)
	if len(exitIDs) == 0 {
		return nil
	}
	exitSet := map[string]bool{}
	for _, id := range exitIDs {
		exitSet[id] = true
	}
	var diags []Diagnostic
	for id, n := range g.Nodes {
		if n == nil || !strings.EqualFold(n.Attr("goal_gate", "false"), "true") {
			continue
		}
		for _, e := range g.Outgoing(id) {
			if e == nil || !exitSet[e.To] {
				continue
			}
			statuses := outcomeEqualsStatuses(strings.TrimSpace(e.Condition()))
			if len(statuses) == 0 {
				continue
			}
			violatesContract := false
			for _, status := range statuses {
				if status == runtime.StatusSuccess || status == runtime.StatusPartialSuccess {
					continue
				}
				violatesContract = true
				break
			}
			if !violatesContract {
				continue
			}
			diags = append(diags, Diagnostic{
				Rule:     "goal_gate_exit_status_contract",
				Severity: SeverityError,
				Message:  "goal_gate node routes to terminal on non-success outcome; use outcome=success (or partial_success) to satisfy goal-gate contract",
				EdgeFrom: e.From,
				EdgeTo:   e.To,
				Fix:      "change terminal edge condition to outcome=success or outcome=partial_success",
			})
		}
	}
	return diags
}

var outcomeAssignmentPattern = regexp.MustCompile(`(?i)\boutcome\s*=\s*['"]?([a-z0-9_-]+)['"]?`)

func lintGoalGatePromptStatusHint(g *model.Graph) []Diagnostic {
	var diags []Diagnostic
	for id, n := range g.Nodes {
		if n == nil || !strings.EqualFold(n.Attr("goal_gate", "false"), "true") {
			continue
		}
		customOutcome, shouldWarn := firstPromptCustomOutcomeWithoutCanonicalSuccess(n.Prompt())
		if !shouldWarn {
			continue
		}
		diags = append(diags, Diagnostic{
			Rule:     "goal_gate_prompt_status_hint",
			Severity: SeverityWarning,
			Message:  fmt.Sprintf("goal_gate prompt instructs custom outcome=%s without canonical success outcome; prefer outcome=success (or partial_success) for gate satisfaction", customOutcome),
			NodeID:   id,
			Fix:      "update prompt instructions to include outcome=success (or outcome=partial_success) when approved",
		})
	}
	return diags
}

func outcomeEqualsStatuses(condExpr string) []runtime.StageStatus {
	var out []runtime.StageStatus
	for _, clause := range strings.Split(condExpr, "&&") {
		clause = strings.TrimSpace(clause)
		if clause == "" || !strings.Contains(clause, "=") || strings.Contains(clause, "!=") || strings.Contains(clause, "==") {
			continue
		}
		parts := strings.SplitN(clause, "=", 2)
		if len(parts) != 2 {
			continue
		}
		if strings.TrimSpace(parts[0]) != "outcome" {
			continue
		}
		raw := strings.Trim(strings.TrimSpace(parts[1]), "\"'")
		status, err := runtime.ParseStageStatus(raw)
		if err != nil {
			continue
		}
		out = append(out, status)
	}
	return out
}

func firstPromptCustomOutcomeWithoutCanonicalSuccess(prompt string) (string, bool) {
	matches := outcomeAssignmentPattern.FindAllStringSubmatch(prompt, -1)
	if len(matches) == 0 {
		return "", false
	}
	var custom []string
	hasCanonicalSuccess := false
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		status, err := runtime.ParseStageStatus(m[1])
		if err != nil {
			continue
		}
		if status == runtime.StatusSuccess || status == runtime.StatusPartialSuccess {
			hasCanonicalSuccess = true
		}
		if !status.IsCanonical() {
			custom = append(custom, string(status))
		}
	}
	if hasCanonicalSuccess || len(custom) == 0 {
		return "", false
	}
	return custom[0], true
}

func lintFidelityValid(g *model.Graph) []Diagnostic {
	valid := map[string]bool{
		"full":           true,
		"truncate":       true,
		"compact":        true,
		"summary:low":    true,
		"summary:medium": true,
		"summary:high":   true,
	}
	var diags []Diagnostic
	for id, n := range g.Nodes {
		if n == nil {
			continue
		}
		if f := strings.TrimSpace(n.Attr("fidelity", "")); f != "" && !valid[f] {
			diags = append(diags, Diagnostic{
				Rule:     "fidelity_valid",
				Severity: SeverityWarning,
				Message:  fmt.Sprintf("invalid fidelity value %q", f),
				NodeID:   id,
			})
		}
	}
	for _, e := range g.Edges {
		if e == nil {
			continue
		}
		if f := strings.TrimSpace(e.Attr("fidelity", "")); f != "" && !valid[f] {
			diags = append(diags, Diagnostic{
				Rule:     "fidelity_valid",
				Severity: SeverityWarning,
				Message:  fmt.Sprintf("invalid fidelity value %q", f),
				EdgeFrom: e.From,
				EdgeTo:   e.To,
			})
		}
	}
	return diags
}

func lintPromptOnCodergenNodes(g *model.Graph) []Diagnostic {
	var diags []Diagnostic
	for id, n := range g.Nodes {
		if n == nil {
			continue
		}
		// Best-effort: default handler is codergen for shape box.
		if n.Shape() != "box" {
			continue
		}
		if strings.TrimSpace(n.Prompt()) == "" {
			diags = append(diags, Diagnostic{
				Rule:     "prompt_on_llm_nodes",
				Severity: SeverityWarning,
				Message:  "codergen node has empty prompt (label will be used)",
				NodeID:   id,
			})
		}
	}
	return diags
}

func lintPromptOnConditionalNodes(g *model.Graph) []Diagnostic {
	var diags []Diagnostic
	for id, n := range g.Nodes {
		if n == nil {
			continue
		}
		if n.Shape() != "diamond" {
			continue
		}
		// Diamond nodes use the ConditionalHandler, which is a pure
		// pass-through that never executes prompts.  A prompt attribute
		// on a diamond is almost certainly a mistake — the author likely
		// intended shape=box (codergen) so the prompt actually runs.
		if strings.TrimSpace(n.Prompt()) != "" {
			diags = append(diags, Diagnostic{
				Rule:     "prompt_on_conditional_node",
				Severity: SeverityWarning,
				Message:  "diamond (conditional) node has a prompt that will be ignored; use shape=box if the prompt should execute",
				NodeID:   id,
			})
		}
	}
	return diags
}

func lintLLMProviderPresent(g *model.Graph) []Diagnostic {
	// Kilroy metaspec: if llm_provider is missing after stylesheet resolution, fail.
	var diags []Diagnostic
	for id, n := range g.Nodes {
		if n == nil {
			continue
		}
		if n.Shape() != "box" {
			continue
		}
		if strings.TrimSpace(n.Attr("llm_provider", "")) == "" {
			diags = append(diags, Diagnostic{
				Rule:     "llm_provider_required",
				Severity: SeverityError,
				Message:  "codergen node missing llm_provider (Kilroy forbids provider auto-detection)",
				NodeID:   id,
			})
		}
	}
	return diags
}

func lintLoopRestartFailureClassGuard(g *model.Graph) []Diagnostic {
	var diags []Diagnostic
	for _, e := range g.Edges {
		if e == nil {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(e.Attr("loop_restart", "false")), "true") {
			continue
		}
		condExpr := strings.TrimSpace(e.Condition())
		if !conditionMentionsFailureOutcome(condExpr) {
			continue
		}
		if conditionHasTransientInfraGuard(condExpr) {
			continue
		}
		diags = append(diags, Diagnostic{
			Rule:     "loop_restart_failure_class_guard",
			Severity: SeverityWarning,
			Message:  "loop_restart on failure edge should be guarded by context.failure_class=transient_infra and paired with a non-restart deterministic fail edge",
			EdgeFrom: e.From,
			EdgeTo:   e.To,
			Fix:      "split edge into transient-infra restart + non-transient retry edges",
		})
	}
	return diags
}

func lintFailLoopFailureClassGuard(g *model.Graph) []Diagnostic {
	var diags []Diagnostic
	for _, e := range g.Edges {
		if e == nil {
			continue
		}
		fromNode := g.Nodes[e.From]
		if fromNode == nil || fromNode.Shape() != "diamond" {
			continue
		}
		condExpr := strings.TrimSpace(e.Condition())
		if !conditionMentionsFailureOutcome(condExpr) {
			continue
		}
		// Only warn for back-edges into nodes that can reach this diamond.
		if !graphReachable(g, e.To, e.From) {
			continue
		}
		if conditionReferencesFailureClass(condExpr) {
			continue
		}
		diags = append(diags, Diagnostic{
			Rule:     "fail_loop_failure_class_guard",
			Severity: SeverityWarning,
			Message:  "failure back-edge from conditional node should guard retry path with context.failure_class and provide deterministic fallback routing",
			EdgeFrom: e.From,
			EdgeTo:   e.To,
			Fix:      "split fail loop edge into failure_class-aware routes",
		})
	}
	return diags
}

func conditionMentionsFailureOutcome(condExpr string) bool {
	for _, clause := range strings.Split(condExpr, "&&") {
		clause = strings.TrimSpace(clause)
		if clause == "" {
			continue
		}
		if strings.Contains(clause, "!=") {
			parts := strings.SplitN(clause, "!=", 2)
			if len(parts) != 2 {
				continue
			}
			key := strings.TrimSpace(parts[0])
			val := strings.Trim(strings.ToLower(strings.TrimSpace(parts[1])), "\"'")
			if key == "outcome" && val == "success" {
				return true
			}
			continue
		}
		if !strings.Contains(clause, "=") {
			continue
		}
		parts := strings.SplitN(clause, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.Trim(strings.ToLower(strings.TrimSpace(parts[1])), "\"'")
		if key != "outcome" {
			continue
		}
		if val == "fail" || val == "retry" || val == "partial_success" {
			return true
		}
	}
	return false
}

func conditionHasTransientInfraGuard(condExpr string) bool {
	for _, clause := range strings.Split(condExpr, "&&") {
		clause = strings.TrimSpace(clause)
		if !strings.Contains(clause, "=") || strings.Contains(clause, "!=") {
			continue
		}
		parts := strings.SplitN(clause, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.Trim(strings.ToLower(strings.TrimSpace(parts[1])), "\"'")
		if key == "context.failure_class" || key == "failure_class" {
			if val == "transient_infra" {
				return true
			}
		}
	}
	return false
}

func conditionReferencesFailureClass(condExpr string) bool {
	for _, clause := range strings.Split(condExpr, "&&") {
		clause = strings.TrimSpace(clause)
		if clause == "" {
			continue
		}
		var key string
		if strings.Contains(clause, "!=") {
			parts := strings.SplitN(clause, "!=", 2)
			if len(parts) != 2 {
				continue
			}
			key = strings.TrimSpace(parts[0])
		} else if strings.Contains(clause, "=") {
			parts := strings.SplitN(clause, "=", 2)
			if len(parts) != 2 {
				continue
			}
			key = strings.TrimSpace(parts[0])
		} else {
			continue
		}
		if key == "context.failure_class" || key == "failure_class" {
			return true
		}
	}
	return false
}

func graphReachable(g *model.Graph, fromID string, targetID string) bool {
	if g == nil {
		return false
	}
	fromID = strings.TrimSpace(fromID)
	targetID = strings.TrimSpace(targetID)
	if fromID == "" || targetID == "" {
		return false
	}
	if fromID == targetID {
		return true
	}
	type void struct{}
	seen := map[string]void{}
	queue := []string{fromID}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if _, ok := seen[cur]; ok {
			continue
		}
		seen[cur] = void{}
		for _, e := range g.Outgoing(cur) {
			if e == nil {
				continue
			}
			next := strings.TrimSpace(e.To)
			if next == "" {
				continue
			}
			if next == targetID {
				return true
			}
			if _, ok := seen[next]; !ok {
				queue = append(queue, next)
			}
		}
	}
	return false
}

func lintPromptFileConflict(g *model.Graph) []Diagnostic {
	var diags []Diagnostic
	for id, n := range g.Nodes {
		if n == nil {
			continue
		}
		pf := strings.TrimSpace(n.Attr("prompt_file", ""))
		if pf == "" {
			continue
		}
		// prompt_file should have been resolved by the engine's expandPromptFiles transform
		// before validation runs. If it's still present, either RepoPath wasn't set (e.g.
		// standalone validate) or the transform didn't run. Warn so the user knows.
		hasPrompt := strings.TrimSpace(n.Attr("prompt", "")) != "" || strings.TrimSpace(n.Attr("llm_prompt", "")) != ""
		if hasPrompt {
			diags = append(diags, Diagnostic{
				Rule:     "prompt_file_conflict",
				Severity: SeverityError,
				Message:  fmt.Sprintf("node has both prompt_file and prompt/llm_prompt — use one or the other"),
				NodeID:   id,
				Fix:      "remove either prompt_file or prompt",
			})
		}
	}
	return diags
}

func lintEscalationModelsSyntax(g *model.Graph) []Diagnostic {
	var diags []Diagnostic
	for id, n := range g.Nodes {
		if n == nil {
			continue
		}
		raw := strings.TrimSpace(n.Attr("escalation_models", ""))
		if raw == "" {
			continue
		}
		for _, entry := range strings.Split(raw, ",") {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}
			idx := strings.Index(entry, ":")
			if idx < 0 {
				diags = append(diags, Diagnostic{
					Rule:     "escalation_models_syntax",
					Severity: SeverityWarning,
					Message:  fmt.Sprintf("escalation_models entry %q missing colon separator (expected provider:model)", entry),
					NodeID:   id,
					Fix:      "use provider:model format, e.g. \"anthropic:claude-opus-4-6\"",
				})
				continue
			}
			prov := strings.ToLower(strings.TrimSpace(entry[:idx]))
			mod := strings.TrimSpace(entry[idx+1:])
			if prov == "" {
				diags = append(diags, Diagnostic{
					Rule:     "escalation_models_syntax",
					Severity: SeverityWarning,
					Message:  fmt.Sprintf("escalation_models entry %q has empty provider", entry),
					NodeID:   id,
				})
			}
			if mod == "" {
				diags = append(diags, Diagnostic{
					Rule:     "escalation_models_syntax",
					Severity: SeverityWarning,
					Message:  fmt.Sprintf("escalation_models entry %q has empty model", entry),
					NodeID:   id,
				})
			}
		}
	}
	return diags
}

// TypeKnownRule implements LintRule for the spec §7.2 "type_known" rule.
// It warns when a node's explicit type override is not in the set of known
// handler types. The known types are provided at construction time so the
// validate package does not depend on the engine's handler registry.
type TypeKnownRule struct {
	KnownTypes map[string]bool
}

func NewTypeKnownRule(knownTypes []string) *TypeKnownRule {
	m := make(map[string]bool, len(knownTypes))
	for _, t := range knownTypes {
		m[t] = true
	}
	return &TypeKnownRule{KnownTypes: m}
}

func (r *TypeKnownRule) Name() string { return "type_known" }

func (r *TypeKnownRule) Apply(g *model.Graph) []Diagnostic {
	var diags []Diagnostic
	for id, n := range g.Nodes {
		if n == nil {
			continue
		}
		t := strings.TrimSpace(n.Attr("type", ""))
		if t == "" {
			continue
		}
		if !r.KnownTypes[t] {
			diags = append(diags, Diagnostic{
				Rule:     "type_known",
				Severity: SeverityWarning,
				Message:  fmt.Sprintf("node type %q is not recognized by the handler registry", t),
				NodeID:   id,
			})
		}
	}
	return diags
}

// lintAllConditionalEdges warns when a non-terminal node has outgoing edges but
// all are conditional (no unconditional fallback). This creates a routing gap:
// if no condition matches at runtime, the engine has no edge to follow.
func lintAllConditionalEdges(g *model.Graph) []Diagnostic {
	var diags []Diagnostic
	exitIDs := make(map[string]bool)
	for _, id := range findAllExitNodeIDs(g) {
		exitIDs[id] = true
	}
	startIDs := make(map[string]bool)
	for _, id := range findAllStartNodeIDs(g) {
		startIDs[id] = true
	}

	// Build per-node outgoing edge lists.
	outgoing := make(map[string][]*model.Edge)
	for _, e := range g.Edges {
		if e != nil {
			outgoing[e.From] = append(outgoing[e.From], e)
		}
	}

	for id, n := range g.Nodes {
		if n == nil {
			continue
		}
		// Skip terminal nodes (no outgoing edges expected).
		if exitIDs[id] {
			continue
		}
		// Skip start nodes (always have unconditional edges by convention).
		if startIDs[id] {
			continue
		}
		edges := outgoing[id]
		if len(edges) == 0 {
			continue // no outgoing edges — other lint rules handle this
		}
		allConditional := true
		for _, e := range edges {
			if strings.TrimSpace(e.Condition()) == "" {
				allConditional = false
				break
			}
		}
		if allConditional {
			diags = append(diags, Diagnostic{
				Rule:     "all_conditional_edges",
				Severity: SeverityWarning,
				NodeID:   id,
				Message:  fmt.Sprintf("node %q has %d outgoing edge(s) but all are conditional; add an unconditional fallback edge to avoid routing gaps", id, len(edges)),
				Fix:      "Add an unconditional edge (no condition attribute) as a fallback route",
			})
		}
	}
	return diags
}
