package engine

import (
	"testing"
)

func TestPrepare_ReferenceStyleDotfile(t *testing.T) {
	dot := []byte(`digraph Workflow {
		graph [
			goal="Test reference compatibility",
			context_fidelity_default="truncate",
			context_thread_default="test-thread",
			default_max_retry="2"
		]

		Start [shape=Mdiamond, label="Start"]
		Exit  [shape=Msquare, label="Exit"]

		Work [
			shape=box,
			llm_provider=openai, llm_model="gpt-5.4",
			llm_prompt="Implement the thing. Goal: $goal\nWrite status.json: outcome=success",
			timeout="300"
		]

		Check [shape=diamond, label="Check"]

		Start -> Work
		Work -> Check
		Check -> Exit [condition="outcome=success"]
		Check -> Work [condition="outcome=fail", loop_restart=true]
		Check -> Exit
	}`)

	g, diags, err := Prepare(dot)
	if err != nil {
		t.Fatalf("Prepare() error: %v", err)
	}
	for _, d := range diags {
		t.Logf("diagnostic: [%s] %s: %s", d.Severity, d.Rule, d.Message)
	}

	// Verify prompt resolved from llm_prompt (with $goal expanded).
	workNode := g.Nodes["Work"]
	if workNode == nil {
		t.Fatal("Work node not found")
	}
	wantPrompt := "Implement the thing. Goal: Test reference compatibility\nWrite status.json: outcome=success"
	if got := workNode.Prompt(); got != wantPrompt {
		t.Errorf("Work.Prompt() = %q, want %q", got, wantPrompt)
	}

	// Verify context_fidelity_default is accessible.
	if got := g.Attrs["context_fidelity_default"]; got != "truncate" {
		t.Errorf("context_fidelity_default = %q, want %q", got, "truncate")
	}

	// Verify context_thread_default is accessible.
	if got := g.Attrs["context_thread_default"]; got != "test-thread" {
		t.Errorf("context_thread_default = %q, want %q", got, "test-thread")
	}

	// Verify timeout attribute is present on agent node.
	if got := workNode.Attr("timeout", ""); got != "300" {
		t.Errorf("Work.timeout = %q, want %q", got, "300")
	}

	// Verify loop_restart edge exists.
	found := false
	for _, e := range g.Edges {
		if e.From == "Check" && e.To == "Work" && e.Attr("loop_restart", "") == "true" {
			found = true
			break
		}
	}
	if !found {
		t.Error("loop_restart edge from Check -> Work not found")
	}
}
