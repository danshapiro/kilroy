# Browser Testing In Kilroy Implementation Plan

> **For Claude:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Kilroy robust for browser testing across existing project-specific `dot` + `run.yaml` pipelines by improving engine behavior, validation guardrails, and skill guidance.

**Architecture:** Extend the existing `tool` node path (shape `parallelogram`) rather than introducing new pipeline formats. Add browser-test-aware artifact harvesting in the engine with browser-node gating, attempt scoping, and size caps; plumb stderr-derived failure reasons so restart classification can act on real browser failure text; extend existing validator contracts without duplicate diagnostics; and update authoring skills/templates so generated project pipelines default to resilient browser test contracts.

**Tech Stack:** Go (`internal/attractor/engine`, `internal/attractor/validate`), Markdown skill docs/templates (`skills/create-dotfile`, `skills/create-runfile`), existing Kilroy CLI/test harness (`go test`, `kilroy attractor validate`).

---

## Scope Check

This is one subsystem: **browser-testing reliability and ergonomics for existing Kilroy pipelines**. It spans engine + validation + authoring guidance, but all changes are in service of the same runtime contract. No additional sub-project split is needed.

## File Structure Map

### Engine Runtime
- Modify: `internal/attractor/engine/handlers.go`
  - Responsibility: integrate browser artifact harvesting into `ToolHandler` execution lifecycle with browser-node gating and stderr-derived failure reason plumbing.
- Modify: `internal/attractor/engine/engine.go`
  - Responsibility: preserve browser artifact directories across retry attempt archiving so per-attempt evidence is retained.
- Modify: `internal/attractor/engine/loop_restart_policy.go`
  - Responsibility: improve deterministic vs transient classification hints for browser-test failures, avoiding ambiguous false-positive patterns.
- Modify: `internal/attractor/engine/archive_attempt_test.go`
  - Responsibility: regression tests for attempt archiving with browser artifact subdirectories.
- Create: `internal/attractor/engine/browser_test_artifacts.go`
  - Responsibility: detect and collect common browser test artifacts (Playwright/Cypress/Selenium outputs) from worktree into stage logs with attempt-scoped filtering and artifact-size guardrails.
- Create: `internal/attractor/engine/browser_test_artifacts_test.go`
  - Responsibility: unit tests for artifact discovery/copy behavior, cache exclusion, and size/scope guardrails.
- Create: `internal/attractor/engine/browser_tool_handler_test.go`
  - Responsibility: integration-level tests for `ToolHandler` browser artifact capture and failure reason extraction.
- Modify: `internal/attractor/engine/retry_classification_integration_test.go`
  - Responsibility: end-to-end tests that prove tool-node stderr-derived browser failures influence loop-restart routing.
- Modify: `internal/attractor/engine/failure_classification_regression_test.go`
  - Responsibility: add browser-infra transient signature coverage that actually fails pre-change.
- Create: `internal/attractor/browsergate/match.go`
  - Responsibility: canonical browser verification/setup matching helpers shared by engine and validator.
- Create: `internal/attractor/browsergate/match_test.go`
  - Responsibility: table-driven regression tests for shared browser-node matching criteria.

### Graph Validation
- Modify: `internal/attractor/validate/validate.go`
  - Responsibility: extend existing validate-script failure-contract lint and add non-duplicative browser inline-command guidance.
- Modify: `internal/attractor/validate/validate_test.go`
  - Responsibility: regression coverage for lint deduplication, browser guidance, and non-browser false-positive prevention.

### Skill/Template Ergonomics
- Modify: `skills/create-dotfile/SKILL.md`
  - Responsibility: require browser verification gate pattern when DoD includes browser behavior.
- Modify: `skills/create-dotfile/reference_template.dot`
  - Responsibility: provide optional browser verification gate scaffold with failure routing conventions.
- Modify: `skills/create-runfile/SKILL.md`
  - Responsibility: run-config guidance for browser setup commands and environment constraints.
- Modify: `skills/create-runfile/reference_run_template.yaml`
  - Responsibility: template comments/examples for browser dependency setup.

### Documentation
- Modify: `README.md`
  - Responsibility: document recommended browser-test `tool` node pattern and artifact outputs.
