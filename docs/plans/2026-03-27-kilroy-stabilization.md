# Kilroy Stabilization Plan

Date: 2026-03-27

## Problem Statement

Kilroy is an attractor-pattern graph runner that orchestrates multi-stage LLM workflows.
It works, but it's fragile, hard to set up, hard to debug, and fails in ways that are
difficult to diagnose. The root causes are architectural:

1. **Three concerns in one layer.** Graph execution, LLM orchestration, and software-development
   workflow opinions (git worktrees, artifact policies, per-node commits) are inseparable. You
   can't run a simple graph without buying into the full stack.

2. **Heavy where it should be light; light where it should be heavy.** Configuration requires
   specifying everything (CXDB, provider backends, model catalogs, artifact policies) with no
   sensible defaults. But validation is shallow — credentials are checked for presence not
   validity, and the system fails 2 hours into a run for things it could catch in 10 seconds.

3. **Invisible decisions.** Edge routing uses 4+ interacting mechanisms (conditions,
   preferred_label, suggested_next_ids, retry_target chains). Provider selection, fidelity
   resolution, failure classification, and implicit fan-out detection all happen silently.
   When a run takes an unexpected path, there's no way to understand why without reading code.

4. **File-based state assembly.** Run state is spread across progress.ndjson, checkpoint.json,
   final.json, status.json per node, and CXDB events. Piecing together what happened requires
   reading multiple files and mentally replaying the run. No queryable operational state exists.

5. **Graphs encode too much.** Prompts, failure policies, verification scripts, status contracts,
   model routing, and fidelity modes all live in DOT files. Graphs are hard to write, hard to
   read, and easy to get subtly wrong.

## Design Principles

These guide every task in this plan:

- **Fail fast.** Validate everything checkable before execution starts. 10 seconds, not 2 hours.
- **Explicit over implicit.** Every routing decision, provider selection, and state transition
  should be visible in the graph or in decision logs. No silent fallbacks.
- **Layered architecture.** The graph runner is the core. Git, CXDB, artifact policies, and
  software-development workflow patterns are composable layers on top, not baked in.
- **Zero-config start.** `kilroy run --graph pipeline.dot` should work with no config file.
  Everything else is opt-in.
- **Observable decisions.** The system should answer "what happened and why" from a single
  query, not a filesystem crawl.

## Phasing

Each phase delivers standalone value and builds on the previous one. Phase 0 can start today.
Script-based test graphs (no LLM involvement) validate each change to isolate engine behavior
from model variability.

---

## Phase 0: Immediate Stabilization

Goal: Make the current system more predictable without structural changes. Each task is
independently shippable. Use script-based test graphs to validate — nodes run shell commands
that produce deterministic output, so we're testing the engine plumbing, not LLM behavior.

### 0.1 Create Script-Based Test Graphs

**What:** Create a small suite of DOT graphs where every node runs a shell script instead of
an LLM. These become the validation harness for all subsequent Phase 0 work and beyond.

**Context:** Currently there's no way to test the engine's traversal, routing, and checkpoint
behavior without involving LLMs. We need deterministic graphs that exercise the engine's core
codepaths: linear traversal, conditional routing, retry loops, failure handling.

**Done when:**
- A `testdata/` directory contains at least 4 graphs:
  - `linear.dot`: start → A → B → C → done (basic traversal)
  - `linear_verify.dot`: start → implement → verify → done, with verify→implement on fail
    (hill-climber pattern with a verify script that succeeds on Nth attempt)
  - `conditional.dot`: start → process → (path_a | path_b based on outcome) → done
  - `fail_fast.dot`: start → node_that_fails → done (tests failure handling)
- Each graph uses `type="command"` or `shape=parallelogram` nodes with shell scripts
- Shell scripts are in `testdata/scripts/`, produce deterministic outcomes via status.json
- All graphs pass `kilroy attractor validate`
- All graphs execute successfully with `kilroy attractor run`

### 0.2 Make CXDB Truly Optional

**What:** Remove the assertion that CXDB configuration is required. Users should be able to
run graphs without CXDB configured, running, or even installed.

**Context:** `config.go:313` asserts CXDB fields are required ("required in v1"). The engine
already nil-checks CXDB access everywhere. The `--no-cxdb` CLI flag exists but config
validation blocks before it's consulted when no CXDB config is provided. This means even a
user who doesn't want CXDB has to provide dummy CXDB config values.

**Done when:**
- A run config with no `cxdb` section is accepted without error
- `kilroy attractor run --graph testdata/linear.dot` works with a minimal config (repo path only)
- Running without CXDB produces no CXDB-related errors or warnings
- Running WITH CXDB still works as before
- Existing tests pass (`go test ./...`)

### 0.3 Strict Outcome Validation

**What:** Reject unrecognized status values from node outcomes instead of silently passing
them through. Typos in status names currently cause silent routing failures.

**Context:** `runtime/status.go` ParseStageStatus accepts any non-empty string as a valid
status. If an LLM writes `"outcome": "proccess"` (typo), it's treated as a custom outcome.
No edge condition matches it, and routing silently falls through to default behavior. The
canonical values are: success, partial_success, retry, fail, skipped.

