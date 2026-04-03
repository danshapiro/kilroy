// Output contract: graphs declare expected output artifacts.
// After a run completes, the engine collects declared outputs to a known location
// and records their paths for status display and API access.
package engine

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/danshapiro/kilroy/internal/attractor/model"
)

// DeclaredOutputs parses the graph's outputs attribute into a list of file names.
func DeclaredOutputs(g *model.Graph) []string {
	if g == nil {
		return nil
	}
	raw := strings.TrimSpace(g.Attrs["outputs"])
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	var names []string
	for _, p := range parts {
		if name := strings.TrimSpace(p); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// OutputResult describes a declared output and whether it was found.
type OutputResult struct {
	Name      string `json:"name"`
	Path      string `json:"path,omitempty"`
	Found     bool   `json:"found"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
}

// CollectOutputs searches for declared outputs in the worktree and copies them
// to the outputs directory under logs_root. Returns the results and any warnings.
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
			warnings = append(warnings, "declared output not found: "+name)
			results = append(results, OutputResult{Name: name, Found: false})
			continue
		}

		dstPath := filepath.Join(outputsDir, name)
		// Create parent directories for nested outputs.
		_ = os.MkdirAll(filepath.Dir(dstPath), 0o755)
		data, err := os.ReadFile(srcPath)
		if err != nil {
			warnings = append(warnings, "declared output unreadable: "+name+": "+err.Error())
			results = append(results, OutputResult{Name: name, Found: false})
			continue
		}
		if err := os.WriteFile(dstPath, data, 0o644); err != nil {
			warnings = append(warnings, "could not collect output: "+name+": "+err.Error())
			results = append(results, OutputResult{Name: name, Path: srcPath, Found: true, SizeBytes: info.Size()})
			continue
		}
		results = append(results, OutputResult{
			Name:      name,
			Path:      dstPath,
			Found:     true,
			SizeBytes: info.Size(),
		})
	}
	return results, warnings
}

// CollectAndRecordOutputs runs output collection and records results in the engine.
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
		e.Warn(w)
	}

	// Write output manifest.
	if err := writeJSON(filepath.Join(e.LogsRoot, "outputs.json"), results); err != nil {
		e.Warn("write outputs.json: " + err.Error())
	}

	// Record in RunDB.
	if e.RunDB != nil {
		for _, r := range results {
			if r.Found {
				e.appendProgress(map[string]any{
					"event":      "output_collected",
					"name":       r.Name,
					"path":       r.Path,
					"size_bytes": r.SizeBytes,
				})
			}
		}
	}
}
