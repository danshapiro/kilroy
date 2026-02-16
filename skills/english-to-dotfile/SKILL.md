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

**Exception (programmatic disambiguation):** If you cannot confidently generate a correct `.dot` file because the user's request is ambiguous in a load-bearing way (identity/meaning) and you cannot ask questions (CLI ingest), output a short clarification request and STOP. In this exception case, do NOT output any `digraph` at all. Start the output with `NEEDS_CLARIFICATION` and include exactly one disambiguation question plus 2-5 concrete options anchored by repo evidence (paths/names).

**Exception (programmatic validation failure):** If after the self-validation/repair loop (Phase 6, max 10 attempts) you still cannot produce a `.dot` that passes validation, output a short failure report and STOP. Do NOT output any `digraph` at all. Start the output with `DOT_VALIDATION_FAILED` and include the last validator errors.

When invoked interactively (in conversation), you may include explanatory text.

## Process

### Phase 0A: Repo Scan + Minimal Disambiguation (Ask 0 Questions If Possible)

This phase exists to prevent building the wrong thing when the user's wording can reasonably refer to multiple distinct targets.

Rules:
- Prefer **zero** clarification questions.
- Ask ONLY **disambiguation** questions (identity/meaning). Do NOT ask preference questions (language, framework, style, etc.).
- Before asking anything, do a quick repo scan/search to try to resolve ambiguity from evidence.
- Ask the **minimum** number of disambiguation questions required to proceed confidently (typically 1).

What counts as a disambiguation question:
- Resolve an ambiguous identifier that could map to multiple real things.
  - Example: "jj" could mean multiple tools; "parser" could refer to multiple packages; "api" could refer to multiple services.

What does NOT count as disambiguation:
- "What language should I code in?"
- "Should we use framework X or Y?"

#### Step 0A.1: Extract Ambiguous Tokens

From the user request, list candidate ambiguous references:
- Short names/acronyms
- Tool/binary names
- Bare filenames without paths
- Component names that might exist multiple times in a monorepo

#### Step 0A.2: Quick Repo Triage (Evidence First)

Timebox to ~60 seconds. Use local inspection to resolve meaning:
- List top-level structure (`ls`)
- Search likely entrypoints/docs (`README*`, `docs/`, `cmd/`, `scripts/`, `internal/`)
- Use ripgrep (`rg`) for each ambiguous token and inspect the most relevant hits

If a single referent is strongly supported by repo evidence, proceed without questions.

#### Step 0A.3: If Still Ambiguous, Ask ONE Disambiguation Question (Interactive)

Interactive mode (conversation):
- Ask exactly one SINGLE-SELECT disambiguation question.
- Provide 2-5 options, each anchored by concrete repo evidence (paths/names).
- Do NOT generate any `.dot` until the user answers.

#### Step 0A.4: If Still Ambiguous, Stop and Request Disambiguation (Programmatic)

Programmatic mode (CLI ingest / cannot ask):
- If ambiguity is load-bearing after repo triage, you MUST NOT emit any `.dot`.
- Output a short clarification request (so ingestion fails fast) and STOP.
- Ask exactly ONE disambiguation question and provide 2-5 options, each anchored by concrete repo evidence (paths/names).
- Do NOT ask preference questions (language/framework/style).

Required output format for this exception case:
```
NEEDS_CLARIFICATION
Question: <single disambiguation question>
Options:
- [A] <option A> (evidence: <paths/names>)
- [B] <option B> (evidence: <paths/names>)
Reply with: A|B|...
```

Downstream requirement (after ambiguity is resolved interactively or via evidence):
- Document disambiguation choices in a shared artifact for downstream nodes:
  - If the pipeline includes `expand_spec`, include a brief "Disambiguation / Assumptions" section in `.ai/spec.md`.
  - If the pipeline uses an existing repo spec file (no `expand_spec`), write `.ai/disambiguation_assumptions.md` and instruct downstream nodes to read both the spec and this assumptions file.

### Phase 0B: Pick Models + Executors + Parallelism + Thinking (Before Writing Any DOT)

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

#### Step 0.1B: Load Preferences File

Read the preferences file at `.claude/skills/english-to-dotfile/preferences.yaml`.

This file contains:
- **Default models per role** (`defaults.models.default`, `.hard`, `.verify`, `.review`). If a role has a non-empty model ID, use it as the starting default for that role. The Weather Report and user overrides can still supersede it.
- **Executor preference** (`executor`). A single global setting: "cli" or "api". When set to "cli", prefer CLI agents (codex, claude, gemini) for all providers. Falls back to API if the CLI binary isn't installed.

If the file is missing or unreadable, proceed with no defaults (same as all fields blank).

#### Step 0.2: Detect Provider Access (API and CLI for All Providers)

Determine, for each provider, whether **API** and/or **CLI** execution is feasible in this environment:
- OpenAI: API key present? CLI executable present?
- Anthropic: API key present? CLI executable present?
- Gemini/Google: API key present? CLI executable present?
- Cerebras: API key present? (API-only, no CLI agent)
- Kimi: API key present (`KIMI_API_KEY`)? (API-only, no CLI agent)
- Zai (GLM): API key present (`ZAI_API_KEY`)? (API-only, no CLI agent)
- Minimax: API key present (`MINIMAX_API_KEY`)? (API-only, no CLI agent)

Also check the run config (if one exists or is being generated alongside the DOT) for provider entries — providers configured there with `backend: api` and API credentials are available even if not in the list above.

If a provider has neither API nor CLI available, you MUST NOT propose models from that provider.

#### Step 0.3: Fetch "What's Current Today" (Weather Report)

Fetch:
- `curl -fsSL https://factory.strongdm.ai/weather-report`

Extract the "Today's Models" list and treat it as the source of **current** model lines for each provider (including any consensus entries). Also extract any per-model parameter guidance (the Weather Report "Parameters" column) to inform thinking.

#### Step 0.4: Fetch Token Costs (Latest LiteLLM Catalog)

Fetch:
- `curl -fsSL https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json`

Use the LiteLLM catalog to verify model IDs and look up costs. However: **if the user explicitly requests a model that is not in the catalog, always obey the user.** New models often appear on provider APIs before LiteLLM adds them. The catalog is a reference, not a gatekeeper. Only reject model IDs that you yourself are inventing without user or Weather Report backing.

#### Step 0.5: Resolve Weather Report Names to Real Model IDs (Best-Effort, Verified)

Weather Report names may not exactly match LiteLLM keys.

For each Weather Report model name:
- Find a matching LiteLLM key by searching the catalog (best-effort string normalization is OK).
- Prefer exact/near-exact matches and "latest" variants when present.
- If a Weather Report model has no catalog match, **use it anyway** — the Weather Report reflects what is actually running in production today. New models routinely appear before LiteLLM catalogs them.

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