- Modify: `docs/strongdm/attractor/README.md`
  - Responsibility: runbook notes for browser-test gates and artifact expectations.

## Chunk 1: Engine Browser Test Runtime Hardening

### Task 1: Add Browser Artifact Discovery/Copy Utility

**Files:**
- Create: `internal/attractor/engine/browser_test_artifacts.go`
- Create: `internal/attractor/engine/browser_test_artifacts_test.go`

- [ ] **Step 1: Write failing utility tests for artifact discovery and copy behavior**

```go
func TestDiscoverBrowserArtifacts_PlaywrightAndCypress(t *testing.T) {
    // Arrange worktree fixtures: playwright-report/, test-results/, cypress/videos/
    // Assert discovered logical artifact set is deterministic and de-duplicated.
}

func TestCollectBrowserArtifacts_CopiesIntoStageBrowserArtifactsDir(t *testing.T) {
    // Arrange source artifacts, run collection, assert copied file layout:
    // stageDir/browser_artifacts/<relative_source_path>
}

func TestCollectBrowserArtifacts_NoMatches_NoError(t *testing.T) {
    // Empty worktree should return empty set and nil error.
}

func TestDiscoverBrowserArtifacts_ExcludesPlaywrightCache(t *testing.T) {
    // Ensure playwright/.cache is ignored to prevent archive bloat.
}

func TestCollectBrowserArtifacts_OnlyCollectsNewOrModifiedVsPreRunSnapshot(t *testing.T) {
    // Old artifacts remain excluded unless modified during the current attempt.
    // Use pre-run snapshot + stage start time to enforce attempt boundaries.
}

func TestCollectBrowserArtifacts_RespectsPerFileAndTotalSizeCaps(t *testing.T) {
    // Oversized files are skipped with summary notes; collector remains non-fatal.
}

func TestCollectBrowserArtifacts_SkipsSymlinksAndOutOfTreeTargets(t *testing.T) {
    // Symlinks and paths resolving outside worktree are skipped for safety.
}

func TestCollectBrowserArtifacts_ClearsPreviousAttemptDestination(t *testing.T) {
    // Destination is reset before copy so stale attempt files do not leak.
}
```

- [ ] **Step 2: Run the targeted test file to confirm failures**

Run: `go test ./internal/attractor/engine -run 'TestDiscoverBrowserArtifacts|TestCollectBrowserArtifacts' -v`
Expected: FAIL (missing discovery/collector implementation)

- [ ] **Step 3: Implement minimal utility with deterministic path ordering**

```go
// discoverBrowserArtifacts(worktreeDir string) []browserArtifact
// snapshotBrowserArtifacts(worktreeDir string) (map[string]artifactFingerprint, error)
// collectBrowserArtifacts(stageDir, worktreeDir string, baseline map[string]artifactFingerprint, startedAt time.Time) (browserArtifactSummary, error)
// - Match known directories/files:
//   playwright-report, test-results, cypress/videos,
//   cypress/screenshots, junit*.xml, *.trace.zip
// - Copy into stageDir/browser_artifacts/<relative_path>
// - Clear stageDir/browser_artifacts before each collection pass so each
//   attempt writes a fresh artifact set.
// - Exclude heavyweight caches (playwright/.cache and other install/cache paths).
// - Collect only files created/modified during the current stage attempt
//   (based on pre-run snapshot + startedAt boundary).
// - Enforce deterministic size guardrails and report skips:
//   per-file cap 10 MiB, total copied cap 50 MiB.
// - Safety: use Lstat and skip symlinks; enforce source path containment under
//   worktree root before copy (never follow paths outside repo).
// - Preserve deterministic sorted results.
```

- [ ] **Step 4: Re-run targeted tests and ensure pass**

Run: `go test ./internal/attractor/engine -run 'TestDiscoverBrowserArtifacts|TestCollectBrowserArtifacts' -v`
Expected: PASS

- [ ] **Step 5: Commit chunk progress**

```bash
git add internal/attractor/engine/browser_test_artifacts.go internal/attractor/engine/browser_test_artifacts_test.go
git commit -m "engine/browser: add browser artifact discovery and collection utility"
```

### Task 2: Integrate Artifact Collector Into ToolHandler

