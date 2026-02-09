# LiteLLM-to-OpenRouter Model Info Migration Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace LiteLLM catalog JSON as Kilroy’s model metadata source with OpenRouter `/api/v1/models` model info, while preserving run reproducibility and safe backward compatibility.

**Architecture:** Introduce a provider-agnostic `modeldb.Catalog` domain model and load it from OpenRouter model info payloads. Keep per-run immutable snapshot semantics (`pinned` and `on_run_start`) but switch default source URL and snapshot filename to OpenRouter. Add compatibility shims for old config keys and old resume artifact names, then migrate engine/router/docs/tests to new naming.

**Tech Stack:** Go, `testing` + `httptest`, YAML/JSON config parsing, shell tooling (`rg`, `jq`, `curl`), existing Attractor engine + modeldb packages.

---

## Scope and Constraints

- Keep current providers (`openai`, `anthropic`, `google`) and backend routing behavior unchanged.
- Keep model catalog metadata-only semantics (must not affect provider call path).
- Keep update policies (`pinned`, `on_run_start`) unchanged.
- Add one-release backward compatibility for old LiteLLM config keys and resume snapshot filename.
- Do not remove old compatibility aliases until all docs/tests/config examples are migrated.

## OpenRouter Field Mapping Contract (to implement)

- Source endpoint: `https://openrouter.ai/api/v1/models`
- Response shape: top-level object with `data: []`
- Catalog ID: `data[i].id` (for example `openai/gpt-5`)
- Provider: prefix before first `/` in `id`
- Context window: `context_length` fallback `top_provider.context_length`
- Max output tokens: `top_provider.max_completion_tokens`
- Input/output cost per token: `pricing.prompt` / `pricing.completion` (decimal strings)
- Supports tools: `supported_parameters` contains `tools`
- Supports reasoning: `supported_parameters` contains `reasoning` or `include_reasoning`
- Supports vision: `architecture.input_modalities` or `architecture.output_modalities` contains `image`

## Task 1: Introduce Generic `modeldb.Catalog` Domain Types

**Files:**
- Create: `internal/attractor/modeldb/catalog.go`
- Create: `internal/attractor/modeldb/catalog_test.go`
- Modify: `internal/attractor/engine/codergen_router.go`

**Step 1: Write the failing test**

```go
func TestCatalogHasProviderModel_AcceptsCanonicalAndProviderRelativeIDs(t *testing.T) {
	c := &Catalog{Models: map[string]ModelEntry{
		"openai/gpt-5":      {Provider: "openai"},
		"anthropic/claude-4": {Provider: "anthropic"},
	}}
	if !CatalogHasProviderModel(c, "openai", "gpt-5") {
		t.Fatalf("expected provider-relative openai model id to resolve")
	}
	if !CatalogHasProviderModel(c, "openai", "openai/gpt-5") {
		t.Fatalf("expected canonical openai model id to resolve")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/attractor/modeldb -run CatalogHasProviderModel -v`
Expected: FAIL with `undefined: Catalog` / `undefined: CatalogHasProviderModel`.

**Step 3: Write minimal implementation**

```go
type Catalog struct {
	Path   string
	SHA256 string
	Models map[string]ModelEntry
}

type ModelEntry struct {
	Provider string
	Mode     string

	ContextWindow   int
	MaxOutputTokens *int

	SupportsTools     bool
	SupportsVision    bool
	SupportsReasoning bool

	InputCostPerToken  *float64
	OutputCostPerToken *float64
}

func CatalogHasProviderModel(c *Catalog, provider, modelID string) bool {
	// Accept either canonical IDs (openai/gpt-5) or provider-relative IDs (gpt-5).
	// Implement via provider-aware canonicalization and case-insensitive comparison.
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/attractor/modeldb -run CatalogHasProviderModel -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/modeldb/catalog.go internal/attractor/modeldb/catalog_test.go internal/attractor/engine/codergen_router.go
git commit -m "refactor(modeldb): add generic Catalog domain model and provider/model matching helpers"
```

## Task 2: Implement OpenRouter Loader into Generic Catalog

**Files:**
- Create: `internal/attractor/modeldb/openrouter.go`
- Create: `internal/attractor/modeldb/openrouter_test.go`

**Step 1: Write the failing test**