- **Low:** cheapest current API providers. Use kimi (kimi-k2.5) as default/hard, with 3-way fan-out across kimi, zai (glm-4.7), and minimax (minimax-m2.5) for thinking stages (DoD, planning, review). High thinking/reasoning on all nodes — these providers are cheap enough to max out reasoning. All API — no CLI agents.
- **Medium:** best current model plan (avoid "middle" choices when there's a clear best and a clear cheapest). 3-way fan-out for thinking stages (DoD, planning, review) per the reference template, using the best current model across all branches. Thinking per Weather Report / strong defaults.
- **High:** 3 best current models from different providers in parallel for thinking-heavy stages (DoD/plan/review), then synthesize. Maximum thinking. Cross-provider diversity maximizes independent reasoning.

The table MUST include:
- Which model(s) you'd use for `impl`, `verify`, and the 3 fan-out branches + synthesis model.
- Parallelism behavior (none vs 3-way).
- Thinking approach (brief).
- Executor availability and recommendation for all detected providers (account for both API+CLI where available; use the `executor` value from the preferences file to pick the preferred one, unless the user specifies otherwise).

After the table, ask:
"Pick `low`, `medium`, or `high`, or reply with overrides (providers/models/executor/parallel/thinking)."

STOP (interactive mode). Do not emit `.dot` until the user replies.

#### Step 0.9: After Selection, Generate DOT

Once the choice/overrides are known:
- There are many valid ways to structure a pipeline. Unless the user asks for a different structure, keep things simple and proven by using the reference profile.
- We do:
  - Start from `skills/english-to-dotfile/reference_template.dot`.
  - Keep the same core flow: DoD (if needed) -> plan fan-out (3) -> consolidate -> single-writer implement -> verify/check -> review fan-out (3) -> consensus -> postmortem -> planning restart.
  - Keep implementation single-writer. Use parallelism for thinking stages (DoD/plan/review), not for code edits.
  - Preserve explicit outcome-based routing and restart edges from the template.
- We do not:
  - Invent new loop/control-flow mechanisms when the reference flow already fits.
  - Add visit-count loop breakers by default.
  - Parallelize implementation unless the user explicitly asks for it and the graph includes clear isolation and merge steps.
- Encode the chosen models via `model_stylesheet` and/or explicit node attrs.
- For Medium, all fan-out branches use the same model (session-level variance). For High, assign different providers to `branch-a`/`branch-b`/`branch-c` in the stylesheet (cross-provider diversity).

#### Reference Looping Profile

For iterative build workflows, keep this structure unless the user asks for a different one:
- Graph attrs include: `default_max_retry`, `retry_target`, `fallback_retry_target`.
- Inner repair loop: `implement -> verify -> check -> implement` on fail.
- Outer improvement loop: `review_consensus -> postmortem -> plan_*` with `loop_restart=true`.
- Routing stays outcome-based (`outcome=success|retry|fail|skip|done` and custom values where needed).
- For any `goal_gate=true` node that can route to terminal, the terminal success route must use `condition="outcome=success"` or `condition="outcome=partial_success"`.
- Do not add visit-count loop breakers (for example `max_node_visits`) by default.

### Phase 1: Requirements Expansion

If the input is short/vague, expand into a structured spec covering: what the software is, language/platform, inputs/outputs, core features, acceptance criteria. Write the expanded spec to `.ai/spec.md` locally (for your reference while building the graph).

**Critical:** The pipeline runs in a fresh git worktree with no pre-existing files. The spec must be created INSIDE the pipeline by an `expand_spec` node. Two scenarios:

**Vague input** (e.g., "solitaire plz"): Add an `expand_spec` node as the first node after start. Its prompt contains the expanded requirements inline and instructs the agent to write `.ai/spec.md`. This is the ONE exception to the "don't inline the spec" rule — the expand_spec node bootstraps the spec into existence.

**Detailed spec already exists** (e.g., a file path like `demo/dttf/dttf-v1.md`): The spec file is already in the repo and will be present in the worktree. No `expand_spec` node needed. All prompts reference the spec by its existing path.

**The spec file is the source of truth.** Prompts reference it by path. Never inline hundreds of lines of spec into a prompt attribute (except in `expand_spec` which creates it).
When assumptions are recorded separately (existing-spec mode), treat `.ai/disambiguation_assumptions.md` as a required companion input for downstream prompts.

### Phase 2: Plan the Implementation Strategy

Default: use a **hill-climbing loop** — the reference template already encodes this pattern:
- Single-writer implement → verify → review consensus → postmortem → re-plan.
- Ask for a best-effort "one shot" implementation (expect partial completion).
- Use review + postmortem to feed focused context back into the next planning round.
- Iterate until the Definition of Done is met or the max loop count is reached.

In most cases, Phase 2 is simply: "use the reference template as-is with a single implementation node."

#### Exception: Explicit Pre-Decomposition

Use explicit pre-decomposition into multiple implementation units only when it clearly reduces total loops (e.g., well-isolated subsystems with no shared state, or when the repo/tooling makes parallel discovery safe).

When decomposing, each unit must be:

- **Achievable in one agent session** (one primary objective, one narrow acceptance contract)
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
- **Go:** `go build ./cmd/<app> ./pkg/<app>/...`, `go test ./cmd/<app>/... ./pkg/<app>/...`, one `pkg/` directory = one unit
- **Python:** `pytest tests/`, `mypy src/`, one module directory = one unit
- **Rust:** `cargo build`, `cargo test`, one crate = one unit
- **TypeScript:** `npm run build`, `npm test`, one package = one unit

For each unit, record: ID (becomes node ID), description, dependencies (other unit IDs), acceptance criteria (commands + expected results), complexity (simple/moderate/hard).

**Identify parallelizable units.** If two units have no dependency on each other (e.g., independent packages, separate CLI commands), note them — they can run in parallel branches.

#### Decomposition Strategy

Think in terms of **information flow**: each node should reduce one kind of uncertainty and leave a clearer artifact for the next node.

- Start with a shared frame so later work is comparable (example: map schema/glossary).
- Decompose along natural repo boundaries, not arbitrary chunks (example: package or domain slices).
- Use parallelism for independent discovery, then converge with one reconciliation step.
- Verify at two levels: local correctness of each slice, then global consistency of the merged result.

Example pattern for “map the codebase”: shared frame -> boundary slices -> merge -> consistency check.

#### Sizing Calibration (Required)

Right-sized node characteristics:
- One primary deliverable (one concrete thing to finish), not a bundle of loosely-related outcomes.
- One dominant scope boundary (typically one subsystem/module/package).
- One dominant failure mode; verification answers one narrow question about that contract.
- Downstream nodes can consume its output without re-reading the entire project.

Too-big signals (split when any of these are true):
- Prompt asks for multiple independent subsystems in one node.
- Prompt requires editing more than one major module/package.
- Prompt combines large implementation work **and** broad audit/comparison work.
- Verify node checks many unrelated concerns instead of one narrow contract.

How to split oversized nodes:
- Extract shared contracts/types first.
- Split each subsystem into its own impl+verify+check chain.
- Move cross-subsystem wiring into a dedicated integration node.
- Keep verification nodes focused on one contract each (build/test + targeted checks).

#### Checkpoint Ergonomics (Information)

- Resume uses the parent run checkpoint at `{logs_root}/checkpoint.json`.
- In parallel fan-out, branch-local progress is not a parent resume point.
- If a run is stopped mid-node or mid-fan-out, in-flight work since the last parent checkpoint may be replayed.

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
            .branch-a { llm_model: BRANCH_A_MODEL; llm_provider: BRANCH_A_PROVIDER; }
            .branch-b { llm_model: BRANCH_B_MODEL; llm_provider: BRANCH_B_PROVIDER; }
            .branch-c { llm_model: BRANCH_C_MODEL; llm_provider: BRANCH_C_PROVIDER; }
        "
    ]

    start [shape=Mdiamond, label="Start"]
    exit  [shape=Msquare, label="Exit"]

    // ... implementation, verification, and routing nodes ...
}
```

#### Provenance header

To improve operator ergonomics and traceability, include a provenance header at the top of the graph attribute block when possible.

Guideline:
- If the source is text, embed it in graph attributes (escaped/chunked if needed).
- If the source is one or more files, link them by repo-relative path and include the commit SHA used to generate the graph.
- Keep provenance inside `graph [ ... ]` so programmatic output still starts with `digraph`.

Suggested attribute pattern:
```
graph [
    provenance_version="1",
    provenance_text_1="...escaped source text..."
    provenance_file_1="path=docs/spec.md;git_sha=<sha>"
]
```

#### Expand spec node (when input is vague)

When the requirements are short/vague and no spec file exists in the repo, add an `expand_spec` node as the first node after start. This node creates the spec that all subsequent nodes reference:

```
expand_spec [
    shape=box,
    auto_status=true,
    prompt="Given these requirements: [INLINE THE EXPANDED REQUIREMENTS HERE].

Expand into a detailed spec covering: [RELEVANT SECTIONS].
Write the spec to .ai/spec.md.

Write status JSON to $KILROY_STAGE_STATUS_PATH (absolute path), fallback to $KILROY_STAGE_STATUS_FALLBACK_PATH, and do not write nested status.json files.
Write status JSON: outcome=success"
]