**Done when:**
- Unknown status values produce a clear error with the invalid value and the list of valid values
- The canonical status set is documented in one place in code
- The test graph suite from 0.1 exercises both valid and invalid outcomes
- Existing graphs that intentionally use custom outcomes (if any in the test suite) are migrated
  to use `preferred_label` for conditional routing instead
- Existing tests pass

### 0.4 Decision Logging

**What:** Add structured events to the progress stream for every routing decision the engine
makes. When a run takes an unexpected path, the decision log should explain exactly why.

**Context:** `next_hop.go` evaluates edge conditions, matches preferred labels, applies weight
tiebreaking, and falls through retry_target chains — but most of these decisions are invisible.
`progress.go` captures stage start/end events but not the reasoning behind edge selection.
Provider selection and retry decisions are also unlogged.

**Done when:**
- Edge condition evaluation logs: which edges were evaluated, what their conditions were,
  whether each condition matched, and which edge was selected
- Provider/model selection logs: which provider and model were chosen for each node
- Retry decision logs: when a retry occurs, the attempt number, backoff delay, and why
- All new events appear in progress.ndjson with structured fields
- The test graphs from 0.1 produce readable decision trails that explain their routing
- Running a conditional graph and reading progress.ndjson clearly shows why each path was taken

### 0.5 Real Credential Validation

**What:** Actually test provider credentials during preflight by making lightweight API calls,
rather than just checking if environment variables are set.

**Context:** `provider_preflight.go` has prompt-probe infrastructure that already makes test
API calls with retry logic. But it's disabled by default and runs in warn-only mode. The
common failure mode: a run starts, executes for hours, then fails at a node that needs a
provider whose API key is missing, invalid, or expired.

**Done when:**
- Preflight makes a real API call to every provider/model combination declared in the graph
- Invalid or missing credentials fail preflight with a clear message naming the provider,
  model, and what's wrong
- Preflight reports ALL credential issues at once, not one at a time
- A graph referencing a provider with a bad key fails in preflight, not at execution time
- Preflight can be skipped with an explicit flag for offline/CI use
- The fail_fast test graph from 0.1 validates this behavior

### 0.6 Dry-Run Mode

**What:** Add `--dry-run` that validates everything — graph structure, provider credentials,
model availability — and reports results without executing any nodes.

**Context:** The current `--preflight` flag runs validation but doesn't do the full bootstrap
(repo checks, artifact policy resolution, effective provider routing). A dry-run should go
further: confirm that the run WOULD succeed by doing everything except actually executing
handlers.

**Done when:**
- `kilroy attractor run --graph test.dot --dry-run` validates graph, resolves all providers,
  tests all credentials, and reports a summary of what would happen
- No git branches are created, no worktrees made, no nodes executed
- Output shows: graph structure summary, provider assignments per node, credential status,
  any warnings or errors
- Exit code reflects whether the run would succeed (0) or fail (non-zero)
- Works with the test graph suite from 0.1

### 0.7 Zero-Config Startup

**What:** Make `kilroy attractor run --graph pipeline.dot` work with no config file at all.
Auto-detect providers from environment, use sensible defaults for everything else.

**Context:** Currently you must write a YAML config file specifying CXDB, provider backends,
model catalogs, artifact policies, and more. A new user has to understand the entire system
architecture to even start. Most of this has one reasonable default.

**Done when:**
- Running with only `--graph` and no `--config` succeeds when provider API keys are in the
  environment (auto-detect: ANTHROPIC_API_KEY → anthropic provider, OPENAI_API_KEY → openai, etc.)
