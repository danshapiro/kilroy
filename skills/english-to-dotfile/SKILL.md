---
name: english-to-dotfile
description: Use when given English requirements (from a single sentence to a full spec) that need to be turned into a .dot pipeline file for Kilroy's Attractor engine to build software.
---

# English to Dotfile

Take English requirements of any size and produce a valid `.dot` pipeline file that Kilroy's Attractor engine can execute to build the described software.

## When to Use

- User provides requirements and wants Kilroy to build them
- Input ranges from "solitaire plz" to a path to a 600-line spec
- You need to produce a `.dot` file, not code

## Output Format

When invoked programmatically (via CLI), output ONLY the raw `.dot` file content. No markdown fences, no explanatory text before or after the digraph. The output must start with `digraph` and end with the closing `}`.

When invoked interactively (in conversation), you may include explanatory text.

## Process

### Phase 0: Pick Models + Executors + Parallelism + Thinking (Before Writing Any DOT)

This phase exists to translate ambiguous requests (or partial constraints like "make it parallel with gemini") into a concrete, runnable model/executor plan.

**Override rule:** The user's commands override everything. Use the information below to fulfill them as best you can (while still ensuring the result is runnable in the current environment).

- **Interactive mode:** present options and wait for the user's choice/overrides. Do not emit any `.dot` until chosen.
- **Programmatic mode (CLI ingest / machine-parseable output):** you cannot ask questions. Apply the same selection process, default to the **Medium** option, and then emit only the `.dot`.

#### Step 0.1: Capture User Constraints (If Any)

Parse the user's message for constraints, including:
- Required providers/models (e.g., "gemini", "opus", "codex", "only anthropic", "no openai")
- Parallelism intent (e.g., "parallel", "consensus", "3-way", "fan-out")
- Executor intent (e.g., "api only", "cli only")
- Thinking intent (e.g., "fast/cheap", "max thinking", "default")

Treat constraints as requirements to satisfy when possible, but still run the full process below to pick concrete model IDs and settings.

#### Step 0.2: Detect Provider Access (API and CLI for All Three)

Determine, for each provider, whether **API** and/or **CLI** execution is feasible in this environment:
- OpenAI: API key present? CLI executable present?
- Anthropic: API key present? CLI executable present?
- Gemini/Google: API key present? CLI executable present?

If a provider has neither API nor CLI available, you MUST NOT propose models from that provider.

#### Step 0.3: Fetch "What's Current Today" (Weather Report)

Fetch:
- `curl -fsSL https://factory.strongdm.ai/weather-report`

Extract the "Today's Models" list and treat it as the source of **current** model lines for each provider (including any consensus entries). Also extract any per-model parameter guidance (the Weather Report "Parameters" column) to inform thinking.

#### Step 0.4: Fetch Token Costs (Latest LiteLLM Catalog)

Fetch:
- `curl -fsSL https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json`

You MUST only use model IDs that exist in this catalog. Never invent model IDs.

#### Step 0.5: Resolve Weather Report Names to Real Model IDs (Best-Effort, Verified)

Weather Report names may not exactly match LiteLLM keys.

For each Weather Report model name:
- Find a matching LiteLLM key by searching the catalog (best-effort string normalization is OK).
- Prefer exact/near-exact matches and "latest" variants when present.
- Reject anything you cannot verify exists as a catalog key.

#### Step 0.6: Define "Current" and "Cheapest (Current-Only)"

- **Current models** are the resolved Weather Report models (after filtering by provider access).
- **Cheapest** must be chosen **only from current model lines** (do not pick older generations just because they are cheaper).
  - If you need "cheaper", reduce thinking and parallelism first.

#### Step 0.7: Decide Thinking (No Fixed Mapping Table)

Choose a thinking level for each option using:
- Weather Report parameter guidance (when present), and
- Otherwise, the option's intent (Low = minimal, Medium = strong/default, High = maximum).

Do not hardcode a brittle mapping. Use best judgment and keep it consistent with the option's cost/quality goal.

#### Step 0.8: Produce a Simple 3-Row Options Table (Then Ask, Then Stop)