**Files:**
- Modify: `internal/attractor/engine/handlers.go`
- Modify: `internal/attractor/engine/engine.go`
- Create: `internal/attractor/engine/browser_tool_handler_test.go`
- Modify: `internal/attractor/engine/archive_attempt_test.go`
- Modify: `internal/attractor/engine/retry_classification_integration_test.go`
- Create: `internal/attractor/browsergate/match.go`
- Create: `internal/attractor/browsergate/match_test.go`

- [ ] **Step 1: Write failing ToolHandler integration tests for artifact capture and failure reason plumbing**

```go
func TestToolHandler_CollectsBrowserArtifacts_OnSuccess(t *testing.T) {}
func TestToolHandler_CollectsBrowserArtifacts_OnFailure(t *testing.T) {}
func TestToolHandler_BrowserArtifactCollectionFailure_IsNonFatal(t *testing.T) {}
func TestToolHandler_BrowserArtifactSummary_EmitsProgressEvent(t *testing.T) {}
func TestToolHandler_BrowserFailureReasonUsesStderrExcerpt_WhenCommandFails(t *testing.T) {}
func TestToolHandler_NonBrowserFailureReason_RemainsExitStatus(t *testing.T) {}
func TestLoopRestart_UsesToolFailureReason_ForBrowserTransientRouting(t *testing.T) {}
func TestArchiveAttemptDir_PreservesBrowserArtifactDirectories(t *testing.T) {}
func TestIsBrowserVerificationNode(t *testing.T) {}
func TestIsBrowserSetupCommand(t *testing.T) {}
```

- [ ] **Step 2: Run failing ToolHandler tests**

Run: `go test ./internal/attractor/browsergate -run 'TestIsBrowserVerificationNode|TestIsBrowserSetupCommand' -v`
Run: `go test ./internal/attractor/engine -run 'TestToolHandler_CollectsBrowserArtifacts|TestToolHandler_BrowserArtifactCollectionFailure_IsNonFatal|TestToolHandler_BrowserArtifactSummary_EmitsProgressEvent|TestToolHandler_BrowserFailureReasonUsesStderrExcerpt|TestToolHandler_NonBrowserFailureReason_RemainsExitStatus|TestLoopRestart_UsesToolFailureReason_ForBrowserTransientRouting|TestArchiveAttemptDir_PreservesBrowserArtifactDirectories' -v`
Expected: FAIL (collector/attempt-archive integration and failure-reason plumbing missing)

- [ ] **Step 3: Call collector from ToolHandler after command completion paths**

```go
// In ToolHandler.Execute:
// - gate collection to browser-relevant tool nodes only:
//   use shared helper package internal/attractor/browsergate:
//   - IsBrowserVerificationNode(cmd, node id/label, attrs)
//   - IsSetupInstallCommand(cmd)
//   criteria: command contains browser verification runner tokens
//   (`playwright test`, `cypress run`, `selenium`, `webdriver`) OR
//   node id/label matches `(browser|e2e|ui)` + `(verify|validate|check|test)`,
//   with explicit override attr collect_browser_artifacts=true
//   and setup/install commands are excluded.
// - capture pre-run artifact snapshot + stage start time before command run
// - after stdout/stderr/timing capture, invoke collectBrowserArtifacts(stageDir, worktreeDir, baseline, startedAt)
// - ensure collector clears stageDir/browser_artifacts before writing new files
//   to prevent stale artifact bleed across retries.
// - append collector summary to progress events
// - never fail stage solely because artifact copy partially fails (warn + continue)
// - on command failure:
//   - browser verification nodes: set FailureReason from stderr excerpt (first actionable line) when present
//   - non-browser nodes: preserve existing FailureReason behavior (runErr.Error())
//   keep raw exit-status text in metadata/context for postmortem.
//
// In engine retry archiving:
// - update archiveAttemptDir to preserve browser_artifacts/ recursively into attempt_N/
// - continue skipping attempt_N and visit_N recursion to avoid nested archive loops.
```

- [ ] **Step 4: Re-run ToolHandler tests**