- No CXDB required (builds on 0.2)
- Default model selected per provider (the provider's current best general-purpose model)
- Default backend selected based on what's available (API key present → api; CLI binary found → cli)
- Repo path defaults to current directory if it's a git repo, or a temp workspace if not
- Config file remains available for overriding any default
- The test graph suite runs with zero config

### 0.9 First-Run Friction Fixes

**What:** A batch of quick fixes for sharp edges discovered during the first real PR review
workflow run. These are all bugs, wrong defaults, or missing error context — not new features.

**Context:** First attempt at running a real PR review graph exposed several issues that all
stem from "the tool assumes you already know how it works." Each fix is small but together
they dramatically improve the first-run experience.

**Done when:**

1. **Auto-detection fills gaps in partial configs** — if a config file is present but doesn't
   declare providers, auto-detection should fill the gaps. Config file existence should not
   disable auto-detection entirely. This is a fix to 0.7.

2. **`require_clean` defaults to false** — the worktree isolates the run from the parent
   repo state. Requiring a clean working directory is pointless friction. Default to false,
   opt-in to true for strict setups.

3. **KILROY_RUN_ID injected into tool nodes** — tool_command nodes don't receive the same
   environment variables as agent nodes. This breaks the `.ai/runs/$KILROY_RUN_ID/` data
   passing convention. Every node type must get the same core env vars.

4. **CLI headless warning non-interactive** — the `claude` CLI's interactive Y/n prompt
   about account suspension kills headless runs. Fix with one-time acknowledgment stored
   in config, a `--accept-cli-risk` flag, or by using `yes |` in the invocation template.

5. **Error messages suggest fixes** — "missing provider backend" should suggest "remove
   --config to use auto-detection, or add llm.providers.X.backend". "File not found in
   worktree" should suggest committing the file. Validation errors should name alternatives.

6. **Worktree file-not-found context** — when a `tool_command` references a path that doesn't
   exist in the worktree, the error should explain that worktrees only contain committed files,
   and suggest `git add && git commit`.

---

## Phase 0.8: CLI-Only Agent Backend (Deprecate API)

Goal: Simplify the agent backend to CLI-only. Use the appropriate CLI tool for each provider
instead of maintaining both API and CLI execution paths.

### Design Context

Team feedback: use `opencode` for all providers that don't have a dedicated CLI agent.
The provider mapping becomes:

- Claude → `claude` CLI (with `--dangerously-skip-permissions`)
- OpenAI → `codex` CLI (with `--sandbox workspace-write`)
- Google → `gemini` CLI (with `--yolo`)
- Everything else → `opencode` CLI (multi-provider, open-source)

This eliminates the entire API backend: the `internal/agent/` package (session management,
tool registry, profiles, output truncation), the `internal/llm/` package (provider adapters,
unified client), and the complexity of maintaining two fundamentally different execution
models. The codergen handler becomes: "invoke a CLI binary with a prompt, capture output."

This also resolves the CLI-vs-API convergence gap — CLI converges better for structural
reasons (purpose-built agents with native tools), so going all-CLI means convergence
improves everywhere. It sidesteps Anthropic ToS concerns since we're using official/community
CLI tooling as intended.

### 0.8.1 Add opencode as a Provider Backend

**What:** Add opencode CLI support alongside the existing claude/codex/gemini CLI backends.
opencode becomes the default backend for any provider that doesn't have a dedicated CLI.

**Context:** `internal/providerspec/builtin.go` defines CLI invocation templates per provider.
Adding opencode follows the same pattern. opencode supports multiple LLM providers through
its own configuration.

**Done when:**
- opencode is a recognized provider backend in the provider spec system
- Graphs can specify `llm_provider=opencode` or have it selected as default
- The invocation template handles opencode's flags for headless/non-interactive mode
- A test graph using opencode validates the integration
- opencode availability is checked in preflight

### 0.8.2 Deprecate API Backend

**What:** Mark the API backend as deprecated. New graphs should use CLI backends only.
The API codepath remains functional but is no longer the recommended path.

**Context:** The API backend lives in `internal/agent/` (session, tool registry, profiles)
and `internal/llm/` (provider adapters). Deprecation means: warning when API backend is
selected, documentation updated, no new features added to the API path.

**Done when:**
- Using `backend: api` in config produces a deprecation warning
- Documentation recommends CLI backends for all providers
- The warning names the recommended CLI alternative for the provider
- Existing API-backend graphs continue to work (no breaking change)

---

## Phase 1: Run Database

Goal: Introduce SQLite as the single source of truth for run-level operational state. Replace
the file-based state assembly (progress.ndjson, checkpoint.json, final.json, per-node
status.json) with queryable structured data.

### Design Context

CXDB is a conversation-history store optimized for turn-level LLM interaction data. It's good
at recording what happened inside a node's LLM session. It is not designed for run-level
operational state — it can't query across runs, filter by outcome, or aggregate decisions.

SQLite fills the operational gap: which nodes ran, what they returned, which edges were taken,
why routing decisions were made. CXDB remains the opt-in deep-dive tool for inspecting LLM
conversations within a node.

The database lives at the XDG state path (the codebase already uses `XDG_STATE_HOME` for
state). Default: `~/.local/state/kilroy/runs.db`. Shared across runs with isolation by run_id.

**Schema management:** Numbered migration files applied automatically on DB open. Migrations
are append-only (add columns and tables, never remove or rename). Each migration is a single
SQL file checked into the repo. The migration runner and the schema contract must be clearly
documented in AGENTS.md or equivalent so that future work (human or agent) follows the pattern.

### 1.1 Schema Design and Migration Infrastructure

**What:** Design the initial schema and build the migration runner. The schema must support
the queries that matter: "what's the status of run X?", "why did node Y take edge Z?",
"which runs failed this week?", "what provider was used for node W?"

**Context:** Today this information is scattered across files that must be parsed and
cross-referenced. The schema should make these queries trivial.

**Done when:**
- A migration runner exists that auto-applies numbered SQL files on DB open
- The initial schema is documented with purpose annotations per table and column
- The schema supports: runs, nodes (with attempts), outcomes, edge decisions, provider selections
- A Go package provides typed read/write access to the schema
- Migration tests verify forward migration from empty DB
- AGENTS.md or equivalent documents the migration pattern and rules

### 1.2 Engine Writes to Run DB

**What:** Wire the engine to write operational events to the run DB alongside (not instead of)
existing file-based output. This is additive — nothing breaks.

**Context:** The engine currently writes progress events via `appendProgress()` in engine.go.
The DB writes should happen at the same points, with the same information, but into structured
tables instead of ndjson lines.

**Done when:**
- Every run creates a row in the runs table
- Every node execution creates rows in nodes and outcomes tables
- Every edge decision creates a row in decisions table
- Existing file-based output (progress.ndjson, checkpoint.json, final.json) still works
- Test graphs from Phase 0 produce correct DB entries
- DB contents match what progress.ndjson reports (cross-validation)

### 1.3 Status Command Reads from Run DB

**What:** Update `kilroy attractor status` to query the run DB as its primary data source,
with filesystem fallback for older runs.

**Context:** The status command currently parses progress.ndjson, checkpoint.json, and final.json
to assemble run state. This is fragile and slow for large runs. The DB makes these queries
instant and reliable.

**Done when:**
- `kilroy attractor status --run <id>` shows run state from the DB
- `kilroy attractor status --list` shows all known runs with their status
- `kilroy attractor status --run <id> --decisions` shows the edge decision log
- Falls back to file-based parsing if run predates the DB
- Works with test graphs from Phase 0

---

## Phase 2: Lifecycle Hooks

Goal: Decouple git, CXDB, artifact policies, and other concerns from the engine core. Replace
hardcoded behavior with a hook system where the engine fires lifecycle events and registered
hooks respond.

### Design Context

Currently the engine directly calls gitutil functions, CXDB sinks, and artifact policy logic.
This means you can't run a graph without git, and every engine change risks breaking these
subsystems. Lifecycle hooks make these composable: a minimal setup has zero hooks (engine just
runs), a full coding-workflow setup registers hooks for git, CXDB, filesystem snapshots, etc.

Hook points: `run.before`, `run.after`, `node.before`, `node.after`, `node.on_fail`,
`node.on_retry`, `run.on_blocked`. These are the natural lifecycle boundaries where
cross-cutting concerns attach.

### 2.1 Define Hook Interface and Registration

**What:** Design and implement the hook interface, registration mechanism, and the lifecycle
event points in the engine where hooks fire.

**Context:** The engine currently has these concerns hardcoded in its main loop: git branch
creation (run.before), git worktree setup (run.before), CXDB context creation (run.before),
per-node git commits (node.after), CXDB event emission (node.after), checkpoint writes
(node.after), stall detection (node.during).

**Done when:**
- A hook interface exists with methods for each lifecycle point
- Hooks can be registered on the engine before a run starts
- The engine calls registered hooks at the correct lifecycle points
- A no-op hook can be registered and fires correctly (verified by test)
- Multiple hooks can be registered and fire in order
- Hook errors are logged but don't crash the engine (configurable: warn vs fail)

### 2.2 Extract Git Operations into Hooks

**What:** Move git branch creation, worktree management, per-node commits, and SHA tracking
out of the engine core and into a git lifecycle hook. The engine doesn't know about git. The
git hook provides worktree isolation, per-node checkpointing, and branch management as an
opt-in capability.

**Context:** Team feedback: "Don't make kilroy understand worktrees." The engine should work
in whatever directory it's given. Git operations (worktree creation, per-node commits, SHA
tracking) are valuable but should be composable — register the git hook and you get them,
don't register it and the engine still works fine in a plain directory.

