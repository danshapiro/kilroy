# Provider Model Catalog Robustness Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Make `attractor run` robust with real OpenRouter model catalogs for Kimi/ZAI and make `--force-model` usable for all built-in providers.

**Architecture:** Unify provider canonicalization through a single source of truth (`providerspec.CanonicalProviderKey`) so config parsing, model catalog loading, and preflight provider/model matching agree on aliases. Add explicit alias coverage for OpenRouter provider prefixes (`moonshotai`, `z-ai`) and harden CLI `--force-model` parsing to accept canonical providers and aliases for all built-ins. Lock the behavior with regression tests at both unit and preflight integration boundaries.

**Tech Stack:** Go (`go test`), Kilroy attractor engine (`internal/attractor/*`), provider metadata (`internal/providerspec`), CLI (`cmd/kilroy`).

---

### Task 1: Reproduce the preflight regression with a focused failing test

**Files:**
- Modify: `internal/attractor/engine/provider_preflight_test.go`
- Test: `internal/attractor/engine/provider_preflight_test.go`

**Step 1: Write the failing test**

Add a regression test that uses OpenRouter-style model IDs (`moonshotai/...`, `z-ai/...`) while graph nodes still use `llm_provider=kimi|zai` with provider-relative model IDs:

```go
func TestRunWithConfig_AllowsKimiAndZai_WhenCatalogUsesOpenRouterPrefixes(t *testing.T) {
	t.Setenv("KILROY_PREFLIGHT_PROMPT_PROBES", "off")

	repo := initTestRepo(t)
	catalog := writeCatalogForPreflight(t, `{
  "data": [
    {"id": "moonshotai/kimi-k2.5"},
    {"id": "z-ai/glm-4.7"}
  ]
}`)

	cfg := testPreflightConfigForProviders(repo, catalog, map[string]BackendKind{
		"kimi": BackendAPI,
		"zai":  BackendAPI,
	})
	dot := []byte(`
digraph G {
  start [shape=Mdiamond]
  a [shape=box, llm_provider="kimi", llm_model="kimi-k2.5", prompt="x"]
  b [shape=box, llm_provider="zai", llm_model="glm-4.7", prompt="x"]
  exit [shape=Msquare]
  start -> a -> b -> exit
}`)

	logsRoot := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "preflight-openrouter-prefix", LogsRoot: logsRoot})
	if err == nil {
		t.Fatalf("expected downstream cxdb error, got nil")
	}
	if strings.Contains(err.Error(), "preflight:") {
		t.Fatalf("unexpected preflight failure: %v", err)
	}
	report := mustReadPreflightReport(t, logsRoot)
	if report.Summary.Fail != 0 {
		t.Fatalf("expected preflight pass summary, got %+v", report.Summary)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/attractor/engine -run TestRunWithConfig_AllowsKimiAndZai_WhenCatalogUsesOpenRouterPrefixes -count=1`

Expected: FAIL with `preflight: llm_provider=kimi ... not present in run catalog`.

**Step 3: Commit test-only regression capture**

```bash
git add internal/attractor/engine/provider_preflight_test.go
git commit -m "test(preflight): reproduce kimi/zai catalog alias mismatch with openrouter provider prefixes"
```

### Task 2: Unify provider canonicalization and add alias coverage

**Files:**
- Modify: `internal/modelmeta/modelmeta.go`
- Modify: `internal/providerspec/builtin.go`
- Modify: `internal/providerspec/spec_test.go`
- Create: `internal/modelmeta/modelmeta_test.go`
- Test: `internal/modelmeta/modelmeta_test.go`
- Test: `internal/providerspec/spec_test.go`

**Step 1: Write the failing unit tests**

Create `internal/modelmeta/modelmeta_test.go`:

```go
package modelmeta

import "testing"

func TestNormalizeProvider_UsesCanonicalAliases(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{in: "gemini", want: "google"},
		{in: "google_ai_studio", want: "google"},
		{in: "moonshot", want: "kimi"},
		{in: "moonshotai", want: "kimi"},
		{in: "z-ai", want: "zai"},
		{in: "z.ai", want: "zai"},
		{in: " openai ", want: "openai"},
	}
	for _, tc := range cases {
		if got := NormalizeProvider(tc.in); got != tc.want {
			t.Fatalf("NormalizeProvider(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}
```