Run: `go test ./internal/attractor/browsergate -run 'TestIsBrowserVerificationNode|TestIsBrowserSetupCommand' -v`
Run: `go test ./internal/attractor/engine -run 'TestToolHandler_CollectsBrowserArtifacts|TestToolHandler_BrowserArtifactCollectionFailure_IsNonFatal|TestToolHandler_BrowserArtifactSummary_EmitsProgressEvent|TestToolHandler_BrowserFailureReasonUsesStderrExcerpt|TestToolHandler_NonBrowserFailureReason_RemainsExitStatus|TestLoopRestart_UsesToolFailureReason_ForBrowserTransientRouting|TestArchiveAttemptDir_PreservesBrowserArtifactDirectories' -v`
Expected: PASS

- [ ] **Step 5: Commit chunk progress**

```bash
git add internal/attractor/browsergate/match.go internal/attractor/browsergate/match_test.go internal/attractor/engine/handlers.go internal/attractor/engine/engine.go internal/attractor/engine/browser_tool_handler_test.go internal/attractor/engine/archive_attempt_test.go internal/attractor/engine/retry_classification_integration_test.go
git commit -m "engine/tool: preserve browser artifacts across retries and plumb failure reasons"
```

### Task 3: Improve Browser Failure Classification Hints

**Files:**
- Modify: `internal/attractor/engine/loop_restart_policy.go`
- Modify: `internal/attractor/engine/failure_classification_regression_test.go`

- [ ] **Step 1: Add failing table tests for browser infra transient signatures**

```go
{name: "transient: playwright browser launch failed (missing deps)", failureReason: "browserType.launch: Host system is missing dependencies", want: failureClassTransientInfra}
{name: "transient: playwright executable missing", failureReason: "browserType.launch: Executable doesn't exist at /home/user/.cache/ms-playwright/chromium", want: failureClassTransientInfra}
{name: "transient: playwright install hint", failureReason: "Please run the following command to download new browsers: npx playwright install", want: failureClassTransientInfra}
// Also update TestTransientInfraReasonHints_Count after adding new hints.
```

- [ ] **Step 2: Run targeted failure classification tests**

Run: `go test ./internal/attractor/engine -run 'TestClassifyFailureClass_AllHeuristicPatterns' -v`
Expected: FAIL on new browser cases

- [ ] **Step 3: Extend transient hint set for common browser infra failures**

```go
// Add normalized hints such as:
// "host system is missing dependencies",
// "browsertype.launch: executable doesn't exist",
// "please run the following command to download new browsers",
// "net::err_name_not_resolved"
// Do NOT add ambiguous hints like "websocket closed" or
// "target page, context or browser has been closed" (high false-positive risk).
// Update TestTransientInfraReasonHints_Count expected value to match the new slice length.
```

- [ ] **Step 4: Re-run targeted tests**

Run: `go test ./internal/attractor/engine -run 'TestClassifyFailureClass_AllHeuristicPatterns|TestTransientInfraReasonHints_Count' -v`
Expected: PASS

- [ ] **Step 5: Commit chunk progress**

```bash
git add internal/attractor/engine/loop_restart_policy.go internal/attractor/engine/failure_classification_regression_test.go
git commit -m "engine/failure-class: classify browser infra failures as transient"
```

## Chunk 2: Validator Guardrails For Browser Tool Gates

### Task 4: Extend Validate-Script Contract Lint And Add Non-Duplicative Browser Inline Guidance

**Files:**
- Modify: `internal/attractor/validate/validate.go`
- Test: `internal/attractor/validate/validate_test.go`

- [ ] **Step 1: Add failing validation tests for browser command guidance without duplicate diagnostics**

```go
func TestLintValidateScriptFailureContract_BrowserScriptMissingFallback(t *testing.T) {}
func TestLintBrowserInlineToolCommandContract_WarnsOnInlineBrowserCommand(t *testing.T) {}
func TestLintBrowserInlineToolCommandContract_SetupCommand_NoWarning(t *testing.T) {}
func TestLintBrowserInlineToolCommandContract_DoesNotDuplicateValidateScriptWarning(t *testing.T) {}
func TestLintBrowserInlineToolCommandContract_NoWarningForScriptContract(t *testing.T) {}
```

- [ ] **Step 2: Run targeted validator tests to confirm failure**

