# Unbounded Input Materialization and Reference-Closure Implementation Plan

> **For Claude:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ensure every user-provided input document and every transitively referenced file is available in the active run worktree and all parallel branch worktrees, without engine-imposed scope limits.

**Architecture:** Add an engine-level input materialization subsystem that computes recursive input closure from declared seeds plus extracted references, then copies all resolved files into run/branch worktrees and records an auditable manifest. Use a dual extractor model: deterministic path/glob scanner for explicit references plus LLM-assisted reference inference for natural-language requirements (for example, “all markdown tests on C drive”). Integrate materialization before stage execution and during branch spawn so new references introduced mid-run are captured.

**Tech Stack:** Go (`internal/attractor/engine`, `internal/attractor/validate`), YAML run-config schema, existing codergen/provider runtime wiring, `go test`, `gofmt`.

---

## Chunk 1: Spec Anchors, Scope, and File Structure

### Why this change is spec-aligned

- Parallel isolation uses git checkpoint + per-branch worktrees, so non-committed inputs are not automatically present in branch execution contexts: [attractor-spec.md:849-853](../../strongdm/attractor/attractor-spec.md), [attractor-spec.md:861](../../strongdm/attractor/attractor-spec.md).
- Existing reliability contract explicitly allows fallback-copy behavior for `status.json`; this is precedent for adding an explicit input-materialization reliability contract: [attractor-spec.md:2231](../../strongdm/attractor/attractor-spec.md).
- Run-level and branch-level execution already create fresh worktrees from git refs in engine code, confirming this is a systemic runtime concern, not a graph-specific issue: [engine.go:376](../../../internal/attractor/engine/engine.go), [parallel_handlers.go:342](../../../internal/attractor/engine/parallel_handlers.go).
- Worktree-relative access assumptions for stack contexts appear in spec examples and are treated as implementation inference for this feature; this plan formalizes that contract explicitly in spec text.

### Scope check

This plan touches one subsystem (engine/runtime input preparation) plus schema/docs/tests. It is cohesive and should stay in a single implementation plan.

### File structure map

**Create**
- `internal/attractor/engine/input_materialization.go`
  - Responsibility: Input closure planner and copy executor for run/branch/stage hydration.
- `internal/attractor/engine/input_reference_scan.go`
  - Responsibility: Deterministic extraction of path/glob references from text files.
- `internal/attractor/engine/input_reference_infer.go`
  - Responsibility: LLM-assisted extraction of implicit references from natural-language requirements.
- `internal/attractor/engine/input_materialization_test.go`
  - Responsibility: Unit tests for closure, recursion, copy semantics, and explicit-missing failures.
- `internal/attractor/engine/input_reference_scan_test.go`
  - Responsibility: Unit tests for markdown/quoted/bare-path/glob extraction.
- `internal/attractor/engine/input_reference_infer_test.go`
  - Responsibility: Unit tests with fake inferer responses and parser robustness.
- `internal/attractor/engine/input_materialization_integration_test.go`
  - Responsibility: End-to-end run + branch hydration tests with real worktree lifecycle.
- `internal/attractor/engine/input_materialization_resume_test.go`
  - Responsibility: Resume-path hydration parity tests after worktree recreation.
- `internal/attractor/engine/prompts/input_materialization_preamble.tmpl`
  - Responsibility: Prompt contract that tells stages where the input manifest is.
- `internal/attractor/validate/input_materialization_contract_guardrail_test.go`
  - Responsibility: Guardrail for new spec language covering input materialization contracts.

**Modify**
- `internal/attractor/engine/config.go`
  - Add `inputs` config schema, defaults, and validation (no engine-imposed caps).
- `internal/attractor/engine/config_test.go`
  - Add schema/validation tests for `inputs` behavior.
- `internal/attractor/engine/run_with_config.go`
  - Wire resolved input policy into engine options and startup.
- `internal/attractor/engine/engine.go`
  - Invoke input materialization at run startup and before node execution.
- `internal/attractor/engine/resume.go`
  - Invoke input materialization when resume recreates the run worktree.
- `internal/attractor/engine/parallel_handlers.go`
  - Invoke input materialization when spawning branch worktrees.
- `internal/attractor/engine/node_env.go`
  - Expose input manifest path to stage processes.
- `internal/attractor/engine/handlers.go`
  - Include input manifest contract preamble in prompt assembly.
