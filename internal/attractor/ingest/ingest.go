package ingest

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/strongdm/kilroy/internal/attractor/engine"
)

//go:embed ingest_prompt.tmpl
var ingestPromptTmpl string

var ingestPrompt = template.Must(template.New("ingest").Parse(ingestPromptTmpl))

const outputFilename = "pipeline.dot"

// Options configures an ingestion run.
type Options struct {
	Requirements string // The English requirements text.
	SkillPath    string // Path to the SKILL.md file.
	Model        string // LLM model ID.
	RepoPath     string // Repository root (working directory for claude).
	Validate     bool   // Whether to validate the .dot output.
	MaxTurns     int    // Max turns for claude (default 15).
}

// Result contains the output of an ingestion run.
type Result struct {
	DotContent string   // The extracted .dot file content.
	Warnings   []string // Any validation warnings.
}

// buildPrompt renders the ingest prompt template with the given requirements.
func buildPrompt(requirements string) string {
	var buf bytes.Buffer
	_ = ingestPrompt.Execute(&buf, struct {
		Requirements string
	}{requirements})
	return buf.String()
}

func buildCLIArgs(opts Options) (string, []string, string, error) {
	exe := envOr("KILROY_CLAUDE_PATH", "claude")
	maxTurns := opts.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 15
	}

	args := []string{
		"--model", opts.Model,
		"--max-turns", fmt.Sprintf("%d", maxTurns),
		"--dangerously-skip-permissions",
	}

	// Give Claude read access to the repo without running inside it.
	// Resolve to absolute because Claude runs from a temp dir.
	if opts.RepoPath != "" {
		absRepo, err := filepath.Abs(opts.RepoPath)
		if err != nil {
			return "", nil, "", fmt.Errorf("resolving repo path: %w", err)
		}
		args = append(args, "--add-dir", absRepo)
	}

	if opts.SkillPath != "" {
		skillContent, err := os.ReadFile(opts.SkillPath)
		if err != nil {
			return "", nil, "", fmt.Errorf("reading skill file: %w", err)
		}
		if len(skillContent) > 0 {
			args = append(args, "--append-system-prompt", string(skillContent))
		}
	}

	// Create a temp working directory so Claude writes pipeline.dot here.
	tmpDir, err := os.MkdirTemp("", "kilroy-ingest-*")
	if err != nil {
		return "", nil, "", fmt.Errorf("creating temp directory: %w", err)
	}

	// The prompt is appended last as a positional argument.
	args = append(args, buildPrompt(opts.Requirements))

	return exe, args, tmpDir, nil
}

// Run executes the ingestion: invokes Claude Code interactively with the skill
// and requirements. Claude writes the .dot file to pipeline.dot in its working
// directory, which is read back after the session ends.
func Run(ctx context.Context, opts Options) (*Result, error) {
	// Verify skill file exists.
	if _, err := os.Stat(opts.SkillPath); err != nil {
		return nil, fmt.Errorf("skill file not found: %s: %w", opts.SkillPath, err)
	}

	exe, args, tmpDir, err := buildCLIArgs(opts)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = tmpDir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err = cmd.Run(); err != nil {
		return nil, fmt.Errorf("claude exited with error: %v", err)
	}

	// Read the .dot file Claude wrote.
	dotPath := filepath.Join(tmpDir, outputFilename)
	dotBytes, err := os.ReadFile(dotPath)
	if err != nil {
		return nil, fmt.Errorf("claude did not write %s: %w", outputFilename, err)
	}

	dotContent := strings.TrimSpace(string(dotBytes))
	if dotContent == "" {
		return nil, fmt.Errorf("%s is empty", outputFilename)
	}

	result := &Result{
		DotContent: dotContent,
	}

	// Optionally validate.
	if opts.Validate {
		_, diags, err := engine.PrepareWithOptions([]byte(dotContent), engine.PrepareOptions{RepoPath: opts.RepoPath})
		if err != nil {
			return result, fmt.Errorf("generated .dot failed validation: %w", err)
		}
		for _, d := range diags {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s: %s (%s)", d.Severity, d.Message, d.Rule))
		}
	}

	return result, nil
}

func envOr(key, def string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	return v
}