Run: `go test ./internal/attractor/validate -run 'TestLintValidateScriptFailureContract|TestLintBrowserInlineToolCommandContract' -v`
Expected: FAIL (new coverage/rules missing)

- [ ] **Step 3: Implement lint rule and wire into validator pipeline**

```go
// Extend lintValidateScriptFailureContract(g *model.Graph) []Diagnostic:
// - keep single source of truth for validate-*.sh + KILROY_VALIDATE_FAILURE contract.
// Add lintBrowserInlineToolCommandContract(g *model.Graph) []Diagnostic:
// - Trigger only for browser verification intent, not setup/install:
//   command contains browser runner tokens (`playwright test`, `cypress run`, `selenium`, `webdriver`)
//   OR node id/label indicates browser verification (`browser|e2e|ui` + `verify|validate|check|test`)
// - Explicitly skip install/bootstrap commands (`npm ci`, `pnpm install`, `yarn install`,
//   `npx playwright install`, `apt-get install`, `brew install`, `pip install`).
// - For matching verify nodes, prefer script wrapper: sh scripts/validate-<stage>.sh
// - Skip this lint when command already matches sh scripts/validate-*.sh
//   so one node never emits duplicate warnings for the same issue.
// - Emit warning-level diagnostics with explicit fix text.
```

- [ ] **Step 4: Re-run targeted validator tests**

Run: `go test ./internal/attractor/validate -run 'TestLintValidateScriptFailureContract|TestLintBrowserInlineToolCommandContract' -v`
Expected: PASS

- [ ] **Step 5: Commit chunk progress**

```bash
git add internal/attractor/validate/validate.go internal/attractor/validate/validate_test.go
git commit -m "validate: extend script contract lint and add browser inline guidance"
```

### Task 5: Add Regression Coverage For Loop-Restart Guard On Browser Verify Nodes

**Files:**
- Modify: `internal/attractor/validate/validate_test.go`

- [ ] **Step 1: Add failing routing-safety test fixtures for browser verify nodes**

```go
func TestLintLoopRestartFailureClassGuard_BrowserVerifyRequiresDeterministicFallback(t *testing.T) {}
```

- [ ] **Step 2: Run targeted test and verify it fails before fixture/assertion refinement**

Run: `go test ./internal/attractor/validate -run 'TestLintLoopRestartFailureClassGuard_BrowserVerifyRequiresDeterministicFallback' -v`
Expected: FAIL

- [ ] **Step 3: Refine fixture and assertions in `validate_test.go` to exercise existing guard semantics deterministically**

```go
// Reuse existing loop_restart_failure_class_guard rule expectations:
// transient retry edge + deterministic non-restart fallback.
```

- [ ] **Step 4: Re-run targeted validator tests**

Run: `go test ./internal/attractor/validate -run 'TestLintLoopRestartFailureClassGuard_BrowserVerifyRequiresDeterministicFallback' -v`
Expected: PASS

- [ ] **Step 5: Commit chunk progress**

```bash
git add internal/attractor/validate/validate_test.go
git commit -m "validate/tests: enforce routing safety expectations for browser verification gates"
```

## Chunk 3: Authoring Ergonomics In Skills/Templates/Docs

### Task 6: Update create-dotfile Skill Guidance

**Files:**
- Modify: `skills/create-dotfile/SKILL.md`
- Modify: `skills/create-dotfile/reference_template.dot`

- [ ] **Step 1: Run baseline template guardrail tests before editing skill/template files**

Run: `go test ./internal/attractor/validate -run 'TestReferenceTemplate' -v`
Expected: PASS currently (baseline capture before edits)

- [ ] **Step 2: Add browser verification guidance in skill contract (`@skills/create-dotfile/SKILL.md`)**

```md
- When DoD requires browser behavior, include a dedicated browser verify tool gate.
- Use script contract: `sh scripts/validate-<stage>.sh || { echo "KILROY_VALIDATE_FAILURE: ..."; exit 1; }` (for browser stages, `validate-browser.sh` is acceptable).
- Route transient infra separately from deterministic UI/product failures.
- If wrapper script does not mention browser tooling directly, set `collect_browser_artifacts=true` on the tool node.
```