- `internal/attractor/engine/cxdb_events.go`
  - Publish input manifest artifacts for observability.
- `internal/attractor/engine/run_with_config_integration_test.go`
  - Verify env contract includes manifest metadata.
- `internal/attractor/engine/parallel_test.go`
  - Add branch materialization regression assertions.
- `internal/attractor/engine/status_json_worktree_test.go`
  - Guard status fallback contract from regression while adding new input contracts.
- `internal/attractor/engine/prompt_assets.go`
  - Register/render input-materialization preamble template.
- `skills/create-runfile/reference_run_template.yaml`
  - Add `inputs` section with explicit defaults.
- `internal/attractor/validate/create_runfile_template_guardrail_test.go`
  - Guardrail test for new run-template inputs contract.
- `docs/strongdm/attractor/attractor-spec.md`
  - Add normative input materialization and reference-closure reliability contract.

---

## Chunk 2: Implementation Tasks

### Task 1: Add Run Config Surface for Unbounded Input Materialization

**Files:**
- Modify: `internal/attractor/engine/config.go`
- Modify: `internal/attractor/engine/config_test.go`
- Modify: `skills/create-runfile/reference_run_template.yaml`
- Modify: `internal/attractor/validate/create_runfile_template_guardrail_test.go`

- [ ] **Step 1: Write failing config tests for the new `inputs` block**

```go
func TestLoadRunConfigFile_InputMaterializationConfig(t *testing.T) {
    raw := []byte(`version: 1
