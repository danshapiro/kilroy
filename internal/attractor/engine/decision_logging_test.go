// Tests for decision logging: edge condition evaluation, edge selection,
// retry decisions, and provider selection progress events.

package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

func TestDecisionLogging_ConditionalRoute(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	pinned := writePinnedCatalog(t)

	// Graph with conditional routing: process succeeds, routing through check
	// which routes to path_a (outcome=success) and NOT path_b (outcome=fail).
	// The unconditional fallback edge satisfies graph validation.
	dot := []byte(`digraph conditional_decision_log {
  graph [goal="Test decision logging for conditional routing", default_max_retry=0]
  start [shape=Mdiamond]
  process [shape=parallelogram, tool_command="echo processed"]
  path_a [shape=parallelogram, tool_command="echo took_path_a"]
  path_b [shape=parallelogram, tool_command="echo took_path_b"]
  done [shape=Msquare]
  start -> process
  process -> path_a [condition="outcome=success"]
  process -> path_b [condition="outcome=fail"]
  process -> done
  path_a -> done
  path_b -> done
}`)
	cfg := minimalToolGraphConfig(repo, pinned)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{
		RunID:       "conditional-decision-log",
		LogsRoot:    logsRoot,
		DisableCXDB: true,
	})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("expected success, got %q", res.FinalStatus)
	}

	progressPath := filepath.Join(logsRoot, "progress.ndjson")
	events := mustReadProgressEventsFile(t, progressPath)

	// Verify edge_condition_evaluated events exist for process node.
	var condEvents []map[string]any
	for _, ev := range events {
		if anyToString(ev["event"]) == "edge_condition_evaluated" &&
			anyToString(ev["node_id"]) == "process" {
			condEvents = append(condEvents, ev)
		}
	}
	if len(condEvents) < 2 {
		t.Fatalf("expected at least 2 edge_condition_evaluated events for process, got %d", len(condEvents))
	}

	// Verify one matched (path_a, outcome=success) and one did not (path_b, outcome=fail).
	var matchedCount, unmatchedCount int
	for _, ev := range condEvents {
		matched, ok := ev["matched"].(bool)
		if !ok {
			t.Fatalf("edge_condition_evaluated event missing matched field: %v", ev)
		}
		edgeTo := anyToString(ev["edge_to"])
		condition := anyToString(ev["condition"])
		if matched {
			matchedCount++
			if edgeTo != "path_a" {
				t.Errorf("matched edge_to: got %q want %q", edgeTo, "path_a")
			}
			if !strings.Contains(condition, "outcome=success") {
				t.Errorf("matched condition: got %q want to contain %q", condition, "outcome=success")
			}
		} else {
			unmatchedCount++
			if edgeTo != "path_b" {
				t.Errorf("unmatched edge_to: got %q want %q", edgeTo, "path_b")
			}
		}
	}
	if matchedCount != 1 {
		t.Errorf("expected 1 matched condition, got %d", matchedCount)
	}
	if unmatchedCount != 1 {
		t.Errorf("expected 1 unmatched condition, got %d", unmatchedCount)
	}

	// Verify edge_selected event has selection_method=condition_match.
	var edgeSelectedEvents []map[string]any
	for _, ev := range events {
		if anyToString(ev["event"]) == "edge_selected" &&
			anyToString(ev["from_node"]) == "process" {
			edgeSelectedEvents = append(edgeSelectedEvents, ev)
		}
	}
	if len(edgeSelectedEvents) == 0 {
		t.Fatalf("expected edge_selected event for process node, got none")
	}
	sel := edgeSelectedEvents[0]
	if anyToString(sel["to_node"]) != "path_a" {
		t.Errorf("edge_selected to_node: got %q want %q", anyToString(sel["to_node"]), "path_a")
	}
	if anyToString(sel["selection_method"]) != "condition_match" {
		t.Errorf("edge_selected selection_method: got %q want %q", anyToString(sel["selection_method"]), "condition_match")
	}
}

func TestDecisionLogging_HillClimber(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	pinned := writePinnedCatalog(t)

	// Hill-climber: verify fails until attempt 3, routing back to implement each time.
	// The unconditional fallback verify->implement satisfies validation and acts as the
	// default route when no condition matches.
	dot := []byte(`digraph hill_climber_decision_log {
  graph [goal="Test decision logging for hill-climber", default_max_retry=0]
  start [shape=Mdiamond]
  implement [
    shape=parallelogram,
    tool_command="count=$(cat attempt_counter 2>/dev/null || echo 0); count=$((count + 1)); echo $count > attempt_counter; echo implemented_attempt_$count"
  ]
  verify [
    shape=parallelogram,
    tool_command="count=$(cat attempt_counter); if [ $count -ge 3 ]; then echo 'all checks pass'; exit 0; else echo 'checks failed'; exit 1; fi"
  ]
  done [shape=Msquare]
  start -> implement
  implement -> verify
  verify -> done [condition="outcome=success"]
  verify -> implement [condition="outcome=fail"]
  verify -> implement
}`)
	cfg := minimalToolGraphConfig(repo, pinned)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{
		RunID:       "hill-climber-decision-log",
		LogsRoot:    logsRoot,
		DisableCXDB: true,
	})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("expected success, got %q", res.FinalStatus)
	}

	progressPath := filepath.Join(logsRoot, "progress.ndjson")
	events := mustReadProgressEventsFile(t, progressPath)

	// Collect edge_condition_evaluated events from verify node.
	var verifyCondEvents []map[string]any
	for _, ev := range events {
		if anyToString(ev["event"]) == "edge_condition_evaluated" &&
			anyToString(ev["node_id"]) == "verify" {
			verifyCondEvents = append(verifyCondEvents, ev)
		}
	}
	// verify runs 3 times: 2 failures routing to implement, 1 success routing to done.
	// Each time, both conditions are evaluated (outcome=success, outcome=fail).
	if len(verifyCondEvents) < 6 {
		t.Fatalf("expected at least 6 edge_condition_evaluated events for verify, got %d", len(verifyCondEvents))
	}

	// Verify the fail->implement edge is taken on early iterations.
	var failToImplement int
	var successToDone int
	for _, ev := range events {
		if anyToString(ev["event"]) == "edge_selected" &&
			anyToString(ev["from_node"]) == "verify" {
			to := anyToString(ev["to_node"])
			switch to {
			case "implement":
				failToImplement++
			case "done":
				successToDone++
			}
		}
	}
	if failToImplement < 2 {
		t.Errorf("expected at least 2 edge_selected verify->implement, got %d", failToImplement)
	}
	if successToDone != 1 {
		t.Errorf("expected 1 edge_selected verify->done, got %d", successToDone)
	}

	// Verify retry_decision events appear for verify node failures.
	var retryDecisions []map[string]any
	for _, ev := range events {
		if anyToString(ev["event"]) == "retry_decision" &&
			anyToString(ev["node_id"]) == "verify" {
			retryDecisions = append(retryDecisions, ev)
		}
	}
	if len(retryDecisions) < 2 {
		t.Errorf("expected at least 2 retry_decision events for verify, got %d", len(retryDecisions))
	}
}
