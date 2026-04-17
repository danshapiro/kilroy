package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

func TestParseEscalationModels_Empty(t *testing.T) {
	chain := parseEscalationModels("")
	if len(chain) != 0 {
		t.Fatalf("expected empty chain, got %d entries", len(chain))
	}
}

func TestParseEscalationModels_SingleEntry(t *testing.T) {
	chain := parseEscalationModels("kimi:kimi-k2.5")
	if len(chain) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(chain))
	}
	wantProv := normalizeProviderKey("kimi")
	if chain[0].Provider != wantProv || chain[0].Model != "kimi-k2.5" {
		t.Fatalf("got %+v, want provider=%q", chain[0], wantProv)
	}
}

func TestParseEscalationModels_MultipleEntries(t *testing.T) {
	chain := parseEscalationModels("kimi:kimi-k2.5, google:gemini-pro, anthropic:claude-opus-4-6")
	if len(chain) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(chain))
	}
	expected := []providerModel{
		{Provider: normalizeProviderKey("kimi"), Model: "kimi-k2.5"},
		{Provider: normalizeProviderKey("google"), Model: "gemini-pro"},
		{Provider: normalizeProviderKey("anthropic"), Model: "claude-opus-4-6"},
	}
	for i, e := range expected {
		if chain[i].Provider != e.Provider || chain[i].Model != e.Model {
			t.Errorf("entry %d: got %+v, want %+v", i, chain[i], e)
		}
	}
}

func TestParseEscalationModels_WhitespaceHandling(t *testing.T) {
	chain := parseEscalationModels("  kimi : kimi-k2.5 ,  google : gemini-pro  ")
	if len(chain) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(chain))
	}
	if chain[0].Provider != normalizeProviderKey("kimi") || chain[1].Provider != normalizeProviderKey("google") {
		t.Fatalf("providers: %q, %q", chain[0].Provider, chain[1].Provider)
	}
}

func TestParseEscalationModels_InvalidEntrySkipped(t *testing.T) {
	chain := parseEscalationModels("kimi:kimi-k2.5, badentry, google:gemini-pro")
	if len(chain) != 2 {
		t.Fatalf("expected 2 entries (invalid skipped), got %d", len(chain))
	}
}

func TestRetriesBeforeEscalation_Default(t *testing.T) {
	got := retriesBeforeEscalation(nil)
	if got != defaultRetriesBeforeEscalation {
		t.Fatalf("got %d, want %d", got, defaultRetriesBeforeEscalation)
	}
}

// --- Integration tests for escalation in executeWithRetry ---

// modelTrackingHandler records the llm_provider:llm_model on the node at each call
// and returns a fixed outcome.
type modelTrackingHandler struct {
	models  []string
	outcome func(call int) runtime.Outcome
}

func (h *modelTrackingHandler) Execute(_ context.Context, _ *Execution, node *model.Node) (runtime.Outcome, error) {
	h.models = append(h.models, node.Attr("llm_provider", "")+"::"+node.Attr("llm_model", ""))
	return h.outcome(len(h.models)), nil
}

func newEscalationTestEngine(t *testing.T, logsRoot string, maxRetries int, escalationModels string, rbe string, handler Handler) (*Engine, *model.Node) {
	t.Helper()

	graphAttrs := `retry.backoff.initial_delay_ms=0`
	if rbe != "" {
		graphAttrs += fmt.Sprintf(`, retries_before_escalation="%s"`, rbe)
	}
	nodeAttrs := fmt.Sprintf(`shape=diamond, type="escalation_test", max_retries="%d", llm_model="default-model", llm_provider="default-prov"`, maxRetries)
	if escalationModels != "" {
		nodeAttrs += fmt.Sprintf(`, escalation_models="%s"`, escalationModels)
	}

	dot := []byte(fmt.Sprintf(`
digraph G {
  graph [%s]
  start [shape=Mdiamond]
  r [%s]
  exit [shape=Msquare]
  start -> r -> exit
}
`, graphAttrs, nodeAttrs))

	g, _, err := Prepare(dot)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	opts := RunOptions{
		RunID:       "escalation-test",
		LogsRoot:    logsRoot,
		WorktreeDir: filepath.Join(logsRoot, "worktree"),
	}
	eng := &Engine{
		Graph:        g,
		Options:      opts,
		LogsRoot:     logsRoot,
		WorktreeDir:  opts.WorktreeDir,
		Context:      runtime.NewContext(),
		Registry:     NewDefaultRegistry(),
		Interviewer:  &AutoApproveInterviewer{},
		AgentBackend: &SimulatedAgentBackend{},
	}
	eng.Registry.Register("escalation_test", handler)
	node := g.Nodes["r"]
	if node == nil {
		t.Fatalf("missing node r")
	}
	return eng, node
}