repo: { path: /tmp/repo }
cxdb: { binary_addr: 127.0.0.1:9009, http_base_url: http://127.0.0.1:9010, autostart: { enabled: false } }
llm: { cli_profile: real, providers: { openai: { backend: cli } } }
modeldb: { openrouter_model_info_path: /tmp/pinned.json }
inputs:
  materialize:
    enabled: true
    include:
      - .ai/**
      - C:/Users/me/**/*.md
    follow_references: true
    infer_with_llm: true
`) 
    // parse and assert fields round-trip exactly
}
```

- [ ] **Step 2: Run targeted config tests and verify failure**

Run: `go test ./internal/attractor/engine -run 'TestLoadRunConfigFile_InputMaterializationConfig' -count=1`
Expected: FAIL (unknown or missing `inputs` schema fields).

- [ ] **Step 3: Implement config structs/defaults/validation (no limits)**

Add an `inputs.materialize` config shape with these fields:
- `enabled` (bool, default true)
- `include` (`[]string`, default `[]`; user-declared, required semantics)
- `default_include` (`[]string`, default `['.ai/**']`; engine-declared, best-effort semantics)
- `follow_references` (bool, default true)
- `infer_with_llm` (bool, default false for backward compatibility)
- `llm_model` (string, optional override)
- `llm_provider` (string, optional override)

Validation rules:
- `include` may be empty when `enabled=true` (the `default_include` set can still drive hydration).
- `default_include` may be empty.
- No max count/size/depth limits.
- `include` entries are fail-on-unmatched.
- `default_include` entries are best-effort when unmatched.
- When `infer_with_llm=true`, resolve inferer model/provider deterministically:
  - use `inputs.materialize.llm_provider` + `inputs.materialize.llm_model` when provided
  - otherwise fail config validation (no implicit inference model selection)
- Preserve strict unknown-field rejection behavior.
- Existing run configs that omit `inputs` must continue to load and run without migration changes.

- [ ] **Step 4: Add failing run-template guardrail test for `inputs` section**

Add assertions to `TestCreateRunfileTemplate_IncludesOperatorMetadata` sibling test:
- template includes `inputs.materialize.enabled`
- template includes `inputs.materialize.include`
- template includes `inputs.materialize.default_include`
- template includes `inputs.materialize.follow_references`
- template includes `inputs.materialize.infer_with_llm`

- [ ] **Step 5: Update run template and make guardrail pass**

Update `skills/create-runfile/reference_run_template.yaml` with explicit `inputs` defaults using `infer_with_llm: false` by default, plus commented guidance showing how to enable inference with explicit `llm_provider` and `llm_model`.

- [ ] **Step 6: Run tests for config + template guardrails**

Run: `go test ./internal/attractor/engine ./internal/attractor/validate -run 'InputMaterialization|CreateRunfileTemplate' -count=1`
Expected: PASS.

- [ ] **Step 7: Commit Task 1**

```bash
git add internal/attractor/engine/config.go \
        internal/attractor/engine/config_test.go \
        skills/create-runfile/reference_run_template.yaml \
        internal/attractor/validate/create_runfile_template_guardrail_test.go
git commit -m "feat(engine): add unbounded input materialization run-config surface"
```

### Task 2: Build Deterministic Reference Scanner and Recursive Closure Planner

**Files:**
- Create: `internal/attractor/engine/input_reference_scan.go`
- Create: `internal/attractor/engine/input_reference_scan_test.go`
- Create: `internal/attractor/engine/input_materialization.go`
- Create: `internal/attractor/engine/input_materialization_test.go`

- [ ] **Step 1: Write failing scanner tests**

Cover extraction from:
- Markdown links: `[tests](docs/tests.md)`
- Quoted paths: `"C:/repo/tests.md"`
- Bare paths/globs: `.ai/definition_of_done.md`, `C:/logs/**/*.md`
- Natural text path hints that are explicit enough to parse without LLM.

- [ ] **Step 2: Run scanner tests and verify failure**

Run: `go test ./internal/attractor/engine -run 'InputReferenceScan' -count=1`
Expected: FAIL (scanner not implemented).

- [ ] **Step 3: Implement deterministic scanner**

Implement extraction that returns normalized candidates with metadata:
- source file
- matched token
- token kind (`path`, `glob`)
- confidence (`explicit`)

- [ ] **Step 4: Write failing closure planner tests**

Create tests for:
- Seed `default_include` `.ai/**` copies user-provided DoD when present.
- If DoD references `tests.md`, closure includes `tests.md`.
- Recursive chain: `a.md -> b.md -> c.md` includes all.
- Deep recursion fixture (`doc_0001.md -> ... -> doc_1500.md`) resolves to completion without fixed-cap truncation.
- User `include` with no matches fails deterministic (`input_include_missing`).
- `default_include` with no matches does not fail.

- [ ] **Step 5: Implement closure planner + copier**

Implement in `input_materialization.go`:
- Expand seed globs from source roots.
- Walk fixed-point recursion over extracted references.
- Preserve unlimited traversal (no hard cap), with cycle protection via visited set.
- Copy resolved files into target worktree mirror paths.
- Map absolute/external sources into deterministic mirror paths under `.kilroy-inputs/external/<sha256-prefix>/...` and record source-to-target mapping.
- Persist canonical snapshot payload under `logs_root/input_snapshot/` so retries/resume/branches can avoid mutable source-workspace dependency.
- Write `inputs_manifest.json` with:
  - `sources`
  - `resolved_files`
  - `source_target_map`
  - `discovered_references`
  - `unresolved_inferred_references`
- `warnings`
- `generated_at`

- [ ] **Step 6: Run deterministic unit suite**

Run: `go test ./internal/attractor/engine -run 'InputReferenceScan|InputMaterialization' -count=1`
Expected: PASS.

- [ ] **Step 7: Commit Task 2**

```bash
git add internal/attractor/engine/input_reference_scan.go \
        internal/attractor/engine/input_reference_scan_test.go \
        internal/attractor/engine/input_materialization.go \
        internal/attractor/engine/input_materialization_test.go
git commit -m "feat(engine): add recursive deterministic input closure and manifest"
```

### Task 3: Add LLM-Assisted Reference Inference for Implicit Requirements

**Files:**
- Create: `internal/attractor/engine/input_reference_infer.go`
- Create: `internal/attractor/engine/input_reference_infer_test.go`
- Modify: `internal/attractor/engine/input_materialization.go`
- Modify: `internal/attractor/engine/run_with_config.go`

- [ ] **Step 1: Write failing tests with fake inferer**

Test cases:
- DoD text: `also run all tests in markdown files in c drive root`.
- Fake inferer returns `<temp_root>/**/*.md` so traversal tests are portable.
- Planner must expand and include all matches in closure.
- Scanner normalization test still covers Windows-style token parsing (`C:/**/*.md`) without requiring Windows filesystem traversal.

- [ ] **Step 2: Run inference tests and verify failure**

Run: `go test ./internal/attractor/engine -run 'InputReferenceInfer' -count=1`
Expected: FAIL.

- [ ] **Step 3: Implement inferer interface and JSON contract**

Define interface:

```go
type InputReferenceInferer interface {
    Infer(ctx context.Context, docs []InputDocForInference, opts InputInferenceOptions) ([]InferredReference, error)
}
```

Required response fields:
- `pattern`
- `rationale`
- `confidence`

Behavior:
- Use inferer when `inputs.materialize.infer_with_llm=true`.
- Pass `inputs.materialize.llm_model` and `inputs.materialize.llm_provider` into inference options when set.
- Treat inferred references as additive and unbounded.
- On inferer failure, continue deterministic path and record warning (non-fatal).

- [ ] **Step 4: Wire inferer into closure planner**

- Add inferer invocation after deterministic extraction per recursion iteration.
- Deduplicate inferred + deterministic candidates by normalized pattern.

- [ ] **Step 5: Add inferer-failure and provider/model override tests**

Add tests asserting:
- inferer errors do not fail stage planning and are recorded in manifest warnings
- `llm_model` and `llm_provider` overrides are respected in inferer calls
- inferer results are cached by source hash and reused on retry/resume without duplicate inference calls

- [ ] **Step 6: Add startup snapshot persistence tests**

Add tests asserting:
- startup writes `logs_root/input_snapshot/` with resolved files
- removing source workspace inputs after startup does not break retry/resume/branch hydration

- [ ] **Step 7: Run focused tests**

Run: `go test ./internal/attractor/engine -run 'InputReferenceInfer|InputMaterialization' -count=1`
Expected: PASS.

- [ ] **Step 8: Commit Task 3**

```bash
git add internal/attractor/engine/input_reference_infer.go \
        internal/attractor/engine/input_reference_infer_test.go \
        internal/attractor/engine/input_materialization.go \
        internal/attractor/engine/run_with_config.go
git commit -m "feat(engine): infer implicit input references with llm-assisted extractor"
```

### Task 4: Integrate Materialization into Run Startup, Stage Attempts, and Parallel Branch Spawn

**Files:**
- Modify: `internal/attractor/engine/engine.go`
- Modify: `internal/attractor/engine/resume.go`
- Modify: `internal/attractor/engine/parallel_handlers.go`
- Create: `internal/attractor/engine/input_materialization_integration_test.go`
- Create: `internal/attractor/engine/input_materialization_resume_test.go`
- Modify: `internal/attractor/engine/parallel_test.go`

- [ ] **Step 1: Write failing integration test for run startup hydration**

Scenario:
- repo has untracked `.ai/definition_of_done.md`
- run config uses `git.require_clean=false`
- first codergen node reads `.ai/definition_of_done.md`

Expected:
- file exists in run worktree before stage execution
- stage can read it and completes successfully

- [ ] **Step 2: Write failing integration test for branch hydration**

Scenario:
- fan-out node creates parallel branches
- DoD + referenced file only exist as untracked input at source workspace

Expected:
- both files present in each branch worktree before branch node executes

- [ ] **Step 3: Write failing resume integration test**

Scenario:
- initial run writes/uses materialized inputs
- resume recreates worktree from checkpoint

Expected:
- resume path rematerializes required inputs before resumed stage execution
- behavior matches fresh-run hydration semantics

- [ ] **Step 4: Run integration tests and verify failure**

Run: `go test ./internal/attractor/engine -run 'InputMaterializationIntegration|InputMaterializationResume|Parallel.*Input' -count=1`
Expected: FAIL.

- [ ] **Step 5: Implement run startup hook**

In `engine.run()`:
- after run worktree creation, execute materialization from source workspace -> run worktree
- persist run-level `inputs_manifest.json` under `logs_root`

- [ ] **Step 6: Implement pre-stage hook**

In `executeNode()`:
- before handler execution, re-run closure against current worktree docs
- source precedence for resolving missing files after startup: current worktree -> persisted run snapshot (`logs_root/input_snapshot/` + `logs_root/inputs_manifest.json` metadata); do not consult mutable source workspace after initial capture
- this captures new references introduced during the run (for example, newly written DoD)

- [ ] **Step 7: Implement branch spawn hook**

In `runBranch()`:
- after branch worktree setup, materialize closure from parent run worktree into branch worktree
- persist branch-local manifest under branch logs root

- [ ] **Step 8: Implement resume hook**

In `resume.go`:
- after resume worktree recreation and before resumed loop execution, run materialization with same policy/source semantics as fresh runs
- reuse persisted closure/inference cache from prior `inputs_manifest.json` + checkpoint extra state to avoid divergence and duplicate inference on resume
- source for resume hydration is `logs_root/input_snapshot/` (not mutable source workspace)

- [ ] **Step 9: Re-run integration tests**

Run: `go test ./internal/attractor/engine -run 'InputMaterializationIntegration|InputMaterializationResume|Parallel.*Input' -count=1`
Expected: PASS.

- [ ] **Step 10: Commit Task 4**

```bash
git add internal/attractor/engine/engine.go \
        internal/attractor/engine/resume.go \
        internal/attractor/engine/parallel_handlers.go \
        internal/attractor/engine/input_materialization_integration_test.go \
        internal/attractor/engine/input_materialization_resume_test.go \
        internal/attractor/engine/parallel_test.go
git commit -m "feat(engine): materialize recursive input closure in run and branch worktrees"
```

### Task 5: Add Runtime Contract + Observability for Materialized Inputs

**Files:**
- Modify: `internal/attractor/engine/node_env.go`
- Modify: `internal/attractor/engine/handlers.go`
- Modify: `internal/attractor/engine/cxdb_events.go`
- Modify: `internal/attractor/engine/run_with_config_integration_test.go`
- Create: `internal/attractor/engine/prompts/input_materialization_preamble.tmpl`
- Modify: `internal/attractor/engine/prompt_assets.go`

- [ ] **Step 1: Write failing env/preamble tests**

Add tests asserting:
- `KILROY_INPUTS_MANIFEST_PATH` is set for codergen runs
- prompt preamble instructs the model to consult the manifest
- `inputs.materialize.enabled=false` disables manifest env/preamble injection
- `follow_references=false` disables recursive closure traversal
- `infer_with_llm=false` skips inferer calls and emits no inferer warning events

- [ ] **Step 2: Run targeted tests and verify failure**

Run: `go test ./internal/attractor/engine -run 'Input.*Manifest|RunWithConfig.*Invocation' -count=1`
Expected: FAIL.

- [ ] **Step 3: Implement env + preamble plumbing**

- Add `KILROY_INPUTS_MANIFEST_PATH` to node env overrides, pointing to stage-local snapshot path `${logs_root}/${node_id}/inputs_manifest.json`.
- Ensure `${logs_root}/${node_id}/inputs_manifest.json` is written before each stage attempt.
- Render and prepend input-materialization preamble alongside status contract preamble.

- [ ] **Step 4: Add CXDB artifact publishing for manifests**

- Upload run-level manifest (`inputs_manifest.json` at logs root), stage-level snapshots (`${node_id}/inputs_manifest.json`), and branch-level manifests when present.
- Emit deterministic warning/error progress events:
  - `input_materialization_warning`
  - `input_materialization_error`
- Emit watchdog-safe lifecycle events:
  - `input_materialization_started`
  - `input_materialization_progress`
  - `input_materialization_completed`
- Add tests asserting event emission shapes and artifact key names are stable.

- [ ] **Step 5: Re-run targeted tests**

Run: `go test ./internal/attractor/engine -run 'Input.*Manifest|RunWithConfig.*Invocation' -count=1`
Expected: PASS.

- [ ] **Step 6: Commit Task 5**

```bash
git add internal/attractor/engine/node_env.go \
        internal/attractor/engine/handlers.go \
        internal/attractor/engine/cxdb_events.go \
        internal/attractor/engine/run_with_config_integration_test.go \
        internal/attractor/engine/prompts/input_materialization_preamble.tmpl \
        internal/attractor/engine/prompt_assets.go
git commit -m "feat(engine): expose input manifest contract to stages and cxdb"
```

### Task 6: Update Attractor Spec and Guardrails

**Files:**
- Modify: `docs/strongdm/attractor/attractor-spec.md`
- Create: `internal/attractor/validate/input_materialization_contract_guardrail_test.go`
- Modify: `internal/attractor/engine/status_json_worktree_test.go`

- [ ] **Step 1: Add failing docs guardrail test**

Add assertions for new normative language keys, for example:
- `input materialization`
- `transitive reference closure`
- `run and branch worktree hydration`
- `default_include is best-effort and include is fail-on-unmatched`

- [ ] **Step 2: Update spec with normative contract**

Document:
- `inputs.materialize` run-config contract
- recursive closure semantics
- no engine-imposed scope limits
- explicit include failure semantics
- manifest artifact semantics
- branch hydration semantics
- resume hydration parity semantics
- inferer-failure fallback semantics
- status contract unchanged from existing Appendix C.1 behavior

- [ ] **Step 3: Run validate + docs-adjacent tests**

Run: `go test ./internal/attractor/validate ./internal/attractor/engine -run 'InputMaterialization|CreateRunfileTemplate|StatusJsonWorktree' -count=1`
Expected: PASS.

- [ ] **Step 4: Commit Task 6**

```bash
git add docs/strongdm/attractor/attractor-spec.md \
        internal/attractor/validate/input_materialization_contract_guardrail_test.go \
        internal/attractor/engine/status_json_worktree_test.go
git commit -m "docs(spec): add normative input materialization and closure contract"
```

### Task 7: Full Verification and Cleanup

**Files:**
- Modify: any files touched above (format/test-only final pass)

- [ ] **Step 1: Format code**

Run: `gofmt -w ./internal/attractor/engine ./internal/attractor/validate`
Expected: no errors.

- [ ] **Step 2: Run focused package suites**

Run: `go test ./internal/attractor/engine ./internal/attractor/validate -count=1`
Expected: PASS.

- [ ] **Step 3: Run full repository tests**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 4: Build CLI**

Run: `go build -o ./kilroy ./cmd/kilroy`
Expected: build succeeds with exit code 0.

- [ ] **Step 5: Commit any final fixes**

```bash
git add -A
git commit -m "test(engine): finalize unbounded input closure coverage and regressions"
```

---

## Acceptance Criteria

- [ ] Untracked user-provided docs (including `.ai/definition_of_done.md`) are present in run worktree before first stage execution.
- [ ] Supported explicit reference syntaxes are covered by tests: markdown links, quoted absolute/relative paths, and explicit glob tokens.
- [ ] Any file referenced by materialized docs is recursively included in closure and available in run worktree.
- [ ] The same closure is present in each branch worktree before branch stage execution.
- [ ] Resume path rematerializes required inputs before resumed stage execution.
- [ ] Natural-language reference requirements are expanded through LLM-assisted inference when enabled.
- [ ] There are no fixed file-count/size/depth caps in input-closure configuration/code paths; a deep-recursion stress fixture passes and stop behavior is driven only by context cancellation/timeouts.
- [ ] Explicit user `include` patterns that cannot be resolved fail before stage execution with deterministic `failure_reason=input_include_missing` and unmatched pattern listing in checkpoint/progress artifacts; unmatched `default_include` patterns do not fail the run.
- [ ] Manifest contract is deterministic: run-level at `logs_root/inputs_manifest.json`, branch-level at `<branch_logs_root>/inputs_manifest.json`, stage-level snapshot at `${logs_root}/${node_id}/inputs_manifest.json`, and stage env var `KILROY_INPUTS_MANIFEST_PATH` points to the stage-level snapshot.
- [ ] Inferer failure is non-fatal and deterministically falls back to scanner-only closure, with warnings in both `input_materialization_warning` progress events and manifest `warnings` array.
- [ ] `inputs.materialize.enabled=false` preserves prior behavior: no input manifests written, no input-materialization progress events emitted, no `KILROY_INPUTS_MANIFEST_PATH` env injection, and no input preamble text added.
- [ ] Long-running input scans emit `input_materialization_started/progress/completed` lifecycle events so the watchdog can observe forward progress.
- [ ] Existing status contract semantics remain unchanged (`status.json` authority/fallback per spec Appendix C.1).
- [ ] Existing valid run configs that omit the `inputs` block continue to load and execute unchanged.
- [ ] All targeted and full tests pass.

---

## Implementation Notes

- Keep behavior opt-out via `inputs.materialize.enabled=false`; default remains enabled.
- `infer_with_llm` defaults to false for backward compatibility; template-generated configs keep it false by default and include commented opt-in guidance with explicit model/provider.
- Preserve existing status contract semantics (`status.json` authority/fallback) unchanged.
- Resume parity requirement: resumed runs must reuse persisted input-closure state and not require source workspace mutation to proceed.
- Initial capture rule: source workspace is consulted only during startup materialization; retries/branches/resume read from persisted run snapshot and worktree state.
- Do not alter production-run command execution behavior beyond input materialization.
- Prime Directive alignment: this is an engine-level robustness improvement for all projects and all graph topologies.