```go
func TestLoadCatalogFromOpenRouterJSON_ParsesPricingAndCapabilities(t *testing.T) {
	p := writeTempFile(t, `{
	  "data": [{
	    "id": "openai/gpt-5",
	    "context_length": 272000,
	    "supported_parameters": ["tools", "reasoning"],
	    "architecture": {"input_modalities":["text","image"],"output_modalities":["text"]},
	    "pricing": {"prompt":"0.00000125", "completion":"0.00001"},
	    "top_provider": {"max_completion_tokens": 128000}
	  }]
	}`)
	c, err := LoadCatalogFromOpenRouterJSON(p)
	if err != nil { t.Fatalf("load: %v", err) }
	m := c.Models["openai/gpt-5"]
	if m.Provider != "openai" || !m.SupportsTools || !m.SupportsReasoning || !m.SupportsVision {
		t.Fatalf("unexpected parsed model: %+v", m)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/attractor/modeldb -run OpenRouter -v`
Expected: FAIL with `undefined: LoadCatalogFromOpenRouterJSON`.

**Step 3: Write minimal implementation**

```go
func LoadCatalogFromOpenRouterJSON(path string) (*Catalog, error) {
	// Read bytes, compute SHA256, decode {"data": [...]}, map each model to ModelEntry,
	// and return Catalog with Models keyed by canonical id.
	// Reject empty catalog.
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/attractor/modeldb -run OpenRouter -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/modeldb/openrouter.go internal/attractor/modeldb/openrouter_test.go
git commit -m "feat(modeldb): load normalized model catalog from OpenRouter model info payload"
```

## Task 3: Replace Resolver Source with OpenRouter Models Endpoint

**Files:**
- Create: `internal/attractor/modeldb/catalog_resolve.go`
- Create: `internal/attractor/modeldb/catalog_resolve_test.go`
- Create: `internal/attractor/modeldb/catalog_resolve_warning_test.go`
- Modify: `internal/attractor/modeldb/litellm_resolve.go` (compat shim only)

**Step 1: Write the failing tests**

```go
func TestResolveModelCatalog_OnRunStartFetch_WritesOpenRouterSnapshotName(t *testing.T) {
	res, err := ResolveModelCatalog(ctx, pinned, logsRoot, CatalogOnRunStart, srv.URL, 2*time.Second)
	if err != nil { t.Fatal(err) }
	if !strings.HasSuffix(res.SnapshotPath, "modeldb/openrouter_models.json") {
		t.Fatalf("unexpected snapshot path: %s", res.SnapshotPath)
	}
}
```

**Step 2: Run tests to verify failure**

Run: `go test ./internal/attractor/modeldb -run ResolveModelCatalog -v`
Expected: FAIL with `undefined: ResolveModelCatalog`.

**Step 3: Write minimal implementation**

```go
func ResolveModelCatalog(ctx context.Context, pinnedPath, logsRoot string, policy CatalogUpdatePolicy, url string, timeout time.Duration) (*ResolvedCatalog, error) {
	if strings.TrimSpace(url) == "" {
		url = "https://openrouter.ai/api/v1/models"
	}
	dstPath := filepath.Join(logsRoot, "modeldb", "openrouter_models.json")
	// pinned: copy pinnedPath
	// on_run_start: fetch URL, fallback to pinned on failure with warning
	// always compute SHA + warning when effective differs from pinned
}
```

Also keep compatibility wrapper:

```go
func ResolveLiteLLMCatalog(...) (*ResolvedCatalog, error) {
	return ResolveModelCatalog(...)
}
```

**Step 4: Run tests to verify pass**

Run: `go test ./internal/attractor/modeldb -v`
Expected: PASS for old and new resolver tests.

**Step 5: Commit**

```bash
git add internal/attractor/modeldb/catalog_resolve.go internal/attractor/modeldb/catalog_resolve_test.go internal/attractor/modeldb/catalog_resolve_warning_test.go internal/attractor/modeldb/litellm_resolve.go
git commit -m "feat(modeldb): resolve per-run catalog snapshot from OpenRouter endpoint with pinned fallback"
```

## Task 4: Migrate Run Config Schema with Backward-Compatible Aliases

**Files:**
- Modify: `internal/attractor/engine/config.go`
- Modify: `internal/attractor/engine/config_test.go`

**Step 1: Write failing tests for new keys + old-key compatibility**