start -> expand_spec -> impl_setup
```

When a detailed spec file already exists in the repo (e.g., `specs/my-spec.md`), skip this node entirely. Just start with `impl_setup`.

#### Toolchain bootstrap for non-default dependencies (DOT + run config)

If the deliverable needs tools that are commonly missing (for example `wasm-pack`, Playwright browsers, Android/iOS SDKs), use a user-controlled two-layer policy:

1. Determine required tools from the planned build/test commands and runtime requirements.
2. Check current environment readiness (`command -v <tool>`) for those tools.
3. Always add an early DOT readiness gate (`shape=parallelogram` `tool_command`) to fail fast with actionable errors.
4. Add companion run config bootstrap (`setup.commands`) only when the user explicitly opts in to auto-install/self-prepare behavior.

Interactive mode:
- If required tools are missing, ask exactly one question before adding installer commands to run config.
- Question pattern: "Missing required tools detected: [list]. Do you want run.yaml setup.commands to install/bootstrap them automatically?"
- If user says yes: add idempotent installer/bootstrap commands.
- If user says no: keep check-only gate and include exact install commands in the failure text.

Programmatic mode:
- You cannot ask questions. Default to non-mutating behavior:
  - include check_toolchain readiness gate,
  - do not add installer commands,
  - include exact install commands in the tool failure message.

Use this split deliberately:
- `setup.commands` prepares the environment before the first node executes and is re-run on resume.
- `check_toolchain` in DOT gives explicit, user-facing failure messages inside the run graph.

Example companion run config fragment (only when user opts in to auto-install):

```yaml
setup:
  timeout_ms: 900000
  commands:
    - command -v cargo >/dev/null
    - command -v wasm-pack >/dev/null || cargo install wasm-pack
    - rustup target list --installed | grep -qx wasm32-unknown-unknown || rustup target add wasm32-unknown-unknown
```

Example DOT readiness gate:

```dot
check_toolchain [
    shape=parallelogram,
    max_retries=0,
    tool_command="bash -lc 'set -euo pipefail; command -v cargo >/dev/null || { echo \"missing required tool: cargo\" >&2; exit 1; }; command -v wasm-pack >/dev/null || { echo \"missing required tool: wasm-pack\" >&2; exit 1; }'"
]

start -> check_toolchain -> impl_setup
```

If the user explicitly wants a non-mutating environment (no auto-install), keep only the readiness gate and make the failure message include the exact install command.

#### CXDB launcher defaults for companion run config (this repo)

When you produce or update a companion `run.yaml` in this repository and CXDB endpoints are local (`127.0.0.1`/`localhost`), default to launcher-based autostart:

```yaml
cxdb:
  binary_addr: 127.0.0.1:9009
  http_base_url: http://127.0.0.1:9010
  autostart:
    enabled: true
    command:
      - /home/user/code/kilroy/scripts/start-cxdb.sh
    wait_timeout_ms: 60000
    poll_interval_ms: 250
    ui:
      url: http://127.0.0.1:9020
```

Port layout:
- **9009** — CXDB binary protocol (msgpack over TCP)
- **9010** — CXDB HTTP API (`/v1/contexts`, `/health`, etc.)
- **9020** — CXDB web UI (Next.js "Context Debugger / Turn DAG Viewer", served by nginx inside the container on port 80, mapped to host port 9020 by `start-cxdb.sh`)

The UI port (9020) is distinct from the API port (9010). `scripts/start-cxdb.sh` maps container port 80 → host port 9020 via `-p ${UI_ADDR}:80` (where `UI_ADDR` defaults to `127.0.0.1:9020`, configurable via `KILROY_CXDB_UI_ADDR`). If the container was created before this mapping existed, the UI won't be reachable until the container is recreated.

Rules:
- Use the absolute launcher path shown above for this repo.
- Set `ui.url` to `http://127.0.0.1:9020` (the UI port), NOT `http://127.0.0.1:9010` (the API port).
- Prefer launcher autostart over manual "start CXDB first" prerequisites for local runs.
- `scripts/start-cxdb.sh` is strict by default and rejects unmanaged healthy endpoints (to avoid accidentally using a test shim on the same ports).
- If the user intentionally wants an external non-docker endpoint, document `KILROY_CXDB_ALLOW_EXTERNAL=1` in the run instructions (or disable autostart explicitly).

If `cxdb.http_base_url` / `cxdb.binary_addr` point to non-local hosts, do not force the local launcher path; keep autostart optional and honor user/environment constraints.

#### Node pattern: implement then verify

For EVERY implementation unit — including `impl_setup` — generate a PAIR of nodes plus a conditional:

```
impl_X [
    shape=box,
    class="hard",
    max_retries=2,
    prompt="Implement [UNIT]. Read [SPEC_PATH] and required dependency files. Write implementation outputs to the declared files.\n\nRun required build/test checks for this unit.\n\nWrite status JSON to $KILROY_STAGE_STATUS_PATH (absolute path), fallback to $KILROY_STAGE_STATUS_FALLBACK_PATH, and do not write nested status.json files.\nWrite status JSON: outcome=success if checks pass, outcome=fail with failure_reason and details otherwise."
]

verify_X [
    shape=box,
    class="verify",
    prompt="Verify [UNIT]. Run: [BUILD_CMD] && [TEST_CMD]\nWrite results to .ai/verify_X.md.\nWrite status JSON to $KILROY_STAGE_STATUS_PATH (absolute path), fallback to $KILROY_STAGE_STATUS_FALLBACK_PATH, and do not write nested status.json files.\nWrite status JSON: outcome=success if all pass, outcome=fail with failure_reason and details otherwise."
]

check_X [shape=diamond, label="X OK?"]

impl_X -> verify_X
verify_X -> check_X
check_X -> impl_Y  [condition="outcome=success"]
check_X -> impl_X  [condition="outcome=fail", label="retry"]
```

For build pipelines, no exceptions: every implementation node (including `impl_setup`) must be followed by verify/check. `expand_spec` is the only build-pipeline node that may skip verification (use `auto_status=true` instead). Non-build workflows may use the relaxed 2-node pattern documented below.

#### Turn budget (`max_agent_turns`)

**Do not add `max_agent_turns` by default.** The reference template omits turn limits, relying on `timeout` as the primary safety guard. Add turn limits only with clear production evidence (e.g., a node consistently running away or finishing well under budget).

