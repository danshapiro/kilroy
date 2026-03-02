# Proposal: Run-Scoped `.ai` Workspace with Branch-Safe Materialization

## Problem

The March 2, 2026 incident (`run_id=01KJPDK649C65Y07TBX1041C73`) exposed two
separate failures:

1. stale binary behavior (`cee6fe8e...`) truncated same-path copies to zero
   bytes
2. startup materialization implicitly copied gitignored repo-local `.ai/*.md`,
   which pulled stale Solitaire content into the run

The stale-binary bug is resolved separately. This proposal fixes the input
boundary and durability model so the same class of incident does not recur.

## Evidence Anchors

1. incident run id: `01KJPDK649C65Y07TBX1041C73`
2. run base SHA vs local binary SHA mismatch was confirmed
3. Solitaire source was `/home/user/code/kilroy/.ai/spec.md` and hash-matched
   the startup snapshot copy
4. parallel worktree recreation amplified bad state because untracked `.ai`
   content is not present in fresh worktrees

## Scope

This proposal changes workspace-state handling for materialized inputs and
run-scoped scratch files. It does not move canonical run/stage logs away from
`logs_root`.

## Normative Constraints This Proposal Keeps

When `inputs.materialize.enabled=true`, the current Appendix C.1 contracts stay
intact:

1. run startup snapshot under `logs_root/input_snapshot/`
2. run-level manifest at `logs_root/inputs_manifest.json`
3. branch-level manifests at `<branch_logs_root>/inputs_manifest.json`
4. stage-level manifests at `logs_root/<node_id>/inputs_manifest.json`
5. `KILROY_INPUTS_MANIFEST_PATH` exposed to stage runtimes
6. `include` fail-on-unmatched with
   `failure_reason=input_include_missing`
7. `default_include` best-effort
8. transitive closure when `follow_references=true`
9. additive `infer_with_llm` fallback to scanner-only closure on inferer failure

When `inputs.materialize.enabled=false`, materialization manifests/env injection
remain suppressed, matching current tests and runtime behavior.

## Decision

Adopt this boundary:

1. stage-visible run scratch files live under
   `./.ai/runs/<run_id>/...` in each active worktree
2. shared source-of-truth inputs stay outside `.ai` (for example `docs/`,
   `specs/`, `policies/`)
3. implicit repo-root `.ai/*` ingestion is disabled by default
4. branch/resume hydration uses persisted snapshot state, not mutable developer
   workspace state

## Fan-In / Metaspec Compatibility

This proposal changes run-scoped workspace-state handling, not core git fan-in
winner semantics.

1. existing fan-in winner selection and ff-only branch promotion remain
   authoritative for code state
2. snapshot lineage merge rules in this proposal apply only to run-scoped
   workspace state under `./.ai/runs/<run_id>/...`
3. no snapshot rule in this proposal may introduce non-ff git merge behavior or
   alter winner selection policy

## Branch-Safe Snapshot Lineage

The previous draft used a single "latest revision" model. That is not safe for
parallel branches. This proposal uses explicit lineage.

### Run Startup

1. create run snapshot revision `R0` at startup under
   `logs_root/input_snapshot/`
2. set run head pointer to `R0`

### Fan-Out Branch Fork

1. for each branch `B`, create branch lineage rooted at current run head
2. persist branch lineage metadata in `<branch_logs_root>/inputs_manifest.json`
   with `base_run_revision=<Rn>` and `branch_head_revision=<B0>`
3. branch hydration reads from branch head only

### Branch Execution

1. branch nodes can advance only branch-local revisions (`B0 -> B1 -> ...`)
2. branch updates do not mutate run head directly
3. stage manifests record `(run_base_revision, branch_revision)` for each stage

### Fan-In Merge Back to Run Lineage

1. merge happens once, at fan-in boundary
2. default merge policy for `./.ai/runs/<run_id>/...` is `none`
   (no implicit cross-branch promotion)
3. optional explicit promotion list can be configured at fan-in
4. conflicting writes in promoted paths produce deterministic
   `failure_reason=input_snapshot_conflict` with conflict list
5. successful merge creates new run head `Rn+1`

### Resume

Resume restores from persisted run head and branch lineage metadata, never from
mutable source workspace files.

