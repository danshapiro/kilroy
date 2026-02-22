package engine

import "testing"

func TestPrepare_ExpandsGoalInPrompts(t *testing.T) {
	g, _, err := Prepare([]byte(`
digraph G {
  graph [goal="Do the thing"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="Goal is: $goal"]
  start -> a -> exit
}
`))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if got := g.Nodes["a"].Attr("prompt", ""); got != "Goal is: Do the thing" {
		t.Fatalf("prompt: %q", got)
	}
}

func TestExpandBaseSHA_ReplacesPlaceholder(t *testing.T) {
	g, _, err := Prepare([]byte(`
digraph G {
  graph [goal="Build it"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  verify [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="Lint changed files: git diff --name-only $base_sha | xargs eslint"]
  start -> verify -> exit
}
`))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	// $base_sha should still be present after Prepare (expanded at Run time, not parse time).
	if got := g.Nodes["verify"].Attr("prompt", ""); got != "Lint changed files: git diff --name-only $base_sha | xargs eslint" {
		t.Fatalf("before expandBaseSHA: prompt = %q", got)
	}

	expandBaseSHA(g, "abc123def")

	want := "Lint changed files: git diff --name-only abc123def | xargs eslint"
	if got := g.Nodes["verify"].Attr("prompt", ""); got != want {
		t.Fatalf("after expandBaseSHA: prompt = %q, want %q", got, want)
	}
}

func TestExpandBaseSHA_NoOpWhenEmpty(t *testing.T) {
	g, _, err := Prepare([]byte(`
digraph G {
  graph [goal="Build it"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="Check $base_sha here"]
  start -> a -> exit
}
`))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	expandBaseSHA(g, "")

	// Should be unchanged when baseSHA is empty.
	if got := g.Nodes["a"].Attr("prompt", ""); got != "Check $base_sha here" {
		t.Fatalf("expandBaseSHA with empty SHA changed prompt: %q", got)
	}
}
