package validate

import (
	"fmt"
	"strings"

	"github.com/strongdm/kilroy/internal/attractor/cond"
	"github.com/strongdm/kilroy/internal/attractor/model"
	"github.com/strongdm/kilroy/internal/attractor/runtime"
	"github.com/strongdm/kilroy/internal/attractor/style"
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

func Validate(g *model.Graph) []Diagnostic {
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
	diags = append(diags, lintFidelityValid(g)...)
	diags = append(diags, lintPromptOnCodergenNodes(g)...)
	diags = append(diags, lintPromptFileConflict(g)...)
	diags = append(diags, lintLLMProviderPresent(g)...)
	diags = append(diags, lintLoopRestartFailureClassGuard(g)...)
	diags = append(diags, lintEscalationModelsSyntax(g)...)
	return diags
}

func ValidateOrError(g *model.Graph) error {
	diags := Validate(g)
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
	if len(ids) != 1 {
		return []Diagnostic{{
			Rule:     "terminal_node",
			Severity: SeverityError,
			Message:  fmt.Sprintf("pipeline must have exactly one exit node (found %d: %v)", len(ids), ids),
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
		if n != nil && n.Shape() == "Mdiamond" {
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

func findExitNodeID(g *model.Graph) string {
	for id, n := range g.Nodes {
		if n != nil && n.Shape() == "Msquare" {
			return id
		}
	}
	for id := range g.Nodes {
		if strings.EqualFold(id, "exit") || strings.EqualFold(id, "end") {
			return id
		}
	}
	return ""
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
	exit := findExitNodeID(g)
	if exit == "" {
		return nil
	}
	if len(g.Outgoing(exit)) > 0 {
		return []Diagnostic{{
			Rule:     "exit_no_outgoing",
			Severity: SeverityError,
			Message:  "exit node must have no outgoing edges",
			NodeID:   exit,
		}}
	}
	return nil
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
