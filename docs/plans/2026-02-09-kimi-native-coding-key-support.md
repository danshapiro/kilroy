# Kimi Native Coding Key Support Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add first-class Kilroy support for Kimi Coding subscription keys so `llm_provider=kimi` works out of the box with current Kimi Coding API behavior.

**Architecture:** Move the built-in `kimi` provider contract from Moonshot OpenAI Chat Completions to Kimi Coding Anthropic Messages, while preserving existing runtime/provider abstractions. Add a provider-aware Anthropic caching default so Kimi uses conservative request payloads that its API accepts. Expand tests and docs to lock behavior and prevent regression.

**Tech Stack:** Go (`internal/providerspec`, `internal/attractor/engine`, `internal/llm/providers/anthropic`), existing preflight/integration test harness, markdown docs.

---

### Task 1: Lock Expected Behavior With Failing Tests

**Files:**
- Modify: `internal/providerspec/spec_test.go`
- Modify: `internal/attractor/engine/provider_runtime_test.go`
- Modify: `internal/attractor/engine/kimi_zai_api_integration_test.go`
- Modify: `internal/attractor/engine/provider_preflight_test.go`

**Step 1: Write failing provider-spec/runtime expectations for Kimi coding defaults**

```go
spec, _ := Builtin("kimi")
if spec.API.Protocol != ProtocolAnthropicMessages { ... }
if spec.API.DefaultBaseURL != "https://api.kimi.com/coding" { ... }
if spec.API.DefaultAPIKeyEnv != "KIMI_API_KEY" { ... }
```

And in runtime tests, assert resolved Kimi protocol is `anthropic_messages`.

**Step 2: Write failing integration/preflight path expectations**

```go
runCase("kimi", "kimi-k2.5", "KIMI_API_KEY", "/coding/v1/messages")
runCase("zai", "glm-4.7", "ZAI_API_KEY", "/api/coding/paas/v4/chat/completions")
```

In preflight tests, accept Kimi prompt-probe on Anthropic messages route and ZAI on OpenAI-compat route.

**Step 3: Run targeted tests to verify red**

Run:
```bash
go test ./internal/providerspec -run Kimi -count=1
go test ./internal/attractor/engine -run 'Kimi|Zai|PreflightPromptProbe_AllProvidersWhenGraphUsesAll' -count=1
```

Expected: failures on old Moonshot/chat-completions assumptions.

**Step 4: Commit red tests**

```bash
git add internal/providerspec/spec_test.go internal/attractor/engine/provider_runtime_test.go internal/attractor/engine/kimi_zai_api_integration_test.go internal/attractor/engine/provider_preflight_test.go
git commit -m "test(kimi): lock native coding endpoint/protocol expectations"
```

### Task 2: Implement Native Kimi Coding Provider Contract

**Files:**
- Modify: `internal/providerspec/builtin.go`
- Modify: `internal/attractor/engine/provider_preflight_test.go` (fixtures/configs where protocol/path is explicit)
- Modify: `internal/attractor/engine/run_with_config_test.go` (provider config test cases)
- Modify: `internal/attractor/engine/api_client_from_runtime_test.go`

**Step 1: Update built-in Kimi API spec**

```go
"kimi": {
  API: &APISpec{
    Protocol:         ProtocolAnthropicMessages,
    DefaultBaseURL:   "https://api.kimi.com/coding",
    DefaultAPIKeyEnv: "KIMI_API_KEY",
    ProfileFamily:    "openai",
  },
}
```

Keep `zai` as OpenAI chat-completions coding path.

**Step 2: Update tests that currently hard-code Kimi OpenAI chat-completions defaults**

- Kimi runtime/probe expectations should now reflect Anthropic protocol and `/coding/v1/messages` path behavior.
- Explicit per-test overrides can remain for backwards-compat path tests when intentionally asserting non-default behavior.

**Step 3: Run targeted tests to verify green**

Run:
```bash
go test ./internal/providerspec -count=1
go test ./internal/attractor/engine -run 'Kimi|Zai|RunWithConfig_AcceptsKimiAndZaiAPIProviders|PreflightPromptProbe_AllProvidersWhenGraphUsesAll' -count=1
```