The recommended workflow becomes: the caller creates a worktree (or the git hook does it on
`run.before`), kilroy executes inside it, the git hook commits per node and tracks state.
But none of that is required — kilroy can run in a temp directory with no git at all.

Git operations are currently spread across engine.go (~15 callsites),
parallel_handlers.go (branch isolation), and resume.go (SHA validation).

**Done when:**
- The engine core has zero direct gitutil imports
- A GitHook implements the hook interface and provides all current git behavior
- Runs WITH the GitHook behave identically to current behavior (worktrees, commits, SHAs)
- Runs WITHOUT the GitHook succeed in any directory (no git required)
- Parallel branch isolation works both ways: git hook uses worktrees, no-git uses temp dirs
- The test graph suite from Phase 0 works both with and without the GitHook

### 2.3 Extract CXDB into Hooks

**What:** Move CXDB context creation, event emission, and turn appending out of the engine
core and into a CXDB lifecycle hook.

**Context:** CXDB integration is already mostly nil-safe, but the plumbing (sink creation,
context ID management, turn appending) is still wired directly into the engine. Extracting it
into a hook makes the nil-checking unnecessary — if no CXDB hook is registered, no CXDB code
runs.

**Done when:**
- The engine core has zero direct CXDB imports
- A CXDBHook implements the hook interface
- Runs with the CXDBHook behave identically to current CXDB behavior
- Runs without the CXDBHook have zero CXDB overhead
- Existing CXDB integration tests pass

### 2.4 Make Checkpoint/Resume Optional via Hooks

**What:** Checkpoint and resume become hook-provided capabilities, not engine-mandated
behavior. Some workflows (exploration, report generation, multi-repo searches) don't benefit
from checkpointing. Making it optional removes overhead and complexity for those cases.

**Context:** Currently the engine always writes checkpoint.json after each node and always
supports resume. For workflows that don't modify a repo or produce incremental artifacts,
this is pure overhead. With the hook system, checkpoint becomes a registered hook that can
be omitted.

**Done when:**
- Checkpoint writes happen in a hook, not in the engine core
- Runs without a checkpoint hook complete successfully (no checkpoint files written)
- Runs with the checkpoint hook behave identically to current behavior
- Resume requires the checkpoint hook to have been active during the original run
- A clear error message if resume is attempted on a run that had no checkpointing

### 2.5 Filesystem Change Tracking Hook

**What:** Provide a hook that captures what filesystem changes each node made during execution.
This is useful for understanding what an agent did without reading its full conversation log.