- Omitted or `0` = unlimited (timeout-only guard).
- `N` = hard cap at N turns (one turn = one LLM API call + tool execution). Resets per retry attempt.
- `timeout` remains the primary safety guard; `max_agent_turns` is a secondary optimization. A wrong turn limit is catastrophic: it kills productive work and wastes every turn the agent already completed.

#### Goal gates

Place `goal_gate=true` on:
- The final integration test node
- Any node producing a critical artifact (e.g., valid font file, working binary)

Goal-gate status contract:
- `goal_gate=true` is only satisfied by canonical `success` or `partial_success`.
- If a goal-gate node routes to terminal, use `condition="outcome=success"` (or `partial_success`) on that terminal edge.
- Do not use `outcome=pass` as the success signal for a terminal route from a goal gate.

#### Review node

Near the end, after all implementation, add a review stage with `goal_gate=true` that reads the spec and validates the full project against it. Use 3 review branches with `class="branch-a"`, `class="branch-b"`, `class="branch-c"` feeding a `review_consensus` node, per the reference template.

The reference template uses the hill-climbing pattern for review failure: review consensus → postmortem → re-plan with `loop_restart=true`. The postmortem produces actionable "what to do next" guidance and feeds it into the next planning stage (via `.ai/` artifacts), so the planner is not restarting cold. Do NOT reset the worktree/code between loops; the next plan must assume the existing partially-complete codebase and focus on the remaining gaps.

For the inner repair loop (verify failure), route back to the implementation node directly (no restart, accumulate context).

#### Advanced Graph Patterns

##### Custom multi-outcome steering

The skill's default impl→verify→check pattern uses binary `outcome=success`/`outcome=fail`. But prompts can define any custom outcome values, and edges can route on them. Use this for workflows with skip/acknowledge/escalate paths.

**Critical: diamond vs box for steering nodes.**
- `shape=diamond` (conditional handler) is a **pure pass-through router**. It reads the `outcome` from the *previous* node's context and routes based on edge conditions. It never executes a prompt. Use diamond for `check_X` nodes that route on a preceding verify node's outcome.
- `shape=box` (codergen handler) **executes a prompt** via an LLM and writes its own outcome. Use box for nodes that need to analyze something, make a decision, and write a custom outcome.

If you need a node that evaluates something and writes `outcome=port`/`outcome=skip`/`outcome=done`, it **must** be `shape=box`. If you just need to forward a prior node's outcome to different edges, use `shape=diamond`.

```
analyze [
    shape=box,
    prompt="Analyze the commit. If it's relevant to our Go codebase, use outcome=port. If it's Python-only or docs-only, use outcome=skip.\n\nWrite status JSON to $KILROY_STAGE_STATUS_PATH (absolute path), fallback to $KILROY_STAGE_STATUS_FALLBACK_PATH, and do not write nested status.json files.\nWrite status JSON: outcome=port or outcome=skip with reasoning."
]

analyze -> plan_port  [condition="outcome=port", label="port"]
analyze -> fetch_next [condition="outcome=skip", label="skip", loop_restart=true]
```

When using custom outcomes, the prompt MUST tell the agent exactly which outcome values to write and when.
For multi-outcome nodes, prefer making the non-happy-path edge unconditional (no `condition=`) so it catches any outcome the LLM writes.

##### Looping/cyclic workflows

Not all pipelines are linear build-then-review chains. Some workflows process items in a loop until done (e.g., processing commits, handling a queue, iterating on feedback). The key pattern:

```
start -> fetch_next
fetch_next -> process [condition="outcome=process"]
fetch_next -> exit    [condition="outcome=done"]
process -> validate
validate -> finalize  [condition="outcome=success"]
validate -> fix       [condition="outcome=fail"]
fix -> validate
finalize -> fetch_next [loop_restart=true]
```

Key elements:
- A **fetch/check** node at the loop head that returns `outcome=done` when there's nothing left
- `loop_restart=true` on the edge that loops back, so each iteration gets a fresh log directory
- The loop body follows the same impl→verify pattern as linear pipelines

Failure-edge restart policy:
- Use `loop_restart=true` on failure edges only when the condition includes `context.failure_class=transient_infra`.
- Always pair that restart edge with a non-restart deterministic fail edge, e.g. `context.failure_class!=transient_infra`.
- Model deterministic checks as explicit `shape=parallelogram` tool nodes (`verify_fmt`, `verify_build`, `verify_test`, `verify_artifacts`), then use a final `shape=box` semantic review node for fidelity/analysis.

Example:
```
check_X -> impl_X [condition="outcome=fail && context.failure_class=transient_infra", loop_restart=true]
check_X -> impl_X [condition="outcome=fail && context.failure_class!=transient_infra"]
```

##### Fan-out / fan-in (parallel consensus)

When you need multiple models to independently tackle the same task and then consolidate:

Default:
- Use fan-out/fan-in for *thinking* stages (Definition of Done proposals, planning variants, review variants).
- Avoid fanning out *implementation* in the same codebase; keep one code-writing node active at a time.
- If implementation fan-out is explicitly requested, do it only with strict isolation (disjoint write scopes + shared files read-only) and a dedicated post-fan-in integration/merge node.
- Fan-out branches should use 2–3 models from **different providers** to maximize diversity of perspective. Same-model fan-out provides only session-level variance; cross-provider fan-out yields genuinely independent reasoning. When the user specifies models for one fan-out stage (e.g., review), reuse those same provider/model assignments for other fan-out stages (DoD, planning) unless the user overrides.

```
// Fan-out: one node fans to 3 parallel workers
consolidate_input -> plan_a
consolidate_input -> plan_b
consolidate_input -> plan_c

// Fan-in: all 3 converge on a synthesis node
plan_a -> synthesize
plan_b -> synthesize
plan_c -> synthesize

synthesize [
    shape=box,
    prompt="Read .ai/plan_a.md, .ai/plan_b.md, .ai/plan_c.md. Synthesize the best elements into .ai/plan_final.md.\n\nWrite status JSON to $KILROY_STAGE_STATUS_PATH (absolute path), fallback to $KILROY_STAGE_STATUS_FALLBACK_PATH, and do not write nested status.json files.\nWrite status JSON: outcome=success when synthesis is complete, outcome=fail with failure_reason and details otherwise."
]
```

Each parallel worker writes its output to a uniquely-named `.ai/` file. The synthesis node reads all of them. This pattern is used for:
- Definition of Done proposals (3 models propose, 1 consolidates)
- Implementation planning (3 plans, 1 debate/consolidate)
- Code review (3 reviewers, 1 consensus)

#### Parallel write-scope discipline (Guideline + Requirement)

- This discipline is primarily for the *exception case* where you intentionally parallelize implementation. The default recommendation is still: one code-writing node at a time, and parallelize planning/review instead.

Guideline:
- Partition fan-out branches by natural ownership boundaries (for example one package/subsystem per branch) so each branch can be validated independently.

Requirement:
- Every fan-out branch prompt MUST declare explicit write-scope paths.
- Branch write scopes MUST be disjoint.
- Shared/core files (for example registry/index/type hubs) are read-only during fan-out.
- Shared/core edits happen in a dedicated post-fan-in integration node.
- Each branch verify node MUST compare changed files vs `$base_sha` and fail with `failure_reason=write_scope_violation` if any changed path is outside the declared scope.

#### Checkpoint Ergonomics (Requirements)

