# Minimax M2.5 Provider Support Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add Minimax M2.5 as a built-in LLM provider so Kilroy pipelines can target `minimax` models via `MINIMAX_API_KEY`.

**Architecture:** Minimax exposes an OpenAI-compatible chat completions API at `https://api.minimax.io/v1/chat/completions`. This means no new adapter package is needed — the existing `openaicompat` adapter handles the wire protocol. We register Minimax as a new built-in provider spec (like ZAI and Cerebras) and wire it through the existing `ProtocolOpenAIChatCompletions` code path in `api_client_from_runtime.go`. We also add a `MINIMAX_BASE_URL` environment override for development/proxy use.

**Tech Stack:** Go, existing `openaicompat` adapter, `providerspec` package.

**Key Minimax API facts (from research):**
- Base URL: `https://api.minimax.io`
- Path: `/v1/chat/completions`
- Auth: `Authorization: Bearer <key>` (standard)
- Model IDs: `MiniMax-M2.5`, `MiniMax-M2.5-lightning`
- Env var: `MINIMAX_API_KEY`
- Supports: streaming, tool calling, reasoning (via `reasoning_split: true` in request body)
- Context window: ~200K tokens
- No special execution policy needed (no forced streaming or min tokens)

---

### Task 1: Register Minimax in Provider Spec

**Files:**
- Modify: `internal/providerspec/builtin.go:101` (add entry before closing brace of `builtinSpecs`)

**Step 1: Add the minimax provider spec**

Add this entry to the `builtinSpecs` map, after the `"cerebras"` entry:

```go
"minimax": {
    Key:     "minimax",
    Aliases: []string{"minimax-ai"},
    API: &APISpec{
        Protocol:           ProtocolOpenAIChatCompletions,
        DefaultBaseURL:     "https://api.minimax.io",
        DefaultPath:        "/v1/chat/completions",
        DefaultAPIKeyEnv:   "MINIMAX_API_KEY",
        ProviderOptionsKey: "minimax",
        ProfileFamily:      "openai",
    },
    Failover: []string{"cerebras"},
},
```

**Rationale:**
- `ProtocolOpenAIChatCompletions` — Minimax's API is OpenAI-compatible, so it routes through the `openaicompat` adapter automatically (see `api_client_from_runtime.go:35-43`).
- `ProviderOptionsKey: "minimax"` — allows `.dot` files to pass Minimax-specific options like `reasoning_split` via `provider_options.minimax`.
- `ProfileFamily: "openai"` — Minimax follows the OpenAI request/response shape.
- `Failover: []string{"cerebras"}` — Cerebras is a reasonable fast fallback.
- `Aliases: []string{"minimax-ai"}` — common alternative name.

**Step 2: Run existing tests to verify nothing broke**

Run: `go test ./internal/providerspec/ -v`
Expected: All existing tests pass. The new entry is additive.

**Step 3: Commit**

```
git add internal/providerspec/builtin.go
git commit -m "feat(providerspec): register minimax as built-in provider

Add Minimax M2.5 to the builtin provider specs using the OpenAI chat
completions protocol. Uses https://api.minimax.io/v1/chat/completions
with MINIMAX_API_KEY env var. Failover chain: minimax → cerebras."
```

---

### Task 2: Add Provider Spec Tests for Minimax

**Files:**
- Modify: `internal/providerspec/spec_test.go`

**Step 1: Write the tests**

Add three items to existing tests and one new test function.

First, update `TestBuiltinSpecsIncludeCoreAndNewProviders` — add `"minimax"` to the providers list:

```go
for _, key := range []string{"openai", "anthropic", "google", "kimi", "zai", "cerebras", "minimax"} {
```

Second, add alias assertions to `TestCanonicalProviderKey_Aliases`:

```go
if got := CanonicalProviderKey("minimax-ai"); got != "minimax" {
    t.Fatalf("minimax-ai alias: got %q want %q", got, "minimax")
}
```

Third, add a new test function for Minimax defaults (after `TestBuiltinKimiDefaultsToCodingAnthropicAPI`):