```go
func TestLoadRunConfigFile_ModelDBOpenRouterKeys(t *testing.T) {
	// uses openrouter_model_info_path/url/update_policy/fetch_timeout_ms
}

func TestLoadRunConfigFile_ModelDBLiteLLMKeysStillAccepted(t *testing.T) {
	// only old litellm_catalog_* keys set; config should still validate
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/attractor/engine -run ModelDB -v`
Expected: FAIL because new keys are unknown/unused.

**Step 3: Implement config alias resolution**

```go
type RunConfigFile struct {
	ModelDB struct {
		OpenRouterModelInfoPath           string `json:"openrouter_model_info_path" yaml:"openrouter_model_info_path"`
		OpenRouterModelInfoUpdatePolicy   string `json:"openrouter_model_info_update_policy" yaml:"openrouter_model_info_update_policy"`
		OpenRouterModelInfoURL            string `json:"openrouter_model_info_url" yaml:"openrouter_model_info_url"`
		OpenRouterModelInfoFetchTimeoutMS int    `json:"openrouter_model_info_fetch_timeout_ms" yaml:"openrouter_model_info_fetch_timeout_ms"`

		// Deprecated compatibility aliases.
		LiteLLMCatalogPath           string `json:"litellm_catalog_path" yaml:"litellm_catalog_path"`
		LiteLLMCatalogUpdatePolicy   string `json:"litellm_catalog_update_policy" yaml:"litellm_catalog_update_policy"`
		LiteLLMCatalogURL            string `json:"litellm_catalog_url" yaml:"litellm_catalog_url"`
		LiteLLMCatalogFetchTimeoutMS int    `json:"litellm_catalog_fetch_timeout_ms" yaml:"litellm_catalog_fetch_timeout_ms"`
	} `json:"modeldb" yaml:"modeldb"`
}
```

Set defaults to OpenRouter URL and resolve effective values from new keys first, old keys second.

**Step 4: Run tests to verify pass**

Run: `go test ./internal/attractor/engine -run 'LoadRunConfigFile.*ModelDB|LoadRunConfigFile_YAMLAndJSON' -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/config.go internal/attractor/engine/config_test.go
git commit -m "feat(engine/config): add openrouter modeldb keys with deprecated litellm key compatibility"
```

## Task 5: Wire Engine/Resume/Artifacts to New Catalog Source and Names

**Files:**
- Modify: `internal/attractor/engine/run_with_config.go`
- Modify: `internal/attractor/engine/engine.go`
- Modify: `internal/attractor/engine/cxdb_events.go`
- Modify: `internal/attractor/engine/resume.go`
- Modify: `internal/attractor/engine/resume_catalog_test.go`
- Modify: `internal/attractor/engine/run_with_config_integration_test.go`

**Step 1: Write failing tests for new artifact + resume fallback behavior**

```go
func TestResume_WithRunConfig_RequiresPerRunModelCatalogSnapshot_OpenRouterName(t *testing.T) {
	_ = os.Remove(filepath.Join(logsRoot, "modeldb", "openrouter_models.json"))
	if _, err := Resume(ctx, logsRoot); err == nil {
		t.Fatalf("expected missing snapshot error")
	}
}
```

Also add test: if `openrouter_models.json` missing but legacy `litellm_catalog.json` exists, resume succeeds.

**Step 2: Run tests to verify failure**

Run: `go test ./internal/attractor/engine -run Resume.*ModelCatalog -v`
Expected: FAIL before wiring changes.

**Step 3: Implement runtime wiring**

```go
resolved, err := modeldb.ResolveModelCatalog(...)
catalog, err := modeldb.LoadCatalogFromOpenRouterJSON(resolved.SnapshotPath)
eng.ModelCatalogPath = resolved.SnapshotPath
eng.ModelCatalogSource = resolved.Source
eng.ModelCatalogSHA = catalog.SHA256
```

Manifest/cxdb artifacts:
- Prefer `modeldb/openrouter_models.json`
- Keep writing legacy manifest keys (`litellm_catalog_*`) for one release to preserve external readers.

Resume path selection order:
1. Manifest new key path
2. `logs_root/modeldb/openrouter_models.json`
3. Manifest legacy key path
4. `logs_root/modeldb/litellm_catalog.json`

**Step 4: Run tests to verify pass**

Run: `go test ./internal/attractor/engine -run 'Resume.*Catalog|RunWithConfig_.*' -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/run_with_config.go internal/attractor/engine/engine.go internal/attractor/engine/cxdb_events.go internal/attractor/engine/resume.go internal/attractor/engine/resume_catalog_test.go internal/attractor/engine/run_with_config_integration_test.go
git commit -m "refactor(engine): switch runtime modeldb snapshot flow to OpenRouter naming with resume compatibility"
```