func TestExecuteWithRetry_EscalatesOnBudgetExhausted(t *testing.T) {
	// Node always fails with budget_exhausted, escalation chain of 2 models,
	// retries_before_escalation=1 (2 attempts per model).
	// With max_retries=5 (6 total attempts), we expect:
	//   attempt 1-2: default model
	//   attempt 3-4: first escalation model
	//   attempt 5-6: second escalation model
	logsRoot := t.TempDir()
	handler := &modelTrackingHandler{
		outcome: func(call int) runtime.Outcome {
			return runtime.Outcome{
				Status:        runtime.StatusFail,
				FailureReason: "turn limit reached (max_turns=60)",
				Meta:          map[string]any{"failure_class": "budget_exhausted"},
			}
		},
	}
	eng, node := newEscalationTestEngine(t, logsRoot, 5, "esc1:esc1-model, esc2:esc2-model", "1", handler)

	out, err := eng.executeWithRetry(context.Background(), node, map[string]int{})
	if err != nil {
		t.Fatalf("executeWithRetry: %v", err)
	}
	if out.Status != runtime.StatusFail {
		t.Fatalf("expected fail, got %s", out.Status)
	}
	if len(handler.models) != 6 {
		t.Fatalf("expected 6 attempts, got %d: %v", len(handler.models), handler.models)
	}

	// Verify model progression: 2x default, 2x esc1, 2x esc2
	expected := []string{
		"default-prov::default-model",
		"default-prov::default-model",
		"esc1::esc1-model",
		"esc1::esc1-model",
		"esc2::esc2-model",
		"esc2::esc2-model",
	}
	for i, want := range expected {
		if handler.models[i] != want {
			t.Errorf("attempt %d: got %q, want %q", i+1, handler.models[i], want)
		}
	}

	// Verify node attrs restored to original values
	if node.Attr("llm_model", "") != "default-model" {
		t.Errorf("node llm_model not restored: %q", node.Attr("llm_model", ""))
	}
	if node.Attr("llm_provider", "") != "default-prov" {
		t.Errorf("node llm_provider not restored: %q", node.Attr("llm_provider", ""))
	}

	// Verify escalation progress event was emitted
	if !hasProgressEvent(t, logsRoot, "escalation_model_switch") {
		t.Errorf("expected escalation_model_switch progress event")
	}
}

func TestExecuteWithRetry_EscalationSucceedsOnSecondModel(t *testing.T) {
	// First model fails twice with budget_exhausted, escalated model succeeds.
	logsRoot := t.TempDir()
	handler := &modelTrackingHandler{
		outcome: func(call int) runtime.Outcome {
			if call <= 2 {
				return runtime.Outcome{
					Status:        runtime.StatusFail,
					FailureReason: "turn limit reached",
					Meta:          map[string]any{"failure_class": "budget_exhausted"},
				}
			}
			return runtime.Outcome{Status: runtime.StatusSuccess}
		},
	}
	eng, node := newEscalationTestEngine(t, logsRoot, 5, "esc1:esc1-model", "1", handler)

	out, err := eng.executeWithRetry(context.Background(), node, map[string]int{})
	if err != nil {
		t.Fatalf("executeWithRetry: %v", err)
	}
	if out.Status != runtime.StatusSuccess {
		t.Fatalf("expected success, got %s", out.Status)
	}
	if len(handler.models) != 3 {
		t.Fatalf("expected 3 attempts, got %d: %v", len(handler.models), handler.models)
	}
	// First 2 attempts: default. Third: escalated (and succeeds).
	if handler.models[0] != "default-prov::default-model" {
		t.Errorf("attempt 1: got %q", handler.models[0])
	}
	if handler.models[2] != "esc1::esc1-model" {
		t.Errorf("attempt 3: got %q, want esc1::esc1-model", handler.models[2])
	}

	// Verify attrs restored even after success during escalation
	if node.Attr("llm_model", "") != "default-model" {
		t.Errorf("node llm_model not restored: %q", node.Attr("llm_model", ""))
	}
}

func TestExecuteWithRetry_NoEscalationOnTransientInfra(t *testing.T) {
	// Transient infra failures should retry with the same model, not escalate.
	logsRoot := t.TempDir()
	handler := &modelTrackingHandler{
		outcome: func(call int) runtime.Outcome {
			return runtime.Outcome{
				Status:        runtime.StatusFail,
				FailureReason: "connection timeout",
				Meta:          map[string]any{"failure_class": "transient_infra"},
			}
		},
	}
	// retries_before_escalation=0 means escalate immediately — but only for escalatable classes
	eng, node := newEscalationTestEngine(t, logsRoot, 3, "esc1:esc1-model", "0", handler)

	eng.executeWithRetry(context.Background(), node, map[string]int{})

	// All attempts should use default model (no escalation for transient)
	for i, m := range handler.models {
		if m != "default-prov::default-model" {
			t.Errorf("attempt %d: got %q, want default model (transient should not escalate)", i+1, m)
		}
	}
}

func TestExecuteWithRetry_NoEscalationWithoutAttribute(t *testing.T) {
	// Without escalation_models attribute, behavior is unchanged.
	logsRoot := t.TempDir()
	handler := &modelTrackingHandler{
		outcome: func(call int) runtime.Outcome {
			return runtime.Outcome{
				Status:        runtime.StatusFail,
				FailureReason: "turn limit reached",
				Meta:          map[string]any{"failure_class": "budget_exhausted"},
			}
		},
	}
	eng, node := newEscalationTestEngine(t, logsRoot, 2, "", "", handler)

	eng.executeWithRetry(context.Background(), node, map[string]int{})

	// All attempts use default model (no escalation chain defined)
	for i, m := range handler.models {
		if m != "default-prov::default-model" {
			t.Errorf("attempt %d: got %q, want default model", i+1, m)
		}
	}
}