- [ ] **Step 3: Add optional browser verify scaffold to `reference_template.dot` comments**

```dot
// verify_browser [shape=parallelogram, collect_browser_artifacts="true", tool_command="sh scripts/validate-browser.sh || { echo 'KILROY_VALIDATE_FAILURE: browser validation script missing or failed'; exit 1; }"]
// check_browser [shape=diamond, label="Browser OK?"]
```

- [ ] **Step 4: Re-run template/validator tests**

Run: `go test ./internal/attractor/validate -run 'TestReferenceTemplate|TestValidate' -v`
Expected: PASS

- [ ] **Step 5: Commit chunk progress**

```bash
git add skills/create-dotfile/SKILL.md skills/create-dotfile/reference_template.dot
git commit -m "skills/create-dotfile: add browser verification gate guidance"
```

### Task 7: Update create-runfile Skill + Template For Browser Setup

**Files:**
- Modify: `skills/create-runfile/SKILL.md`
- Modify: `skills/create-runfile/reference_run_template.yaml`

- [ ] **Step 1: Update runfile guidance for browser prerequisites (`@skills/create-runfile/SKILL.md`)**

```md
- If graph contains browser verify gate, include setup commands for browser deps.
- Prefer deterministic install commands and explicit timeout expectations.
```

- [ ] **Step 2: Add browser setup examples to run template comments**

```yaml
setup:
  commands:
    - npm ci
    - npx playwright install --with-deps
```

- [ ] **Step 3: Validate runfile template remains schema-compatible**

Run: `go test ./internal/attractor/validate -run 'TestCreateRunfileTemplate' -v`
Expected: PASS

- [ ] **Step 4: Commit chunk progress**

```bash
git add skills/create-runfile/SKILL.md skills/create-runfile/reference_run_template.yaml
git commit -m "skills/create-runfile: document browser dependency setup defaults"
```

### Task 8: Document Runtime Behavior + Artifacts

**Files:**
- Modify: `README.md`
- Modify: `docs/strongdm/attractor/README.md`

- [ ] **Step 1: Document browser gate pattern in main README**

```md
- Use tool nodes for browser tests.
- Preferred script contract and failure routing pattern.
- Browser artifacts captured under stage logs (attempt-scoped, cache-excluded, size-capped).
```

- [ ] **Step 2: Add runbook note in attractor README for browser artifact harvesting + lint expectations**

```md
- Enumerate auto-captured browser artifacts and explicit cache exclusions.
- Clarify transient vs deterministic classification intent.
```

- [ ] **Step 3: Verify documentation includes required browser contract language**

Run: `grep -En 'validate-<stage>|validate-browser|browser artifacts|attempt-scoped|size cap|transient' README.md`
Run: `grep -En 'validate-<stage>|validate-browser|browser artifacts|attempt-scoped|size cap|transient' docs/strongdm/attractor/README.md`
Expected: both commands return matches, confirming contract + artifact constraints + failure classification guidance in each file

- [ ] **Step 4: Commit docs updates**

```bash
git add README.md docs/strongdm/attractor/README.md
git commit -m "docs: add browser testing runtime and artifact guidance"
```

## Chunk 4: End-To-End Verification And CI Parity

### Task 9: Add/Run Focused Regression Matrix For Browser Hardening

**Files:**
- Modify (if needed): `scripts/e2e-guardrail-matrix.sh`
- Test: `internal/attractor/engine/*_test.go`
- Test: `internal/attractor/validate/*_test.go`

- [ ] **Step 1: Add focused browser-hardening entries to guardrail matrix script**

```bash
# relabel existing matrix entries from [1/7]..[7/7] to [1/10]..[7/10], then append:
run_test_checked "[8/10] browser artifact collector + safety" \
  go test ./internal/attractor/engine -run '^TestDiscoverBrowserArtifacts|^TestCollectBrowserArtifacts' -count=1
run_test_checked "[9/10] browser tool handler + retry routing" \
  go test ./internal/attractor/engine -run '^TestToolHandler_CollectsBrowserArtifacts|^TestToolHandler_BrowserFailureReasonUsesStderrExcerpt|^TestToolHandler_NonBrowserFailureReason_RemainsExitStatus|^TestLoopRestart_UsesToolFailureReason_ForBrowserTransientRouting|^TestArchiveAttemptDir_PreservesBrowserArtifactDirectories' -count=1
run_test_checked "[10/10] browser validator lint coverage" \
  go test ./internal/attractor/validate -run '^TestLintValidateScriptFailureContract|^TestLintBrowserInlineToolCommandContract' -count=1
```