## Task 6: Migrate Router/Preflight to Generic Catalog Types

**Files:**
- Modify: `internal/attractor/engine/codergen_router.go`
- Modify: `internal/attractor/engine/run_with_config.go`
- Modify: `internal/attractor/engine/codergen_failover_test.go`
- Modify: `internal/attractor/engine/model_catalog_metadata_only_test.go`

**Step 1: Write failing failover/preflight tests for OpenRouter-style IDs**

```go
func TestCatalogHasProviderModel_OpenRouterCanonicalIDs(t *testing.T) {
	c := &modeldb.Catalog{Models: map[string]modeldb.ModelEntry{
		"openai/gpt-5.2-codex": {Provider: "openai"},
	}}
	if !modeldb.CatalogHasProviderModel(c, "openai", "gpt-5.2-codex") {
		t.Fatalf("expected openrouter canonical id mapping")
	}
}
```

**Step 2: Run tests to verify failure**

Run: `go test ./internal/attractor/engine -run 'Failover|ModelCatalogIsMetadataOnly' -v`
Expected: FAIL while old types/functions remain.

**Step 3: Implement minimal updates**

- Change router catalog type from `*modeldb.LiteLLMCatalog` to `*modeldb.Catalog`.
- Replace local provider-model matching helpers with `modeldb.CatalogHasProviderModel`.
- Keep failover strategy order unchanged.

**Step 4: Run tests to verify pass**

Run: `go test ./internal/attractor/engine -run 'Failover|ModelCatalogIsMetadataOnly|RunWithConfig_FailsFastWhenProviderBackendMissing' -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/codergen_router.go internal/attractor/engine/run_with_config.go internal/attractor/engine/codergen_failover_test.go internal/attractor/engine/model_catalog_metadata_only_test.go
git commit -m "refactor(engine/router): use generic modeldb catalog for preflight model validation and failover selection"
```

## Task 7: Update `internal/llm` Model Catalog Loader to OpenRouter Payload

**Files:**
- Modify: `internal/llm/model_catalog.go`
- Modify: `internal/llm/model_catalog_test.go`

**Step 1: Write failing test with OpenRouter payload shape**

```go
func TestLoadModelCatalogFromOpenRouterJSON_GetListLatest(t *testing.T) {
	p := writeTempFile(t, `{"data":[{"id":"openai/gpt-5","context_length":272000,"pricing":{"prompt":"0.000001","completion":"0.00001"},"supported_parameters":["tools"],"architecture":{"input_modalities":["text"],"output_modalities":["text"]}}]}`)
	c, err := LoadModelCatalogFromOpenRouterJSON(p)
	if err != nil { t.Fatal(err) }
	if c.GetModelInfo("openai/gpt-5") == nil { t.Fatalf("expected model") }
}
```

**Step 2: Run test to verify failure**

Run: `go test ./internal/llm -run OpenRouter -v`
Expected: FAIL with undefined loader.

**Step 3: Implement loader + compatibility wrapper**

```go
func LoadModelCatalogFromOpenRouterJSON(path string) (*ModelCatalog, error) { /* parse data[] */ }

// Deprecated; keep for compatibility.
func LoadModelCatalogFromLiteLLMJSON(path string) (*ModelCatalog, error) {
	return LoadModelCatalogFromOpenRouterJSON(path)
}
```

**Step 4: Run tests to verify pass**

Run: `go test ./internal/llm -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/llm/model_catalog.go internal/llm/model_catalog_test.go
git commit -m "feat(llm): parse normalized model metadata from OpenRouter model list payload"
```

## Task 8: Pinned Snapshot, Scripts, and Documentation Migration

**Files:**
- Create: `internal/attractor/modeldb/pinned/openrouter_models.json`
- Modify: `scripts/run_benchmarks.sh`
- Modify: `README.md`
- Modify: `docs/strongdm/attractor/kilroy-metaspec.md`
- Modify: `docs/strongdm/attractor/test-coverage-map.md`
- Modify: `docs/strongdm/attractor/reliability-troubleshooting.md`
- Modify: `skills/using-kilroy/SKILL.md`
- Modify: `skills/english-to-dotfile/SKILL.md`

**Step 1: Add failing doc/script assertions**

Run a grep guard script expecting no required runtime references to old keys:

```bash
rg -n "modeldb\.litellm_catalog|litellm_catalog\.json|model_prices_and_context_window" README.md docs/strongdm/attractor scripts/run_benchmarks.sh skills
```

Expected initially: matches found.

**Step 2: Generate pinned OpenRouter snapshot**

Run:

```bash
curl -fsSL https://openrouter.ai/api/v1/models | jq '.data |= sort_by(.id)' > internal/attractor/modeldb/pinned/openrouter_models.json
```

**Step 3: Update docs/scripts/config examples**

Key renames:
- `litellm_catalog_path` -> `openrouter_model_info_path`
- `litellm_catalog_update_policy` -> `openrouter_model_info_update_policy`
- `litellm_catalog_url` -> `openrouter_model_info_url`
- `litellm_catalog_fetch_timeout_ms` -> `openrouter_model_info_fetch_timeout_ms`
- `modeldb/litellm_catalog.json` -> `modeldb/openrouter_models.json`

Retain one explicit note in docs that old keys are deprecated but supported temporarily.

**Step 4: Run validation checks**

Run:

```bash
go test ./internal/attractor/... ./internal/llm/...
rg -n "litellm_catalog_path|litellm_catalog_update_policy|litellm_catalog_url|litellm_catalog_fetch_timeout_ms" README.md docs/strongdm/attractor scripts/run_benchmarks.sh skills
```

Expected:
- tests PASS
- grep only returns explicit deprecation notes (or zero results)

**Step 5: Commit**

```bash
git add internal/attractor/modeldb/pinned/openrouter_models.json scripts/run_benchmarks.sh README.md docs/strongdm/attractor/kilroy-metaspec.md docs/strongdm/attractor/test-coverage-map.md docs/strongdm/attractor/reliability-troubleshooting.md skills/using-kilroy/SKILL.md skills/english-to-dotfile/SKILL.md
git commit -m "docs(modeldb): migrate runtime and user guidance from LiteLLM catalog terminology to OpenRouter model info"
```

## Task 9: Final Cleanup and Full Regression Pass

**Files:**
- Modify: `internal/attractor/modeldb/litellm.go`
- Modify: `internal/attractor/modeldb/litellm_test.go`
- Modify: `internal/attractor/modeldb/litellm_resolve_test.go`
- Modify: `internal/attractor/modeldb/litellm_resolve_warning_test.go`

**Step 1: Write cleanup test expectations**

- Add assertions that deprecated wrappers call the new OpenRouter-backed implementation.
- Add TODO deadline comment for wrapper removal.

**Step 2: Run targeted tests to verify current failure state**

Run: `go test ./internal/attractor/modeldb -run LiteLLM -v`
Expected: FAIL if wrappers are stale.

**Step 3: Implement deprecation wrappers cleanly**

```go
// Deprecated: use LoadCatalogFromOpenRouterJSON.
func LoadLiteLLMCatalog(path string) (*Catalog, error) {
	return LoadCatalogFromOpenRouterJSON(path)
}
```

Do the same for resolver wrappers.

**Step 4: Full regression test run**

Run:

```bash
go test ./internal/attractor/modeldb ./internal/attractor/engine ./internal/llm
```

Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/modeldb/litellm.go internal/attractor/modeldb/litellm_test.go internal/attractor/modeldb/litellm_resolve_test.go internal/attractor/modeldb/litellm_resolve_warning_test.go
git commit -m "chore(modeldb): convert LiteLLM symbols to deprecated wrappers over OpenRouter-backed catalog path"
```

## Verification Matrix (Run at End)

```bash
go test ./internal/attractor/modeldb -v
go test ./internal/attractor/engine -v
go test ./internal/llm -v
./kilroy attractor run --graph demo/dttf/dttf.dot --config /tmp/run-config-openrouter.yaml --allow-test-shim
./kilroy attractor resume --logs-root /tmp/<run>/logs
```

Expected outcomes:
- `modeldb/openrouter_models.json` exists in logs root.
- `manifest.json` contains new modeldb catalog keys and legacy compatibility keys.
- Resume succeeds with new snapshot name and with legacy fallback file.
- CLI/API routing behavior unchanged from pre-migration behavior.

## References

- `@skills/using-kilroy/SKILL.md`
- `@skills/english-to-dotfile/SKILL.md`
- OpenRouter models endpoint docs: `https://openrouter.ai/docs/api-reference/list-available-models`