- Prefer nodes that each produce one checkpoint-worthy artifact or decision.
- Avoid long internal dependency chains inside a single node.
- Avoid one giant fan-out wave for large systems; use staged fan-out waves.
- After each fan-in, add a parent checkpoint barrier node (for example `verify_batch_N` or `consolidate_batch_N`) before launching the next fan-out.
- Split broad implementation into multiple fan-out/fan-in phases so operators can stop/resume with minimal replay.

##### Relaxed node patterns (2-node vs 3-node)

The mandatory 3-node pattern (impl → verify → diamond check) is the **default for build pipelines**. But for non-build workflows (analysis, review, processing loops), a 2-node pattern is acceptable:

**Use 3-node (impl → verify → check) when:**
- The node produces code that must compile/pass tests
- There's a concrete build/test command to run

**Use 2-node (work → check) when:**
- The node is analytical (review, planning, triage) with no build step
- The node's prompt already includes outcome routing instructions
- The verify step would just be "read what the previous node wrote"

In the 2-node pattern, the work node acts as its own steer — its prompt instructs the agent to write `outcome=success`/`outcome=fail`/`outcome=<custom>` directly.

##### File-based inter-node communication

Nodes communicate through the filesystem, not through context variables. Each node writes its output to a named file under `.ai/`, and downstream nodes' prompts tell them which files to read:

```
plan [
    shape=box,
    prompt="Create an implementation plan. Write to .ai/plan.md.\n\nWrite status JSON to $KILROY_STAGE_STATUS_PATH (absolute path), fallback to $KILROY_STAGE_STATUS_FALLBACK_PATH, and do not write nested status.json files.\nWrite status JSON: outcome=success when the plan is complete, outcome=fail with failure_reason and details otherwise."
]

implement [
    shape=box,
    prompt="Follow the plan in .ai/plan.md. Implement all items. Log changes to .ai/impl_log.md.\n\nWrite status JSON to $KILROY_STAGE_STATUS_PATH (absolute path), fallback to $KILROY_STAGE_STATUS_FALLBACK_PATH, and do not write nested status.json files.\nWrite status JSON: outcome=success when implementation is complete, outcome=fail with failure_reason and details otherwise."
]

review [
    shape=box,
    prompt="Read .ai/plan.md and .ai/impl_log.md. Review implementation against the plan. Write review to .ai/review.md.\n\nWrite status JSON to $KILROY_STAGE_STATUS_PATH (absolute path), fallback to $KILROY_STAGE_STATUS_FALLBACK_PATH, and do not write nested status.json files.\nWrite status JSON: outcome=success when review passes, outcome=fail with failure_reason and details otherwise."
]
```

This pattern is mandatory because each node runs in a fresh agent session with no memory of prior nodes. The filesystem is the only shared state.

### Phase 4: Write Prompts

Every prompt must be **self-contained**. The agent executing it has no memory of prior nodes. Every prompt MUST include:

1. **What to do**: "Implement the bitmap threshold conversion per section 1.4 of demo/dttf/dttf-v1.md"
2. **What to read**: "Read demo/dttf/dttf-v1.md section 1.4 and pkg/dttf/types.go"
3. **What to write**: "Create pkg/dttf/loader.go with the LoadGlyphs function"
4. **Acceptance criteria**: "Run `go build ./cmd/dttf ./pkg/dttf/...` and `go test ./cmd/dttf/... ./pkg/dttf/...` — both must pass"
5. **Outcome instructions**: "Write status JSON to `$KILROY_STAGE_STATUS_PATH` (absolute path), fallback to `$KILROY_STAGE_STATUS_FALLBACK_PATH`, avoid nested status.json files, and set outcome=success/fail with failure_reason + details as appropriate"

Validation scope policy:
- Required checks must be scoped to the project/module paths created by the pipeline (for Go, prefer `./cmd/<app>` + `./pkg/<app>/...`).
- Do NOT default to repo-wide `./...` required checks in monorepos/sandboxed environments unless the user explicitly requests full-repo validation.
- Lint commands in verify nodes MUST be scoped to files changed by the current feature, not the entire project. Pre-existing lint errors in unrelated files will cause infinite retry loops.
  - Use `$base_sha` (the commit SHA at run start, expanded by the engine) to identify changed files.
  - TypeScript/JS: `git diff --name-only $base_sha -- '*.ts' '*.tsx' '*.js' '*.jsx' | xargs -r npx eslint`
  - Go: scope to project paths (`./cmd/<app>/...`, `./pkg/<app>/...`), not `./...`
  - Python: `git diff --name-only $base_sha -- '*.py' | xargs -r ruff check`
- Build and test commands may run project-wide (failures in changed code are real problems).
- If no files match the lint filter, skip lint and report success.
- Repo-wide network-dependent checks are advisory. If attempted and blocked by DNS/proxy/network policy, record them as skipped in `.ai/` output and continue based on scoped required checks.

File integrity and formatting policy:
- Guideline: Include a lightweight structural integrity check before broad tests so malformed files fail fast.
- Requirement: Verify prompts MUST include syntax/parse/compile sanity and formatter checks scoped to changed files or the module under test.
  - Rust example: `cargo fmt --all -- --check` then scoped `cargo check`
  - Go example: `gofmt` check on changed `.go` files then scoped `go build`
  - TypeScript example: formatter check on changed files then scoped typecheck/build
  - Python example: formatter/lint check on changed files then scoped import/test sanity
- On these failures, use stable classes like `failure_reason=format_invalid` or `failure_reason=parse_invalid` with concrete file/line details.

Workspace artifact hygiene:
- Guideline: Build/test artifacts are expected locally but should not be treated as feature output.
- Requirement: Verify prompts MUST fail when feature diffs include artifact/output paths unless explicitly required by spec.
- Check changed files vs `$base_sha` and block paths such as `target/`, `dist/`, `build/`, `.pytest_cache/`, `node_modules/`, coverage outputs, temp files, or backup files.
- Include both cargo target naming variants in artifact checks: `.cargo-target*` and `.cargo_target*`.
- Use `failure_reason=artifact_pollution` and list offending paths in `details`.

#### Mandatory status-file contract

Every **generated** codergen prompt (i.e., the final `.dot` output) MUST explicitly instruct the agent to write status JSON to `$KILROY_STAGE_STATUS_PATH` (absolute path).

Required wording (or equivalent) in generated prompts:
- "Write status JSON to `$KILROY_STAGE_STATUS_PATH` (absolute path)."
- "If that path is unavailable, use `$KILROY_STAGE_STATUS_FALLBACK_PATH`."
- "Do not write status.json inside nested module directories after `cd`."

The reference template uses abbreviated status instructions (just `$KILROY_STAGE_STATUS_PATH`) to keep prompts readable as structural scaffolding. The Phase 4 prompt templates supply the full contract when generating real pipelines.

#### Canonical failure-status contract (Guideline + Requirement)

Guideline:
- Keep failure payloads machine-stable and human-readable so retries/routing/diagnostics remain deterministic.

Requirement:
- For `outcome=fail` or `outcome=retry`, status JSON MUST include both:
  - `failure_reason` (short stable reason code)
  - `details` (human-readable explanation)
- For deterministic verify/check nodes, include:
  - `failure_class` (`deterministic` or `transient_infra`)
  - canonical `failure_reason` enums where applicable (for example `environmental_tooling_blocks`, `artifact_pollution`, `compile_failed`)
  - optional `failure_signature` when prose may vary across retries.