## Explicit Import Declaration Schema

Current runtime config has `include` and `default_include`. This proposal adds a
typed alias while preserving backward compatibility.

### New Schema

`inputs.materialize.imports`:

```yaml
inputs:
  materialize:
    imports:
      - pattern: "docs/requirements.md"
        required: true
      - pattern: "docs/context/*.md"
        required: false
```

### Mapping Rules

1. `required=true` maps to `include`
2. `required=false` maps to `default_include`
3. `required` defaults to `true`
4. normalized output preserves first-seen order and de-duplicates exact entries

### Validation Rules

1. `pattern` is required and must be non-empty
2. `imports` cannot be used together with explicit `include`/`default_include`
   in the same config (deterministic validation error:
   `failure_reason=input_imports_conflict`)
3. unknown fields in import entries fail validation
4. existing configs that use only `include/default_include` continue to work
   unchanged

## Defaults and Template Migration

To make "implicit root `.ai` ingestion is disabled by default" real in runtime
behavior:

1. engine default for `inputs.materialize.default_include` changes from
   `.ai/*.md` to empty
2. `skills/create-runfile/reference_run_template.yaml` must no longer seed
   root `.ai/*.md` by default
3. config/default tests must be updated to assert the new no-implicit-import
   baseline
4. existing run configs that explicitly set `include/default_include` keep their
   behavior unchanged

## Run-Scoped Hydration Rules for Dynamic Paths

`./.ai/runs/<run_id>/...` contains dynamic run-id segments, so static import
lists are not sufficient as the primary durability mechanism.

1. `imports` / `include` / `default_include` select source-of-truth inputs
   (for example docs/spec files), not run-scoped scratch lineage state
2. run-scoped files are restored from persisted lineage state, not from static
   glob expansion over mutable workspace files
3. branch and resume hydration must reconstruct run-scoped state from persisted
   run/branch heads even when source workspace `.ai` content is absent
4. reference discovery/inference must not implicitly re-import stale
   source-workspace run-scratch files as seed inputs

## Migration for Existing Hardcoded `.ai` Runtime Paths

The proposal now includes concrete migration for known root `.ai` assumptions.

1. `internal/attractor/engine/stage_status_contract.go`
   - keep primary `worktree/status.json`
   - change fallback order:
     1. `worktree/.ai/runs/<run_id>/status.json`
     2. legacy `worktree/.ai/status.json` (compatibility window)
2. `internal/attractor/runstate/snapshot.go`
   - read postmortem/review first from
     `worktree/.ai/runs/<run_id>/postmortem_latest.md` and
     `worktree/.ai/runs/<run_id>/review_final.md`
   - then legacy fallback to root `.ai/...` paths
3. `cmd/kilroy/attractor_status_follow.go`
   - update displayed source labels to new run-scoped paths
   - keep legacy-path display fallback during migration
4. `internal/attractor/engine/failure_dossier.go`
   - write/read run-scoped path first:
     `worktree/.ai/runs/<run_id>/failure_dossier.json`
   - legacy fallback/read compatibility for `worktree/.ai/failure_dossier.json`
     during migration window
5. status-contract metadata surfaces
   - keep contract env keys stable, but update fallback path values and prompt
     preamble examples to run-scoped-first
   - update invocation metadata (`status_fallback_path`) to reflect the new
     path order
6. status/snapshot test + fixture surfaces
   - patch tests/fixtures that assert legacy root `.ai/status.json`,
     postmortem/review, and related fallback behavior

## Skills, Templates, and Reference Graph Impacts

This migration is not complete if only engine internals change.

1. `skills/create-dotfile/SKILL.md` and
   `skills/create-dotfile/reference_template.dot`
   - update producer/consumer guidance from root `.ai/*` to run-scoped
     `./.ai/runs/$KILROY_RUN_ID/...` where content is run scratch state
   - preserve compatibility guidance where dual-read is intentionally supported
2. `skills/build-dod/SKILL.md`
   - update evidence path contract to run-scoped locations (or explicitly define
     compatibility behavior) so generated DoDs do not reintroduce root-path
     coupling
3. `skills/create-runfile/reference_run_template.yaml`
   - remove implicit root `.ai` default include behavior
   - illustrate explicit imports for source-of-truth inputs
