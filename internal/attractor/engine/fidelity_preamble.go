package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

func buildFidelityPreamble(ctx *runtime.Context, runID string, goal string, fidelity string, prevNode string, completed []string) string {
	lines := []string{
		"Kilroy Context",
		fmt.Sprintf("RunID: %s", strings.TrimSpace(runID)),
		fmt.Sprintf("Goal: %s", strings.TrimSpace(goal)),
		fmt.Sprintf("Fidelity: %s", strings.TrimSpace(fidelity)),
	}
	if strings.TrimSpace(prevNode) != "" {
		lines = append(lines, fmt.Sprintf("PreviousNode: %s", strings.TrimSpace(prevNode)))
	}
	if len(completed) > 0 {
		lines = append(lines, fmt.Sprintf("CompletedNodes: %s", strings.Join(completed, ", ")))
	}

	// For truncate fidelity, intentionally keep the preamble minimal.
	if fidelity == "truncate" {
		return strings.Join(lines, "\n")
	}

	// Stable context dump (best-effort): key=value lines sorted by key.
	if ctx != nil {
		vals := ctx.SnapshotValues()
		keys := make([]string, 0, len(vals))
		for k := range vals {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		lines = append(lines, "Context:")
		maxKeys := 25
		if strings.HasPrefix(fidelity, "summary:") {
			maxKeys = 60
		}
		for i, k := range keys {
			if i >= maxKeys {
				lines = append(lines, fmt.Sprintf("... (%d more keys)", len(keys)-maxKeys))
				break
			}
			v := vals[k]
			lines = append(lines, fmt.Sprintf("- %s=%v", k, v))
		}
	}
	return strings.Join(lines, "\n")
}

func decodeCompletedNodes(ctx *runtime.Context) []string {
	if ctx == nil {
		return nil
	}
	v, ok := ctx.Get("completed_nodes")
	if !ok || v == nil {
		return nil
	}
	switch x := v.(type) {
	case []string:
		return append([]string{}, x...)
	case []any:
		out := make([]string, 0, len(x))
		for _, it := range x {
			s := strings.TrimSpace(fmt.Sprint(it))
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}
