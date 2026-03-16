package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/danshapiro/kilroy/internal/attractor/model"
)

// Transform can mutate the parsed graph between parse and validate (attractor-spec DoD).
type Transform interface {
	ID() string
	Apply(g *model.Graph) error
}

// TransformRegistry stores transforms to apply in registration order.
type TransformRegistry struct {
	transforms []Transform
}

func NewTransformRegistry() *TransformRegistry { return &TransformRegistry{} }

func (r *TransformRegistry) Register(t Transform) {
	if r == nil || t == nil {
		return
	}
	r.transforms = append(r.transforms, t)
}

func (r *TransformRegistry) List() []Transform {
	if r == nil || len(r.transforms) == 0 {
		return nil
	}
	return append([]Transform{}, r.transforms...)
}

// Built-in transforms.

type goalExpansionTransform struct{}

func (t goalExpansionTransform) ID() string { return "expand_goal" }
func (t goalExpansionTransform) Apply(g *model.Graph) error {
	expandGoal(g)
	return nil
}

// expandPromptFiles resolves prompt_file attributes to inline prompt content.
// For each node with a prompt_file attribute, the file is read relative to repoPath
// and its contents replace the node's prompt. This runs before $goal expansion so
// variables like $goal and $base_sha are still expanded in the loaded content.
//
// Rules:
//   - prompt_file is resolved relative to repoPath (the repository root).
//   - If both prompt and prompt_file are set, it is an error (ambiguous).
//   - If the referenced file does not exist or is unreadable, it is an error.
//   - After expansion, prompt_file is removed from the node attributes.
func expandPromptFiles(g *model.Graph, repoPath string) error {
	if repoPath == "" {
		return nil
	}
	for _, n := range g.Nodes {
		if n == nil {
			continue
		}
		pf := strings.TrimSpace(n.Attrs["prompt_file"])
		if pf == "" {
			continue
		}

		// Conflict check: prompt_file and prompt are mutually exclusive.
		if strings.TrimSpace(n.Attrs["prompt"]) != "" || strings.TrimSpace(n.Attrs["llm_prompt"]) != "" {
			return fmt.Errorf("node %q: prompt_file and prompt are mutually exclusive", n.ID)
		}

		// Resolve path relative to repo root.
		resolved := pf
		if !filepath.IsAbs(pf) {
			resolved = filepath.Join(repoPath, pf)
		}
		data, err := os.ReadFile(resolved)
		if err != nil {
			return fmt.Errorf("node %q: prompt_file %q: %w", n.ID, pf, err)
		}
		n.Attrs["prompt"] = string(data)
		delete(n.Attrs, "prompt_file")
	}
	return nil
}