**Context:** Currently there's no structured record of what files a node created, modified,
or deleted. The git hook captures this implicitly (via commits), but without git there's no
visibility. A dedicated hook can snapshot before/after and produce a diff.

**Done when:**
- A filesystem tracking hook can be registered that records file changes per node
- Changes are recorded in the run DB (or a structured output) as: path, operation (create/modify/delete), size
- The hook works independently of git (doesn't require worktrees or commits)
- Change records are visible via `kilroy attractor status --run <id> --node <node_id>`

### 2.6 Extract Artifact Policy into Hooks

**What:** Move artifact policy resolution, checkpoint exclusion globs, and managed root
configuration out of the engine core and into a hook.

**Context:** Artifact policies are resolved once per run and applied as configuration to node
execution. Currently this is tightly coupled to the engine's setup phase and config parsing.

**Done when:**
- Artifact policy is applied via a hook, not hardcoded in the engine
- Runs without the hook use no artifact policy (simple default behavior)
- Runs with the hook behave identically to current behavior
- Configuration for artifact profiles still works through the run config

---

## Phase 2.5: Run Supervisor

Goal: Introduce a supervisor layer that sits above the graph runner and manages run lifecycle,
health monitoring, blocker detection, and user notification. This is the "babysitter" that
watches runs and knows when to surface issues vs when to stay quiet.

### Design Context

Currently the engine tries to be both executor and supervisor. The stall watchdog, loop_restart
circuit breakers, and failure classification heuristics are all supervisor concerns jammed into
the traversal loop. This makes the engine complex and the supervision logic hard to tune.

The supervisor sits outside the engine. It monitors runs by querying the run DB (Phase 1) and
intervenes through lifecycle hooks (Phase 2). It can manage multiple concurrent runs. Its
policies are configurable: "if stuck for 10 minutes, try a different model; if stuck for 30
minutes, notify me; if the same node fails 3 times with the same error, stop and ask."

This is also the layer that fulfills the "quickly determine if user input is needed" goal.
The supervisor watches early run progress and surfaces blockers (missing auth, missing data,
infrastructure failures) to the user as fast as possible, then stays quiet while the run
proceeds unattended.

### 2.5.1 Supervisor Core and Health Monitoring

**What:** Build the supervisor as a wrapper around the engine that monitors run health by
polling the run DB and applying configurable policies.

**Context:** The engine's current stall watchdog (hardcoded thresholds, embedded in the main
loop) is a primitive version of this. The supervisor replaces it with a clean separation:
the engine runs, the supervisor observes and reacts.

**Done when:**
- A supervisor can start a run and monitor its progress via the run DB
- Configurable health policies: stall detection, repeated failure detection, progress thresholds
- The supervisor can detect and report: stalled nodes, repeated failures on the same node,
  runs that haven't made progress in N minutes
- Health status is queryable: `kilroy attractor status --run <id>` shows supervisor assessment
- The supervisor works with the script-based test graphs (e.g., a graph with a deliberately
  slow node triggers stall detection)

### 2.5.2 Blocker Detection and User Notification

**What:** The supervisor identifies situations that require user attention and surfaces them
clearly, distinguishing "stuck and retrying" from "truly blocked."

**Context:** The current system doesn't distinguish between transient failures (worth retrying)
and blockers (need human input). A rate-limited API call looks the same to the user as a
missing database. The supervisor should classify blockers and notify the user with actionable
context.

**Done when:**
- The supervisor classifies run states: healthy, degraded (retrying), blocked (needs user),
  failed (terminal)
- Blocked state includes: what's blocking, which node, what the user can do about it
- A notification mechanism exists (at minimum: CLI output, structured event in DB; extensible
  to webhooks, etc.)
- The "quickly determine if user input needed" pattern works: supervisor watches early nodes
  and surfaces prerequisite failures (auth, missing resources) within the first few minutes