Run: `rg -n \"DiscoverBrowserArtifacts|CollectBrowserArtifacts|ArchiveAttemptDir_PreservesBrowserArtifactDirectories|BrowserFailureReasonUsesStderrExcerpt|NonBrowserFailureReason_RemainsExitStatus|LoopRestart_UsesToolFailureReason|LintBrowserInlineToolCommandContract|LintValidateScriptFailureContract\" scripts/e2e-guardrail-matrix.sh`
Expected: engine + validator browser-hardening command families are present

- [ ] **Step 2: Run engine-focused tests**

Run: `go test ./internal/attractor/browsergate -run 'TestIsBrowserVerificationNode|TestIsBrowserSetupCommand' -v`
Run: `go test ./internal/attractor/engine -run 'TestDiscoverBrowserArtifacts|TestToolHandler_CollectsBrowserArtifacts|TestArchiveAttemptDir_PreservesBrowserArtifactDirectories|TestToolHandler_BrowserFailureReasonUsesStderrExcerpt|TestToolHandler_NonBrowserFailureReason_RemainsExitStatus|TestLoopRestart_UsesToolFailureReason_ForBrowserTransientRouting|TestClassifyFailureClass_AllHeuristicPatterns|TestTransientInfraReasonHints_Count' -v`
Expected: PASS

- [ ] **Step 3: Run validate-focused tests**

Run: `go test ./internal/attractor/validate -run 'TestLintValidateScriptFailureContract|TestLintBrowserInlineToolCommandContract|TestLintLoopRestartFailureClassGuard_BrowserVerifyRequiresDeterministicFallback' -v`
Expected: PASS

- [ ] **Step 4: Run CI-parity checklist commands**

Run: `gofmt -l . | grep -v '^\./\.claude/' | grep -v '^\.claude/'`
Expected: no output

Run: `go vet ./...`
Expected: exit 0

Run: `go build ./cmd/kilroy/`
Expected: exit 0

Run: `go test ./...`
Expected: PASS

- [ ] **Step 5: Validate demo graphs**

Run: `for f in demo/**/*.dot; do echo "Validating $f"; ./kilroy attractor validate --graph "$f"; done`
Expected: all validations succeed

- [ ] **Step 6: Commit verification/matrix updates**

```bash
git add scripts/e2e-guardrail-matrix.sh
# add any new/updated *_test.go files produced in this chunk
git commit -m "test(e2e): add browser testing hardening regression coverage"
```

## Chunk 5: Final Branch Wrap-Up

### Task 10: Prepare Merge-Ready Branch Metadata

**Files:**
- Modify: `docs/superpowers/plans/2026-03-02-browser-testing-in-kilroy.md` (checklist completion notes only)

- [ ] **Step 1: Add a `## Implementation Summary` subsection to this plan with 3-5 bullets**

```md
## Implementation Summary
- Added browser artifact harvesting for tool-based browser test stages.
- Added validator guardrails for brittle browser test commands.
- Updated skill/template/docs guidance for resilient browser test contracts.
```

- [ ] **Step 2: Add a `## Command Outcomes` subsection to this plan listing every command run and pass/fail result**

```md
## Command Outcomes
- `go test ./internal/attractor/engine -run '...' -v` -> PASS
- `go test ./internal/attractor/validate -run '...' -v` -> PASS
- `go test ./...` -> PASS
```

- [ ] **Step 3: Run status check to confirm only intended files are modified**

Run: `git status --short`
Expected: only files from this implementation plan (or explicitly intended browser-hardening files) are listed; no unrelated changes

- [ ] **Step 4: Commit checklist-completion notes in this plan file only**

```bash
git add docs/superpowers/plans/2026-03-02-browser-testing-in-kilroy.md
git commit -m "docs(plan): finalize browser testing hardening implementation checklist"
```