```go
func TestBuiltinMinimaxDefaultsToOpenAICompatAPI(t *testing.T) {
	spec, ok := Builtin("minimax")
	if !ok {
		t.Fatalf("expected minimax builtin")
	}
	if spec.API == nil {
		t.Fatalf("expected minimax api spec")
	}
	if got := spec.API.Protocol; got != ProtocolOpenAIChatCompletions {
		t.Fatalf("minimax protocol: got %q want %q", got, ProtocolOpenAIChatCompletions)
	}
	if got := spec.API.DefaultBaseURL; got != "https://api.minimax.io" {
		t.Fatalf("minimax base url: got %q want %q", got, "https://api.minimax.io")
	}
	if got := spec.API.DefaultAPIKeyEnv; got != "MINIMAX_API_KEY" {
		t.Fatalf("minimax api_key_env: got %q want %q", got, "MINIMAX_API_KEY")
	}
}
```

Fourth, add minimax to `TestBuiltinFailoverDefaultsAreSingleHop`:

```go
{provider: "minimax", want: []string{"cerebras"}},
```

**Step 2: Run the tests**

Run: `go test ./internal/providerspec/ -v`
Expected: All pass, including the new minimax tests.

**Step 3: Commit**

```
git add internal/providerspec/spec_test.go
git commit -m "test(providerspec): add minimax builtin spec assertions

Verify minimax appears in builtins, alias resolves, defaults match
OpenAI chat completions protocol, and failover chain is correct."
```

---

### Task 3: Add MINIMAX_BASE_URL Environment Override

**Files:**
- Modify: `internal/attractor/engine/api_client_from_runtime.go:52-74`

**Step 1: Write the failing test**

Add to `internal/attractor/engine/api_client_from_runtime_test.go`:

```go
func TestResolveBuiltInBaseURLOverride_MinimaxUsesEnvOverride(t *testing.T) {
	t.Setenv("MINIMAX_BASE_URL", "http://127.0.0.1:8888")
	got := resolveBuiltInBaseURLOverride("minimax", "https://api.minimax.io")
	if got != "http://127.0.0.1:8888" {
		t.Fatalf("minimax base url override mismatch: got %q want %q", got, "http://127.0.0.1:8888")
	}
}

func TestResolveBuiltInBaseURLOverride_MinimaxDoesNotOverrideCustom(t *testing.T) {
	t.Setenv("MINIMAX_BASE_URL", "http://127.0.0.1:8888")
	got := resolveBuiltInBaseURLOverride("minimax", "https://custom.minimax.internal")
	if got != "https://custom.minimax.internal" {
		t.Fatalf("explicit minimax base url should win, got %q", got)
	}
}
```

**Step 2: Run tests to see them fail**

Run: `go test ./internal/attractor/engine/ -run TestResolveBuiltInBaseURLOverride_Minimax -v`
Expected: FAIL — override not yet implemented.

**Step 3: Add the minimax case to `resolveBuiltInBaseURLOverride`**

In `api_client_from_runtime.go`, add a new case in the switch after the `"google"` case:

```go
case "minimax":
    if env := strings.TrimSpace(os.Getenv("MINIMAX_BASE_URL")); env != "" {
        if normalized == "" || normalized == "https://api.minimax.io" {
            return env
        }
    }
```

**Step 4: Run the tests**

Run: `go test ./internal/attractor/engine/ -run TestResolveBuiltInBaseURLOverride -v`
Expected: All pass.

**Step 5: Commit**

```
git add internal/attractor/engine/api_client_from_runtime.go internal/attractor/engine/api_client_from_runtime_test.go
git commit -m "feat(engine): support MINIMAX_BASE_URL env override

Allow overriding the Minimax base URL via MINIMAX_BASE_URL environment
variable, matching the pattern used by OpenAI, Anthropic, and Google."
```

---

### Task 4: Add Runtime Registration Test for Minimax

**Files:**
- Modify: `internal/attractor/engine/api_client_from_runtime_test.go`

**Step 1: Write the test**

Add after the existing runtime registration tests:

```go
func TestNewAPIClientFromProviderRuntimes_RegistersMinimaxViaOpenAICompat(t *testing.T) {
	runtimes := map[string]ProviderRuntime{
		"minimax": {
			Key:     "minimax",
			Backend: BackendAPI,
			API: providerspec.APISpec{
				Protocol:           providerspec.ProtocolOpenAIChatCompletions,
				DefaultBaseURL:     "http://127.0.0.1:0",
				DefaultPath:        "/v1/chat/completions",
				DefaultAPIKeyEnv:   "MINIMAX_API_KEY",
				ProviderOptionsKey: "minimax",
			},
		},
	}
	t.Setenv("MINIMAX_API_KEY", "test-key")
	c, err := newAPIClientFromProviderRuntimes(runtimes)
	if err != nil {
		t.Fatalf("newAPIClientFromProviderRuntimes: %v", err)
	}
	if len(c.ProviderNames()) != 1 || c.ProviderNames()[0] != "minimax" {
		t.Fatalf("expected minimax adapter, got %v", c.ProviderNames())
	}
}
```

**Step 2: Run the test**

Run: `go test ./internal/attractor/engine/ -run TestNewAPIClientFromProviderRuntimes_RegistersMinimaxViaOpenAICompat -v`
Expected: PASS (the `ProtocolOpenAIChatCompletions` switch case already handles this — no new production code needed).

**Step 3: Commit**

```
git add internal/attractor/engine/api_client_from_runtime_test.go
git commit -m "test(engine): verify minimax registers via openaicompat adapter

Confirm that a minimax provider runtime with ProtocolOpenAIChatCompletions
correctly creates an openaicompat adapter in the API client."
```

---

### Task 5: Verify Execution Policy (No Special Policy Needed)

**Files:**
- Modify: `internal/llm/provider_execution_policy_test.go`

**Step 1: Add minimax to the "no special policy" test**

Read the existing test file first. The test at line 19 lists providers that should have no special policy. Add `"minimax"` to that list:

```go
for _, provider := range []string{"openai", "anthropic", "google", "zai", "minimax"} {
```

**Step 2: Run the test**

Run: `go test ./internal/llm/ -run TestExecutionPolicy -v`
Expected: PASS — minimax has no special execution policy (unlike Kimi which requires forced streaming).

**Step 3: Commit**

```
git add internal/llm/provider_execution_policy_test.go
git commit -m "test(llm): verify minimax has no special execution policy

Minimax does not require forced streaming or minimum max_tokens,
unlike Kimi. Add to the no-special-policy assertion list."
```

---

### Task 6: Run Full Test Suite and Final Verification

**Step 1: Run all affected test packages**

Run: `go test ./internal/providerspec/ ./internal/llm/... ./internal/attractor/engine/ -v -count=1`
Expected: All tests pass.

**Step 2: Run go vet**

Run: `go vet ./internal/providerspec/ ./internal/llm/... ./internal/attractor/engine/`
Expected: No warnings.

**Step 3: Final commit (if any cleanup needed)**

If all tests pass with no issues, no commit needed here.

---

## Summary of Changes

| File | Change |
|------|--------|
| `internal/providerspec/builtin.go` | Add `"minimax"` entry to `builtinSpecs` |
| `internal/providerspec/spec_test.go` | Add minimax to builtins check, alias test, defaults test, failover test |
| `internal/attractor/engine/api_client_from_runtime.go` | Add `"minimax"` case to `resolveBuiltInBaseURLOverride` |
| `internal/attractor/engine/api_client_from_runtime_test.go` | Add base URL override tests and runtime registration test |
| `internal/llm/provider_execution_policy_test.go` | Add `"minimax"` to no-special-policy list |

## What We Don't Need to Change

- **No new adapter package** — Minimax uses standard OpenAI chat completions format, handled by `openaicompat`.
- **No changes to `llmclient/env.go`** — ZAI and Cerebras aren't imported there either; the `openaicompat` adapter is created dynamically by `api_client_from_runtime.go` based on the protocol in the provider spec.
- **No execution policy** — Minimax doesn't require forced streaming or minimum token counts.
- **No new protocol constant** — `ProtocolOpenAIChatCompletions` already exists.

## Usage After Implementation

Set the environment variable and use in `.dot` pipelines:

```bash
export MINIMAX_API_KEY="your-key-here"
```

In a `.dot` file node:
```dot
node [provider="minimax" model="MiniMax-M2.5"]
```

For the lightning variant:
```dot
node [provider="minimax" model="MiniMax-M2.5-lightning"]
```

To pass Minimax-specific options (like reasoning):
```dot
node [provider="minimax" model="MiniMax-M2.5" provider_options='{"minimax":{"reasoning_split":true}}']
```