- Do not emit non-canonical fail payloads like `{"outcome":"fail","gaps":[...]}` without `failure_reason`.
- If structured diagnostics are needed, place them inside `details` (text or serialized JSON) while still including `failure_reason`.

Allowed examples:
- `{"outcome":"fail","failure_reason":"compile_failed","details":"cargo check failed in src/types.rs:42"}`
- `{"outcome":"retry","failure_reason":"transient_infra","details":"provider timeout while generating verify report"}`

Implementation prompt template:
```
Goal: $goal

Implement [DESCRIPTION].

Spec: [SPEC_PATH], section [SECTION_REF].
Read: [DEPENDENCY_FILES] for types/interfaces you need.

Create/modify:
- [FILE_LIST]

Build-first strategy:
- FIRST MILESTONE: Achieve a clean `[BUILD_COMMAND]` with stub/skeleton implementations before filling in logic.
- If you spend more than a third of your turns on build errors without reaching a clean compile, simplify your approach: comment out broken code, add stubs, get to green, then iterate.

Acceptance:
- `[BUILD_COMMAND]` must pass
- `[TEST_COMMAND]` must pass

Write status JSON to `$KILROY_STAGE_STATUS_PATH` (absolute path). If unavailable, use `$KILROY_STAGE_STATUS_FALLBACK_PATH`. Do not write status.json in nested module directories after `cd`.
Write status JSON: outcome=success if all criteria pass, outcome=fail with failure_reason and details otherwise.
```

Verification prompt template:
```
Verify [UNIT_DESCRIPTION] was implemented correctly.

Run:
1. `[BUILD_COMMAND]`
2. Lint ONLY files changed by this feature (do NOT lint the entire project):
   `git diff --name-only $base_sha -- [FILE_EXTENSIONS] | xargs -r [LINT_COMMAND]`
   If no files match, skip lint and note "no changed files to lint" in results.
3. Structural integrity + formatting checks scoped to changed files/module under test (language-appropriate; fail fast on parse/format issues).
4. `[TEST_COMMAND]`
5. [DOMAIN_SPECIFIC_CHECKS]
6. Artifact hygiene check: fail if diff vs `$base_sha` includes artifact/output/temp/backup paths unless explicitly required by spec.

IMPORTANT: Pre-existing lint errors in unrelated files must not block this feature.

Write results to .ai/verify_[NODE_ID].md.
Write status JSON to `$KILROY_STAGE_STATUS_PATH` (absolute path). If unavailable, use `$KILROY_STAGE_STATUS_FALLBACK_PATH`. Do not write status.json in nested module directories after `cd`.
Write status JSON: outcome=success if ALL pass, outcome=fail with failure_reason and details.
```

Use language-appropriate commands: `go build`/`go test` for Go, `cargo build`/`cargo test` for Rust, `npm run build`/`npm test` for TypeScript, `pytest`/`mypy` for Python, etc.

#### Steering/analysis prompt template (for multi-outcome nodes)

For nodes that route to different paths based on analysis (not just success/fail):

```
Goal: $goal

Analyze [SUBJECT].

Read: [INPUT_FILES]

Evaluate against these criteria:
- [CRITERION_1]: if true, use outcome=[VALUE_1]
- [CRITERION_2]: if true, use outcome=[VALUE_2]
- [CRITERION_3]: if true, use outcome=[VALUE_3]

Write your analysis to .ai/[ANALYSIS_FILE].md.
Write status JSON to `$KILROY_STAGE_STATUS_PATH` (absolute path). If unavailable, use `$KILROY_STAGE_STATUS_FALLBACK_PATH`. Do not write status.json in nested module directories after `cd`.
Write status JSON with the appropriate outcome value. If the chosen outcome is `fail` or `retry`, include both `failure_reason` and `details`.
```

#### Prompt complexity scaling

Simple impl/verify prompts (5-10 lines) are fine for straightforward tasks. But prompts for complex workflows should be substantially richer:

- **Simple tasks** (create a file, run a test): 5-10 line prompt
- **Moderate tasks** (implement a module per spec): 15-25 line prompt with spec references, file lists, acceptance criteria
- **Complex tasks** (multi-step with external tools, conditional logic): 30-60 line prompt with numbered steps, embedded commands, examples of expected output, and explicit conditional logic

The reference dotfiles in `docs/strongdm/dot specs/` demonstrate production-quality prompts with multi-paragraph instructions, embedded shell commands with examples, numbered steps, and conditional branches within a single prompt.

For implementation nodes with a build step, use the **progressive build pattern** to prevent late-stage failures:

```
Implementation approach:
1. Create all files with stub/skeleton implementations (verify: [BUILD_COMMAND] must pass)
2. Implement module A logic (verify: [BUILD_COMMAND] must pass)
3. Implement module B logic (verify: [BUILD_COMMAND] must pass)
4. Write tests (verify: [TEST_COMMAND] must pass)

Do NOT proceed to the next module until the current one compiles.
```

This catches type errors and interface mismatches incrementally rather than discovering them after 50 turns of work. It naturally pairs with the build-first strategy in the implementation prompt template.

### Phase 5: Model Selection

Use Phase 0B to decide concrete model IDs, providers, executor plan, parallelism, and thinking. Then:

- Assign `class` attributes based on Phase 2 complexity and node role: default, `hard`, `verify`, `branch-a`/`branch-b`/`branch-c` (for fan-out branches including reviews).
- Encode the chosen plan in the graph `model_stylesheet` so nodes inherit `llm_provider`, `llm_model`, and (optionally) `reasoning_effort`.

#### Escalation chain generation

When producing the Medium or High option, generate an `escalation_models` attribute for complex implementation nodes (class="hard"). The chain should:

1. Include all available models ordered by capability (least capable first, most capable last — typically correlates with cost)
2. Use highest reasoning/thinking settings (the escalation is for capability, not speed)
3. Skip models that are already the node's primary model (from the stylesheet)
4. Set `max_retries` on nodes with escalation chains to accommodate the full chain: `(len(chain) + 1) * (retries_before_escalation + 1) - 1`

Example for a Medium plan with kimi-k2.5 as the primary model:
```dot
impl_core [
    shape=box, class="hard", max_retries=8,
    escalation_models="zai:glm-4.7, google:gemini-pro, anthropic:claude-opus-4-6"
]
```

For the Low option, omit `escalation_models` (single model, minimal cost).

#### Provider-specific reasoning behavior

**Cerebras GLM 4.7 (`zai-glm-4.7`):**
- Reasoning is **always on** — there is no `reasoning_effort` control for this model (that parameter only applies to `gpt-oss-120b` on Cerebras). Setting `reasoning_effort` on a GLM 4.7 node is harmless but has no effect.
- The relevant knob is `clear_thinking` (Cerebras-specific). When `true` (the default), prior turns' reasoning is stripped from context. When `false`, prior reasoning is preserved across turns — essential for agent-loop mode where the model should build on its prior chain-of-thought.
- The engine automatically sets `clear_thinking: false` for Cerebras when running in agent-loop mode, so no manual configuration is needed in the dotfile or run config.
- If you need to override this default, pass it via `provider_options` on the request (the openaicompat adapter merges `provider_options[cerebras]` into the request body).

### Phase 6: Self-Validate and Auto-Repair the DOT (Iterate Until It Passes, Cap 10)

Before emitting the final output, you MUST validate the candidate DOT locally and repair any issues, iterating until it passes or you hit the attempt cap (10).

Run these validators (both when available):

1. Graphviz parser check (if `dot` is installed):
   - `dot -Tsvg <graph.dot> -o /dev/null`