Before generating DOT, present exactly these three options in a single table:

- **Low:** cheapest current model plan (no older models). Minimal thinking. No parallelism.
- **Medium:** best current model plan (avoid "middle" choices when there's a clear best and a clear cheapest). Thinking per Weather Report / strong defaults. No parallelism.
- **High:** 3 best current models in parallel for thinking-heavy stages (plan/review), then synthesize. Maximum thinking.

The table MUST include:
- Which model(s) you'd use for `impl`, `verify`, and `review` (and for High, the 3 parallel branches + the synthesis model).
- Parallelism behavior (none vs 3-way).
- Thinking approach (brief).
- Executor availability and recommendation **for OpenAI, Anthropic, and Gemini** (account for both API+CLI where available; pick a preferred one consistent with constraints).

After the table, ask:
"Pick `low`, `medium`, or `high`, or reply with overrides (providers/models/executor/parallel/thinking)."

STOP (interactive mode). Do not emit `.dot` until the user replies.

#### Step 0.9: After Selection, Generate DOT

Once the choice/overrides are known:
- Encode the chosen models via `model_stylesheet` and/or explicit node attrs.
- If High (or the user requested parallel), implement 3-way parallel planning/review with a fan-out + fan-in + synthesis pattern.
- Keep the rest of the pipeline generation process unchanged.

### Phase 1: Requirements Expansion

If the input is short/vague, expand into a structured spec covering: what the software is, language/platform, inputs/outputs, core features, acceptance criteria. Write the expanded spec to `.ai/spec.md` locally (for your reference while building the graph).

**Critical:** The pipeline runs in a fresh git worktree with no pre-existing files. The spec must be created INSIDE the pipeline by an `expand_spec` node. Two scenarios:

**Vague input** (e.g., "solitaire plz"): Add an `expand_spec` node as the first node after start. Its prompt contains the expanded requirements inline and instructs the agent to write `.ai/spec.md`. This is the ONE exception to the "don't inline the spec" rule — the expand_spec node bootstraps the spec into existence.

**Detailed spec already exists** (e.g., a file path like `specs/dttf-v1.md`): The spec file is already in the repo and will be present in the worktree. No `expand_spec` node needed. All prompts reference the spec by its existing path.

**The spec file is the source of truth.** Prompts reference it by path. Never inline hundreds of lines of spec into a prompt attribute (except in `expand_spec` which creates it).

### Phase 2: Decompose into Implementation Units

Break the spec into units. Each unit must be:

- **Achievable in one agent session** (~25 agent turns, ~20 min)
- **Testable** with a concrete command (build, test, lint, etc.)
- **Clearly bounded** by files created/modified

Sizing heuristics (language-agnostic):
- Core types/interfaces = early unit (everything depends on them)
- One package/module = one unit (not one file, not one function)
- Each major algorithm/subsystem = its own unit
- CLI/glue code = late unit
- Test harness = after the code it tests
- Integration test = final unit

Language-specific examples:
- **Go:** `go build ./...`, `go test ./pkg/X/...`, one `pkg/` directory = one unit
- **Python:** `pytest tests/`, `mypy src/`, one module directory = one unit
- **Rust:** `cargo build`, `cargo test`, one crate = one unit
- **TypeScript:** `npm run build`, `npm test`, one package = one unit

For each unit, record: ID (becomes node ID), description, dependencies (other unit IDs), acceptance criteria (commands + expected results), complexity (simple/moderate/hard).

**Identify parallelizable units.** If two units have no dependency on each other (e.g., independent packages, separate CLI commands), note them — they can run in parallel branches.

### Phase 3: Build the Graph

#### Required structure

```
digraph project_name {
    graph [
        goal="One-sentence summary of what the software does",
        rankdir=LR,
        default_max_retry=3,
        retry_target="<first implementation node>",
        fallback_retry_target="<second implementation node>",
        model_stylesheet="
            * { llm_model: DEFAULT_MODEL_ID; llm_provider: DEFAULT_PROVIDER; }
            .hard { llm_model: HARD_MODEL_ID; llm_provider: HARD_PROVIDER; }
            .verify { llm_model: VERIFY_MODEL_ID; llm_provider: VERIFY_PROVIDER; reasoning_effort: VERIFY_REASONING; }
            .review { llm_model: REVIEW_MODEL_ID; llm_provider: REVIEW_PROVIDER; reasoning_effort: REVIEW_REASONING; }
        "
    ]

    start [shape=Mdiamond, label="Start"]
    exit  [shape=Msquare, label="Exit"]

    // ... implementation, verification, and routing nodes ...
}
```

#### Expand spec node (when input is vague)

When the requirements are short/vague and no spec file exists in the repo, add an `expand_spec` node as the first node after start. This node creates the spec that all subsequent nodes reference:

```
expand_spec [
    shape=box,
    timeout=600,
    auto_status=true,
    prompt="Given these requirements: [INLINE THE EXPANDED REQUIREMENTS HERE].

Expand into a detailed spec covering: [RELEVANT SECTIONS].
Write the spec to .ai/spec.md.

Write status.json: outcome=success"
]

start -> expand_spec -> impl_setup
```

When a detailed spec file already exists in the repo (e.g., `specs/my-spec.md`), skip this node entirely. Just start with `impl_setup`.

#### Node pattern: implement then verify

For EVERY implementation unit — including `impl_setup` — generate a PAIR of nodes plus a conditional:

```
impl_X [
    shape=box,
    class="hard",
    timeout=1200,
    max_retries=2,
    prompt="..."
]

verify_X [
    shape=box,
    class="verify",
    timeout=300,
    prompt="Verify [UNIT]. Run: [BUILD_CMD] && [TEST_CMD]\nWrite results to .ai/verify_X.md.\nWrite status.json: outcome=success if all pass, outcome=fail with failure details otherwise."
]

check_X [shape=diamond, label="X OK?"]

impl_X -> verify_X
verify_X -> check_X
check_X -> impl_Y  [condition="outcome=success"]
check_X -> impl_X  [condition="outcome=fail", label="retry"]
```

No exceptions. `expand_spec` is the only node that may skip verification (use `auto_status=true` instead).

#### Goal gates

Place `goal_gate=true` on:
- The final integration test node
- Any node producing a critical artifact (e.g., valid font file, working binary)

#### Review node

Near the end, after all implementation, add a review node with `class="review"` and `goal_gate=true` that reads the spec and validates the full project against it.

On review failure, `check_review` must loop back to a LATE-STAGE node — typically the integration/polish node or the last major impl node. Never loop back to `impl_setup` or the beginning. The review failure means something is broken or missing in the final product, not that the entire project needs to be rebuilt from scratch.

### Phase 4: Write Prompts

Every prompt must be **self-contained**. The agent executing it has no memory of prior nodes. Every prompt MUST include:

1. **What to do**: "Implement the bitmap threshold conversion per section 1.4 of specs/dttf-v1.md"
2. **What to read**: "Read specs/dttf-v1.md section 1.4 and pkg/dttf/types.go"
3. **What to write**: "Create pkg/dttf/loader.go with the LoadGlyphs function"
4. **Acceptance criteria**: "Run `go build ./...` and `go test ./pkg/dttf/...` — both must pass"
5. **Outcome instructions**: "Write status.json: outcome=success if all pass, outcome=fail with failure_reason"

Implementation prompt template:
```
Goal: $goal

Implement [DESCRIPTION].

Spec: [SPEC_PATH], section [SECTION_REF].
Read: [DEPENDENCY_FILES] for types/interfaces you need.

Create/modify:
- [FILE_LIST]

Acceptance:
- `[BUILD_COMMAND]` must pass
- `[TEST_COMMAND]` must pass

Write status.json: outcome=success if all criteria pass, outcome=fail with failure_reason otherwise.
```

Verification prompt template:
```
Verify [UNIT_DESCRIPTION] was implemented correctly.

Run:
1. `[BUILD_COMMAND]`
2. `[LINT_COMMAND]`
3. `[TEST_COMMAND]`
4. [DOMAIN_SPECIFIC_CHECKS]

Write results to .ai/verify_[NODE_ID].md.
Write status.json: outcome=success if ALL pass, outcome=fail with details.
```

Use language-appropriate commands: `go build`/`go test` for Go, `cargo build`/`cargo test` for Rust, `npm run build`/`npm test` for TypeScript, `pytest`/`mypy` for Python, etc.

### Phase 5: Model Selection

Use Phase 0 to decide concrete model IDs, providers, executor plan, parallelism, and thinking. Then:

- Assign `class` attributes based on Phase 2 complexity and node role: default, `hard`, `verify`, `review`.
- Encode the chosen plan in the graph `model_stylesheet` so nodes inherit `llm_provider`, `llm_model`, and (optionally) `reasoning_effort`.

## Kilroy DSL Quick Reference

### Shapes (handler types)

| Shape | Handler | Use |
|-------|---------|-----|
| `Mdiamond` | start | Entry point. Exactly one. |
| `Msquare` | exit | Exit point. Exactly one. |
| `box` | codergen | LLM task (default). |
| `diamond` | conditional | Routes on edge conditions. |
| `hexagon` | wait.human | Human approval gate. |

### Node attributes

`label`, `shape`, `prompt`, `max_retries`, `goal_gate`, `retry_target`, `class`, `timeout`, `llm_model`, `llm_provider`, `reasoning_effort`, `allow_partial`, `fidelity`, `thread_id`

### Edge attributes

`label`, `condition`, `weight`, `fidelity`, `thread_id`, `loop_restart`

### Conditions

```
condition="outcome=success"
condition="outcome=fail"
condition="outcome=success && context.tests_passed=true"
condition="outcome!=success"
```

Custom outcome values work: `outcome=port`, `outcome=skip`, `outcome=needs_fix`. Define them in prompts, route on them in edges.

### Canonical outcomes

`success`, `partial_success`, `retry`, `fail`, `skipped`

## Anti-Patterns

1. **No verification after implementation.** Every impl node MUST have a verify node after it. Never chain impl → impl → impl. This includes `impl_setup`.
2. **Labels instead of conditions.** `[label="success"]` does NOT route. Use `[condition="outcome=success"]`.
3. **All failures → exit.** Failure edges must loop back to the implementation node for retry, not to exit.
4. **Multiple exit nodes.** Exactly one `shape=Msquare` node. Route failures through conditionals, not separate exits.
5. **Prompts without outcome instructions.** Every prompt must tell the agent what to write in status.json.
6. **Inlining the spec.** Reference the spec file by path. Don't copy it into prompt attributes. Exception: `expand_spec` node bootstraps the spec.
7. **Missing graph attributes.** Always set `goal`, `model_stylesheet`, `default_max_retry`.
8. **Wrong shapes.** Start is `Mdiamond` not `circle`. Exit is `Msquare` not `doublecircle`.
9. **No timeout.** Every codergen node needs `timeout`. Implementation: 1200. Verification: 300. Review: 600. Expand spec: 600.
10. **Build files after implementation.** Project setup (module file, directory structure) must be the FIRST implementation node.
11. **Catastrophic review rollback.** Review failure (`check_review -> impl_X`) must target a LATE node (integration, CLI, or the last major impl). Never loop `check_review` back to `impl_setup` — this throws away all work. Target the last integration or polish node.
12. **Missing verify class.** Every verify node MUST have `class="verify"` so the model stylesheet applies your intended verify model and thinking.
13. **Missing expand_spec for vague input.** If no spec file exists in the repo, the pipeline MUST include an `expand_spec` node. Without it, `impl_setup` references `.ai/spec.md` that doesn't exist in the fresh worktree.
14. **Hardcoding language commands.** Use the correct build/test/lint commands for the project's language. Don't write `go build` for a Python project.

## Example: Minimal Pipeline (vague input, Go)

```dot
digraph linkcheck {
    graph [
        goal="Build a Go CLI tool that checks URLs for broken links",
        rankdir=LR,
        default_max_retry=3,
        retry_target="impl_setup",
        fallback_retry_target="impl_core",
        model_stylesheet="
            * { llm_model: DEFAULT_MODEL_ID; llm_provider: DEFAULT_PROVIDER; }
            .hard { llm_model: HARD_MODEL_ID; llm_provider: HARD_PROVIDER; }
            .verify { llm_model: VERIFY_MODEL_ID; llm_provider: VERIFY_PROVIDER; reasoning_effort: VERIFY_REASONING; }
            .review { llm_model: REVIEW_MODEL_ID; llm_provider: REVIEW_PROVIDER; reasoning_effort: REVIEW_REASONING; }
        "
    ]

    start [shape=Mdiamond, label="Start"]
    exit  [shape=Msquare, label="Exit"]

    // Spec expansion (vague input — bootstraps .ai/spec.md into existence)
    expand_spec [
        shape=box, timeout=600, auto_status=true,
        prompt="Given the requirements: Build a Go CLI tool called linkcheck that takes a URL, crawls it, checks all links for HTTP status, reports broken ones. Supports robots.txt, configurable depth, JSON and text output.\n\nExpand into a detailed spec. Write to .ai/spec.md covering: CLI interface, packages, data types, error handling, test plan.\n\nWrite status.json: outcome=success"
    ]

    // Project setup
    impl_setup [
        shape=box, timeout=600,
        prompt="Goal: $goal\n\nRead .ai/spec.md. Create Go project: go.mod, cmd/linkcheck/main.go stub, pkg/ directories.\n\nRun: go build ./...\n\nWrite status.json: outcome=success if builds, outcome=fail otherwise."
    ]

    verify_setup [
        shape=box, class="verify", timeout=300,
        prompt="Verify project setup.\n\nRun:\n1. go build ./...\n2. go vet ./...\n3. Check go.mod and cmd/ exist\n\nWrite results to .ai/verify_setup.md.\nWrite status.json: outcome=success if all pass, outcome=fail with details."
    ]

    check_setup [shape=diamond, label="Setup OK?"]

    // Core implementation
    impl_core [
        shape=box, class="hard", timeout=1200, max_retries=2,
        prompt="Goal: $goal\n\nRead .ai/spec.md. Implement: URL crawling, link extraction, HTTP checking, robots.txt parser, output formatters (text + JSON). Create tests.\n\nRun: go test ./...\n\nWrite status.json: outcome=success if tests pass, outcome=fail otherwise."
    ]

    verify_core [
        shape=box, class="verify", timeout=300,
        prompt="Verify core implementation.\n\nRun:\n1. go build ./...\n2. go vet ./...\n3. go test ./... -v\n\nWrite results to .ai/verify_core.md.\nWrite status.json: outcome=success if all pass, outcome=fail with details."
    ]

    check_core [shape=diamond, label="Core OK?"]

    // Review
    review [
        shape=box, class="review", timeout=600, goal_gate=true,
        prompt="Goal: $goal\n\nRead .ai/spec.md. Review the full implementation against the spec. Check: all features implemented, tests pass, CLI works, error handling correct.\n\nRun: go build ./cmd/linkcheck && go test ./...\n\nWrite review to .ai/final_review.md.\nWrite status.json: outcome=success if complete, outcome=fail with what's missing."
    ]

    check_review [shape=diamond, label="Review OK?"]

    // Flow
    start -> expand_spec -> impl_setup -> verify_setup -> check_setup
    check_setup -> impl_core       [condition="outcome=success"]
    check_setup -> impl_setup      [condition="outcome=fail", label="retry"]

    impl_core -> verify_core -> check_core
    check_core -> review           [condition="outcome=success"]
    check_core -> impl_core        [condition="outcome=fail", label="retry"]

    review -> check_review
    check_review -> exit           [condition="outcome=success"]
    check_review -> impl_core      [condition="outcome=fail", label="fix"]
}
```

Note how this example follows every rule:
- `expand_spec` bootstraps the spec (vague input)
- `impl_setup` has its own verify/check pair
- `verify_setup` and `verify_core` both have `class="verify"`
- `check_review` failure loops to `impl_core` (late node), NOT to `impl_setup`
- Graph has `retry_target` and `fallback_retry_target`
- All prompts include `Goal: $goal`
- Model stylesheet covers all four classes
