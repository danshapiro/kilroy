# Manual Model Registry + Alias Resolution Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a minimal checked-in manual model registry so too-new model names (and user shorthand like "minimax 25") resolve to canonical provider model IDs during DOT generation/runtime checks.

**Architecture:** Introduce a single manual registry file at `internal/attractor/modeldb/manual_models.yaml`, load it through `internal/attractor/modeldb`, and merge it into the effective run catalog. Centralize node model resolution in engine code so preflight, runtime routing, and catalog checks all use one path. Advertise the registry in the skill/docs where model discovery happens.

**Tech Stack:** Go (`internal/attractor/modeldb`, `internal/attractor/engine`), YAML (`gopkg.in/yaml.v3`), Markdown docs.

---

### Task 1: Add Manual Model Registry File + Loader (TDD)

**Files:**
- Create: `internal/attractor/modeldb/manual_models.yaml`
- Create: `internal/attractor/modeldb/manual_models.go`
- Create: `internal/attractor/modeldb/manual_models_test.go`

**Step 1: Write the failing test**

```go
package modeldb

import (
	"path/filepath"
	"testing"
)

func TestLoadManualModels_ParsesAndNormalizesAliases(t *testing.T) {
	p := filepath.Join("testdata", "manual_models.yaml")
	mm, err := LoadManualModels(p)
	if err != nil {
		t.Fatalf("LoadManualModels: %v", err)
	}
	if got, ok := mm.Resolve("minimax", "minimax 25"); !ok || got != "minimax-m2.5" {
		t.Fatalf("Resolve(minimax,minimax 25)=(%q,%v) want (minimax-m2.5,true)", got, ok)
	}
	if got, ok := mm.Resolve("zai", "glm 5.0"); !ok || got != "glm-5" {
		t.Fatalf("Resolve(zai,glm 5.0)=(%q,%v) want (glm-5,true)", got, ok)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/attractor/modeldb -run TestLoadManualModels_ParsesAndNormalizesAliases -count=1`
Expected: FAIL with `undefined: LoadManualModels`.

**Step 3: Write minimal implementation**

```go
package modeldb

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type ManualModelEntry struct {
	Provider             string    `yaml:"provider"`
	Model                string    `yaml:"model"`
	Aliases              []string  `yaml:"aliases"`
	InputCostPerMillion  *float64  `yaml:"input_cost_per_1m,omitempty"`
	OutputCostPerMillion *float64  `yaml:"output_cost_per_1m,omitempty"`
}

type ManualModels struct {
	entries []ManualModelEntry
	index   map[string]string
}

type manualModelsFile struct {
	Models []ManualModelEntry `yaml:"models"`
}

func LoadManualModels(path string) (*ManualModels, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f manualModelsFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("parse manual models yaml: %w", err)
	}
	mm := &ManualModels{entries: f.Models, index: map[string]string{}}
	for _, e := range f.Models {
		p := strings.ToLower(strings.TrimSpace(e.Provider))
		m := strings.TrimSpace(e.Model)
		if p == "" || m == "" {
			continue
		}
		mm.index[keyForAlias(p, m)] = m
		for _, a := range e.Aliases {
			if s := strings.TrimSpace(a); s != "" {
				mm.index[keyForAlias(p, s)] = m
			}
		}
	}
	return mm, nil
}

func (m *ManualModels) Resolve(provider, hint string) (string, bool) {
	if m == nil || len(m.index) == 0 {
		return "", false
	}
	v, ok := m.index[keyForAlias(provider, hint)]
	return v, ok
}

func keyForAlias(provider, modelLike string) string {
	p := strings.ToLower(strings.TrimSpace(provider))
	s := strings.ToLower(strings.TrimSpace(modelLike))
	s = strings.Join(strings.Fields(s), " ")
	return p + "::" + s
}
```

Also add `internal/attractor/modeldb/manual_models.yaml`:

```yaml
models:
  - provider: minimax
    model: minimax-m2.5
    aliases: ["minimax 2.5", "minimax 25", "m2.5"]
  - provider: kimi
    model: kimi-k2.5
    aliases: ["kimi 2.5", "kimi k2.5", "k2.5"]
  - provider: zai
    model: glm-5
    aliases: ["glm 5.0", "glm 5", "glm5", "glm-5.0"]
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/attractor/modeldb -run TestLoadManualModels_ParsesAndNormalizesAliases -count=1`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/modeldb/manual_models.yaml internal/attractor/modeldb/manual_models.go internal/attractor/modeldb/manual_models_test.go
git commit -m "feat(modeldb): add manual model registry with alias resolver"
```

### Task 2: Merge Manual Models Into Effective Run Catalog (TDD)

**Files:**
- Modify: `internal/attractor/modeldb/manual_models.go`
- Modify: `internal/attractor/engine/run_with_config.go`
- Modify: `internal/attractor/engine/resume.go`
- Modify: `internal/attractor/engine/run_with_config_test.go`

**Step 1: Write the failing test**

Add a focused test in `internal/attractor/engine/run_with_config_test.go`:

```go
func TestLoadCatalogForRun_MergesManualModels(t *testing.T) {
	catalogPath := writeProviderCatalogForTest(t) // does not include minimax-m2.5
	repo := t.TempDir()
	manualPath := filepath.Join(repo, "internal", "attractor", "modeldb", "manual_models.yaml")
	_ = os.MkdirAll(filepath.Dir(manualPath), 0o755)
	_ = os.WriteFile(manualPath, []byte("models:\n  - provider: minimax\n    model: minimax-m2.5\n    aliases: [\"minimax 25\"]\n"), 0o644)

	c, err := loadCatalogForRun(catalogPath, repo)
	if err != nil {
		t.Fatalf("loadCatalogForRun: %v", err)
	}
	if !modeldb.CatalogHasProviderModel(c, "minimax", "minimax-m2.5") {
		t.Fatalf("expected merged manual model minimax-m2.5")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/attractor/engine -run TestLoadCatalogForRun_MergesManualModels -count=1`
Expected: FAIL because `loadCatalogForRun` currently only loads OpenRouter JSON and has old signature.

**Step 3: Write minimal implementation**

- Change signature to:

```go
func loadCatalogForRun(path string, repoPath string) (*modeldb.Catalog, error)
```

- In that function:
  - Load base catalog from OpenRouter JSON.
  - Best-effort load `filepath.Join(repoPath, "internal", "attractor", "modeldb", "manual_models.yaml")`.
  - Merge manual entries into `catalog.Models` and `catalog.CoveredProviders`.

Add merge method in `manual_models.go`:

```go
func (m *ManualModels) MergeIntoCatalog(c *Catalog) {
	if m == nil || c == nil {
		return
	}
	if c.Models == nil {
		c.Models = map[string]ModelEntry{}
	}
	if c.CoveredProviders == nil {
		c.CoveredProviders = map[string]bool{}
	}
	for _, e := range m.entries {
		p := strings.ToLower(strings.TrimSpace(e.Provider))
		id := strings.TrimSpace(e.Model)
		if p == "" || id == "" {
			continue
		}
		c.CoveredProviders[p] = true
		if _, exists := c.Models[id]; !exists {
			c.Models[id] = ModelEntry{Provider: p, Mode: "chat"}
		}
	}
}
```

- Update callers:
  - `RunWithConfig`: `loadCatalogForRun(resolved.SnapshotPath, cfg.Repo.Path)`
  - `Resume`: `loadCatalogForRun(snapshotPath, m.RepoPath)`

**Step 4: Run test to verify it passes**

Run: `go test ./internal/attractor/engine -run TestLoadCatalogForRun_MergesManualModels -count=1`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/modeldb/manual_models.go internal/attractor/engine/run_with_config.go internal/attractor/engine/resume.go internal/attractor/engine/run_with_config_test.go
git commit -m "feat(engine): merge manual model registry into effective run catalog"
```

### Task 3: DRY Node Model Resolution in Engine (TDD)

**Files:**
- Create: `internal/attractor/engine/model_resolution.go`
- Create: `internal/attractor/engine/model_resolution_test.go`
- Modify: `internal/attractor/engine/run_with_config.go`
- Modify: `internal/attractor/engine/provider_preflight.go`
- Modify: `internal/attractor/engine/codergen_router.go`

**Step 1: Write the failing test**

```go
func TestResolveNodeModelID_UsesManualAlias(t *testing.T) {
	n := model.NewNode("n")
	n.Attrs["llm_provider"] = "minimax"
	n.Attrs["llm_model"] = "minimax 25"
	mm := &modeldb.ManualModels{}
	// assume helper constructor for tests exists
	mm = mustManualModelsFromEntries(t, []modeldb.ManualModelEntry{{
		Provider: "minimax",
		Model:    "minimax-m2.5",
		Aliases:  []string{"minimax 25"},
	}})

	got := resolveNodeModelID(n, mm)
	if got != "minimax-m2.5" {
		t.Fatalf("resolveNodeModelID=%q want minimax-m2.5", got)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/attractor/engine -run TestResolveNodeModelID_UsesManualAlias -count=1`
Expected: FAIL with `undefined: resolveNodeModelID`.

**Step 3: Write minimal implementation**

```go
func resolveNodeModelID(n *model.Node, manual *modeldb.ManualModels) string {
	if n == nil {
		return ""
	}
	provider := normalizeProviderKey(n.Attr("llm_provider", ""))
	modelID := strings.TrimSpace(n.Attr("llm_model", ""))
	if modelID == "" {
		modelID = strings.TrimSpace(n.Attr("model", ""))
	}
	if provider != "" && modelID != "" && manual != nil {
		if resolved, ok := manual.Resolve(provider, modelID); ok {
			return resolved
		}
	}
	return modelID
}
```

Replace duplicated extraction logic in:
- `internal/attractor/engine/run_with_config.go`
- `internal/attractor/engine/provider_preflight.go`
- `internal/attractor/engine/codergen_router.go`

with `resolveNodeModelID(...)`.

**Step 4: Run tests to verify it passes**

Run: `go test ./internal/attractor/engine -run 'TestResolveNodeModelID_UsesManualAlias|TestLoadCatalogForRun_MergesManualModels' -count=1`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/model_resolution.go internal/attractor/engine/model_resolution_test.go internal/attractor/engine/run_with_config.go internal/attractor/engine/provider_preflight.go internal/attractor/engine/codergen_router.go
git commit -m "refactor(engine): centralize node model resolution and alias handling"
```

### Task 4: Advertise Manual Registry in Unknown-Model Warnings (TDD)

**Files:**
- Modify: `internal/attractor/engine/run_with_config.go`
- Modify: `internal/attractor/engine/run_with_config_test.go`

**Step 1: Write the failing test**

Add/adjust a test asserting warning message includes the manual file path hint:

```go
if !strings.Contains(check.Message, "internal/attractor/modeldb/manual_models.yaml") {
	t.Fatalf("expected manual registry hint in warning: %q", check.Message)
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/attractor/engine -run TestRunWithConfig_.*Catalog.* -count=1`
Expected: FAIL because current warning text has no manual registry hint.

**Step 3: Write minimal implementation**

Update warning text in `validateProviderModelPairs`:

```go
Message: fmt.Sprintf("llm_provider=%s backend=%s model=%s not present in run catalog (catalog may be stale; prompt probe will validate). If this is a new model, add it to internal/attractor/modeldb/manual_models.yaml", provider, backend, modelID),
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/attractor/engine -run TestRunWithConfig_.*Catalog.* -count=1`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/run_with_config.go internal/attractor/engine/run_with_config_test.go
git commit -m "chore(preflight): point unknown-model warnings to manual model registry"
```

### Task 5: Advertise Registry in Skill + Operator Docs

**Files:**
- Modify: `skills/english-to-dotfile/SKILL.md`
- Modify: `docs/strongdm/attractor/README.md`

**Step 1: Write the failing doc checks**

Use assertions via ripgrep in shell script style:

```bash
rg -n "manual_models.yaml" skills/english-to-dotfile/SKILL.md docs/strongdm/attractor/README.md
```

Expected right now: no match for the new file path.

**Step 2: Run check to verify it fails**

Run: `rg -n "internal/attractor/modeldb/manual_models.yaml" skills/english-to-dotfile/SKILL.md docs/strongdm/attractor/README.md`
Expected: non-zero exit.

**Step 3: Write minimal documentation updates**

- In `skills/english-to-dotfile/SKILL.md` Phase 3.3 lookup order, insert before pinned catalog:

```md
2. `internal/attractor/modeldb/manual_models.yaml` (too-new models + aliases)
3. `internal/attractor/modeldb/pinned/openrouter_models.json`.
```

- In `docs/strongdm/attractor/README.md` add one bullet under model/catalog notes:

```md
- Too-new model names and shorthand aliases can be added in `internal/attractor/modeldb/manual_models.yaml`.
```

**Step 4: Run check to verify it passes**

Run: `rg -n "internal/attractor/modeldb/manual_models.yaml" skills/english-to-dotfile/SKILL.md docs/strongdm/attractor/README.md`
Expected: matching lines in both files.

**Step 5: Commit**

```bash
git add skills/english-to-dotfile/SKILL.md docs/strongdm/attractor/README.md
git commit -m "docs: advertise manual model registry for too-new model names"
```

### Task 6: Final Verification + Cleanup Commit

**Files:**
- Validate-only task (no new files)

**Step 1: Run focused tests**

Run: `go test ./internal/attractor/modeldb ./internal/attractor/engine -count=1`
Expected: PASS.

**Step 2: Run full test suite**

Run: `go test ./... -count=1`
Expected: PASS.

**Step 3: Validate skill/docs discovery and warning text**

Run:

```bash
rg -n "manual_models.yaml" skills/english-to-dotfile/SKILL.md docs/strongdm/attractor/README.md internal/attractor/engine/run_with_config.go
```

Expected: all three files reference `internal/attractor/modeldb/manual_models.yaml`.

**Step 4: Smoke-check alias behavior in a generated DOT/run path**

Run a small test graph or unit test where `llm_provider=minimax` and `llm_model="minimax 25"`.
Expected: runtime/preflight uses `minimax-m2.5` after resolution.

**Step 5: Commit**

```bash
git add -A
git commit -m "feat(modeldb): support manual too-new model names and alias resolution end-to-end"
```

## Notes for Implementer

- Keep this minimal and YAGNI: no new config knobs unless required.
- Do not convert catalog warnings into hard failures.
- Do not add fuzzy matching beyond explicit aliases in `manual_models.yaml`.
- Keep provider canonicalization behavior unchanged (`providerspec.CanonicalProviderKey`).
- Keep existing `--force-model` behavior intact.
- Reference skill behavior in `@skills/english-to-dotfile/SKILL.md` when updating model lookup guidance.

Plan complete and saved to `docs/plans/2026-02-20-manual-model-registry-aliases.md`. Two execution options:

1. Subagent-Driven (this session) - I dispatch fresh subagent per task, review between tasks, fast iteration

2. Parallel Session (separate) - Open new session with executing-plans, batch execution with checkpoints

Which approach?
