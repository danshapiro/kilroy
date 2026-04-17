// Manages the .kilroy/ convention directory in the workspace.
// Writes standard files (INPUT.md, CONTEXT.md, TASK.md, FEEDBACK.md) for inter-node data exchange.
package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

const (
	kilroyDir       = ".kilroy"
	kilroyDataDir   = "KILROY_DATA_DIR"
	inputFileName   = "INPUT.md"
	contextFileName = "CONTEXT.md"
	taskFileName    = "TASK.md"
	feedbackFile    = "FEEDBACK.md"
)

// kilroyDirPath returns the absolute path to .kilroy/ in the workspace.
func kilroyDirPath(worktreeDir string) string {
	return filepath.Join(worktreeDir, kilroyDir)
}

// initKilroyDir creates the .kilroy/ directory and .kilroy/data/ subdirectory
// in the workspace. Called once at run start after the workspace is ready.
func initKilroyDir(worktreeDir string) error {
	kd := kilroyDirPath(worktreeDir)
	if err := os.MkdirAll(filepath.Join(kd, "data"), 0o755); err != nil {
		return fmt.Errorf("create .kilroy/: %w", err)
	}
	return nil
}

// writeInputMD writes .kilroy/INPUT.md from the run's structured inputs.
// Each key-value pair is rendered as a markdown section.
func writeInputMD(worktreeDir string, inputs map[string]any) error {
	if len(inputs) == 0 {
		return writeKilroyFile(worktreeDir, inputFileName, "# Input\n\nNo structured inputs provided for this run.\n")
	}

	var b strings.Builder
	b.WriteString("# Input\n\n")

	// Sort keys for deterministic output.
	keys := make([]string, 0, len(inputs))
	for k := range inputs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		v := inputs[k]
		b.WriteString(fmt.Sprintf("## %s\n\n%v\n\n", k, v))
	}
	return writeKilroyFile(worktreeDir, inputFileName, b.String())
}

// writeContextMD writes .kilroy/CONTEXT.md with accumulated run context.
// Called before each node execution to give it visibility into prior work.
func writeContextMD(worktreeDir string, completedNodes []string, nodeOutcomes map[string]string, currentNode string) error {
	var b strings.Builder
	b.WriteString("# Run Context\n\n")

	if len(completedNodes) == 0 {
		b.WriteString("This is the first node in the run. No prior context available.\n")
	} else {
		b.WriteString("## Completed Nodes\n\n")
		for _, nodeID := range completedNodes {
			outcome := nodeOutcomes[nodeID]
			if outcome == "" {
				outcome = "unknown"
			}
			b.WriteString(fmt.Sprintf("- **%s**: %s\n", nodeID, outcome))
		}
		b.WriteString("\n")
	}

	if currentNode != "" {
		b.WriteString(fmt.Sprintf("## Current Node\n\n%s\n", currentNode))
	}

	return writeKilroyFile(worktreeDir, contextFileName, b.String())
}

// writeTaskMD writes .kilroy/TASK.md with the current node's task description.
// Uses the node's prompt attribute if available, otherwise the label.
func writeTaskMD(worktreeDir, nodeID, prompt, label string) error {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("# Task: %s\n\n", nodeID))

	if prompt != "" {
		b.WriteString(prompt)
		b.WriteString("\n")
	} else if label != "" {
		b.WriteString(label)
		b.WriteString("\n")
	} else {
		b.WriteString("No task description available.\n")
	}

	return writeKilroyFile(worktreeDir, taskFileName, b.String())
}

// writeFeedbackMD writes .kilroy/FEEDBACK.md with failure information for retries.
func writeFeedbackMD(worktreeDir, nodeID, failureReason string, attempt int, details string) error {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("# Feedback: %s (attempt %d)\n\n", nodeID, attempt))
	b.WriteString(fmt.Sprintf("## Failure Reason\n\n%s\n\n", failureReason))
	if details != "" {
		b.WriteString(fmt.Sprintf("## Details\n\n%s\n", details))
	}
	return writeKilroyFile(worktreeDir, feedbackFile, b.String())
}

// clearFeedbackMD removes .kilroy/FEEDBACK.md (called on first attempt of a node).
func clearFeedbackMD(worktreeDir string) {
	_ = os.Remove(filepath.Join(kilroyDirPath(worktreeDir), feedbackFile))
}

// writeKilroyFile writes content to a file inside .kilroy/.
func writeKilroyFile(worktreeDir, name, content string) error {
	path := filepath.Join(kilroyDirPath(worktreeDir), name)
	return os.WriteFile(path, []byte(content), 0o644)
}

// writeKilroyPreNodeFiles writes CONTEXT.md and TASK.md before a node executes.
// Best-effort: errors are warnings, not fatal.
func (e *Engine) writeKilroyPreNodeFiles(node *model.Node, completed []string, nodeOutcomes map[string]runtime.Outcome) {
	if e == nil || node == nil {
		return
	}

	// Build outcome summary for CONTEXT.md.
	outcomeSummary := make(map[string]string, len(nodeOutcomes))
	for id, out := range nodeOutcomes {
		outcomeSummary[id] = string(out.Status)
		if out.FailureReason != "" {
			outcomeSummary[id] += " — " + out.FailureReason
		}
	}
	if err := writeContextMD(e.WorktreeDir, completed, outcomeSummary, node.ID); err != nil {
		e.Warn("write CONTEXT.md: " + err.Error())
	}

	// Resolve task description from prompt or label.
	prompt := strings.TrimSpace(node.Attr("prompt", ""))
	if prompt == "" {
		prompt = strings.TrimSpace(node.Attr("llm_prompt", ""))
	}
	label := strings.TrimSpace(node.Label())
	if err := writeTaskMD(e.WorktreeDir, node.ID, prompt, label); err != nil {
		e.Warn("write TASK.md: " + err.Error())
	}
}

// ensureGitignoreKilroy adds .kilroy/ to the workspace's .gitignore
// if it's not already listed. Safe to call when no .gitignore exists.
func ensureGitignoreKilroy(worktreeDir string) {
	gitignorePath := filepath.Join(worktreeDir, ".gitignore")
	existing, _ := os.ReadFile(gitignorePath)
	content := string(existing)

	// Check if .kilroy/ is already ignored.
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == ".kilroy/" || line == ".kilroy" || line == "/.kilroy/" || line == "/.kilroy" {
			return
		}
	}

	// Append .kilroy/ to .gitignore.
	if len(content) > 0 && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += ".kilroy/\n"
	_ = os.WriteFile(gitignorePath, []byte(content), 0o644)
}
