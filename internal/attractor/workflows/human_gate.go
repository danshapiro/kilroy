// Layer 2 handler for human-in-the-loop gates (hexagon nodes).
// Blocks until a human responds via an interviewer backend.
package workflows

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/engine"
	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

// HumanGateHandler presents choices to a human via the configured Interviewer
// and routes based on their selection. Registered for "wait.human" type.
type HumanGateHandler struct{}

func (h *HumanGateHandler) Execute(ctx context.Context, exec *engine.Execution, node *model.Node) (runtime.Outcome, error) {
	edges := exec.Graph.Outgoing(node.ID)
	if len(edges) == 0 {
		return runtime.Outcome{Status: runtime.StatusFail, FailureReason: "no outgoing edges for human gate"}, nil
	}

	options := make([]engine.Option, 0, len(edges))
	used := map[string]bool{}
	for i, e := range edges {
		if e == nil {
			continue
		}
		label := strings.TrimSpace(e.Label())
		if label == "" {
			label = e.To
		}
		key := engine.AcceleratorKey(label)
		if key == "" || used[key] {
			key = fmt.Sprintf("%d", i+1)
		}
		used[key] = true
		options = append(options, engine.Option{
			Key:   key,
			Label: label,
			To:    e.To,
		})
	}

	q := engine.Question{
		Type:    engine.QuestionSingleSelect,
		Text:    node.Attr("question", node.Label()),
		Options: options,
		Stage:   node.ID,
	}
	interviewer := exec.Engine.Interviewer
	if interviewer == nil {
		interviewer = &engine.AutoApproveInterviewer{}
	}
	interviewStart := time.Now()
	exec.Engine.CXDBInterviewStarted(ctx, node.ID, q.Text, string(q.Type))

	ans := interviewer.Ask(q)
	interviewDurationMS := time.Since(interviewStart).Milliseconds()

	if ans.TimedOut {
		exec.Engine.CXDBInterviewTimeout(ctx, node.ID, q.Text, interviewDurationMS)
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

	exec.Engine.CXDBInterviewCompleted(ctx, node.ID, ans.Value, interviewDurationMS)

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
