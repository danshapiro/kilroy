# Run-Scoped AI Explicit Imports Implementation Plan

> **For Claude:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement run-scoped `.ai/runs/<run_id>/...` scratch-state durability with explicit imports/fan-in promotion contracts, remove implicit root `.ai` ingestion, and migrate runtime/skill/docs surfaces to the new path model.

**Architecture:** Keep Appendix C.1 input materialization behavior intact while splitting concerns: static source-of-truth imports (`include`/`default_include` or new typed `imports`) vs dynamic run-scoped lineage state. Add a lineage manager under `logs_root/input_snapshot/` that tracks run/branch revision heads and drives branch/resume hydration and fan-in promotion deterministically. Migrate all hardcoded root `.ai` runtime fallbacks to run-scoped paths and update templates/skills/docs so new graphs/configs never reintroduce root `.ai` coupling.

**Tech Stack:** Go (`internal/attractor/engine`, `internal/attractor/runstate`, `cmd/kilroy`), YAML/JSON config parsing, DOT skill templates, Go test + repo CI checklist.

---

## Scope Check

This is one integrated subsystem change (input materialization boundary + run-scoped durability + runtime path migration + authoring surfaces). Splitting into independent plans would increase rework because config/schema, lineage hydration, and template guidance must land together to avoid mixed contracts.

## File Structure

### Engine config and schema
- Modify: `internal/attractor/engine/config.go`
  - Add typed `imports` and `fan_in.promote_run_scoped` schema fields.
  - Change `default_include` default from `.ai/*.md` to empty.
  - Enforce validation/mapping/conflict rules.
- Modify: `internal/attractor/engine/config_test.go`
  - Add schema/default/conflict regression coverage.

### Input materialization + lineage runtime
- Modify: `internal/attractor/engine/input_materialization.go`
  - Integrate imports mapping, run/branch/stage manifest revision metadata, and lineage-aware hydration hooks.
- Create: `internal/attractor/engine/input_snapshot_lineage.go`
  - Own revision graph model, run/branch heads, revision persistence, deterministic fan-in promotion merge, and conflict detection.
- Create: `internal/attractor/engine/input_snapshot_lineage_test.go`
  - Unit coverage for branch isolation, promotion expansion, deterministic ordering, conflict behavior.
- Modify: `internal/attractor/engine/parallel_handlers.go`
  - Apply fan-in promotion merge at join boundary without altering git winner/ff semantics.
- Note: Resume hydration wiring is implemented inside `internal/attractor/engine/input_materialization.go`; `resume.go` orchestration callsites remain unchanged.
- Modify tests:
  - `internal/attractor/engine/input_materialization_test.go`
  - `internal/attractor/engine/input_materialization_integration_test.go`
  - `internal/attractor/engine/input_materialization_resume_test.go`
  - `internal/attractor/engine/input_manifest_contract_test.go`
  - `internal/attractor/engine/parallel_test.go` and/or `parallel_guardrails_test.go` (fan-in promotion assertions)

### Runtime path migration surfaces
- Modify: `internal/attractor/engine/stage_status_contract.go`
- Modify: `internal/attractor/engine/stage_status_contract_test.go`
- Modify: `internal/attractor/engine/failure_dossier.go`
- Modify: `internal/attractor/engine/failure_dossier_test.go`
- Modify: `internal/attractor/runstate/snapshot.go`
- Modify: `internal/attractor/runstate/snapshot_test.go`
- Modify: `cmd/kilroy/attractor_status_follow.go`
- Modify prompt templates:
  - `internal/attractor/engine/prompts/stage_status_contract_preamble.tmpl`
  - `internal/attractor/engine/prompts/failure_dossier_preamble.tmpl`

### Skills/templates/docs/demos
- Modify: `skills/create-runfile/reference_run_template.yaml`
- Modify: `skills/create-runfile/SKILL.md` (@create-runfile)
- Modify: `skills/create-dotfile/SKILL.md` (@create-dotfile)
- Modify: `skills/create-dotfile/reference_template.dot` (@create-dotfile)
- Modify: `skills/build-dod/SKILL.md` (@build-dod)
- Modify guardrail tests:
  - `internal/attractor/validate/create_runfile_template_guardrail_test.go`
  - `internal/attractor/validate/reference_template_guardrail_test.go`
  - `internal/attractor/validate/input_materialization_contract_guardrail_test.go`
- Modify canonical examples that currently model root `.ai` run scratch:
  - `demo/substack-pipeline-v01.dot`
  - `docs/strongdm/dot specs/semport.dot`
  - Any additional matches from `rg -n "\.ai/" demo docs/strongdm -g '*.dot'` that represent run scratch state.
- Modify spec/docs references where defaults/path contracts are normative:
  - `docs/strongdm/attractor/attractor-spec.md`
  - `docs/strongdm/attractor/README.md`

## Chunk 1: Config Schema, Defaults, and Template Contract

### Task 1: Add `imports` + fan-in promotion schema and validation

**Files:**
- Create: `internal/attractor/engine/input_materialization_config.go`
- Modify: `internal/attractor/engine/config.go`
- Create: `internal/attractor/engine/input_materialization_config_test.go`
- Modify: `internal/attractor/engine/config_test.go`

- [ ] **Step 1: Write failing config tests for imports mapping/defaults/conflicts**

```go
func TestLoadRunConfigFile_InputMaterializationImportsMapping(t *testing.T) {
	cfg, err := loadRunConfigFromBytesForTest(t, []byte(`