2. Kilroy Attractor validator:
   - Prefer `./kilroy attractor validate --graph <graph.dot>` if `./kilroy` exists
   - Otherwise use `go run ./cmd/kilroy attractor validate --graph <graph.dot>`

Repair loop (max 10 attempts):

1. Draft the DOT in memory as `candidate_dot` (still follow all rules above).
2. For attempt 1..10:
   - Write `candidate_dot` to a temporary file (prefer `mktemp`; otherwise write under `.ai/`).
   - Run Graphviz check (if available). Capture the error output.
   - Run Kilroy validate. Capture the diagnostics.
   - If BOTH succeed, stop and emit exactly `candidate_dot` as your final response.
   - If either fails, apply the smallest possible edits to `candidate_dot` to address the reported errors, then retry.
3. If attempt 10 still fails:
   - Programmatic mode: output `DOT_VALIDATION_FAILED` with the last error messages and STOP (no digraph).
   - Interactive mode: explain what failed and include the last error messages (do not pretend it validates).

Common repairs (use validator output; do not guess blindly):

- Remove any non-DOT text outside the `digraph { ... }`.
- Fix quoting/escaping in string attributes (especially `model_stylesheet` and `prompt`).
- Ensure required graph attrs exist: `goal`, `model_stylesheet`, `default_max_retry`, `retry_target`, `fallback_retry_target`.
- Optional: `retries_before_escalation` (default: 2) — same-model retry count before escalating to the next model in a node's `escalation_models` chain. Applied globally; override with caution.
- Ensure exactly one `start` and one `exit`, with correct `shape` and reachability.
- Fix missing semicolons / commas / brackets in node/edge attribute lists.
- Replace edge `label="success"` style routing with proper `condition="outcome=..."`.
- For looped workflows, verify reference loop structure still exists: inner fail-retry loop, postmortem restart loop, and explicit outcome-based routing.

## Kilroy DSL Quick Reference

### Shapes (handler types)

| Shape | Handler | Use |
|-------|---------|-----|
| `Mdiamond` | start | Entry point. Exactly one. |
| `Msquare` | exit | Exit point. Exactly one. |
| `box` | codergen | LLM task (default for all nodes). |
| `diamond` | conditional | **Pure pass-through** routing point. Routes based on edge conditions against current context. **Never executes prompts** — any `prompt` attribute on a diamond node is ignored. If you need a node that executes an LLM prompt AND routes on the result, use `shape=box` instead. |
| `hexagon` | wait.human | Human approval gate (only for interactive runners). |
| `component` | parallel | Fan-out: executes outgoing branches concurrently. |
| `tripleoctagon` | parallel.fan_in | Fan-in: waits for branches, selects best result. |
| `parallelogram` | tool | Shell command execution (uses `tool_command` attribute). |

### Node attributes

| Attribute | Description |
|-----------|-------------|
| `label` | Display name (defaults to node ID) |
| `shape` | Handler type: `Mdiamond` (start), `Msquare` (exit), `box` (codergen), `diamond` (conditional), `hexagon` (wait.human), `component` (parallel fan-out), `tripleoctagon` (fan-in), `parallelogram` (tool/shell) |
| `type` | Explicit handler override (takes precedence over shape) |
| `prompt` | LLM instruction. Supports `$goal` and `$base_sha` expansion. Also accepted as `llm_prompt` (alias). |
| `prompt_file` | Path to an external file whose contents replace `prompt` at prepare time. Resolved relative to repo root; mutually exclusive with `prompt`. |
| `class` | Comma-separated classes for model stylesheet targeting (e.g., `"hard"`, `"verify"`, `"review"`) |
| `max_retries` | Additional attempts beyond initial execution. `max_retries=3` = 4 total. |
| `goal_gate` | `true` = node must succeed before pipeline can exit |
| `retry_target` | Node ID to jump to if this goal_gate fails |
| `fallback_retry_target` | Secondary retry target |
| `allow_partial` | `true` = accept PARTIAL_SUCCESS when retries exhausted instead of FAIL. Use on long-running nodes where partial progress is valuable. |
| `max_agent_turns` | Optional turn budget: caps LLM request-response cycles. Omitted or `0` = unlimited. This is a secondary optimization — use `timeout` as the primary guard and add turn limits only once you have production data. See "Turn budget" section. |
| `timeout` | Duration (e.g., `"300"`, `"900s"`, `"15m"`). Applies to any node type. Bare integers are seconds. |
| `auto_status` | `true` = auto-generate SUCCESS outcome if handler writes no status.json. Only use on `expand_spec`. |
| `llm_model` | Override model for this node (overrides stylesheet) |
| `llm_provider` | Override provider for this node |
| `reasoning_effort` | `low`, `medium`, `high` |
| `escalation_models` | Comma-separated `provider:model` pairs for capability escalation. When the node fails with `budget_exhausted` or `compilation_loop`, the engine cycles through these models after `retries_before_escalation` same-model retries. Example: `"kimi:kimi-k2.5, anthropic:claude-opus-4-6"` |
| `fidelity` | Context fidelity: `full`, `truncate`, `compact`, `summary:low`, `summary:medium`, `summary:high` |
| `thread_id` | Thread key for LLM session reuse under `full` fidelity |

### Edge attributes

| Attribute | Description |
|-----------|-------------|
| `label` | Display caption and preferred-label routing key |
| `condition` | Boolean guard: `outcome=success`, `outcome=fail`, `outcome=skip`, etc. AND-only (`&&`). |
| `weight` | Numeric priority for edge selection (higher wins among equally eligible edges) |
| `fidelity` | Override fidelity mode for the target node |
| `thread_id` | Override thread key for session reuse at target node |
| `loop_restart` | When `true`, terminates the current run and re-launches with a fresh log directory starting at the edge's target node. Use on edges that loop back to much-earlier nodes where accumulated context/logs would be stale. |

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

