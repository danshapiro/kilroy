package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/strongdm/kilroy/internal/attractor/runtime"
)

func TestFanIn_AllFail_AggregatesFailureClass(t *testing.T) {
	tests := []struct {
		name      string
		results   []parallelBranchResult
		wantClass string
	}{
		{
			name: "deterministic_if_any_branch_deterministic",
			results: []parallelBranchResult{
				{
					BranchKey: "a",
					Outcome: runtime.Outcome{
						Status:        runtime.StatusFail,
						FailureReason: "request timeout after 10s",
					},
				},
				{
					BranchKey: "b",
					Outcome: runtime.Outcome{
						Status:        runtime.StatusFail,
						FailureReason: "unknown flag: --verbose",
					},
				},
			},
			wantClass: string(failureClassDeterministic),
		},
		{
			name: "transient_only_when_all_branches_transient",
			results: []parallelBranchResult{
				{
					BranchKey: "a",
					Outcome: runtime.Outcome{
						Status:        runtime.StatusFail,
						FailureReason: "request timeout after 10s",
					},
				},
				{
					BranchKey: "b",
					Outcome: runtime.Outcome{
						Status:        runtime.StatusFail,
						FailureReason: "connection reset by peer",
					},
				},
			},
			wantClass: string(failureClassTransientInfra),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := runtime.NewContext()
			ctx.Set("parallel.results", tc.results)
			h := &FanInHandler{}
			out, err := h.Execute(context.Background(), &Execution{
				Context:     ctx,
				WorktreeDir: t.TempDir(),
			}, nil)
			if err != nil {
				t.Fatalf("Execute error: %v", err)
			}
			if out.Status != runtime.StatusFail {
				t.Fatalf("status=%q want=%q", out.Status, runtime.StatusFail)
			}

			classVal := strings.TrimSpace(anyToString(out.Meta[failureMetaClass]))
			if classVal != tc.wantClass {
				t.Fatalf("meta[%s]=%q want=%q", failureMetaClass, classVal, tc.wantClass)
			}
			signature := strings.TrimSpace(anyToString(out.Meta[failureMetaSignature]))
			if signature == "" {
				t.Fatalf("meta[%s] should be non-empty", failureMetaSignature)
			}
		})
	}
}
