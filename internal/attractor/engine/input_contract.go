// Input contract: structured inputs for graph runs.
// Graphs declare required inputs via graph attribute inputs="key1,key2".
// Inputs are loaded from --input YAML/JSON files and injected into the
// context as input.* keys and into tool_command env as KILROY_INPUT_*.
package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"

	"gopkg.in/yaml.v3"
)

// LoadInputFile reads a YAML or JSON input file and returns the values.
func LoadInputFile(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read input file: %w", err)
	}
	var values map[string]any
	// Try JSON first (it's a subset of YAML).
	if err := json.Unmarshal(data, &values); err != nil {
		// Fall back to YAML.
		if err := yaml.Unmarshal(data, &values); err != nil {
			return nil, fmt.Errorf("parse input file (not valid JSON or YAML): %w", err)
		}
	}
	return values, nil
}

// LoadInputString parses a JSON string of inputs (e.g. from --input '{"key": "value"}').
func LoadInputString(s string) (map[string]any, error) {
	var values map[string]any
	if err := json.Unmarshal([]byte(s), &values); err != nil {
		return nil, fmt.Errorf("parse input JSON: %w", err)
	}
	return values, nil
}

// ValidateRequiredInputs checks that all required inputs declared in the graph
// are present in the provided values. Returns an error listing missing inputs.
func ValidateRequiredInputs(g *model.Graph, values map[string]any) error {
	required := requiredInputs(g)
	if len(required) == 0 {
		return nil
	}
	var missing []string
	for _, key := range required {
		if _, ok := values[key]; !ok {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required inputs: %s (declared in graph attribute inputs=%q)",
			strings.Join(missing, ", "), g.Attrs["inputs"])
	}
	return nil
}

// requiredInputs parses the graph's inputs attribute into a list of keys.
func requiredInputs(g *model.Graph) []string {
	raw := strings.TrimSpace(g.Attrs["inputs"])
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	var keys []string
	for _, p := range parts {
		if k := strings.TrimSpace(p); k != "" {
			keys = append(keys, k)
		}
	}
	return keys
}

// InjectInputsIntoContext adds input values to the runtime context as input.* keys.
func InjectInputsIntoContext(ctx *runtime.Context, values map[string]any) {
	if ctx == nil || len(values) == 0 {
		return
	}
	for k, v := range values {
		ctx.Set("input."+k, v)
	}
	// Store the full inputs map for access by handlers.
	ctx.Set("inputs", values)
}

// InputEnvVars returns environment variables for input values.
// Keys are uppercased and prefixed with KILROY_INPUT_ (e.g. pr_number → KILROY_INPUT_PR_NUMBER).
func InputEnvVars(values map[string]any) map[string]string {
	if len(values) == 0 {
		return nil
	}
	env := make(map[string]string, len(values))
	for k, v := range values {
		envKey := "KILROY_INPUT_" + strings.ToUpper(strings.ReplaceAll(k, ".", "_"))
		env[envKey] = fmt.Sprint(v)
	}
	return env
}

// ExpandInputVariables replaces $input.key placeholders in node prompts.
func ExpandInputVariables(g *model.Graph, values map[string]any) {
	if g == nil || len(values) == 0 {
		return
	}
	for _, n := range g.Nodes {
		if n == nil {
			continue
		}
		for _, attrKey := range []string{"prompt", "llm_prompt"} {
			p := n.Attrs[attrKey]
			if p == "" {
				continue
			}
			for k, v := range values {
				placeholder := "$input." + k
				if strings.Contains(p, placeholder) {
					p = strings.ReplaceAll(p, placeholder, fmt.Sprint(v))
				}
			}
			n.Attrs[attrKey] = p
		}
	}
}