- Test graphs exercise the blocked detection (e.g., a node that requires a file that doesn't exist)

### 2.5.3 Self-Introspection

**What:** The system should be able to explain its own state, decisions, and plan in
human-readable terms. Not just "what happened" but "what is the system's model of what's
happening and why."

**Context:** Team feedback: "needs to introspect itself." This means the supervisor can
answer questions like:
- "Why are you stuck?" → "Node X has failed 3 times with the same build error. The failure
  is deterministic — retrying won't help."
- "What's your plan?" → "I'm on node 4 of 7. Next is verify, then review."
- "What have you tried?" → "Attempt 1: error A. Attempt 2: same. Attempt 3: different error B."

This is built on the run DB (Phase 1) for data and the supervisor for interpretation.

**Done when:**
- `kilroy attractor status --run <id> --explain` produces a human-readable narrative of
  the run's current state, recent decisions, and what's expected next
- The explanation includes: current node, progress through graph, recent failures with
  reasons, what the system plans to do next
- For stuck/failed runs: the explanation diagnoses why (deterministic failure, missing
  resource, repeated error) and suggests what the user can do
- The explanation is generated from run DB data, not from parsing log files

### 2.5.4 Supervisor Intervention Policies

**What:** Allow the supervisor to take corrective action based on configurable policies, using
lifecycle hooks as the intervention mechanism.

**Context:** Today, model escalation and retry decisions are hardcoded in the engine. The
supervisor should own these decisions: "this node failed 3 times with model X, try model Y"
or "this run has been stuck for an hour, pause and notify." The engine provides the hooks;
the supervisor decides when and how to use them.

**Done when:**
- The supervisor can pause and resume a run
- The supervisor can request a model/provider change for a failing node (via hook or context update)
- Policies are configurable (not hardcoded): retry limits, escalation models, stall thresholds,
  notification preferences
- A default policy set works out of the box (sane defaults, not overwhelming configuration)
- The engine's current hardcoded stall watchdog and escalation logic can be removed in favor
  of supervisor-driven policies

---

## Phase 3: Explicit Routing

Goal: Replace the multi-mechanism routing system with a single, predictable mechanism.
Every routing decision should be visible in the graph as an explicit edge with an explicit
condition.

### Design Context

The current routing system has 4+ interacting mechanisms evaluated in priority order:
1. Condition expressions on edges
2. preferred_label matching
3. suggested_next_ids targeting
4. retry_target chains (node → node fallback → graph → graph fallback)

Plus implicit fan-out detection (BFS for convergence points), goal gate enforcement at exit,
and failure classification that affects routing eligibility. A graph author cannot predict
which mechanism will fire without mentally simulating the engine.

The target: **edges have conditions, conditions are evaluated in declaration order, first
match wins.** Retry routing, failure handling, and default paths are all expressed as explicit
edges in the graph. The engine evaluates and logs; the graph declares.

### 3.1 Consolidate Routing to Condition-Based Edges

**What:** Make condition-based edge evaluation the single routing mechanism. Remove
preferred_label matching, suggested_next_ids, and implicit weight-based tiebreaking from
the engine's routing logic.

**Context:** `next_hop.go` implements the 5-step priority cascade. The condition evaluator
in `cond/cond.go` already handles the important cases. The other mechanisms exist as fallbacks
for cases where conditions aren't specified, but they make routing unpredictable.

**Done when:**
- Edge routing evaluates conditions in edge declaration order, selects first match
- An unconditional edge (no condition attribute) matches anything (serves as default/fallback)
- preferred_label and suggested_next_ids are no longer consulted for routing
- The decision log clearly shows: which edges were candidates, which conditions matched,
  which edge was selected
- Test graphs from Phase 0 validate the new routing behavior
- A migration guide documents how to convert existing graphs (retry_target → explicit edges)

### 3.2 Remove Implicit Fan-Out Detection

**What:** Remove the engine's BFS-based convergence detection that silently enables or
disables parallel execution based on graph topology.

**Context:** When a node has multiple eligible outgoing edges, the engine runs `findJoinNode()`
to detect topological convergence. If found, it dispatches parallel branches. If not, it
silently falls back to single-edge selection. This means the same graph structure can produce
either parallel or sequential execution depending on subtle topology differences.

**Done when:**
- Parallel execution only happens at explicit parallel nodes (shape=component)
- Nodes with multiple outgoing edges where multiple conditions match produce a clear error
  or warning, not silent single-edge fallback
- The engine never silently converts sequential routing into parallel execution
- Validation warns when multiple unconditional edges leave a non-parallel node
- Test graphs verify both parallel (explicit) and multi-edge (error) cases

### 3.3 Retire retry_target Chains

**What:** Remove the retry_target and fallback_retry_target attributes from nodes and graphs.
Replace with explicit retry edges in the graph.

**Context:** retry_target is an invisible routing mechanism: when a node fails and no
fail-condition edge matches, the engine checks node.retry_target, then node.fallback_retry_target,
then graph.retry_target, then graph.fallback_retry_target. This 4-level chain is invisible in
the graph and interacts with failure classification in non-obvious ways.

**Done when:**
- retry_target and fallback_retry_target attributes are no longer consulted by the engine
- Retry routing is expressed as edges: `node_a -> retry_node [condition="outcome=fail"]`
- Validation warns if a node has outgoing edges but no edge handles `outcome=fail`
- A migration guide documents the conversion pattern
- Existing test graphs are migrated to explicit retry edges

### 3.4 Retire Goal Gate Mechanism

**What:** Remove the goal_gate enforcement that happens at the exit node. Replace with
explicit edges — if a critical node must succeed before the pipeline can exit, express that
as graph structure, not as a metadata annotation checked at a distance.

**Context:** goal_gate=true on a node means the engine tracks it and checks at exit time
whether it reached SUCCESS. If not, it routes to the node's retry_target. This is another
invisible routing layer: the graph author marks a node as critical, but the enforcement
happens at the exit node, not at the marked node. With explicit routing (Phase 3.1), the
graph can express "don't proceed unless this succeeded" directly as edge conditions.

**Done when:**
- goal_gate attribute is no longer consulted by the engine
- Critical-path enforcement is expressed as edge conditions on the relevant nodes
- Validation no longer requires goal_gate nodes to have retry_targets
- A migration guide documents the conversion pattern

### 3.5 Prompt File Support

**What:** Support `prompt_file` attribute on nodes to reference external prompt files. Prompts
can be inline (current behavior) or external (file reference) — author's choice per node.

**Context:** Production graphs embed multi-paragraph prompts as DOT string attributes. This
makes graphs hard to read and hard to diff. Moving prompts to separate files improves
readability without losing the ability to have simple inline prompts for small nodes. The
graph remains the single source of truth for structure; prompt files are referenced by path
and resolved at parse time.

**Done when:**
- Nodes can use `prompt_file="path/to/prompt.md"` instead of or alongside `prompt="..."`
- prompt_file paths are resolved relative to the graph file's directory
- Validation checks that referenced prompt files exist
- Variable expansion ($goal, etc.) works in prompt files the same as inline prompts
- Inline `prompt` and `prompt_file` can coexist (prompt_file takes precedence, or they
  concatenate — decide during implementation)
- Existing graphs with inline prompts continue to work unchanged

### 3.6 Simplify Fidelity Model

**What:** Collapse the 6 fidelity modes (full, truncate, compact, summary:low/medium/high)
and 4-level precedence chain into a simpler model that the engine manages automatically.
Graph authors shouldn't need to think about token budgets.

**Context:** The fidelity system controls how much conversation context carries between nodes.
It has 6 modes, a 4-level precedence chain (edge → node → graph → default), and a separate
thread_id resolution chain. This is infrastructure-level complexity exposed as an authoring
concern. In practice, there are two meaningful modes: "continue the conversation" (the agent
keeps its context) and "fresh start" (new context with a summary of what happened).

**Done when:**
- The graph-authoring surface is reduced to at most 2-3 fidelity options with clear semantics
- The engine handles context budgeting automatically (compaction, summarization) without
  graph author involvement
- The precedence chain is simplified (node attribute or graph default, no edge-level override)
- Thread management is handled by the agent handler, not the engine
- Existing graphs with fidelity attributes still work (backward compat during transition)

### 3.7 Run Input Contract

**What:** A `--input` flag that accepts a file or directory of structured startup context.
Contents are available to all nodes via variable expansion and the filesystem.

**Context:** Currently the only way to pass runtime data into a graph is `$goal` or ambient
environment variables. A PR review needs: repo, PR number, branch, review criteria. An
exploration run needs: list of repos, search terms. The input contract is the bridge between
whatever launches kilroy and the graph's execution.

**Done when:**
- `kilroy run --graph pr-review.dot --input run-input.yaml` loads structured context
- Input values are available via variable expansion in prompts: `$input.pr_number`,
  `$input.repo`, `$input.task`
- Input files (in a directory) are available at `$INPUT_DIR` in tool_command nodes
- The graph can declare required inputs and validation rejects missing ones
- Works with both config-file and zero-config modes

### 3.8 Run Output Contract

**What:** A way for graphs to declare output artifacts and a known location for results.
After a run, `kilroy status` should tell you where the outputs are.

**Context:** Currently, outputs are scattered through the worktree and stage logs. Finding
the PR review report requires spelunking through nested directories. The run should have a
declared output location or manifest.

**Done when:**
- Nodes can declare `output_file="review-report.md"` or similar
- Declared outputs are collected to a known location after the run
- `kilroy status --run <id>` shows where outputs are
- Alternatively: a convention like `$OUTPUT_DIR` that nodes write to, and the engine copies
  to a discoverable location post-run

### 3.9 Node Data Passing Conventions

**What:** Document and enforce clear conventions for how nodes pass data to each other.
Currently this is ad-hoc filesystem usage with no validation or documentation.

**Context:** Nodes pass data by writing files to paths like `.ai/runs/$KILROY_RUN_ID/` and
hoping subsequent nodes find them. This broke during the PR review workflow because tool
nodes didn't get `KILROY_RUN_ID`. Even when it works, the convention is undiscoverable.

**Done when:**
- A clear, documented convention exists for inter-node data: where to write, how to name
  files, how subsequent nodes find them
- The engine validates that files referenced in prompts (via `$input_dir` or similar) exist
- Context updates in outcomes can pass small structured data between nodes
- The convention works for both tool_command nodes and agent nodes

---

## Phase 4: Clean Handler Interface

Goal: Establish a clean boundary between the engine (graph traversal, routing, lifecycle) and
handlers (the work each node does). Handlers own their dependencies. The engine provides
minimal context.

### Design Context

Currently the codergen handler receives the full engine, the graph, the logs root, provider
runtimes, the model catalog, and the CXDB sink. This couples handlers to engine internals.
The target: handlers receive node attributes and a run context (key-value store), and return
an outcome. How they invoke LLMs, manage tools, or interpret results is their concern.

The parallel/fan-in boundary also needs cleanup: the engine owns the concurrency lifecycle
(recognizing parallel structure, managing worker pools, evaluating join policies), while
hooks handle isolation (git worktrees, temp directories) and handlers do the actual branch work.

### 4.1 Define Minimal Handler Interface

**What:** Design the handler interface that handlers implement. It should provide enough
context for handlers to do their work without coupling them to engine internals.

**Context:** The current handler interface varies by handler type. Codergen handlers receive
substantially more context than tool handlers. The goal is a uniform interface that works for
all handler types while allowing handlers to bring their own dependencies via closure or
constructor injection.

**Done when:**
- A handler interface is defined with a single Execute method
- The method receives: node attributes (read-only), run context (read/write), workspace path,
  and a logger/event emitter
- The method returns: an Outcome (status, failure_reason, context_updates)
- The three core handler types (agent, command, gate) implement this interface
- The engine dispatches to handlers through this interface only
- Test graphs validate all three handler types through the new interface

### 4.2 Reform Status Contract

**What:** Replace the file-based status contract (LLM writes JSON to a specific path) with
handler-interpreted outcomes. The handler extracts success/fail from the agent's behavior,
not from a file the agent was instructed to write.

**Context:** Currently, every codergen node's prompt must include instructions to write
status.json to `$KILROY_STAGE_STATUS_PATH` with specific fields. LLMs get the format wrong,
write to the wrong path, or forget entirely. This is the most fragile part of the pipeline —
a typo in a JSON field name silently breaks routing.

With a reformed contract, the handler owns interpretation: for CLI agents, exit code + output
parsing. For API agents, the final message content. For command/tool nodes, exit code. The
LLM doesn't need to know about status.json at all. The handler reports a structured outcome
to the engine through the handler interface, which writes it to the run DB.

**Done when:**
- The agent handler can determine success/fail without the LLM writing a status file
- Command handlers use exit code (0 = success, non-zero = fail)
- Agent handlers interpret the agent's final output or behavior to determine outcome
- Status file writing is optional (an agent CAN still write one to provide richer outcome
  data like preferred_label or context_updates, but it's not required for basic routing)
- Prompts no longer need the KILROY_STAGE_STATUS_PATH boilerplate
- The run DB records the outcome regardless of how it was determined

### 4.3 Clean Parallel/Fan-In Boundary

**What:** Separate the engine's concurrency management from the handler-level isolation and
merge strategies. The engine manages worker pools and join policies. Hooks manage isolation.
Handlers do the branch work.

**Context:** The parallel handler currently creates git worktrees, clones engine instances,
manages worker pools, evaluates join policies, and handles fan-in winner selection — all in
one handler. The engine-level concerns (concurrency, join policy) should be in the engine.
The isolation concerns (worktrees, temp dirs) should be in hooks. The winner selection should
be in the fan-in handler.

**Done when:**
- The engine recognizes parallel nodes and manages concurrent branch dispatch
- Join policy evaluation (wait_all, first_success, k_of_n, quorum) is engine-level
- Error policy (continue, fail_fast, ignore) is engine-level
- Branch isolation is provided by lifecycle hooks (git hook creates worktrees; no-git hook
  creates temp directories)
- Fan-in winner selection remains handler-level
- Parallel execution works with and without git hooks
- Test graphs with parallel nodes validate the new boundary

---

## Test Strategy

Every phase is validated with the script-based test graph suite from 0.1. No LLM involvement
in engine validation.

For each completed task:
1. `go test ./...` passes (existing tests)
2. All test graphs in the suite execute correctly
3. The specific behavior change is demonstrated (e.g., decision log shows edge evaluation
   for 0.4, bad credentials fail preflight for 0.5)

LLM-backed validation (real graphs with real models) happens after Phase 0 is complete and
the engine plumbing is proven solid with deterministic graphs.

## Progressive Delivery

This plan is designed to build while the plane is flying:

- **Phase 0** (8 tasks) — each independently shippable, immediately improves the current system;
  includes CLI-only backend shift (opencode for non-claude/codex providers, deprecate API)
- **Phase 1** (3 tasks) — additive; the DB writes alongside existing files, nothing breaks
- **Phase 2** (6 tasks) — decouples concerns; git removed from engine (caller manages worktrees),
  CXDB/artifacts into hooks, checkpoint optional
- **Phase 2.5** (4 tasks) — adds the supervisor; run monitoring, blocker detection,
  self-introspection, intervention policies
- **Phase 3** (6 tasks) — changes graph semantics and authoring model; requires graph migration
- **Phase 4** (3 tasks) — cleans interfaces; handler contract, status reform, parallel boundary

At any point, we can pause and use the system as-is. Each completed phase leaves the system
strictly better than before.

---

## Future Work (Beyond This Plan)

These are known gaps that this plan doesn't address but should be tackled eventually:

### Remove API Backend Entirely

Phase 0.8 deprecates the API backend in favor of CLI-only (claude, codex, gemini, opencode).
Once the deprecation has been in place and CLI-only is proven reliable, the API backend code
(`internal/agent/`, `internal/llm/`) can be removed entirely. This is a significant code
reduction that simplifies maintenance and testing.

### Configuration Surface Simplification

Zero-config startup (Phase 0.7) addresses the new-user experience, but the config file
itself remains a combinatorial explosion for advanced users. The config model should be
reviewed and simplified: collapse redundant options, remove options that have only one
reasonable value, group related settings, and provide clear documentation of what each
option does and when you'd change it.

### Graph Authoring Tooling

Writing DOT files by hand is error-prone. Future tooling could include: a graph visualizer
that shows the execution topology, a graph editor with validation feedback, graph templates
for common patterns (linear+verify, fan-out consensus), and prompt scaffolding that generates
the boilerplate for new nodes.

### Iteration Patterns (Dynamic Loops Over Collections)

Some workflows need to process a dynamic list: "review each changed file," "test each PR,"
"chew through this task list until empty." The engine supports loops (edge back to earlier
node) but not dynamic iteration over a collection where the loop count isn't known at graph
authoring time. This is probably a pattern built on context variables (set a list, loop node
pops items, exit condition checks if list is empty) rather than a new engine primitive.
Worth investigating what patterns emerge from real usage.
