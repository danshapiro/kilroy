# Fix Pre-existing Test Failures

**Date:** 2026-02-22
**Status:** Proposed
**Problem:** Multiple tests fail on macOS across several packages. All failures reproduce
on `upstream/main` — they are not regressions from local changes.

## Failures

### 1. macOS symlink path mismatch

**Tests:**
- `TestResolveDetachedPaths_ConvertsRelativeToAbsolute` (`cmd/kilroy/detach_paths_test.go`)
- `TestLoadProjectDocs_WalksFromGitRootToWorkingDir_InDepthOrder` (`internal/agent/project_docs_test.go`)

**Root cause:** On macOS, `/var/folders` is a symlink to `/private/var/folders`.
`t.TempDir()` returns the symlink path (`/var/...`) but `filepath.Abs()` and
`git rev-parse --show-toplevel` resolve through symlinks, returning `/private/var/...`.
Assertions compare these unequal strings and fail.

**Fix:** Normalize both sides of path comparisons with `filepath.EvalSymlinks()`:

```go
// detach_paths_test.go
tempDir, _ := filepath.EvalSymlinks(t.TempDir())
```

For `project_docs_test.go`, the same symlink issue likely causes
`dirsFromRootToCwd()` to produce a mismatched path list. Apply
`filepath.EvalSymlinks` to the git root and working directory before computing
the relative path.

**Files to modify:**
- `cmd/kilroy/detach_paths_test.go`
- `internal/agent/project_docs.go` or `internal/agent/project_docs_test.go`

---

### 2. Preflight tests missing API key env vars

**Tests:**
- `TestRunWithConfig_WarnsWhenCLIModelNotInCatalogForProvider` — google/BackendAPI, no `GEMINI_API_KEY`
- `TestRunWithConfig_WarnsWhenAPIModelNotInCatalogForProvider` — openai/BackendAPI, no `OPENAI_API_KEY`
- `TestRunWithConfig_WarnsAndContinues_WhenProviderNotInCatalog` — cerebras/BackendAPI, sets `CEREBRAS_API_KEY` but `report.Summary.Fail != 0`
- `TestRunWithConfig_ForceModel_BypassesCatalogGate` — openai/BackendAPI, no `OPENAI_API_KEY`
- `TestRunWithConfig_AllowsKimiAndZai_WhenCatalogUsesOpenRouterPrefixes` — sets kimi/zai keys but cerebras is pulled in via failover chain synthesis

**Root cause:** These are NOT integration tests — other similar tests in the same file
use `t.Setenv("OPENAI_API_KEY", "k-test")` to satisfy the `provider_api_credentials`
preflight check. The failing tests were likely updated to use `BackendAPI` (to isolate
catalog checks from CLI binary presence) but the corresponding `t.Setenv` calls for the
required API key env vars were not added.

For the kimi/zai test, `resolveProviderRuntimes()` synthesizes builtin failover targets
recursively (provider_runtime.go:91-123). This pulls cerebras into the runtime map via
kimi or zai's builtin failover chain. The `usedAPIProviders()` function then traverses
failover chains and includes cerebras in the preflight check list, but no
`CEREBRAS_API_KEY` is set.

**Fix:** Add the missing `t.Setenv` calls:

```go
// TestRunWithConfig_WarnsWhenCLIModelNotInCatalogForProvider
t.Setenv("GEMINI_API_KEY", "k-test")

// TestRunWithConfig_WarnsWhenAPIModelNotInCatalogForProvider
t.Setenv("OPENAI_API_KEY", "k-test")

// TestRunWithConfig_ForceModel_BypassesCatalogGate
t.Setenv("OPENAI_API_KEY", "k-test")

// TestRunWithConfig_AllowsKimiAndZai_WhenCatalogUsesOpenRouterPrefixes
t.Setenv("CEREBRAS_API_KEY", "k-test")  // pulled in via failover chain
```

For the cerebras/WarnsAndContinues test, verify whether the existing
`t.Setenv("CEREBRAS_API_KEY", "k-cerebras")` is sufficient or if additional
failover-chain providers also need keys.

**Files to modify:**
- `internal/attractor/engine/provider_preflight_test.go`

---

### 3. Reference template missing postmortem prompt attribute

**Test:**
- `TestReferenceTemplate_PostmortemPromptClarifiesStatusContract` (`internal/attractor/validate/reference_template_guardrail_test.go`)

**Root cause:** The reference template (`skills/english-to-dotfile/reference_template.dot`)
defines `postmortem []` with no attributes. A comment on line 281 says
"Note: status reflects analysis completion, not implementation state" but that is a DOT
comment, not a node attribute. The test expects `pm.Attr("prompt", "")` to contain
"whether you completed the analysis".

**Fix:** Add a `prompt` attribute to the postmortem node in `reference_template.dot`
that includes the required phrase. The prompt should instruct the LLM that the status
field reflects whether the analysis was completed, not whether the implementation succeeded.

**Files to modify:**
- `skills/english-to-dotfile/reference_template.dot`

---

### 4. Process group termination (flaky / environment-specific)

**Tests:**
- `TestWaitWithIdleWatchdog_ContextCancelKillsProcessGroup` (`internal/attractor/engine/codergen_process_test.go`)
- `TestRunProviderCapabilityProbe_RespectsParentContextCancel` (`internal/attractor/engine/provider_preflight_test.go`)
- `TestEnsureCXDBReady_AutostartProcessTerminatedOnContextCancel` (engine package)

**Root cause:** These tests verify that child processes are killed when context is
canceled. They fail intermittently, likely due to timing sensitivity — the process
group signal may not propagate to grandchild processes quickly enough, or background
processes spawned by shell scripts escape the process group.

**Recommendation:** Investigate whether these are genuinely flaky or indicate a real
process-management gap. If flaky, increase timeouts or add retry logic in the assertions.
If real, improve `terminateProcessGroup()` / `forceKillProcessGroup()` to handle
grandchild processes.

**Files to investigate:**
- `internal/attractor/engine/codergen_process.go`
- `internal/attractor/engine/provider_preflight.go`

## Priority

1. Preflight missing env vars (bug, easy fix)
2. macOS symlink normalization (bug, easy fix)
3. Reference template postmortem prompt (incomplete template)
4. Process group termination (needs investigation)
