// Output contract enforcement at graph and node levels.
// Graph-level: declared outputs collected to logs_root/outputs/ after run completion.
// Node-level: validates that files declared in the outputs= node attribute exist after execution.
package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

// OutputResult records whether a declared output was found and collected.
type OutputResult struct {
	Name      string `json:"name"`
	Found     bool   `json:"found"`
	Path      string `json:"path,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
}

// DeclaredOutputs parses the graph-level outputs attribute (comma-separated).
func DeclaredOutputs(g *model.Graph) []string {
	if g == nil {
		return nil
	}
	raw := strings.TrimSpace(g.Attrs["outputs"])
	if raw == "" {
		return nil
	}
	var files []string
	for _, f := range strings.Split(raw, ",") {
		f = strings.TrimSpace(f)
		if f != "" {
			files = append(files, f)
		}
	}
	return files
}

// CollectOutputs copies declared output files from the worktree to logsRoot/outputs/.
// Returns results for each declared output and warnings for missing files.
func CollectOutputs(declared []string, worktreeDir, logsRoot string) ([]OutputResult, []string) {
	if len(declared) == 0 {
		return nil, nil
	}
	outputsDir := filepath.Join(logsRoot, "outputs")
	_ = os.MkdirAll(outputsDir, 0o755)

	var results []OutputResult
	var warnings []string

	for _, name := range declared {
		srcPath := filepath.Join(worktreeDir, name)
		info, err := os.Stat(srcPath)
		if err != nil {
			results = append(results, OutputResult{Name: name, Found: false})
			warnings = append(warnings, fmt.Sprintf("declared output %q not found in workspace", name))
			continue
		}

		dstPath := filepath.Join(outputsDir, name)
		_ = os.MkdirAll(filepath.Dir(dstPath), 0o755)
		if err := copyFile(srcPath, dstPath, 0o644); err != nil {
			warnings = append(warnings, fmt.Sprintf("failed to collect output %q: %v", name, err))
			results = append(results, OutputResult{Name: name, Found: true, SizeBytes: info.Size()})
			continue
		}
		results = append(results, OutputResult{
			Name:      name,
			Found:     true,
			Path:      dstPath,
			SizeBytes: info.Size(),
		})
	}
	return results, warnings
}

// CollectAndRecordOutputs collects graph-level declared outputs and writes outputs.json.
func (e *Engine) CollectAndRecordOutputs() {
	if e == nil || e.Graph == nil {
		return
	}
	declared := DeclaredOutputs(e.Graph)
	if len(declared) == 0 {
		return
	}
	results, warnings := CollectOutputs(declared, e.WorktreeDir, e.LogsRoot)
	for _, w := range warnings {
		e.Warn("output: " + w)
	}
	outputsJSON := filepath.Join(e.LogsRoot, "outputs.json")
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		e.Warn("marshal outputs.json: " + err.Error())
		return
	}
	if err := os.WriteFile(outputsJSON, data, 0o644); err != nil {
		e.Warn("write outputs.json: " + err.Error())
	}
}


// parseOutputContract returns the list of declared output files from the node's
// outputs attribute. Returns nil if the attribute is not set (no contract).
func parseOutputContract(node *model.Node) []string {
	if node == nil {
		return nil
	}
	raw := strings.TrimSpace(node.Attr("outputs", ""))
	if raw == "" {
		return nil
	}
	var files []string
	for _, f := range strings.Split(raw, ",") {
		f = strings.TrimSpace(f)
		if f != "" {
			files = append(files, f)
		}
	}
	return files
}

// checkOutputContract verifies that all declared output files exist in the workspace.
// Returns the list of missing files (nil if all present or no contract declared).
func checkOutputContract(worktreeDir string, outputs []string) []string {
	if len(outputs) == 0 {
		return nil
	}
	var missing []string
	for _, f := range outputs {
		path := filepath.Join(worktreeDir, f)
		if _, err := os.Stat(path); err != nil {
			missing = append(missing, f)
		}
	}
	return missing
}

// enforceOutputContract checks the output contract and downgrades the outcome
// if declared outputs are missing. Returns the (possibly modified) outcome and
// true if the contract was violated.
func enforceOutputContract(e *Engine, node *model.Node, out runtime.Outcome, attempt int) (runtime.Outcome, bool) {
	outputs := parseOutputContract(node)
	if len(outputs) == 0 {
		return out, false
	}

	missing := checkOutputContract(e.WorktreeDir, outputs)
	if len(missing) == 0 {
		return out, false
	}

	// Build failure details.
	reason := fmt.Sprintf("output contract: missing %s", strings.Join(missing, ", "))
	details := fmt.Sprintf("Node %q declares outputs=%q.\nAfter execution, the following files were not found in the workspace:\n", node.ID, node.Attr("outputs", ""))
	for _, f := range missing {
		details += fmt.Sprintf("- %s\n", f)
	}

	// Write FEEDBACK.md.
	if err := writeFeedbackMD(e.WorktreeDir, node.ID, reason, attempt, details); err != nil {
		e.Warn("write FEEDBACK.md for output contract: " + err.Error())
	}

	// Downgrade outcome.
	out.Status = runtime.StatusFail
	out.FailureReason = reason
	return out, true
}