4. canonical docs/demos/reference DOTs (for example in `demo/` and
   `docs/strongdm/dot specs/`)
   - replace root `.ai/*` path assumptions where they model run scratch outputs
   - keep any intentional legacy examples clearly labeled as compatibility-only

## Compatibility Window

1. one release supports dual-read (run-scoped first, legacy fallback second)
2. emit deprecation warnings when legacy root `.ai` paths are consumed
3. remove legacy fallback in the next major cleanup after migration telemetry
   confirms low usage

## Implementation Plan

1. config + validation
   - add `imports` schema to `InputMaterializationConfig`
   - implement normalize/validate mapping and conflict rules
2. defaults + template migration
   - change implicit materialization defaults to no root `.ai` import
   - update runfile reference template and config-default tests
3. lineage-aware snapshot manager
   - add run/branch revision lineage metadata and deterministic merge
   - emit deterministic conflict failures
4. hydration integration
   - hydrate run/branch/resume from lineage pointers
   - implement explicit dynamic-path rules for run-scoped state
5. runtime path migration
   - patch status/snapshot/status-follow + failure-dossier and related contract
     surfaces to run-scoped-first dual-read
6. tests + fixtures
   - patch integration/unit fixtures that assert legacy root `.ai` defaults and
     fallback paths
7. skills/templates/docs/demos
   - update skills and reference template dotfile to avoid hardcoded root `.ai`
     scratch paths
   - update canonical examples/reference DOTs accordingly
8. metaspec alignment pass
   - document and assert that fan-in winner ff-only semantics remain unchanged
     while snapshot lineage handles run-scoped workspace state

## Test Plan

1. branch isolation:
   - branch A writes run-scoped file, branch B cannot observe it before fan-in
2. deterministic fan-in merge:
   - explicit promotion conflict yields `input_snapshot_conflict`
3. lineage resume:
   - recreated worktree restores from persisted run/branch lineage state
4. conditional contracts:
   - manifests/env present only when `inputs.materialize.enabled=true`
   - manifests/env suppressed when disabled
5. imports schema:
   - mapping to `include/default_include` is correct
   - `imports + include/default_include` emits `input_imports_conflict`
6. defaults migration:
   - no implicit root `.ai` import when config omits include/default_include
   - explicit include/default_include configs preserve prior behavior
7. dynamic run-scoped hydration:
   - run-scoped lineage state is restored on branch/resume without relying on
     static import patterns
8. path migration:
   - status fallback and status-follow prefer run-scoped `.ai/runs/<run_id>`
   - postmortem/review loading prefers run-scoped `.ai/runs/<run_id>`
   - failure dossier path prefers run-scoped `.ai/runs/<run_id>`
   - legacy root `.ai` fallback still works during compatibility window
9. skills/template/example drift:
   - reference template and generated guidance do not hardcode legacy root `.ai`
     scratch paths
10. metaspec compatibility:
   - fan-in winner selection + ff-only merge behavior unchanged
   - snapshot lineage merge applies only to run-scoped workspace-state artifacts
11. unchanged C.1 behavior:
   - `input_include_missing`, default-include best effort, closure, and inferer
     fallback behavior remain intact

## Reviewer Checklist

1. Does the proposal avoid cross-branch leakage by defining branch lineage and
   fan-in merge rules?
2. Does it reconcile lineage merge behavior with existing metaspec fan-in winner
   ff-only semantics (code state unchanged)?
3. Does it include concrete migration for all known hardcoded `.ai` runtime
   paths (status, postmortem/review, failure dossier, contract metadata/tests)?
4. Does it remove implicit root `.ai` ingestion defaults from runtime/template
   behavior?
5. Does it define how dynamic run-scoped paths are hydrated without depending on
   static include globs?
6. Does it scope manifest/env contracts to
   `inputs.materialize.enabled=true`?
7. Does it define a concrete `imports` schema and validator behavior?
8. Does it include required updates for skills, reference template dotfile, and
   canonical demo/reference DOTs?
9. Does it preserve canonical `logs_root` artifacts and Appendix C.1 semantics?

## Incident-Specific Cleanup

Removed stale local scratch file:
`/home/user/code/kilroy/.ai/spec.md`