1. **No verification after implementation (in build pipelines).** Every impl node that produces code MUST have a verify node after it. Never chain impl → impl → impl. Exception: analytical/triage nodes in non-build workflows may use the 2-node pattern (see "Relaxed node patterns" above).
2. **Labels instead of conditions.** `[label="success"]` does NOT route. Use `[condition="outcome=success"]`.
3. **All failures → exit.** Failure edges must loop back to the implementation node for retry, not to exit.
4. **Multiple exit nodes.** Exactly one `shape=Msquare` node. Route failures through conditionals, not separate exits.
5. **Prompts without outcome instructions.** Every prompt must tell the agent what to write in status.json.
6. **Inlining the spec.** Reference the spec file by path. Don't copy it into prompt attributes. Exception: `expand_spec` node bootstraps the spec.
7. **Missing graph attributes.** Always set `goal`, `model_stylesheet`, `default_max_retry`.
8. **Wrong shapes.** Start must be `Mdiamond`. Exit must be `Msquare`. The validator also accepts nodes with id `start`/`exit` regardless of shape, but always use the canonical shapes.
9. **Unnecessary timeouts.** Do NOT add timeouts to simple impl/verify nodes in linear pipelines — a single CLI run can legitimately take hours. DO add timeouts to nodes in looping pipelines (to prevent infinite hangs) or nodes calling external services. Prefer the reference/template timeout ranges over ad-hoc new values.
10. **Build files after implementation.** Project setup (module file, directory structure) must be the FIRST implementation node.
11. **Catastrophic review rollback.** In inner repair loops, review/check failure must target a LATE node (integration, CLI, or the last major impl) — never route directly from a check node back to `impl_setup`. For broad failures, use the outer hill-climbing loop (review consensus → postmortem → re-plan) which preserves context via postmortem artifacts and guides the next iteration without discarding work.
12. **Missing verify class.** Every verify node MUST have `class="verify"` so the model stylesheet applies your intended verify model and thinking.
13. **Missing expand_spec for vague input.** If no spec file exists in the repo, the pipeline MUST include an `expand_spec` node. Without it, `impl_setup` references `.ai/spec.md` that doesn't exist in the fresh worktree.
14. **Hardcoding language commands.** Use the correct build/test/lint commands for the project's language. Don't write `go build` for a Python project.
15. **Missing file-based handoff.** Every node that produces output for downstream nodes must write it to a named `.ai/` file. Every node that consumes prior output must be told which files to read. Relying on context variables for large data (plans, reviews, logs) does not work — use the filesystem.
16. **Binary-only outcomes in steering nodes.** If a workflow has more than two paths (e.g., process/skip/done), define custom outcome values in the prompt and route on them with conditions. Don't force everything into success/fail.
17. **Unscoped Go monorepo checks.** Do NOT make repo-wide `go build ./...`, `go vet ./...`, or `go test ./...` required by default. Scope required checks to generated project paths (e.g., `./cmd/<app>`, `./pkg/<app>/...`). Treat blocked repo-wide network checks as advisory/skipped.
18. **Unscoped lint in verify nodes.** Do NOT use `npm run lint`, `ruff check .`, or any project-wide lint command in verify nodes. Scope lint to changed files using `git diff --name-only $base_sha`. Pre-existing errors in unrelated files cause infinite retry loops where the agent burns tokens trying to fix code it didn't write.
19. **Overly aggressive API preflight timeouts in run config.** When producing or updating a companion run config for real-provider runs, set `preflight.prompt_probes.timeout_ms: 60000` (60s) as the baseline to reduce startup failures from transient provider latency spikes.
20. **Missing toolchain readiness gates for non-default build dependencies.** If the deliverable needs tools that are often absent (for example `wasm-pack`, Playwright browsers, mobile SDKs), add an early `shape=parallelogram` tool node that checks prerequisites and blocks the pipeline before expensive LLM stages.
21. **Auto-install bootstrap without explicit user opt-in (interactive mode).** When tools are missing, do not silently add installer commands to run config. Ask the user first, then apply their choice.
22. **Toolchain checks with no bootstrap path (when auto-install is intended).** If the run is expected to self-prepare the environment, companion run config must include idempotent `setup.commands` install/bootstrap steps. A check-only gate without setup bootstrap causes immediate hard failure.
23. **Unguarded failure restarts in inner retry loops.** In inner repair loops (check → impl retry), do NOT set `loop_restart=true` on `outcome=fail` edges — these retries should accumulate context, not restart fresh. If an inner retry edge truly needs restart (transient infrastructure failure), guard it with `context.failure_class=transient_infra` and add a companion non-restart edge for deterministic failures. The outer hill-climbing loop (`postmortem -> plan_*`) correctly uses `loop_restart=true` because accumulated logs from the prior iteration are stale and a fresh log directory is needed.
24. **Local CXDB configs without launcher autostart.** In this repo, do not emit companion `run.yaml` that points at local CXDB endpoints but omits `cxdb.autostart` launcher wiring. That creates fragile manual setup and can silently attach to the wrong daemon.
25. **Non-canonical fail payloads.** Do NOT emit `outcome=fail` or `outcome=retry` without both `failure_reason` and `details`.
26. **Artifact pollution in feature diffs.** Do NOT allow build/cache/temp/backup artifacts in changed-file diffs unless explicitly required by the spec.
27. **Overlapping fan-out write scopes.** Do NOT let parallel branches modify shared/core files; reserve shared edits for a dedicated post-fan-in integration node. See also the interface-pinning pattern below.
28. **Using `outcome=pass` as goal-gate terminal success.** For `goal_gate=true` nodes, do NOT route to `shape=Msquare` on `condition="outcome=pass"` (or other custom success aliases). Use `condition="outcome=success"` or `condition="outcome=partial_success"` so goal-gate contract checks and exit routing stay aligned.

#### Fan-out coordination: interface-pinning pattern

When a fan-out involves branches that implement against shared types or interfaces, add a `define_contracts` or `impl_scaffold` node BEFORE the fan-out `component` node. This node writes comprehensive shared type definitions, constants, function signatures, and interface contracts to well-known paths. Fan-out branch prompts reference these files as **read-only inputs**.

Why: Parallel branches run in isolated worktrees with no communication. If each branch independently defines shared types, they will diverge. The fan-in selects one winner and discards losers' changes, causing type mismatches at integration.

Pattern:
```
impl_scaffold -> verify_scaffold -> check_scaffold -> fanout -> [branches] -> fanin
```

The scaffold prompt should:
1. Define ALL shared types, constants, and enums comprehensively — anticipate what branches will need
2. Create stub modules with correct function signatures for every parallel branch to implement against
3. Verify the project compiles with stubs before proceeding to fan-out

Each fan-out branch prompt should include:
- "Read [SHARED_TYPE_FILES] — these are your interface contract. Do NOT modify these shared files."
- "Implement ONLY your assigned module files."
28. **`reasoning_effort` on Cerebras GLM 4.7.** Do NOT set `reasoning_effort` on GLM 4.7 nodes expecting it to control reasoning depth — that parameter only works on Cerebras `gpt-oss-120b`. GLM 4.7 reasoning is always on. The engine automatically sets `clear_thinking: false` for Cerebras agent-loop nodes to preserve reasoning context across turns.
29. **Parallel code-writing in a shared worktree (default disallowed).** Do NOT run multiple programming/implementation nodes in parallel that touch the same codebase state. If implementation fan-out is required, enforce strict isolation (disjoint write scopes, shared files read-only) and converge via an explicit integration/merge node.
30. **Generating DOTs from scratch when a validated template exists.** For production-quality runs, start from `skills/english-to-dotfile/reference_template.dot` and make minimal, validated edits. New topologies are allowed, but they are higher-risk and must be validated early/cheap to avoid expensive runaway loops.
31. **Ad-hoc turn budgets.** Do not add `max_agent_turns` by default. The reference template omits turn limits intentionally. Add them only with clear production evidence that a node runs away or finishes well under budget.
32. **Putting prompts on diamond (conditional) nodes.** Diamond nodes are pure pass-through routers handled by `ConditionalHandler` — they forward the previous node's outcome to edge conditions and never execute prompts. A `prompt` attribute on a diamond node is dead code. If the node needs to run an LLM prompt and write its own outcome, use `shape=box`. The Kilroy validator warns on this (`prompt_on_conditional_node`).
33. **Defaulting to visit-count loop breakers.** Do not add visit-count controls (for example `max_node_visits`) as the default loop guardrail profile.

## Notes on Reference Dotfile Conventions

Some reference dotfiles in `docs/strongdm/dot specs/` use attributes not defined in the Attractor spec. These are harmless (stored but ignored by the engine) and should NOT be emitted by this skill:

- `node_type` (e.g., `stack.steer`, `stack.observe`) — handler type is determined by `shape` or explicit `type` attribute, not by `node_type`
- `is_codergen` — codergen handler is determined by shape, not by this flag
- `context_fidelity_default` — use `default_fidelity` (the spec-canonical name; the engine accepts both)
- `context_thread_default` — use graph-level `thread_id` (the engine accepts both)