version: 1
repo: { path: /tmp/repo }
cxdb: { binary_addr: 127.0.0.1:9009, http_base_url: http://127.0.0.1:9010 }
llm: { providers: { openai: { backend: api } } }
modeldb: { openrouter_model_info_path: /tmp/catalog.json }
inputs:
  materialize:
    imports:
      - pattern: docs/requirements.md
        required: true
      - pattern: docs/context/*.md
        required: false
`))
	if err != nil { t.Fatalf("LoadRunConfigFile: %v", err) }
	if got := strings.Join(cfg.Inputs.Materialize.Include, ","); got != "docs/requirements.md" {
		t.Fatalf("include=%q", got)
	}
	if got := strings.Join(cfg.Inputs.Materialize.DefaultInclude, ","); got != "docs/context/*.md" {
		t.Fatalf("default_include=%q", got)
	}
}
```

```go
func TestLoadRunConfigFile_InputMaterializationImportsConflict(t *testing.T) {
	_, err := loadRunConfigFromBytesForTest(t, []byte(`
version: 1
repo: { path: /tmp/repo }
cxdb: { binary_addr: 127.0.0.1:9009, http_base_url: http://127.0.0.1:9010 }
llm: { providers: { openai: { backend: api } } }
modeldb: { openrouter_model_info_path: /tmp/catalog.json }
inputs:
  materialize:
    include: [docs/required.md]
    imports:
      - pattern: docs/required.md
`))
	if err == nil || !strings.Contains(err.Error(), "failure_reason=input_imports_conflict") {
		t.Fatalf("expected deterministic failure_reason=input_imports_conflict, got %v", err)
	}
}
```

```go
func TestLoadRunConfigFile_InputMaterializationImportsDefaultIncludeConflict(t *testing.T) {
	_, err := loadRunConfigFromBytesForTest(t, []byte(`
version: 1
repo: { path: /tmp/repo }
cxdb: { binary_addr: 127.0.0.1:9009, http_base_url: http://127.0.0.1:9010 }
llm: { providers: { openai: { backend: api } } }
modeldb: { openrouter_model_info_path: /tmp/catalog.json }
inputs:
  materialize:
    default_include: [docs/optional/*.md]
    imports:
      - pattern: docs/required.md
`))
	if err == nil || !strings.Contains(err.Error(), "failure_reason=input_imports_conflict") {
		t.Fatalf("expected deterministic failure_reason=input_imports_conflict, got %v", err)
	}
}
```

```go
func TestLoadRunConfigFile_InputMaterializationImportsConflict_ExplicitEmptyLegacyFields(t *testing.T) {
	tests := []string{
		`
version: 1
repo: { path: /tmp/repo }
cxdb: { binary_addr: 127.0.0.1:9009, http_base_url: http://127.0.0.1:9010 }
llm: { providers: { openai: { backend: api } } }
modeldb: { openrouter_model_info_path: /tmp/catalog.json }
inputs:
  materialize:
    include: []
    imports:
      - pattern: docs/required.md
`,
		`
version: 1
repo: { path: /tmp/repo }
cxdb: { binary_addr: 127.0.0.1:9009, http_base_url: http://127.0.0.1:9010 }
llm: { providers: { openai: { backend: api } } }
modeldb: { openrouter_model_info_path: /tmp/catalog.json }
inputs:
  materialize:
    default_include: []
    imports:
      - pattern: docs/required.md
`,
	}
	for _, yml := range tests {
		_, err := loadRunConfigFromBytesForTest(t, []byte(yml))
		if err == nil || !strings.Contains(err.Error(), "failure_reason=input_imports_conflict") {
			t.Fatalf("expected deterministic failure_reason=input_imports_conflict, got %v", err)
		}
	}
}
```

```go
func TestLoadRunConfigFile_InputMaterializationImportsPatternRequired(t *testing.T) {
	_, err := loadRunConfigFromBytesForTest(t, []byte(`
version: 1
repo: { path: /tmp/repo }
cxdb: { binary_addr: 127.0.0.1:9009, http_base_url: http://127.0.0.1:9010 }
llm: { providers: { openai: { backend: api } } }
modeldb: { openrouter_model_info_path: /tmp/catalog.json }
inputs:
  materialize:
    imports:
      - pattern: "   "
`))
	if err == nil || !strings.Contains(err.Error(), "inputs.materialize.imports[0].pattern") {
		t.Fatalf("expected imports pattern validation error, got %v", err)
	}
}
```

```go
func TestLoadRunConfigFile_InputMaterializationImportsRequiredDefaultsTrue(t *testing.T) {
	cfg, err := loadRunConfigFromBytesForTest(t, []byte(`
version: 1
repo: { path: /tmp/repo }
cxdb: { binary_addr: 127.0.0.1:9009, http_base_url: http://127.0.0.1:9010 }
llm: { providers: { openai: { backend: api } } }
modeldb: { openrouter_model_info_path: /tmp/catalog.json }
inputs:
  materialize:
    imports:
      - pattern: docs/required.md
`))
	if err != nil {
		t.Fatalf("LoadRunConfigFile: %v", err)
	}
	if got := strings.Join(cfg.Inputs.Materialize.Include, ","); got != "docs/required.md" {
		t.Fatalf("required defaults to include, got %q", got)
	}
	if len(cfg.Inputs.Materialize.DefaultInclude) != 0 {
		t.Fatalf("default_include must be empty, got %v", cfg.Inputs.Materialize.DefaultInclude)
	}
}
```

```go
func TestLoadRunConfigFile_InputMaterializationImportsDedupeFirstSeenOrder(t *testing.T) {
	cfg, err := loadRunConfigFromBytesForTest(t, []byte(`
version: 1
repo: { path: /tmp/repo }
cxdb: { binary_addr: 127.0.0.1:9009, http_base_url: http://127.0.0.1:9010 }
llm: { providers: { openai: { backend: api } } }
modeldb: { openrouter_model_info_path: /tmp/catalog.json }
inputs:
  materialize:
    imports:
      - pattern: docs/a.md
      - pattern: docs/a.md
      - pattern: docs/b.md
        required: false
      - pattern: docs/b.md
        required: false
`))
	if err != nil {
		t.Fatalf("LoadRunConfigFile: %v", err)
	}
	if got := strings.Join(cfg.Inputs.Materialize.Include, ","); got != "docs/a.md" {
		t.Fatalf("include dedupe/order mismatch: %q", got)
	}
	if got := strings.Join(cfg.Inputs.Materialize.DefaultInclude, ","); got != "docs/b.md" {
		t.Fatalf("default_include dedupe/order mismatch: %q", got)
	}
}
```

```go
func TestLoadRunConfigFile_InputMaterializationImportsDedupe_FirstRequiredWins(t *testing.T) {
	cfg, err := loadRunConfigFromBytesForTest(t, []byte(`
version: 1
repo: { path: /tmp/repo }
cxdb: { binary_addr: 127.0.0.1:9009, http_base_url: http://127.0.0.1:9010 }
llm: { providers: { openai: { backend: api } } }
modeldb: { openrouter_model_info_path: /tmp/catalog.json }
inputs:
  materialize:
    imports:
      - pattern: docs/shared.md
        required: true
      - pattern: docs/shared.md
        required: false
`))
	if err != nil {
		t.Fatalf("LoadRunConfigFile: %v", err)
	}
	if got := strings.Join(cfg.Inputs.Materialize.Include, ","); got != "docs/shared.md" {
		t.Fatalf("include first-seen winner mismatch: %q", got)
	}
	if len(cfg.Inputs.Materialize.DefaultInclude) != 0 {
		t.Fatalf("default_include must not duplicate first-seen pattern, got %v", cfg.Inputs.Materialize.DefaultInclude)
	}
}
```

```go
func TestLoadRunConfigFile_InputMaterializationImportsDedupe_FirstBestEffortWins(t *testing.T) {
	cfg, err := loadRunConfigFromBytesForTest(t, []byte(`
version: 1
repo: { path: /tmp/repo }
cxdb: { binary_addr: 127.0.0.1:9009, http_base_url: http://127.0.0.1:9010 }
llm: { providers: { openai: { backend: api } } }
modeldb: { openrouter_model_info_path: /tmp/catalog.json }
inputs:
  materialize:
    imports:
      - pattern: docs/shared.md
        required: false
      - pattern: docs/shared.md
        required: true
`))
	if err != nil {
		t.Fatalf("LoadRunConfigFile: %v", err)
	}
	if len(cfg.Inputs.Materialize.Include) != 0 {
		t.Fatalf("include must remain empty when first-seen import is best-effort, got %v", cfg.Inputs.Materialize.Include)
	}
	if got := strings.Join(cfg.Inputs.Materialize.DefaultInclude, ","); got != "docs/shared.md" {
		t.Fatalf("default_include first-seen winner mismatch: %q", got)
	}
}
```

```go
func TestLoadRunConfigFile_InputMaterializationImportsUnknownFieldRejected(t *testing.T) {
	_, err := loadRunConfigFromBytesForTest(t, []byte(`
version: 1
repo: { path: /tmp/repo }
cxdb: { binary_addr: 127.0.0.1:9009, http_base_url: http://127.0.0.1:9010 }
llm: { providers: { openai: { backend: api } } }
modeldb: { openrouter_model_info_path: /tmp/catalog.json }
inputs:
  materialize:
    imports:
      - pattern: docs/required.md
        extra: nope
`))
	if err == nil || !strings.Contains(err.Error(), "extra") {
		t.Fatalf("expected unknown imports field error, got %v", err)
	}
}
```

```go
func TestLoadRunConfigFile_InputMaterializationLegacyIncludeDefaultIncludeStillWorks(t *testing.T) {
	cfg, err := loadRunConfigFromBytesForTest(t, []byte(`
version: 1
repo: { path: /tmp/repo }
cxdb: { binary_addr: 127.0.0.1:9009, http_base_url: http://127.0.0.1:9010 }
llm: { providers: { openai: { backend: api } } }
modeldb: { openrouter_model_info_path: /tmp/catalog.json }
inputs:
  materialize:
    include: [docs/required.md]
    default_include: [docs/optional/*.md]
`))
	if err != nil {
		t.Fatalf("legacy include/default_include must remain valid: %v", err)
	}
	if got := strings.Join(cfg.Inputs.Materialize.Include, ","); got != "docs/required.md" {
		t.Fatalf("include mismatch: %q", got)
	}
	if got := strings.Join(cfg.Inputs.Materialize.DefaultInclude, ","); got != "docs/optional/*.md" {
		t.Fatalf("default_include mismatch: %q", got)
	}
}
```

```go
func TestLoadRunConfigFile_InputMaterializationFanInPromoteValidation(t *testing.T) {
	tests := []struct{
		name string
		entry string
	}{
		{name: "absolute", entry: "/tmp/postmortem_latest.md"},
		{name: "windows-absolute", entry: "C:/tmp/postmortem_latest.md"},
		{name: "dotdot", entry: "../outside.md"},
		{name: "embedded-dotdot", entry: "safe/../escape.md"},
		{name: "empty", entry: "\"   \""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadRunConfigFromBytesForTest(t, []byte(fmt.Sprintf(`
version: 1
repo: { path: /tmp/repo }
cxdb: { binary_addr: 127.0.0.1:9009, http_base_url: http://127.0.0.1:9010 }
llm: { providers: { openai: { backend: api } } }
modeldb: { openrouter_model_info_path: /tmp/catalog.json }
inputs:
  materialize:
    fan_in:
      promote_run_scoped: [%s]
`, tc.entry)))
			if err == nil || !strings.Contains(err.Error(), "promote_run_scoped") {
				t.Fatalf("expected promote_run_scoped validation error, got %v", err)
			}
		})
	}
}
```

```go
func TestLoadRunConfigFile_InputMaterializationFanInPromoteNormalizeAndDedupe(t *testing.T) {
	cfg, err := loadRunConfigFromBytesForTest(t, []byte(`
version: 1
repo: { path: /tmp/repo }
cxdb: { binary_addr: 127.0.0.1:9009, http_base_url: http://127.0.0.1:9010 }
llm: { providers: { openai: { backend: api } } }
modeldb: { openrouter_model_info_path: /tmp/catalog.json }
inputs:
  materialize:
    fan_in:
      promote_run_scoped:
        - postmortem_latest.md
        - ./postmortem_latest.md
        - review_final.md
`))
	if err != nil {
		t.Fatalf("expected valid promote_run_scoped list, got %v", err)
	}
	got := strings.Join(cfg.Inputs.Materialize.FanIn.PromoteRunScoped, ",")
	want := "postmortem_latest.md,review_final.md"
	if got != want {
		t.Fatalf("promote_run_scoped normalize+dedupe: got %q want %q", got, want)
	}
}
```

```go
func TestLoadRunConfigFile_InputMaterializationFanInPromoteAllowsGlob(t *testing.T) {
	_, err := loadRunConfigFromBytesForTest(t, []byte(`
version: 1
repo: { path: /tmp/repo }
cxdb: { binary_addr: 127.0.0.1:9009, http_base_url: http://127.0.0.1:9010 }
llm: { providers: { openai: { backend: api } } }
modeldb: { openrouter_model_info_path: /tmp/catalog.json }
inputs:
  materialize:
    fan_in:
      promote_run_scoped:
        - "**/review_*.md"
`))
	if err != nil {
		t.Fatalf("glob entries must be accepted in config validation: %v", err)
	}
}
```

```go
func TestLoadRunConfigFile_InputMaterializationImportsConflict_JSONExplicitEmptyLegacyFields(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "run.json")
	if err := os.WriteFile(p, []byte(`{
  "version": 1,
  "repo": {"path": "/tmp/repo"},
  "cxdb": {"binary_addr":"127.0.0.1:9009","http_base_url":"http://127.0.0.1:9010"},
  "llm": {"providers": {"openai": {"backend":"api"}}},
  "modeldb": {"openrouter_model_info_path":"/tmp/catalog.json"},
  "inputs": {"materialize": {"include": [], "imports": [{"pattern":"docs/required.md"}]}}
}`), 0o644); err != nil {
		t.Fatalf("write run.json: %v", err)
	}
	_, err := LoadRunConfigFile(p)
	if err == nil || !strings.Contains(err.Error(), "failure_reason=input_imports_conflict") {
		t.Fatalf("expected deterministic failure_reason=input_imports_conflict, got %v", err)
	}
}
```

```go
func TestLoadRunConfigFile_InputMaterializationDefaults_NoImplicitRootAI(t *testing.T) {
	cfg, err := loadRunConfigFromBytesForTest(t, []byte(`
version: 1
repo: { path: /tmp/repo }
cxdb: { binary_addr: 127.0.0.1:9009, http_base_url: http://127.0.0.1:9010 }
llm: { providers: { openai: { backend: api } } }
modeldb: { openrouter_model_info_path: /tmp/catalog.json }
`))
	if err != nil { t.Fatalf("LoadRunConfigFile: %v", err) }
	if len(cfg.Inputs.Materialize.DefaultInclude) != 0 {
		t.Fatalf("default_include default must be empty, got %v", cfg.Inputs.Materialize.DefaultInclude)
	}
}
```

- [ ] **Step 2: Run targeted config tests to verify failure first**

Run: `go test ./internal/attractor/engine -run 'TestLoadRunConfigFile_InputMaterialization|TestLoadRunConfigFile_InputMaterializationDefaultsAndValidation' -count=1`
Expected: FAIL with missing `imports` mapping/validation, missing `promote_run_scoped` validation, and old `.ai/*.md` default assertions.

- [ ] **Step 3: Implement schema + normalize/validate mapping rules**

```go
type InputImportEntry struct {
	Pattern  string `json:"pattern" yaml:"pattern"`
	Required *bool  `json:"required,omitempty" yaml:"required,omitempty"`
}

type InputMaterializationFanInConfig struct {
	PromoteRunScoped []string `json:"promote_run_scoped,omitempty" yaml:"promote_run_scoped,omitempty"`
}

type materializeFieldPresence struct {
	IncludeDeclared        bool
	DefaultIncludeDeclared bool
}

func detectMaterializeFieldPresence(path string, raw []byte) (materializeFieldPresence, error) {
	if strings.EqualFold(filepath.Ext(path), ".json") {
		return detectMaterializeFieldPresenceJSON(raw)
	}
	return detectMaterializeFieldPresenceYAML(raw)
}

func detectMaterializeFieldPresenceYAML(raw []byte) (materializeFieldPresence, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return materializeFieldPresence{}, err
	}
	doc := &root
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		doc = root.Content[0]
	}
	inputs := yamlChildMap(doc, "inputs")
	materialize := yamlChildMap(inputs, "materialize")
	return materializeFieldPresence{
		IncludeDeclared:        yamlMapHasKey(materialize, "include"),
		DefaultIncludeDeclared: yamlMapHasKey(materialize, "default_include"),
	}, nil
}

func detectMaterializeFieldPresenceJSON(raw []byte) (materializeFieldPresence, error) {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return materializeFieldPresence{}, err
	}
	m := nestedMap(root, "inputs", "materialize")
	_, includeDeclared := m["include"]
	_, defaultDeclared := m["default_include"]
	return materializeFieldPresence{
		IncludeDeclared:        includeDeclared,
		DefaultIncludeDeclared: defaultDeclared,
	}, nil
}

func nestedMap(root map[string]any, keys ...string) map[string]any {
	cur := root
	for _, key := range keys {
		next, ok := cur[key].(map[string]any)
		if !ok {
			return map[string]any{}
		}
		cur = next
	}
	return cur
}

func yamlChildMap(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		k := node.Content[i]
		v := node.Content[i+1]
		if strings.TrimSpace(k.Value) == key {
			return v
		}
	}
	return nil
}

func yamlMapHasKey(node *yaml.Node, key string) bool {
	if node == nil || node.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if strings.TrimSpace(node.Content[i].Value) == key {
			return true
		}
	}
	return false
}

var windowsAbsPathRE = regexp.MustCompile(`^[A-Za-z]:[\\\\/]`)

func isAbsolutePathLike(path string) bool {
	if filepath.IsAbs(path) {
		return true
	}
	if windowsAbsPathRE.MatchString(path) {
		return true
	}
	return strings.HasPrefix(path, `\\`)
}

func (m *InputMaterializationConfig) normalizeImports(p materializeFieldPresence) error {
	if len(m.Imports) == 0 {
		return nil // preserve legacy include/default_include behavior
	}
	if p.IncludeDeclared || p.DefaultIncludeDeclared {
		return fmt.Errorf("failure_reason=input_imports_conflict: imports cannot be combined with include/default_include")
	}
	required := []string{}
	bestEffort := []string{}
	seen := map[string]bool{}
	for i, imp := range m.Imports {
		pattern := strings.TrimSpace(imp.Pattern)
		if pattern == "" {
			return fmt.Errorf("inputs.materialize.imports[%d].pattern is required", i)
		}
		if seen[pattern] {
			continue // first-seen wins across required/best-effort buckets
		}
		isRequired := true
		if imp.Required != nil {
			isRequired = *imp.Required
		}
		if isRequired {
			required = append(required, pattern)
		} else {
			bestEffort = append(bestEffort, pattern)
		}
		seen[pattern] = true
	}
	m.Include = required
	m.DefaultInclude = bestEffort
	return nil
}

func normalizeAndValidatePromoteRunScoped(entries []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(entries))
	for i, raw := range entries {
		s := strings.TrimSpace(raw)
		if s == "" {
			return nil, fmt.Errorf("inputs.materialize.fan_in.promote_run_scoped[%d] must be non-empty", i)
		}
		if isAbsolutePathLike(s) {
			return nil, fmt.Errorf("inputs.materialize.fan_in.promote_run_scoped[%d] must be relative", i)
		}
		normalizedSlashes := strings.ReplaceAll(s, "\\", "/")
		for _, seg := range strings.Split(normalizedSlashes, "/") {
			if seg == ".." {
				return nil, fmt.Errorf("inputs.materialize.fan_in.promote_run_scoped[%d] must not contain '..' segments", i)
			}
		}
		clean := filepath.ToSlash(filepath.Clean(normalizedSlashes))
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
			return nil, fmt.Errorf("inputs.materialize.fan_in.promote_run_scoped[%d] must not contain '..' segments", i)
		}
		clean = strings.TrimPrefix(clean, "./")
		if !seen[clean] {
			seen[clean] = true
			out = append(out, clean)
		}
	}
	return out, nil
}

normalized, err := normalizeAndValidatePromoteRunScoped(m.FanIn.PromoteRunScoped)
if err != nil {
	return err
}
m.FanIn.PromoteRunScoped = normalized

presence, err := detectMaterializeFieldPresence(path, b)
if err != nil {
	return nil, err
}
if err := cfg.Inputs.Materialize.normalizeImports(presence); err != nil {
	return nil, err
}
```

```go
if len(cfg.Inputs.Materialize.DefaultInclude) == 0 {
	cfg.Inputs.Materialize.DefaultInclude = nil // no implicit root .ai default
}
```

- [ ] **Step 4: Re-run targeted tests, then full engine config package tests**

Run: `go test ./internal/attractor/engine -run 'TestLoadRunConfigFile_InputMaterialization|TestLoadRunConfigFile_InputMaterializationDefaultsAndValidation' -count=1`
Expected: PASS.

Run: `go test ./internal/attractor/engine -count=1`
Expected: PASS.

- [ ] **Step 5: Commit config schema changes**

```bash
git add internal/attractor/engine/config.go internal/attractor/engine/input_materialization_config.go internal/attractor/engine/input_materialization_config_test.go internal/attractor/engine/config_test.go
git commit -m "engine/config: add imports alias and fan-in promotion schema"
```

### Task 2: Update runfile template + guardrails for new no-implicit-import baseline

**Files:**
- Modify: `skills/create-runfile/reference_run_template.yaml`
- Modify: `internal/attractor/validate/create_runfile_template_guardrail_test.go`

- [ ] **Step 1: Write failing guardrail expectations for template contract**

```go
func TestCreateRunfileTemplate_UsesImportsAndFanInPromotionExample(t *testing.T) {
	template := loadCreateRunfileTemplateMap(t)
	materialize := asMap(t, asMap(t, template["inputs"], "inputs")["materialize"], "inputs.materialize")
	if _, ok := materialize["imports"]; !ok {
		t.Fatal("template must include inputs.materialize.imports")
	}
	if _, ok := materialize["include"]; ok {
		t.Fatal("template must not include include when imports schema is used")
	}
	if _, ok := materialize["default_include"]; ok {
		t.Fatal("template must not include implicit default_include seed")
	}
	if raw, ok := materialize["fan_in"]; ok {
		fanIn := asMap(t, raw, "inputs.materialize.fan_in")
		if promoteRaw, ok := fanIn["promote_run_scoped"]; ok {
			promote := asSlice(t, promoteRaw, "inputs.materialize.fan_in.promote_run_scoped")
			if len(promote) != 0 {
				t.Fatal("template default must not opt into fan-in promotion")
			}
		}
	}
}
```

- [ ] **Step 2: Run validator guardrail tests to verify failure first**

Run: `go test ./internal/attractor/validate -run 'TestCreateRunfileTemplate' -count=1`
Expected: FAIL because template still seeds `default_include: [".ai/*.md"]` and lacks new fields.

- [ ] **Step 3: Patch template and guardrail tests**

```yaml
inputs:
  materialize:
    enabled: true
    imports:
      - pattern: "docs/**/*.md"
        required: false
    fan_in:
      promote_run_scoped: []
    follow_references: true
    infer_with_llm: false

    # Optional opt-in when run-scoped promotion is desired:
    # fan_in:
    #   promote_run_scoped:
    #     - postmortem_latest.md
    #     - review_final.md
```

Update `create_runfile_template_guardrail_test.go` to remove obsolete assertions that require `inputs.materialize.default_include` to exist in the template.
Update `create_runfile_template_guardrail_test.go` to remove obsolete assertions that require `inputs.materialize.include` to exist in the template.

- [ ] **Step 4: Re-run guardrail tests**

Run: `go test ./internal/attractor/validate -run 'TestCreateRunfileTemplate' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit template contract updates**

```bash
git add skills/create-runfile/reference_run_template.yaml internal/attractor/validate/create_runfile_template_guardrail_test.go
git commit -m "validate/template: remove implicit root .ai include defaults"
```

## Chunk 2: Lineage-Aware Snapshot and Hydration

### Task 3: Introduce lineage model and deterministic revision persistence

**Files:**
- Create: `internal/attractor/engine/input_snapshot_lineage.go`
- Test: `internal/attractor/engine/input_snapshot_lineage_test.go`

- [ ] **Step 1: Write failing unit tests for run/branch lineage operations**

```go
func TestInputSnapshotLineage_ForkBranchAndAdvanceRevision(t *testing.T) {
	lineage := newInputSnapshotLineage("run-123")
	r0 := lineage.CreateRunRevision("", nil)
	b0, err := lineage.ForkBranch("branch-a", r0)
	if err != nil {
		t.Fatalf("ForkBranch: %v", err)
	}
	b1 := lineage.AdvanceBranch("branch-a", b0, map[string]string{"postmortem_latest.md": "sha256:aaa"})
	if lineage.BranchHeads["branch-a"] != b1 {
		t.Fatalf("branch head mismatch")
	}
}
```

```go
func TestInputSnapshotLineage_MergePromotionsDetectsConflict(t *testing.T) {
	lineage := newInputSnapshotLineage("run-123")
	r0 := lineage.CreateRunRevision("", map[string]string{})
	a0, _ := lineage.ForkBranch("a", r0)
	b0, _ := lineage.ForkBranch("b", r0)
	lineage.Revisions[a0] = InputSnapshotRev{ID: a0, ParentIDs: []string{r0}, Scope: "branch", BranchKey: "a", FileDigest: map[string]string{"postmortem_latest.md": "sha256:aaa"}}
	lineage.Revisions[b0] = InputSnapshotRev{ID: b0, ParentIDs: []string{r0}, Scope: "branch", BranchKey: "b", FileDigest: map[string]string{"postmortem_latest.md": "sha256:bbb"}}

	_, conflicts, err := lineage.MergePromotedPaths([]string{"postmortem_latest.md"}, map[string]string{"a": a0, "b": b0})
	if err == nil || !strings.Contains(err.Error(), "input_snapshot_conflict") {
		t.Fatalf("expected input_snapshot_conflict, got %v", err)
	}
	if len(conflicts) != 1 || conflicts[0].Path != "postmortem_latest.md" {
		t.Fatalf("unexpected conflicts payload: %+v", conflicts)
	}
}
```

```go
func TestInputSnapshotLineage_BranchAdvanceDoesNotMutateRunHead(t *testing.T) {
	lineage := newInputSnapshotLineage("run-123")
	r0 := lineage.CreateRunRevision("", map[string]string{})
	lineage.RunHead = r0
	b0, _ := lineage.ForkBranch("a", r0)
	_ = lineage.AdvanceBranch("a", b0, map[string]string{"postmortem_latest.md": "sha256:aaa"})
	if lineage.RunHead != r0 {
		t.Fatalf("branch advancement must not mutate run head before fan-in: got %q want %q", lineage.RunHead, r0)
	}
}
```

```go
func TestInputSnapshotLineage_MergePromotionsConflictOrdering(t *testing.T) {
	lineage := newInputSnapshotLineage("run-123")
	r0 := lineage.CreateRunRevision("", map[string]string{})
	a0, _ := lineage.ForkBranch("a", r0)
	b0, _ := lineage.ForkBranch("b", r0)
	lineage.Revisions[a0] = InputSnapshotRev{ID: a0, ParentIDs: []string{r0}, Scope: "branch", BranchKey: "a", FileDigest: map[string]string{"z.md": "sha256:aaa", "a.md": "sha256:aaa"}}
	lineage.Revisions[b0] = InputSnapshotRev{ID: b0, ParentIDs: []string{r0}, Scope: "branch", BranchKey: "b", FileDigest: map[string]string{"z.md": "sha256:bbb", "a.md": "sha256:bbb"}}
	_, conflicts, err := lineage.MergePromotedPaths([]string{"a.md", "z.md"}, map[string]string{"a": a0, "b": b0})
	if err == nil || !strings.Contains(err.Error(), "input_snapshot_conflict") {
		t.Fatalf("expected input_snapshot_conflict, got %v", err)
	}
	if len(conflicts) != 2 || conflicts[0].Path != "a.md" || conflicts[1].Path != "z.md" {
		t.Fatalf("conflicts must be sorted by path: %+v", conflicts)
	}
}
```

```go
func TestInputSnapshotLineage_SaveLoadAtomic(t *testing.T) {
	logsRoot := t.TempDir()
	lineage := newInputSnapshotLineage("run-123")
	r0 := lineage.CreateRunRevision("", map[string]string{"review_final.md": "sha256:abc"})
	lineage.RunHead = r0
	if err := lineage.SaveAtomic(logsRoot); err != nil {
		t.Fatalf("SaveAtomic: %v", err)
	}
	loaded, err := LoadInputSnapshotLineage(logsRoot)
	if err != nil {
		t.Fatalf("LoadInputSnapshotLineage: %v", err)
	}
	if loaded.RunHead != r0 {
		t.Fatalf("run head mismatch: got %q want %q", loaded.RunHead, r0)
	}
}
```

- [ ] **Step 2: Run new lineage tests and confirm they fail before implementation**

Run: `go test ./internal/attractor/engine -run 'TestInputSnapshotLineage' -count=1`
Expected: FAIL with undefined lineage types/functions.

- [ ] **Step 3: Implement lineage persistence and merge primitives**

```go
type InputSnapshotLineage struct {
	RunID       string                       `json:"run_id"`
	RunHead     string                       `json:"run_head_revision"`
	BranchHeads map[string]string            `json:"branch_heads"`
	Revisions   map[string]InputSnapshotRev  `json:"revisions"`
}

type InputSnapshotRev struct {
	ID         string            `json:"id"`
	ParentIDs  []string          `json:"parent_ids"`
	Scope      string            `json:"scope"` // run|branch|fan_in
	BranchKey  string            `json:"branch_key,omitempty"`
	FileDigest map[string]string `json:"run_scoped_file_digest"`
	GeneratedAt string           `json:"generated_at"`
}
```

```go
const (
	inputLineageFileName = "lineage.json"
	inputRevisionDirName = "revisions"
)

func inputLineagePath(logsRoot string) string {
	return filepath.Join(strings.TrimSpace(logsRoot), "input_snapshot", inputLineageFileName)
}

func inputRevisionRoot(logsRoot string, revID string) string {
	return filepath.Join(strings.TrimSpace(logsRoot), "input_snapshot", inputRevisionDirName, strings.TrimSpace(revID), "run_scoped")
}

func (l *InputSnapshotLineage) SaveAtomic(logsRoot string) error {
	return runtime.WriteJSONAtomicFile(inputLineagePath(logsRoot), l)
}

func (l *InputSnapshotLineage) MergePromotedPaths(promote []string, branchRevs map[string]string) (string, []InputSnapshotConflict, error) {
	patterns := normalizePromotePatterns(promote)
	pathToEntries := map[string]map[string]InputSnapshotConflictDigestPair{}
	for branchKey, revID := range branchRevs {
		rev, ok := l.Revisions[revID]
		if !ok {
			return "", nil, fmt.Errorf("unknown branch revision %q", revID)
		}
		for _, rel := range matchPromotedPaths(patterns, rev.FileDigest) {
			if _, ok := pathToEntries[rel]; !ok {
				pathToEntries[rel] = map[string]InputSnapshotConflictDigestPair{}
			}
			pathToEntries[rel][branchKey] = InputSnapshotConflictDigestPair{
				BranchKey:  branchKey,
				RevisionID: revID,
				Digest:     rev.FileDigest[rel],
			}
		}
	}

	merged := map[string]string{}
	conflicts := make([]InputSnapshotConflict, 0)
	for _, rel := range sortedMapKeys(pathToEntries) {
		entries := make([]InputSnapshotConflictDigestPair, 0, len(pathToEntries[rel]))
		for _, branchKey := range sortedMapKeys(pathToEntries[rel]) {
			entries = append(entries, pathToEntries[rel][branchKey])
		}
		if len(entries) == 0 {
			continue
		}
		firstDigest := entries[0].Digest
		sameDigest := true
		for _, entry := range entries[1:] {
			if entry.Digest != firstDigest {
				sameDigest = false
				break
			}
		}
		if !sameDigest {
			conflicts = append(conflicts, InputSnapshotConflict{Path: rel, BranchDigests: entries})
			continue
		}
		merged[rel] = firstDigest
	}
	if len(conflicts) > 0 {
		sort.SliceStable(conflicts, func(i, j int) bool { return conflicts[i].Path < conflicts[j].Path })
		return "", conflicts, fmt.Errorf("failure_reason=input_snapshot_conflict")
	}
	newHead := l.CreateRunRevision(l.RunHead, merged)
	l.RunHead = newHead
	return newHead, nil, nil
}
```

- [ ] **Step 4: Re-run lineage tests**

Run: `go test ./internal/attractor/engine -run 'TestInputSnapshotLineage' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit lineage primitives**

```bash
git add internal/attractor/engine/input_snapshot_lineage.go internal/attractor/engine/input_snapshot_lineage_test.go
git commit -m "engine/input: add run-branch snapshot lineage manager"
```

### Task 4: Wire lineage into run/branch/stage/resume materialization flows

**Files:**
- Modify: `internal/attractor/engine/input_materialization.go`
- Modify: `internal/attractor/engine/engine.go`
- Create: `internal/attractor/engine/input_lineage_test_helpers_test.go`
- Modify: `internal/attractor/engine/input_materialization_integration_test.go`
- Modify: `internal/attractor/engine/input_materialization_resume_test.go`
- Modify: `internal/attractor/engine/input_manifest_contract_test.go`

- [ ] **Step 1: Add failing integration tests for branch isolation + resume-from-lineage**

```go
var branchIsolationDOT = []byte(`
digraph P {
  graph [goal="lineage branch isolation"]
  start [shape=Mdiamond]
  par [shape=component]
  a [shape=parallelogram, tool_command="mkdir -p .ai/runs/$KILROY_RUN_ID && echo a > .ai/runs/$KILROY_RUN_ID/branch_a_only.md"]
  b [shape=parallelogram, tool_command="test ! -f .ai/runs/$KILROY_RUN_ID/branch_a_only.md"]
  join [shape=tripleoctagon]
  exit [shape=Msquare]
  start -> par
  par -> a
  par -> b
  a -> join
  b -> join
  join -> exit
}
`)

var resumeSeedDOT = []byte(`
digraph R {
  graph [goal="lineage resume seed"]
  start [shape=Mdiamond]
  write [shape=parallelogram, tool_command="mkdir -p .ai/runs/$KILROY_RUN_ID && echo seeded > .ai/runs/$KILROY_RUN_ID/postmortem_latest.md"]
  exit [shape=Msquare]
  start -> write -> exit
}
`)

func runParallelPromotionFixture(t *testing.T, promote []string) *Result {
	t.Helper()
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	cfg := newInputMaterializationRunConfigForTest(t, repo)
	cfg.Inputs.Materialize.FanIn.PromoteRunScoped = append([]string{}, promote...)
	res, err := RunWithConfig(context.Background(), branchIsolationDOT, cfg, RunOptions{RunID: "lineage-promotion", LogsRoot: logsRoot, DisableCXDB: true})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}
	return res
}

func runParallelConflictFixture(t *testing.T, promote []string) runtime.Outcome {
	t.Helper()
	res := runParallelPromotionFixture(t, promote)
	b, err := os.ReadFile(filepath.Join(res.LogsRoot, "join", "status.json"))
	if err != nil {
		t.Fatalf("read join status: %v", err)
	}
	out, err := runtime.DecodeOutcomeJSON(b)
	if err != nil {
		t.Fatalf("decode join status: %v", err)
	}
	return out
}

func mustLoadLineage(t *testing.T, logsRoot string) *InputSnapshotLineage {
	t.Helper()
	lineage, err := LoadInputSnapshotLineage(logsRoot)
	if err != nil {
		t.Fatalf("LoadInputSnapshotLineage: %v", err)
	}
	return lineage
}

func resumeFromCheckpointForTest(ctx context.Context, logsRoot string) error {
	_, err := Resume(ctx, logsRoot)
	return err
}

func mustParallelResultByStartNode(t *testing.T, results []parallelBranchResult, nodeID string) parallelBranchResult {
	t.Helper()
	for _, r := range results {
		if strings.TrimSpace(r.StartNodeID) == strings.TrimSpace(nodeID) {
			return r
		}
	}
	t.Fatalf("missing parallel branch result for node %q", nodeID)
	return parallelBranchResult{}
}
```

```go
func TestInputMaterialization_Lineage_BranchIsolationBeforeFanIn(t *testing.T) {
	// branch A writes .ai/runs/<run_id>/postmortem_latest.md
	// branch B must not see it before fan-in promotion merge
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	cfg := newInputMaterializationRunConfigForTest(t, repo)
	cfg.Inputs.Materialize.FanIn.PromoteRunScoped = nil // default none

	res, err := RunWithConfig(context.Background(), branchIsolationDOT, cfg, RunOptions{RunID: "lineage-branch-isolation", LogsRoot: logsRoot, DisableCXDB: true})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}
	results := mustLoadParallelResults(t, filepath.Join(res.LogsRoot, "par", "parallel_results.json"))
	if len(results) != 2 {
		t.Fatalf("expected 2 branch results, got %d", len(results))
	}
	aResult := mustParallelResultByStartNode(t, results, "a")
	bResult := mustParallelResultByStartNode(t, results, "b")
	aFile := filepath.Join(aResult.WorktreeDir, ".ai", "runs", res.RunID, "branch_a_only.md")
	bFile := filepath.Join(bResult.WorktreeDir, ".ai", "runs", res.RunID, "branch_a_only.md")
	if !fileExists(aFile) {
		t.Fatalf("writer branch must retain its own run-scoped file: %s", aFile)
	}
	if fileExists(bFile) {
		t.Fatalf("cross-branch leakage detected: %s", bFile)
	}
}
```

```go
func TestInputMaterialization_Lineage_ResumeHydratesRunScopedWithoutWorkspaceAI(t *testing.T) {
	// remove source/worktree .ai, keep lineage persisted, resume should restore run-scoped files
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	cfg := newInputMaterializationRunConfigForTest(t, repo)
	res, err := RunWithConfig(context.Background(), resumeSeedDOT, cfg, RunOptions{RunID: "lineage-resume", LogsRoot: logsRoot, DisableCXDB: true})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}
	// Write run-scoped data after startup so resume must hydrate from lineage revision,
	// not from startup snapshot content.
	runScoped := filepath.Join(res.WorktreeDir, ".ai", "runs", "lineage-resume", "postmortem_latest.md")
	if err := os.MkdirAll(filepath.Dir(runScoped), 0o755); err != nil {
		t.Fatalf("mkdir run scoped: %v", err)
	}
	if err := os.WriteFile(runScoped, []byte("lineage-only-content"), 0o644); err != nil {
		t.Fatalf("write run scoped: %v", err)
	}
	_ = os.RemoveAll(filepath.Join(repo, ".ai"))
	_ = os.RemoveAll(filepath.Join(res.WorktreeDir, ".ai"))

	if err := resumeFromCheckpointForTest(context.Background(), logsRoot); err != nil {
		t.Fatalf("resumeFromCheckpointForTest: %v", err)
	}
	if !fileExists(filepath.Join(logsRoot, "worktree", ".ai", "runs", "lineage-resume", "postmortem_latest.md")) {
		t.Fatal("resume failed to hydrate run-scoped file from persisted lineage")
	}
}
```

```go
func TestInputMaterialization_Lineage_ManifestsCarryRevisionFields(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	cfg := newInputMaterializationRunConfigForTest(t, repo)
	res, err := RunWithConfig(context.Background(), branchIsolationDOT, cfg, RunOptions{RunID: "lineage-manifest-fields", LogsRoot: logsRoot, DisableCXDB: true})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}
	branchResults := mustLoadParallelResults(t, filepath.Join(res.LogsRoot, "par", "parallel_results.json"))
	branchManifest := mustLoadInputManifest(t, inputRunManifestPath(branchResults[0].LogsRoot))
	if strings.TrimSpace(branchManifest.BaseRunRevision) == "" || strings.TrimSpace(branchManifest.BranchHeadRevision) == "" {
		t.Fatalf("branch manifest missing base/head revisions: %+v", branchManifest)
	}
	branchStageManifest := mustLoadInputManifest(t, inputStageManifestPath(branchResults[0].LogsRoot, "a"))
	if strings.TrimSpace(branchStageManifest.RunBaseRevision) == "" || strings.TrimSpace(branchStageManifest.BranchRevision) == "" {
		t.Fatalf("branch stage manifest missing lineage tuple: %+v", branchStageManifest)
	}
}
```

- [ ] **Step 2: Run target integration tests to verify failure first**

Run: `go test ./internal/attractor/engine -run 'TestInputMaterialization_Lineage|TestInputMaterializationResume' -count=1`
Expected: FAIL because current hydration uses mutable workspace roots and has no lineage pointers.

- [ ] **Step 3: Implement lineage-aware hydration + stage manifest revision tuple**

```go
type InputManifest struct {
	Sources                      []string                    `json:"sources"`
	ResolvedFiles                []string                    `json:"resolved_files"`
	SourceTargetMap              []InputSourceTargetMapEntry `json:"source_target_map"`
	RunBaseRevision string `json:"run_base_revision,omitempty"`
	BranchRevision  string `json:"branch_revision,omitempty"`
	BaseRunRevision string `json:"base_run_revision,omitempty"`
	BranchHeadRevision string `json:"branch_head_revision,omitempty"`
}
```

```go
func (e *Engine) materializeRunStartupInputs(ctx context.Context) error {
	if !e.inputMaterializationEnabled() {
		return nil
	}
	if err := e.ensureLineageLoaded(); err != nil {
		return err
	}
	manifest, err := e.materializeInputsWithPolicy(ctx, inputMaterializationRunScope, "", []string{e.Options.RepoPath}, e.WorktreeDir, inputSnapshotFilesRoot(e.LogsRoot), inputRunManifestPath(e.LogsRoot), true)
	if err != nil {
		return err
	}
	if err := e.ensureRunStartupLineage(ctx, manifest); err != nil {
		return err
	}
	if err := persistRevisionSnapshot(e.LogsRoot, e.inputLineage.RunHead, e.WorktreeDir, e.Options.RunID); err != nil {
		return err
	}
	return nil
}
```

```go
func (e *Engine) ensureLineageLoaded() error {
	if e.inputLineage != nil {
		return nil
	}
	lineage, err := LoadInputSnapshotLineage(e.LogsRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if lineage == nil {
		lineage = newInputSnapshotLineage(e.Options.RunID)
	}
	e.inputLineage = lineage
	return nil
}
```

```go
func (e *Engine) materializeBranchStartupInputs(ctx context.Context, parentWorktree, parentLogsRoot string) error {
	// read parent run head revision, fork branch lineage, hydrate .ai/runs/<run_id>/ from branch head snapshot
	if !e.inputMaterializationEnabled() {
		return nil
	}
	parentLineage, err := LoadInputSnapshotLineage(parentLogsRoot)
	if err != nil {
		return err
	}
	if parentLineage == nil {
		return fmt.Errorf("missing parent lineage")
	}
	e.inputLineage = parentLineage
	baseRev := strings.TrimSpace(e.inputLineage.RunHead)
	if baseRev == "" {
		return fmt.Errorf("parent lineage missing run head revision")
	}
	e.activeBranchKey = branchKeyFromWorktree(e.WorktreeDir)
	e.activeRunBaseRevision = baseRev
	branchRev, err := e.inputLineage.ForkBranch(e.activeBranchKey, baseRev)
	if err != nil {
		return err
	}
	e.activeBranchRevision = branchRev
	if err := hydrateRunScopedRevision(e.WorktreeDir, e.Options.RunID, inputRevisionRoot(parentLogsRoot, branchRev)); err != nil {
		return err
	}
	manifest, err := e.materializeInputsWithPolicy(ctx, inputMaterializationBranchScope, "", []string{parentWorktree, inputSnapshotFilesRoot(parentLogsRoot)}, e.WorktreeDir, "", inputRunManifestPath(e.LogsRoot), true)
	if err != nil {
		return err
	}
	manifest.BaseRunRevision = baseRev
	manifest.BranchHeadRevision = branchRev
	if err := persistRevisionSnapshot(e.LogsRoot, branchRev, e.WorktreeDir, e.Options.RunID); err != nil {
		return err
	}
	return writeJSON(inputRunManifestPath(e.LogsRoot), manifest)
}
```

```go
func (e *Engine) attachStageLineageTuple(manifest *InputManifest) {
	if manifest == nil {
		return
	}
	if strings.TrimSpace(e.activeBranchKey) != "" {
		manifest.RunBaseRevision = e.activeRunBaseRevision
		manifest.BranchRevision = e.activeBranchRevision
		return
	}
	manifest.RunBaseRevision = e.inputLineage.RunHead
	manifest.BranchRevision = ""
}
```

```go
// In executeNode/materialize stage-input path, attach tuple before writing stage manifest.
stageManifest, err := e.materializeInputsWithPolicy(ctx, inputMaterializationStageScope, node.ID, sourceRoots, e.WorktreeDir, "", inputStageManifestPath(e.LogsRoot, node.ID), false)
if err != nil {
	return runtime.Outcome{Status: runtime.StatusFail, FailureReason: err.Error()}, nil
}
e.attachStageLineageTuple(stageManifest)
if err := writeJSON(inputStageManifestPath(e.LogsRoot, node.ID), stageManifest); err != nil {
	return runtime.Outcome{Status: runtime.StatusFail, FailureReason: err.Error()}, nil
}
```

```go
func (e *Engine) materializeResumeStartupInputs(ctx context.Context) error {
	// hydrate run-scoped files from persisted run head only
	if !e.inputMaterializationEnabled() {
		return nil
	}
	lineage, err := LoadInputSnapshotLineage(e.LogsRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if lineage != nil {
		e.inputLineage = lineage
		if runHead := strings.TrimSpace(lineage.RunHead); runHead != "" {
			if err := hydrateRunScopedRevision(e.WorktreeDir, e.Options.RunID, inputRevisionRoot(e.LogsRoot, runHead)); err != nil {
				return err
			}
		}
	}
	_, err = e.materializeInputsWithPolicy(ctx, inputMaterializationResumeScope, "", []string{e.WorktreeDir, inputSnapshotFilesRoot(e.LogsRoot)}, e.WorktreeDir, "", inputRunManifestPath(e.LogsRoot), true)
	return err
}
```

```go
func (e *Engine) advanceLineageAfterStage(nodeID string) error {
	if !e.inputMaterializationEnabled() {
		return nil
	}
	if err := e.ensureLineageLoaded(); err != nil {
		return err
	}
	digest, err := digestRunScopedState(e.WorktreeDir, e.Options.RunID)
	if err != nil {
		return err
	}
	nextRev := ""
	if strings.TrimSpace(e.activeBranchKey) != "" {
		nextRev = e.inputLineage.AdvanceBranch(e.activeBranchKey, e.activeBranchRevision, digest)
		e.activeBranchRevision = nextRev
	} else {
		nextRev = e.inputLineage.CreateRunRevision(e.inputLineage.RunHead, digest)
		e.inputLineage.RunHead = nextRev
	}
	if err := persistRevisionSnapshot(e.LogsRoot, nextRev, e.WorktreeDir, e.Options.RunID); err != nil {
		return err
	}
	return e.inputLineage.SaveAtomic(e.LogsRoot)
}

// executeNode integration point (after status.json write):
if err := e.advanceLineageAfterStage(node.ID); err != nil {
	return runtime.Outcome{Status: runtime.StatusFail, FailureReason: err.Error()}, nil
}
```

```go
func persistRevisionSnapshot(logsRoot, revID, worktreeDir, runID string) error {
	src := filepath.Join(worktreeDir, ".ai", "runs", runID)
	dst := inputRevisionRoot(logsRoot, revID)
	parent := filepath.Dir(dst)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, strings.TrimSpace(revID)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(tmp, 0o755); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if err := copyTree(src, tmp); err != nil {
		return err
	}
	_ = os.RemoveAll(dst)
	return os.Rename(tmp, dst)
}
```

- [ ] **Step 4: Re-run integration tests for materialization contract and resume**

Run: `go test ./internal/attractor/engine -run 'TestInputMaterializationIntegration|TestInputMaterializationResume|TestInputManifestContract' -count=1`
Expected: PASS.

Run: `go test ./internal/attractor/engine -run 'TestInputMaterialization_Lineage|TestInputMaterializationResume|TestInputManifestContract' -count=1`
Expected: PASS, including the new lineage-specific integration tests.

- [ ] **Step 5: Commit lineage hydration integration**

```bash
git add internal/attractor/engine/input_materialization.go internal/attractor/engine/engine.go internal/attractor/engine/input_lineage_test_helpers_test.go internal/attractor/engine/input_materialization_integration_test.go internal/attractor/engine/input_materialization_resume_test.go internal/attractor/engine/input_manifest_contract_test.go
git commit -m "engine/input: hydrate run-scoped state from persisted lineage"
```

### Task 5: Apply fan-in promotion merge without changing git winner semantics

**Files:**
- Modify: `internal/attractor/engine/parallel_handlers.go`
- Modify: `internal/attractor/engine/parallel_guardrails_test.go`
- Modify: `internal/attractor/engine/parallel_test.go`

- [ ] **Step 1: Add failing fan-in tests for promotion/no-promotion/conflict cases**

```go
func TestFanIn_RunScopedPromotion_DefaultNoneDoesNotPromote(t *testing.T) {
	res := runParallelPromotionFixture(t, nil)
	if fileExists(filepath.Join(res.WorktreeDir, ".ai", "runs", res.RunID, "postmortem_latest.md")) {
		t.Fatal("default none must not promote run-scoped files")
	}
}
func TestFanIn_RunScopedPromotion_ExplicitListPromotesDeterministically(t *testing.T) {
	res := runParallelPromotionFixture(t, []string{"postmortem_latest.md", "review_final.md"})
	if !fileExists(filepath.Join(res.WorktreeDir, ".ai", "runs", res.RunID, "postmortem_latest.md")) {
		t.Fatal("expected promoted postmortem file in winner worktree")
	}
	lineage := mustLoadLineage(t, res.LogsRoot)
	if strings.TrimSpace(lineage.RunHead) == "" || len(lineage.Revisions) < 2 {
		t.Fatalf("fan-in promotion must advance run head (Rn+1), got %+v", lineage)
	}
}
func TestFanIn_RunScopedPromotion_ConflictFailsWithInputSnapshotConflict(t *testing.T) {
	out := runParallelConflictFixture(t, []string{"postmortem_latest.md"})
	if out.Status != runtime.StatusFail || out.FailureReason != "input_snapshot_conflict" {
		t.Fatalf("expected input_snapshot_conflict, got %+v", out)
	}
	conflicts := metaConflictList(t, out.Meta["conflicts"])
	if len(conflicts) == 0 || conflicts[0]["path"] == "" {
		t.Fatalf("conflicts payload missing sorted path list: %+v", out.Meta)
	}
}
func TestFanIn_RunScopedPromotion_ConflictPayloadSortedByPathThenBranch(t *testing.T) {
	out := runParallelConflictFixture(t, []string{"a.md", "z.md"})
	conflicts := metaConflictList(t, out.Meta["conflicts"])
	if len(conflicts) != 2 {
		t.Fatalf("expected two conflicts, got %+v", conflicts)
	}
	if conflicts[0]["path"] != "a.md" || conflicts[1]["path"] != "z.md" {
		t.Fatalf("conflicts must be sorted by path: %+v", conflicts)
	}
	b0 := anyToStringSlice(t, conflicts[0]["branches"])
	if len(b0) != 2 || b0[0] > b0[1] {
		t.Fatalf("branch keys must be sorted within each conflict payload: %+v", conflicts)
	}
}
func TestFanIn_RunScopedPromotion_UnresolvedGlobIsBestEffort(t *testing.T) {
	res := runParallelPromotionFixture(t, []string{"does-not-exist-*.md"})
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("unresolved promotion globs must be best-effort, got %q", res.FinalStatus)
	}
}

func metaConflictList(t *testing.T, raw any) []map[string]any {
	t.Helper()
	items, ok := raw.([]any)
	if !ok {
		t.Fatalf("conflicts payload must decode as []any, got %T", raw)
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("conflict item must decode as map[string]any, got %T", item)
		}
		out = append(out, m)
	}
	return out
}

func anyToStringSlice(t *testing.T, raw any) []string {
	t.Helper()
	items, ok := raw.([]any)
	if !ok {
		t.Fatalf("expected []any for branch list, got %T", raw)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, strings.TrimSpace(fmt.Sprint(item)))
	}
	return out
}
```

- [ ] **Step 2: Run fan-in tests and capture expected failures**

Run: `go test ./internal/attractor/engine -run 'TestFanIn_RunScopedPromotion|TestFanIn_' -count=1`
Expected: FAIL because fan-in currently only ff-forwards git and never merges run-scoped lineage.

- [ ] **Step 3: Implement promotion merge in fan-in handler**

```go
// after winner selection + ff-only fast-forward:
newHead, conflicts, err := exec.Engine.mergeRunScopedFanInState(results)
if err != nil {
	if isInputSnapshotConflictError(err) {
		return runtime.Outcome{
			Status: runtime.StatusFail,
			FailureReason: "input_snapshot_conflict",
			Meta: map[string]any{
				"conflicts": conflictsToMeta(conflicts), // sorted by path then branch_key
			},
		}, nil
	}
	return runtime.Outcome{Status: runtime.StatusFail, FailureReason: err.Error()}, nil
}
exec.Context.Set("input_lineage.run_head_revision", newHead)
```

- [ ] **Step 4: Re-run fan-in tests**

Run: `go test ./internal/attractor/engine -run 'TestFanIn_RunScopedPromotion|TestFanIn_' -count=1`
Expected: PASS and existing winner-selection/ff-only behavior remains unchanged.

Run: `go test ./internal/attractor/engine -run 'TestInputSnapshotLineage|TestInputMaterialization_Lineage|TestFanIn_RunScopedPromotion' -count=1`
Expected: PASS for the full Chunk 2 integration surface.

- [ ] **Step 5: Commit fan-in promotion merge integration**

```bash
git add internal/attractor/engine/parallel_handlers.go internal/attractor/engine/parallel_guardrails_test.go internal/attractor/engine/parallel_test.go
git commit -m "engine/fanin: merge promoted run-scoped lineage paths deterministically"
```

## Chunk 3: Runtime Path Migration, Skills, and Docs

### Task 6: Migrate runtime `.ai` fallbacks to `.ai/runs/<run_id>/...`

**Files:**
- Modify: `internal/attractor/engine/stage_status_contract.go`
- Modify: `internal/attractor/engine/failure_dossier.go`
- Modify: `internal/attractor/runstate/snapshot.go`
- Modify: `cmd/kilroy/attractor_status_follow.go`
- Modify: `cmd/kilroy/attractor_status_follow_test.go`
- Modify templates:
  - `internal/attractor/engine/prompts/stage_status_contract_preamble.tmpl`
  - `internal/attractor/engine/prompts/failure_dossier_preamble.tmpl`
- Test:
  - `internal/attractor/engine/stage_status_contract_test.go`
  - `internal/attractor/engine/failure_dossier_test.go`
  - `internal/attractor/engine/run_with_config_integration_test.go`
  - `internal/attractor/engine/status_json_worktree_test.go`
  - `internal/attractor/runstate/snapshot_test.go`

- [ ] **Step 1: Write failing tests asserting new run-scoped fallback paths**

```go
if got, want := c.FallbackPath, filepath.Join(wt, ".ai", "runs", runID, "status.json"); got != want {
	t.Fatalf("fallback path: got %q want %q", got, want)
}
```

```go
if got := strings.TrimSpace(anyToString(inv["status_fallback_path"])); got != filepath.Join(res.WorktreeDir, ".ai", "runs", res.RunID, "status.json") {
	t.Fatalf("status_fallback_path=%q", got)
}
```

```go
func TestApplyVerbose_RunScopedArtifactsOnly(t *testing.T) {
	logsRoot := t.TempDir()
	runID := "run-42"
	newRoot := filepath.Join(logsRoot, "worktree", ".ai", "runs", runID)
	oldRoot := filepath.Join(logsRoot, "worktree", ".ai")
	if err := os.MkdirAll(newRoot, 0o755); err != nil {
		t.Fatalf("mkdir new root: %v", err)
	}
	if err := os.MkdirAll(oldRoot, 0o755); err != nil {
		t.Fatalf("mkdir old root: %v", err)
	}
	_ = os.WriteFile(filepath.Join(newRoot, "postmortem_latest.md"), []byte("new-postmortem"), 0o644)
	_ = os.WriteFile(filepath.Join(oldRoot, "postmortem_latest.md"), []byte("legacy-postmortem"), 0o644)
	s := &Snapshot{LogsRoot: logsRoot, RunID: runID}
	if err := ApplyVerbose(s); err != nil {
		t.Fatalf("ApplyVerbose: %v", err)
	}
	if got := strings.TrimSpace(s.PostmortemText); got != "new-postmortem" {
		t.Fatalf("ApplyVerbose must read run-scoped artifact only, got %q", got)
	}
}
```

```go
func TestPrintVerboseSnapshot_RunScopedLabelsOnly(t *testing.T) {
	s := &runstate.Snapshot{RunID: "run-42", PostmortemText: "pm", ReviewText: "rv"}
	var buf bytes.Buffer
	printVerboseSnapshot(&buf, s)
	out := buf.String()
	if !strings.Contains(out, "worktree/.ai/runs/run-42/postmortem_latest.md") {
		t.Fatalf("missing run-scoped postmortem label: %s", out)
	}
	if strings.Contains(out, "worktree/.ai/postmortem_latest.md") {
		t.Fatalf("legacy root .ai label must not appear: %s", out)
	}
}
```

- [ ] **Step 2: Run affected runtime tests to verify failure first**

Run: `go test ./internal/attractor/engine ./internal/attractor/runstate -run 'TestStageStatusContract|TestRunWithConfig_CLIBackend_StatusContract|TestRun_FailureDossier|TestApplyVerbose' -count=1`
Expected: FAIL with old root `.ai` path assumptions.

Run: `go test ./cmd/kilroy -run 'TestPrintVerboseSnapshot_RunScopedLabelsOnly' -count=1`
Expected: FAIL because status-follow verbose labels still reference `worktree/.ai/*.md`.

- [ ] **Step 3: Implement path migration and remove legacy root `.ai` fallback consumption**

```go
func runScopedAIPath(worktree, runID, name string) string {
	return filepath.Join(worktree, ".ai", "runs", runID, name)
}
```

```go
fallback := runScopedAIPath(wtAbs, runID, "status.json")
failureDossierWorktreeRelativePath = filepath.ToSlash(filepath.Join(".ai", "runs", runID, failureDossierFileName))
```

```go
if b, err := os.ReadFile(filepath.Join(s.LogsRoot, "worktree", ".ai", "runs", s.RunID, "postmortem_latest.md")); err == nil { ... }
// Intentionally do not read legacy worktree/.ai/postmortem_latest.md fallback.
if b, err := os.ReadFile(filepath.Join(s.LogsRoot, "worktree", ".ai", "runs", s.RunID, "review_final.md")); err == nil { ... }
// Intentionally do not read legacy worktree/.ai/review_final.md fallback.
```

```go
func runScopedArtifactLabel(runID, name string) string {
	if strings.TrimSpace(runID) == "" {
		return filepath.ToSlash(filepath.Join("worktree", ".ai", "runs", "<run_id>", name))
	}
	return filepath.ToSlash(filepath.Join("worktree", ".ai", "runs", runID, name))
}

if s.PostmortemText != "" {
	fmt.Fprintf(w, "\n--- postmortem (%s) ---\n%s\n", runScopedArtifactLabel(s.RunID, "postmortem_latest.md"), s.PostmortemText)
}
if s.ReviewText != "" {
	fmt.Fprintf(w, "\n--- review (%s) ---\n%s\n", runScopedArtifactLabel(s.RunID, "review_final.md"), s.ReviewText)
}
```

- [ ] **Step 4: Re-run runtime path migration test set**

Run: `go test ./internal/attractor/engine ./internal/attractor/runstate -run 'TestStageStatusContract|TestRunWithConfig_CLIBackend_StatusContract|TestRun_FailureDossier|TestApplyVerbose' -count=1`
Expected: PASS.

Run: `go test ./cmd/kilroy -run 'TestPrintVerboseSnapshot_RunScopedLabelsOnly' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit runtime path migration**

```bash
git add internal/attractor/engine/stage_status_contract.go internal/attractor/engine/failure_dossier.go internal/attractor/runstate/snapshot.go cmd/kilroy/attractor_status_follow.go cmd/kilroy/attractor_status_follow_test.go internal/attractor/engine/prompts/stage_status_contract_preamble.tmpl internal/attractor/engine/prompts/failure_dossier_preamble.tmpl internal/attractor/engine/stage_status_contract_test.go internal/attractor/engine/failure_dossier_test.go internal/attractor/engine/run_with_config_integration_test.go internal/attractor/engine/status_json_worktree_test.go internal/attractor/runstate/snapshot_test.go
git commit -m "engine/runtime: move status and dossier fallbacks to run-scoped .ai"
```

### Task 7: Update skills/templates/examples/docs to remove root `.ai` scratch coupling

**Files:**
- Modify: `skills/create-dotfile/SKILL.md`
- Modify: `skills/create-dotfile/reference_template.dot`
- Modify: `skills/build-dod/SKILL.md`
- Modify: `skills/create-runfile/SKILL.md`
- Modify: `skills/create-runfile/reference_run_template.yaml`
- Modify: `demo/substack-pipeline-v01.dot`
- Modify: `docs/strongdm/dot specs/semport.dot`
- Modify: `docs/strongdm/attractor/attractor-spec.md`
- Modify: `docs/strongdm/attractor/README.md`
- Test: `internal/attractor/validate/reference_template_guardrail_test.go`

- [ ] **Step 1: Add/adjust guardrail tests that forbid new root `.ai` scratch guidance in reference surfaces**

```go
func TestReferenceTemplate_UsesRunScopedAIScratchPaths(t *testing.T) {
	text := string(loadReferenceTemplate(t))
	if strings.Contains(text, "Writes: .ai/postmortem_latest.md") {
		t.Fatal("reference template must use .ai/runs/$KILROY_RUN_ID/postmortem_latest.md")
	}
}
```

```go
func TestReferenceSurfaces_NoLegacyRootAIScratchPaths(t *testing.T) {
	paths := []string{
		"skills/create-dotfile/reference_template.dot",
		"skills/create-dotfile/SKILL.md",
		"skills/build-dod/SKILL.md",
		"skills/create-runfile/SKILL.md",
	}
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(b)
		if strings.Contains(text, ".ai/postmortem_latest.md") || strings.Contains(text, ".ai/review_final.md") {
			t.Fatalf("%s must not reference legacy root .ai scratch files", path)
		}
	}
}
```

- [ ] **Step 2: Run guardrail tests and confirm they fail first**

Run: `go test ./internal/attractor/validate -run 'TestReferenceTemplate_|TestReferenceSurfaces_|TestCreateRunfileTemplate_' -count=1`
Expected: FAIL while skill/template text still references root `.ai/*` scratch paths.

- [ ] **Step 3: Patch skill/template/docs/example paths and contracts**

```dot
// Writes: .ai/runs/$KILROY_RUN_ID/postmortem_latest.md
// Reads: .ai/runs/$KILROY_RUN_ID/definition_of_done.md
```

```md
Run-scoped scratch files live at `.ai/runs/<run_id>/...`; do not rely on root `.ai/*.md` implicit ingestion.
```

- [ ] **Step 4: Re-run validate package tests**

Run: `go test ./internal/attractor/validate -count=1`
Expected: PASS with updated guardrails and docs/template references.

- [ ] **Step 5: Commit skills/docs/template migrations**

```bash
git add skills/create-dotfile/SKILL.md skills/create-dotfile/reference_template.dot skills/build-dod/SKILL.md skills/create-runfile/SKILL.md skills/create-runfile/reference_run_template.yaml demo/substack-pipeline-v01.dot "docs/strongdm/dot specs/semport.dot" docs/strongdm/attractor/attractor-spec.md docs/strongdm/attractor/README.md internal/attractor/validate/reference_template_guardrail_test.go
git commit -m "skills/docs: migrate run scratch guidance to .ai/runs/<run_id>"
```

### Task 8: Full verification and CI checklist

**Files:**
- Modify if needed from fixes discovered during verification.

- [ ] **Step 1: Run gofmt check exactly like CI**

Run: `gofmt -l . | grep -v '^\./\.claude/' | grep -v '^\.claude/'`
Expected: no output.

- [ ] **Step 2: Run vet and build**

Run: `go vet ./... && go build ./cmd/kilroy/`
Expected: exit 0.

- [ ] **Step 3: Run full test suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 4: Validate demo graphs with rebuilt binary**

Run: `go build -o ./kilroy ./cmd/kilroy && for f in demo/**/*.dot; do echo "Validating $f"; ./kilroy attractor validate --graph "$f"; done`
Expected: all demo graph validations succeed.

- [ ] **Step 5: Commit final verification adjustments**

```bash
git add -A
git commit -m "engine/input: finalize run-scoped imports and lineage migration"
```

## Execution Notes

- Preserve fan-in winner semantics: git state remains ff-only to selected winner; lineage merge applies only to run-scoped workspace state.
- Keep Appendix C.1 contracts unchanged except the intentional default/include/path boundary updates described in the proposal.
- Do not reintroduce compatibility fallbacks to root `.ai` runtime paths.