Extend `internal/providerspec/spec_test.go`:

```go
if got := CanonicalProviderKey("moonshotai"); got != "kimi" {
	t.Fatalf("moonshotai alias: got %q want %q", got, "kimi")
}
if got := CanonicalProviderKey("google_ai_studio"); got != "google" {
	t.Fatalf("google_ai_studio alias: got %q want %q", got, "google")
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/modelmeta ./internal/providerspec -run 'TestNormalizeProvider_UsesCanonicalAliases|TestCanonicalProviderKey_Aliases' -count=1`

Expected: FAIL because `moonshotai` and `google_ai_studio` are not canonicalized yet.

**Step 3: Write minimal implementation**

Update provider aliases in `internal/providerspec/builtin.go`:

```go
"google": {
	Key:     "google",
	Aliases: []string{"gemini", "google_ai_studio"},
	// ...
},
"kimi": {
	Key:     "kimi",
	Aliases: []string{"moonshot", "moonshotai"},
	// ...
},
```

Delegate `NormalizeProvider` to provider spec canonicalization in `internal/modelmeta/modelmeta.go`:

```go
import "github.com/strongdm/kilroy/internal/providerspec"

func NormalizeProvider(p string) string {
	return providerspec.CanonicalProviderKey(p)
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/modelmeta ./internal/providerspec -count=1`

Expected: PASS.

**Step 5: Commit**

```bash
git add internal/modelmeta/modelmeta.go internal/modelmeta/modelmeta_test.go internal/providerspec/builtin.go internal/providerspec/spec_test.go
git commit -m "fix(provider-canonicalization): unify aliases through providerspec and add moonshotai/google_ai_studio support"
```

### Task 3: Lock catalog matching behavior with unit tests

**Files:**
- Modify: `internal/attractor/modeldb/catalog_test.go`
- Test: `internal/attractor/modeldb/catalog_test.go`

**Step 1: Write the tests**

Add:

```go
func TestCatalogHasProviderModel_AcceptsOpenRouterProviderPrefixes(t *testing.T) {
	c := &Catalog{Models: map[string]ModelEntry{
		"moonshotai/kimi-k2.5": {},
		"z-ai/glm-4.7":         {},
	}}
	if !CatalogHasProviderModel(c, "kimi", "kimi-k2.5") {
		t.Fatalf("expected kimi provider-relative model to match moonshotai prefix")
	}
	if !CatalogHasProviderModel(c, "kimi", "moonshotai/kimi-k2.5") {
		t.Fatalf("expected kimi canonical/openrouter id to match")
	}
	if !CatalogHasProviderModel(c, "zai", "glm-4.7") {
		t.Fatalf("expected zai provider-relative model to match z-ai prefix")
	}
	if !CatalogHasProviderModel(c, "zai", "z-ai/glm-4.7") {
		t.Fatalf("expected zai canonical/openrouter id to match")
	}
}
```

**Step 2: Run tests**

Run: `go test ./internal/attractor/modeldb -run 'TestCatalogHasProviderModel_' -count=1`

Expected: PASS (locks regression fix).

**Step 3: Commit**

```bash
git add internal/attractor/modeldb/catalog_test.go
git commit -m "test(modeldb): cover catalog matching for moonshotai and z-ai provider prefixes"
```

### Task 4: Make CLI force-model overrides support Kimi/ZAI and aliases

**Files:**
- Modify: `cmd/kilroy/main.go`
- Modify: `cmd/kilroy/main_exit_codes_test.go`
- Test: `cmd/kilroy/main_exit_codes_test.go`

**Step 1: Write the failing CLI parser test**

Add test in `cmd/kilroy/main_exit_codes_test.go`:

```go
func TestParseForceModelFlags_AcceptsKimiAndZaiAliases(t *testing.T) {
	got, specs, err := parseForceModelFlags([]string{
		"moonshot=kimi-k2.5",
		"z-ai=glm-4.7",
	})
	if err != nil {
		t.Fatalf("parseForceModelFlags: %v", err)
	}
	want := map[string]string{
		"kimi": "kimi-k2.5",
		"zai":  "glm-4.7",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("overrides: got %#v want %#v", got, want)
	}
	wantSpecs := []string{
		"kimi=kimi-k2.5",
		"zai=glm-4.7",
	}
	if !reflect.DeepEqual(specs, wantSpecs) {
		t.Fatalf("canonical specs: got %#v want %#v", specs, wantSpecs)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./cmd/kilroy -run TestParseForceModelFlags_AcceptsKimiAndZaiAliases -count=1`

Expected: FAIL with unsupported provider error.

**Step 3: Write minimal implementation**

In `cmd/kilroy/main.go`, use provider-spec canonicalization and allow all built-in provider keys:

```go
import "github.com/strongdm/kilroy/internal/providerspec"

func normalizeRunProviderKey(provider string) string {
	return providerspec.CanonicalProviderKey(provider)
}

func isSupportedForceModelProvider(provider string) bool {
	_, ok := providerspec.Builtins()[provider]
	return ok
}
```

Update parse check:

```go
if !isSupportedForceModelProvider(provider) {
	return nil, nil, fmt.Errorf("--force-model %q has unsupported provider %q (allowed: openai, anthropic, google, kimi, zai)", raw, strings.TrimSpace(parts[0]))
}
```

**Step 4: Run CLI tests**

Run: `go test ./cmd/kilroy -run 'TestParseForceModelFlags_|TestUsage_IncludesAllowTestShimFlag' -count=1`

Expected: PASS.

**Step 5: Commit**

```bash
git add cmd/kilroy/main.go cmd/kilroy/main_exit_codes_test.go
git commit -m "feat(cli): allow --force-model for kimi and zai using canonical provider aliases"
```

### Task 5: Update docs for provider aliases and force-model behavior

**Files:**
- Modify: `README.md`
- Modify: `docs/strongdm/attractor/README.md`
- Modify: `docs/strongdm/attractor/provider-plugin-migration.md`

**Step 1: Update README provider alias/operator guidance**

Add explicit alias guidance near provider setup:

```md
- Provider aliases: `gemini -> google`, `google_ai_studio -> google`, `moonshot`/`moonshotai -> kimi`, `z-ai`/`z.ai -> zai`.
- `--force-model` supports built-in providers: `openai`, `anthropic`, `google`, `kimi`, `zai` (aliases accepted).
```

**Step 2: Update attractor docs**

Mirror the same alias and force-model support notes in:
- `docs/strongdm/attractor/README.md`
- `docs/strongdm/attractor/provider-plugin-migration.md`

**Step 3: Commit docs**

```bash
git add README.md docs/strongdm/attractor/README.md docs/strongdm/attractor/provider-plugin-migration.md
git commit -m "docs(attractor): document openrouter alias canonicalization and force-model provider support"
```

### Task 6: Final validation and integration commit

**Files:**
- Validate-only task (no file edits expected)

**Step 1: Run focused package tests**

Run:

```bash
go test ./internal/modelmeta ./internal/providerspec ./internal/attractor/modeldb ./internal/attractor/engine ./cmd/kilroy -count=1
```

Expected: PASS.

**Step 2: Run full suite**

Run:

```bash
go test ./... -count=1
```

Expected: PASS.

**Step 3: Validate original reproduction path**

Use corrected JSON shape and `llm.providers.*` placement, then run:

```bash
./kilroy attractor run --detach --graph demo/rogue/rogue_fast.dot --config <run_config_real.json> --run-id <id> --logs-root <logs_root>
```

Expected: preflight no longer fails with `llm_provider=kimi ... not present in run catalog`.

**Step 4: Final commit**

```bash
git add -A
git commit -m "fix(attractor): robust provider/model canonicalization across preflight catalog checks and CLI force-model overrides"
```