Expected: pass.

**Step 4: Commit contract change**

```bash
git add internal/providerspec/builtin.go internal/attractor/engine/provider_preflight_test.go internal/attractor/engine/run_with_config_test.go internal/attractor/engine/api_client_from_runtime_test.go internal/attractor/engine/kimi_zai_api_integration_test.go internal/attractor/engine/provider_runtime_test.go internal/providerspec/spec_test.go
git commit -m "feat(kimi): make coding API contract the built-in default"
```

### Task 3: Fix Kimi Runtime Compatibility for Anthropic Prompt Caching

**Files:**
- Modify: `internal/llm/providers/anthropic/adapter.go`
- Modify: `internal/llm/providers/anthropic/adapter_test.go`

**Step 1: Add provider-aware default for auto_cache**

Implement helper signature change:

```go
func anthropicAutoCacheEnabled(provider string, opts map[string]any) bool
```

Behavior:
- Honor explicit `provider_options.anthropic.auto_cache` when present.
- Default `true` for provider `anthropic`.
- Default `false` for non-Anthropic providers (e.g., `kimi`) to avoid incompatible cache-control payloads.

**Step 2: Wire helper into `Complete` and `Stream`**

```go
autoCache := anthropicAutoCacheEnabled(a.Name(), req.ProviderOptions)
```

**Step 3: Add tests**

- Existing Anthropic tests still pass with default caching behavior.
- New test: provider `kimi` does not emit `anthropic-beta: prompt-caching-2024-07-31` by default and request body has no auto-added `cache_control` unless explicitly enabled.

**Step 4: Run tests**

```bash
go test ./internal/llm/providers/anthropic -count=1
```

Expected: pass.

**Step 5: Commit caching compatibility fix**

```bash
git add internal/llm/providers/anthropic/adapter.go internal/llm/providers/anthropic/adapter_test.go
git commit -m "fix(kimi): disable anthropic auto-cache by default for non-anthropic providers"
```

### Task 4: Update User-Facing Docs and Validate End-to-End

**Files:**
- Modify: `README.md`
- Modify: `docs/strongdm/attractor/provider-plugin-migration.md`
- Modify: `docs/strongdm/attractor/kilroy-metaspec.md`

**Step 1: Update provider examples and protocol notes**

Document Kimi defaults as:
- `protocol: anthropic_messages`
- `base_url: https://api.kimi.com/coding`
- `api_key_env: KIMI_API_KEY`

Retain ZAI coding endpoint docs.

**Step 2: Run focused integration and full repo tests**

```bash
go test ./internal/providerspec ./internal/llm/providers/anthropic ./internal/attractor/engine -count=1
go test ./... -count=1
```

**Step 3: Manual smoke preflight check with real env (non-destructive)**

Run `kilroy attractor run --detach` using `demo/rogue/rogue_fast.dot` and verify `preflight_report.json` shows prompt-probe pass for `kimi` and `zai` with the Kimi coding contract.

**Step 4: Commit docs + verification updates**

```bash
git add README.md docs/strongdm/attractor/provider-plugin-migration.md docs/strongdm/attractor/kilroy-metaspec.md
git commit -m "docs(kimi): document native coding endpoint and protocol"
```

### Task 5: Independent Fresh Eyes + Follow-Up Fixes

**Files:**
- Review scope: diff against `main`

**Step 1: Invoke independent reviewer**

```bash
"${CLAUDE_PLUGIN_ROOT}/skills/fresheyes/fresheyes.sh" --claude "Review the changes between main and this branch using git diff main...HEAD."
```

**Step 2: Apply any actionable findings**

- Fix code/docs/tests for legitimate findings.
- Re-run affected tests.

**Step 3: Commit follow-up fixes**

```bash
git add -A
git commit -m "fix: address fresheyes review findings for kimi native support"
```

**Step 4: Final verification and handoff summary**

```bash
go test ./... -count=1
```

Report:
- commits created
- tests run + result
- preflight smoke result
- residual risks (if any)
