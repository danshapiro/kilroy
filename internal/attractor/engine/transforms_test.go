package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danshapiro/kilroy/internal/attractor/model"
)

type setGraphAttrTransform struct {
	key   string
	value string
}

func (t setGraphAttrTransform) ID() string { return "set_attr" }
func (t setGraphAttrTransform) Apply(g *model.Graph) error {
	g.Attrs[t.key] = t.value
	return nil
}

type appendGraphAttrTransform struct {
	key    string
	suffix string
}

func (t appendGraphAttrTransform) ID() string { return "append_attr" }
func (t appendGraphAttrTransform) Apply(g *model.Graph) error {
	g.Attrs[t.key] = g.Attrs[t.key] + t.suffix
	return nil
}

type fixBadConditionTransform struct{}

func (t fixBadConditionTransform) ID() string { return "fix_condition" }
func (t fixBadConditionTransform) Apply(g *model.Graph) error {
	for _, e := range g.Edges {
		if e == nil {
			continue
		}
		if strings.TrimSpace(e.Attrs["condition"]) == "outcome=" {
			e.Attrs["condition"] = "outcome=success"
		}
	}
	return nil
}

func TestPrepare_Transforms_RunBetweenParseAndValidate_InRegistrationOrder(t *testing.T) {
	dot := []byte(`
digraph G {
  start [shape=Mdiamond]
  cond [shape=diamond]
  exit [shape=Msquare]
  start -> cond
  cond -> exit [condition="outcome="]
}
`)

	// No transforms: validation fails.
	if _, _, err := Prepare(dot); err == nil {
		t.Fatalf("expected validation error, got nil")
	}

	reg := NewTransformRegistry()
	reg.Register(setGraphAttrTransform{key: "x", value: "1"})
	reg.Register(appendGraphAttrTransform{key: "x", suffix: "2"})
	reg.Register(fixBadConditionTransform{})

	g, _, err := PrepareWithRegistry(dot, reg)
	if err != nil {
		t.Fatalf("PrepareWithRegistry: %v", err)
	}
	if got := g.Attrs["x"]; got != "12" {
		t.Fatalf("transform order: got %q want %q", got, "12")
	}
}

func TestExpandPromptFiles_LoadsFileContent(t *testing.T) {
	dir := t.TempDir()
	promptContent := "Build all models from spec section 5.\n$goal\n"
	if err := os.WriteFile(filepath.Join(dir, "prompts", "impl.md"), nil, 0o755); err != nil {
		// Create dir first.
	}
	_ = os.MkdirAll(filepath.Join(dir, "prompts"), 0o755)
	if err := os.WriteFile(filepath.Join(dir, "prompts", "impl.md"), []byte(promptContent), 0o644); err != nil {
		t.Fatalf("write prompt file: %v", err)
	}

	g := model.NewGraph("test")
	n := model.NewNode("build")
	n.Attrs["prompt_file"] = "prompts/impl.md"
	_ = g.AddNode(n)

	if err := expandPromptFiles(g, dir); err != nil {
		t.Fatalf("expandPromptFiles: %v", err)
	}

	got := g.Nodes["build"].Attrs["prompt"]
	if got != promptContent {
		t.Fatalf("prompt = %q, want %q", got, promptContent)
	}
	if _, ok := g.Nodes["build"].Attrs["prompt_file"]; ok {
		t.Fatal("prompt_file attribute should be removed after expansion")
	}
}

func TestExpandPromptFiles_ErrorOnConflict(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "p.md"), []byte("content"), 0o644)

	g := model.NewGraph("test")
	n := model.NewNode("build")
	n.Attrs["prompt_file"] = "p.md"
	n.Attrs["prompt"] = "inline prompt"
	_ = g.AddNode(n)

	err := expandPromptFiles(g, dir)
	if err == nil {
		t.Fatal("expected error for conflicting prompt and prompt_file")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExpandPromptFiles_ErrorOnMissingFile(t *testing.T) {
	dir := t.TempDir()

	g := model.NewGraph("test")
	n := model.NewNode("build")
	n.Attrs["prompt_file"] = "nonexistent.md"
	_ = g.AddNode(n)

	err := expandPromptFiles(g, dir)
	if err == nil {
		t.Fatal("expected error for missing prompt_file")
	}
	if !strings.Contains(err.Error(), "nonexistent.md") {
		t.Fatalf("error should mention file path: %v", err)
	}
}

func TestExpandPromptFiles_NoOpWithoutRepoPath(t *testing.T) {
	g := model.NewGraph("test")
	n := model.NewNode("build")
	n.Attrs["prompt_file"] = "prompts/impl.md"
	_ = g.AddNode(n)

	// Should not error even though file doesn't exist — no repoPath means skip.
	if err := expandPromptFiles(g, ""); err != nil {
		t.Fatalf("expandPromptFiles with empty repoPath: %v", err)
	}
	// prompt_file should still be present (not resolved).
	if _, ok := g.Nodes["build"].Attrs["prompt_file"]; !ok {
		t.Fatal("prompt_file should remain when repoPath is empty")
	}
}

func TestExpandPromptFiles_GoalExpansionStillWorks(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "p.md"), []byte("Build: $goal"), 0o644)

	dotSrc := []byte(`
digraph G {
  goal="Build the app"
  start [shape=Mdiamond]
  build [shape=box, prompt_file="p.md", llm_provider=openai, llm_model=gpt-5.2]
  exit [shape=Msquare]
  start -> build -> exit
}
`)
	g, _, err := PrepareWithOptions(dotSrc, PrepareOptions{RepoPath: dir})
	if err != nil {
		t.Fatalf("PrepareWithOptions: %v", err)
	}
	got := g.Nodes["build"].Prompt()
	if !strings.Contains(got, "Build the app") {
		t.Fatalf("$goal not expanded in prompt_file content: %q", got)
	}
	if strings.Contains(got, "$goal") {
		t.Fatalf("$goal placeholder still present: %q", got)
	}
}
