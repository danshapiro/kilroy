// Tests for the input contract: loading, validation, injection, expansion.
package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

func TestLoadInputFile_YAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input.yaml")
	_ = os.WriteFile(path, []byte("pr_number: 42\npr_repo: danshapiro/kilroy\n"), 0o644)
	values, err := LoadInputFile(path)
	if err != nil {
		t.Fatalf("LoadInputFile: %v", err)
	}
	if values["pr_repo"] != "danshapiro/kilroy" {
		t.Fatalf("pr_repo = %v, want danshapiro/kilroy", values["pr_repo"])
	}
}

func TestLoadInputFile_JSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input.json")
	_ = os.WriteFile(path, []byte(`{"pr_number": 42, "pr_repo": "danshapiro/kilroy"}`), 0o644)
	values, err := LoadInputFile(path)
	if err != nil {
		t.Fatalf("LoadInputFile: %v", err)
	}
	if v, ok := values["pr_number"].(float64); !ok || v != 42 {
		t.Fatalf("pr_number = %v, want 42", values["pr_number"])
	}
}

func TestLoadInputString(t *testing.T) {
	values, err := LoadInputString(`{"key": "value", "count": 3}`)
	if err != nil {
		t.Fatalf("LoadInputString: %v", err)
	}
	if values["key"] != "value" {
		t.Fatalf("key = %v, want value", values["key"])
	}
}

func TestValidateRequiredInputs_AllPresent(t *testing.T) {
	g := model.NewGraph("test")
	g.Attrs["inputs"] = "pr_number,pr_repo"
	values := map[string]any{"pr_number": 42, "pr_repo": "foo/bar"}
	if err := ValidateRequiredInputs(g, values); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRequiredInputs_Missing(t *testing.T) {
	g := model.NewGraph("test")
	g.Attrs["inputs"] = "pr_number,pr_repo"
	values := map[string]any{"pr_number": 42}
	err := ValidateRequiredInputs(g, values)
	if err == nil {
		t.Fatal("expected error for missing input")
	}
	if !strings.Contains(err.Error(), "pr_repo") {
		t.Fatalf("error should mention pr_repo: %v", err)
	}
}

func TestValidateRequiredInputs_NoDeclared(t *testing.T) {
	g := model.NewGraph("test")
	// No inputs attribute — anything goes.
	if err := ValidateRequiredInputs(g, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExpandInputVariables(t *testing.T) {
	g := model.NewGraph("test")
	n := model.NewNode("step")
	n.Attrs["prompt"] = "Review PR $input.pr_number in $input.pr_repo"
	_ = g.AddNode(n)

	values := map[string]any{"pr_number": 42, "pr_repo": "foo/bar"}
	ExpandInputVariables(g, values)

	got := g.Nodes["step"].Attrs["prompt"]
	if !strings.Contains(got, "42") || !strings.Contains(got, "foo/bar") {
		t.Fatalf("prompt = %q, want expanded values", got)
	}
	if strings.Contains(got, "$input.") {
		t.Fatalf("prompt still has $input. placeholders: %q", got)
	}
}

func TestInputEnvVars(t *testing.T) {
	values := map[string]any{"pr_number": 42, "pr_repo": "foo/bar"}
	env := InputEnvVars(values)
	if env["KILROY_INPUT_PR_NUMBER"] != "42" {
		t.Fatalf("KILROY_INPUT_PR_NUMBER = %q, want 42", env["KILROY_INPUT_PR_NUMBER"])
	}
	if env["KILROY_INPUT_PR_REPO"] != "foo/bar" {
		t.Fatalf("KILROY_INPUT_PR_REPO = %q, want foo/bar", env["KILROY_INPUT_PR_REPO"])
	}
}

func TestInjectInputsIntoContext(t *testing.T) {
	ctx := runtime.NewContext()
	values := map[string]any{"pr_number": 42}
	InjectInputsIntoContext(ctx, values)
	if got := ctx.GetString("input.pr_number", ""); got == "" {
		t.Fatal("input.pr_number not found in context")
	}
}

func TestInputContract_ToolGraphWithInputs(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	pinned := writePinnedCatalog(t)

	dot := []byte(`digraph input_test {
  graph [goal="Test input contract", inputs="greeting"]
  start [shape=Mdiamond]
  greet [shape=parallelogram, tool_command="echo $KILROY_INPUT_GREETING"]
  done [shape=Msquare]
  start -> greet -> done
}`)
	cfg := minimalToolGraphConfig(repo, pinned)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{
		RunID:       "input-test-001",
		LogsRoot:    logsRoot,
		DisableCXDB: true,
		Inputs:      map[string]any{"greeting": "hello_world"},
	})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("expected success, got %q", res.FinalStatus)
	}

	// Verify the tool saw the input env var.
	stdout, err := os.ReadFile(filepath.Join(logsRoot, "greet", "stdout.log"))
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if !strings.Contains(string(stdout), "hello_world") {
		t.Fatalf("stdout = %q, want hello_world", string(stdout))
	}
}
