package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

func TestRun_WaitHuman_RoutesOnQueueInterviewerSelection(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	dot := []byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  gate  [shape=hexagon, label="Gate"]
  approve [shape=parallelogram, tool_command="echo approve"]
  fix     [shape=parallelogram, tool_command="echo fix"]
  exit  [shape=Msquare]

  start -> gate
  gate -> approve [label="[A] Approve"]
  gate -> fix     [label="[F] Fix"]
  approve -> exit
  fix -> exit
}
`)
	g, _, err := Prepare(dot)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	logsRoot := t.TempDir()
	opts := RunOptions{RepoPath: repo, RunID: "human", LogsRoot: logsRoot}
	if err := opts.applyDefaults(); err != nil {
		t.Fatalf("applyDefaults: %v", err)
	}

	eng := &Engine{
		Graph:           g,
		Options:         opts,
		DotSource:       append([]byte{}, dot...),
		LogsRoot:        opts.LogsRoot,
		WorktreeDir:     opts.WorktreeDir,
		Context:         runtime.NewContext(),
		Registry:        NewDefaultRegistry(),
		Interviewer:     &QueueInterviewer{Answers: []Answer{{Value: "F"}}},
		CodergenBackend: &SimulatedCodergenBackend{},
	}
	eng.RunBranch = fmt.Sprintf("%s/%s", opts.RunBranchPrefix, opts.RunID)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, err = eng.run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// Selected branch should execute "fix" and skip "approve".
	if _, err := os.Stat(filepath.Join(logsRoot, "fix", "status.json")); err != nil {
		t.Fatalf("expected fix to execute: %v", err)
	}
	if _, err := os.Stat(filepath.Join(logsRoot, "approve", "status.json")); err == nil {
		t.Fatalf("expected approve to be skipped")
	}

	// Human gate outcome should include selection in context_updates.
	b, err := os.ReadFile(filepath.Join(logsRoot, "gate", "status.json"))
	if err != nil {
		t.Fatalf("read gate status.json: %v", err)
	}
	out, err := runtime.DecodeOutcomeJSON(b)
	if err != nil {
		t.Fatalf("decode gate status.json: %v", err)
	}
	if got := fmt.Sprint(out.ContextUpdates["human.gate.selected"]); got != "fix" {
		t.Fatalf("human.gate.selected: %v", got)
	}
}

func TestWaitHumanHandler_TimeoutWithDefaultChoice(t *testing.T) {
	// Build a minimal graph: gate -> approve, gate -> fix
	g := newTestGraph(t, "gate", "[A] Approve", "approve", "[F] Fix", "fix")

	tests := []struct {
		name           string
		defaultChoice  string
		wantStatus     runtime.StageStatus
		wantNext       string // expected SuggestedNextIDs[0], empty if RETRY
		wantFailReason string
	}{
		{
			name:          "default matches key",
			defaultChoice: "F",
			wantStatus:    runtime.StatusSuccess,
			wantNext:      "fix",
		},
		{
			name:          "default matches target node ID",
			defaultChoice: "approve",
			wantStatus:    runtime.StatusSuccess,
			wantNext:      "approve",
		},
		{
			name:          "default case-insensitive match",
			defaultChoice: "a",
			wantStatus:    runtime.StatusSuccess,
			wantNext:      "approve",
		},
		{
			name:           "default empty — RETRY",
			defaultChoice:  "",
			wantStatus:     runtime.StatusRetry,
			wantFailReason: "human gate timeout, no default",
		},
		{
			name:           "default does not match any option — RETRY",
			defaultChoice:  "Z",
			wantStatus:     runtime.StatusRetry,
			wantFailReason: "human gate timeout, no default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := g.Nodes["gate"]
			if tt.defaultChoice != "" {
				// Set the attribute on a copy to avoid cross-test interference.
				cp := *node
				cp.Attrs = make(map[string]string, len(node.Attrs))
				for k, v := range node.Attrs {
					cp.Attrs[k] = v
				}
				cp.Attrs["human.default_choice"] = tt.defaultChoice
				node = &cp
			}

			exec := &Execution{
				Graph: g,
				Engine: &Engine{
					Interviewer: &CallbackInterviewer{Fn: func(q Question) Answer {
						return Answer{TimedOut: true}
					}},
				},
			}

			h := &WaitHumanHandler{}
			out, err := h.Execute(context.Background(), exec, node)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if out.Status != tt.wantStatus {
				t.Fatalf("status: got %v, want %v (failure_reason=%q)", out.Status, tt.wantStatus, out.FailureReason)
			}
			if tt.wantNext != "" {
				if len(out.SuggestedNextIDs) == 0 || out.SuggestedNextIDs[0] != tt.wantNext {
					t.Fatalf("SuggestedNextIDs: got %v, want [%s]", out.SuggestedNextIDs, tt.wantNext)
				}
			}
			if tt.wantFailReason != "" && out.FailureReason != tt.wantFailReason {
				t.Fatalf("FailureReason: got %q, want %q", out.FailureReason, tt.wantFailReason)
			}
		})
	}
}

// newTestGraph builds a minimal graph with a hexagon "gate" node and the given
// outgoing edges. Arguments are triples: (label, target, label, target, ...).
func newTestGraph(t *testing.T, gateID string, edgeLabelTargets ...string) *model.Graph {
	t.Helper()
	if len(edgeLabelTargets)%2 != 0 {
		t.Fatal("edgeLabelTargets must be pairs of (label, target)")
	}
	g := model.NewGraph("test")
	gateNode := model.NewNode(gateID)
	gateNode.Attrs["shape"] = "hexagon"
	gateNode.Attrs["label"] = "Gate"
	if err := g.AddNode(gateNode); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < len(edgeLabelTargets); i += 2 {
		label := edgeLabelTargets[i]
		target := edgeLabelTargets[i+1]
		targetNode := model.NewNode(target)
		if err := g.AddNode(targetNode); err != nil {
			// node already exists — fine
		}
		e := model.NewEdge(gateID, target)
		e.Attrs["label"] = label
		if err := g.AddEdge(e); err != nil {
			t.Fatal(err)
		}
	}
	return g
}

