# Kilroy Platform Reframe

Date: 2026-04-03

## Identity

Kilroy is a software operations automation platform that uses DOT graphs to codify
repeatable patterns of LLM-assisted work.

Graphs are version-controlled workflow definitions — reviewable in PRs, shareable across
teams, reusable across repos. Nodes are tasks (shell commands, LLM agents, human gates).
Edges are transitions with conditions that can be evaluated by the engine or influenced by
agent judgment. The graph is simultaneously human-readable, machine-executable, and visually
renderable (standard Graphviz tooling) — no translation step.

The novel combination: declarative graph structure + LLM-evaluated edge transitions +
agent nodes that do creative work. Nobody else does this with DOT files. Nobody else uses
the workflow definition as the visualization.

## Vision

A central orchestrator with visibility across projects dispatches kilroy runs:

```
Workflow Library (version-controlled, team-owned)
  ├── pr-review.dot         — "how we review PRs"
  ├── implement-spec.dot    — "how we build from a specification"
  ├── dependency-update.dot — "how we update dependencies"
  ├── deploy-validate.dot   — "how we validate deployments"
  └── tech-debt-sweep.dot   — "how we pay down tech debt"

Scheduler / Orchestrator
  ├── "PR #74 opened on kilroy" → dispatch pr-review.dot
  ├── "Tuesday 2am" → dispatch dependency-update.dot across 12 repos
  ├── "PagerDuty alert" → dispatch incident-triage.dot
  └── "Post-deploy hook" → dispatch deploy-validate.dot
```

Each graph is a codified pattern. The scheduler knows when to run them. Kilroy knows how.
The agents inside adapt to whatever they find.

## Layered Architecture

Three layers with enforced import boundaries (separate Go packages, no cross-layer imports
upward). Each layer adds capabilities. Layer 0 is useful alone.

### Layer 0: Graph Runner

The execution primitive. Parses DOT, traverses nodes, evaluates edge conditions, persists
state. Knows nothing about LLMs, humans, git, or software development.

- DOT parsing and graph model
- Graph traversal: walk nodes, evaluate conditions, select edges, advance
- Handler interface: `Execute(ctx, node, context) → Outcome` (Layer 0 defines the contract)
- Built-in handlers: start, exit, conditional, tool (shell command)
- Context: thread-safe key-value store shared across nodes
- Outcome model: status, context_updates, failure_reason, preferred_label, suggested_next_ids
- Condition expression language: `outcome=success && context.foo=bar`
- Edge selection algorithm (5-step priority: conditions → preferred_label → suggested_next_ids → weight → lexical)
- Event system: typed events emitted at lifecycle points, observers subscribe
- Hook system: lifecycle points where external concerns attach (run.before, node.after, etc.)
- Checkpoint/resume (optional — off by default, opt-in via config or hook)
- SQLite run database: runs, nodes, outcomes, edge decisions, queryable
- Run lifecycle management: prune, labels, cross-run queries
- Input contract: `--input run-input.yaml`, graph declares required inputs, validation rejects missing
- Output contract: graph declares output artifacts, engine surfaces them at completion
- Workspace abstraction: `--workspace /path/to/dir` separates graph location from execution location
- HTTP API: run lifecycle endpoints, status, SSE event streaming

A Layer 0-only graph is a DOT file where every node is a `tool_command` (shell script) or
a conditional routing point. Think: a smarter Makefile that can loop, branch, checkpoint,
and resume. Useful for build pipelines, deployment scripts, data processing — anything
expressible as a graph of shell commands with conditional transitions.

### Layer 1: Agent Capabilities

Makes graphs LLM-aware. Registers handler implementations and hooks with the Layer 0 engine.

- Agent handler: invokes LLM CLI tools (claude, codex, gemini, opencode, others)
- CLI session management via tmux: spawn, send input, read output, wait for completion
  - Generic tmux interaction patterns (session lifecycle, pane management, output capture)
  - Per-tool specifics: permissions flags, model selection, API key configuration
  - Extensible: adding a new CLI tool means defining its invocation template
- Provider detection: scan environment for API keys, select backends
- Model stylesheet: CSS-like selectors for per-node LLM model/provider assignment
- Context fidelity: controls how much prior context carries between agent nodes
- LLM-influenced routing: agent outcomes include preferred_label/suggested_next_ids
  that feed into Layer 0's edge selection algorithm
- Failure classification: transient vs deterministic, retry gating
- Preflight validation: test credentials before execution starts
- HTTP API extensions: provider status, model availability endpoints

### Layer 2: Workflow Patterns

Opinionated capabilities for specific workflow types. Registers handlers and hooks with
the Layer 0 engine. Layer 2 components are independently opt-in.

- Human-in-the-loop: interviewer protocol, hexagon gate nodes, timeout/default handling
  - Multiple interviewer backends: console, web, Slack, auto-approve, queue
- Git integration (as a hook, not engine core):
  - Worktree creation for run isolation
  - Per-node commits for incremental checkpointing
  - Branch management and SHA tracking
  - Parallel branch isolation via worktrees
- Manager/supervisor loop: observe/steer cycles over child pipelines
- Parallel fan-out with workspace isolation
- Workflow packages: graph + scripts + prompts as a portable, self-contained unit
  - Package directory structure: `graph.dot`, `scripts/`, `prompts/`
  - Scripts travel with the graph — no hardcoded absolute paths
  - Package copied/mounted into workspace before execution
- Software development conventions:
  - `.ai/runs/$KILROY_RUN_ID/` data passing between nodes
  - Artifact policies
  - Build/test integration patterns
- HTTP API extensions: human-in-the-loop question/answer endpoints, supervisor status

### Import Rules

```
Layer 0 (engine/)     — imports nothing from L1 or L2
Layer 1 (agents/)     — imports Layer 0 interfaces only
Layer 2 (workflows/)  — imports Layer 0 interfaces, may import Layer 1 interfaces
cmd/kilroy/           — wires all layers together, registers handlers/hooks
```

The startup code in `cmd/kilroy/` composes the layers: register the agent handler (L1),
register the git hook (L2), register the human gate (L2), start the engine (L0). Someone
who only wants Layer 0 skips L1/L2 registration.

## Reference Spec

The attractor spec (`docs/strongdm/attractor/attractor-spec.md`) is the north star for
graph semantics, handler contracts, edge selection, condition language, and state model.
The spec's design is sound — the implementation deviated from it. This plan brings the
implementation back in line with the spec's vision and extends it where the spec has gaps
(workspace abstraction, input/output contracts, run lifecycle, HTTP API).

Things in the spec we preserve as-is:
- Handler interface (Section 4.1)
- Edge selection algorithm, all 5 steps (Section 3.3)
- Context and Outcome model (Section 5.1, 5.2)
- Condition expression language (Section 10)
- Model stylesheet (Section 8)
- Validation and lint rules (Section 7)
- Event types (Section 9.6)
- Checkpoint structure (Section 5.3)

Things in the spec we extend:
- Input/output contracts (spec has only `$goal`)
- Run lifecycle management (spec has no fleet management)
- HTTP API (spec Section 9.5 describes it, not implemented)
- Workspace abstraction (spec assumes cwd)
- CLI session management (spec's CodergenBackend is abstract)

## Phase 1: Foundation (Layer 0)

Goal: Build the graph runner as a clean, standalone layer. Enforce the import boundary.
Everything above Layer 0 is a registered handler or hook.

### 1.1 Package Structure and Import Boundaries

**What:** Create the new package layout. Move existing engine code into Layer 0. Identify
and extract Layer 1/L2 code into their packages. Handler registrations move to cmd/kilroy/.

**Context:** Today everything lives in `internal/attractor/engine/`. The codergen handler,
git operations, CXDB sink, human gate, parallel worktree management — all in one package.
The new structure enforces that the engine package has zero imports from agent or workflow
packages.

**Done when:**
- Package structure exists: `engine/` (L0), `agents/` (L1), `workflows/` (L2)
- `engine/` compiles with no imports from `agents/` or `workflows/`
- Handler registration happens in `cmd/kilroy/` startup, not in engine init
- All existing tests pass
- A graph using only tool_command nodes runs without L1 or L2 registered

### 1.2 SQLite Run Database

**What:** SQLite as the single source of truth for run-level operational state. Every run,
node execution, outcome, and edge decision is recorded and queryable.

**Context:** Today run state is spread across progress.ndjson, checkpoint.json, final.json,
and per-node status.json. Answering "what failed this week" requires parsing 323 directories.
The database makes operational queries instant.

**Done when:**
- Migration runner auto-applies numbered SQL files on DB open
- Schema supports: runs (with labels, inputs, timing), nodes (with attempts), outcomes,
  edge decisions, provider selections
- Engine writes to DB at every lifecycle point (run start, node start, node complete,
  edge selection, run complete)
- `kilroy attractor status --latest` reads from DB
- `kilroy attractor runs list` shows runs with labels, inputs, status, timing
- `kilroy attractor runs prune` works against DB
- Existing file-based output (progress.ndjson, checkpoint.json) still written alongside
- Test graphs produce correct DB entries

### 1.3 Input Contract

**What:** `--input run-input.yaml` provides structured data to the graph. Graphs declare
required inputs. Validation rejects missing inputs before execution.

**Context:** Currently the only input mechanism is `$goal` and ambient environment variables.
A PR review graph needs repo, PR number, review criteria. An implementation graph needs a
spec file. Without input contracts, every workflow is a hack with undocumented env vars.

**Done when:**
- `kilroy attractor run --graph g.dot --input input.yaml` loads structured context
- Input values available via variable expansion in prompts: `$input.pr_number`, `$input.repo`
- Input values available as environment variables in tool_command nodes: `$KILROY_INPUT_PR_NUMBER`
- Graph can declare required inputs: `inputs="pr_repo,pr_number"` graph attribute
- Validation rejects runs with missing required inputs
- Inputs recorded in run DB and visible in `runs list`
- Works with both config-file and zero-config modes

### 1.4 Workspace Abstraction

**What:** `--workspace /path/to/dir` separates where the graph lives from where it runs.
The engine executes in the workspace directory. The graph file can be anywhere.

**Context:** Today graph location and execution location are conflated. Cross-repo workflows
require hardcoded absolute paths. The PR review graph targeting freshell had to hardcode
`/Users/matt/sw/personal/kilroy/workflows/pr-review/scripts/setup-pr.sh`.

**Done when:**
- `kilroy attractor run --graph /path/to/graph.dot --workspace /path/to/repo` works
- Tool_command paths resolve relative to workspace, not graph location
- If `--workspace` is omitted, defaults to cwd (current behavior preserved)
- Prompt file references (`prompt_file=`) resolve relative to graph file location
- Graph + scripts can live in a separate workflow package directory

### 1.5 Output Contract

**What:** Graphs declare output artifacts. After a run, the engine tells you where results are.

**Context:** Currently finding outputs requires `find ~/.local/state/kilroy/.../worktree -name
"review-report.md"`. For an automated platform, the caller needs to know where the report
landed without filesystem spelunking.

**Done when:**
- Graph attribute `outputs="review-report.md,summary.json"` declares expected outputs
- Output files are collected to a known location after run completion
- `kilroy attractor status --run <id>` shows output artifact paths
- Run DB records output locations
- If declared outputs are missing at run completion, emit a warning (not error)

### 1.6 HTTP API (Core)

**What:** REST + SSE endpoints for run lifecycle, status, and event streaming. This is how
remote systems dispatch and monitor runs.

**Context:** The spec (Section 9.5) describes this but it was never implemented. The "eye in
the sky" orchestrator needs an API to dispatch runs and monitor their progress.

**Done when:**
- `POST /runs` — start a new run (graph, workspace, input)
- `GET /runs/{id}` — run status, progress, outcomes
- `GET /runs/{id}/events` — SSE stream of run events
- `POST /runs/{id}/cancel` — cancel a running pipeline
- `GET /runs` — list runs with filtering (status, labels, date range)
- `GET /runs/{id}/outputs` — list output artifacts
- Server starts with `kilroy server` or `kilroy attractor serve`
- Composable: higher layers register additional routes (L2 adds /runs/{id}/questions)

### 1.7 Run Lifecycle Management

**What:** Auto-prune, TTL-based cleanup, stale worktree detection, run labels.

**Context:** 323 orphaned run directories. Stale worktrees from old runs caused branch lock
conflicts. No way to identify runs beyond reading manifests. `runs list` shows identical
goal text for every PR review run.

**Done when:**
- `--label key=value` flag on run start, stored in DB
- `kilroy attractor runs list` shows labels
- `kilroy attractor runs prune --older-than 7d` works against DB + filesystem
- On startup, detect and warn about stale worktrees that will conflict
- Completed/failed run worktrees auto-cleaned after configurable TTL
- Run timing (per-node duration, total duration) recorded in DB and visible in status

## Phase 2: Agent Capabilities (Layer 1)

Goal: Make graphs LLM-aware. Build robust CLI tool management. Register agent handlers
and provider infrastructure with the Layer 0 engine.

### 2.1 CLI Session Management via Tmux

**What:** Spawn and manage CLI agent sessions (claude, codex, gemini, opencode) via tmux.
Generic session management patterns plus per-tool invocation templates.

**Context:** Current implementation spawns CLI tools as subprocesses with stdin/stdout pipes.
This is fragile — no observability into what the agent is doing, no persistence if the parent
has issues, no interactive capabilities. Tmux gives us persistent, observable, interactive
agent sessions.

**Done when:**
- Generic tmux session manager: create session, send keys, read output, wait for pattern,
  capture pane contents, kill session
- Agent handler uses tmux instead of subprocess pipes
- Per-tool invocation templates define: binary name, permission flags, model flag,
  API key env var, headless mode configuration
- Templates exist for: claude, codex, gemini, opencode
- Adding a new CLI tool means adding a template (no code changes to the session manager)
- Running agent can be observed by attaching to its tmux session
- Agent output captured and stored in run DB / stage logs

### 2.2 Provider Detection and Routing

**What:** Auto-detect available providers from environment. Route nodes to providers based
on model stylesheet, node attributes, or auto-detection.

**Context:** This exists today (Phase 0.7 zero-config) but needs to compose cleanly with
the layered architecture. Provider detection is a Layer 1 concern that registers with
the Layer 0 engine.

**Done when:**
- Provider detection scans environment for API keys and CLI binaries
- Detection results registered with engine as provider metadata
- Model stylesheet resolution uses detected providers
- Preflight validates all providers needed by the graph
- Provider status visible via HTTP API
- Works with both zero-config and explicit config

### 2.3 Context Fidelity

**What:** Control how much prior context carries between agent nodes. The spec defines 6
modes with a 4-level precedence chain.

**Context:** This is a Layer 1 concern — it only matters for agent nodes. The engine
doesn't need to know about context windowing. The agent handler manages fidelity
internally, deciding how much prior conversation to include when starting a new agent
session.

**Done when:**
- Fidelity resolution happens in the agent handler, not the engine
- Agent handler supports at minimum: fresh (no context), summary (condensed prior work),
  full (continued session via tmux session reuse)
- Fidelity mode set via node attribute or stylesheet
- Thread management (session reuse for `full` fidelity) handled by tmux session naming

### 2.4 Failure Classification and Retry

**What:** Classify agent failures as transient (retry) vs deterministic (don't retry).
Gate retry decisions on failure class.

**Context:** The spec (Section 3.5) defines this well. The agent handler needs to interpret
CLI tool exit codes and output to determine failure class. Layer 0's retry mechanism
consults the failure class to decide whether to retry.

**Done when:**
- Agent handler classifies failures from CLI exit codes and stderr patterns
- Failure classes: transient_infra, budget_exhausted, deterministic, canceled
- Layer 0 retry policy consults failure_class before retrying
- Decision logged in run DB
- Common failure patterns per CLI tool documented in invocation templates

## Phase 3: Workflow Patterns (Layer 2)

Goal: Build the opinionated workflow capabilities that make kilroy useful for software
operations. Each component is independently opt-in.

### 3.1 Git Integration as Hook

**What:** Move all git operations out of the engine core and into a lifecycle hook.
The engine doesn't know about git. The git hook provides worktree isolation, per-node
commits, and branch management as opt-in capabilities.

**Context:** Git operations are currently spread across engine.go (~15 callsites),
parallel_handlers.go, and resume.go. This is the biggest extraction — it touches the
most code and has the most subtle interactions (parallel branch isolation, resume SHA
validation, checkpoint commit tracking).

**Done when:**
- Engine has zero direct gitutil imports
- A GitHook implements the hook interface: creates worktrees on run.before, commits on
  node.after, cleans up on run.after
- Runs WITH the GitHook behave identically to current behavior
- Runs WITHOUT the GitHook succeed in any directory (no git required)
- Parallel branch isolation works both ways: git hook uses worktrees, no-git uses temp dirs
- Test graphs pass both with and without the git hook

### 3.2 Human-in-the-Loop

**What:** Move the human gate handler and interviewer protocol to Layer 2. The engine
doesn't know about humans — it just has a handler that blocks until an external event
resolves it.

**Context:** Already implemented (WaitHumanHandler, interviewer implementations). Needs
to be moved to the workflows package and registered at startup.

**Done when:**
- WaitHumanHandler lives in workflows/, registered at startup
- All interviewer implementations (console, auto-approve, queue, callback, recording)
  move to workflows/
- HTTP API extension: `POST /runs/{id}/questions/{qid}/answer` for web-based interaction
- Existing tests pass from new location

### 3.3 Workflow Packages

**What:** A self-contained directory that bundles a graph with its scripts and prompts.
Portable, sharable, version-controlled.

**Context:** The PR review workflow required hardcoded absolute paths because scripts
lived in a different repo than the execution target. Workflow packages solve this by
bundling everything together.

**Done when:**
- A workflow package is a directory containing: `graph.dot`, `scripts/`, `prompts/`
- `kilroy attractor run --package /path/to/pr-review/` loads the package
- Package scripts are available in the workspace at a known path (e.g., `.kilroy/package/scripts/`)
- Tool_command nodes reference scripts relative to the package: `tool_command=".kilroy/package/scripts/setup.sh"`
- Prompt files reference prompts relative to the package
- A package can be pointed at any workspace

### 3.4 Supervisor Prototype

**What:** Detect stuck/blocked runs and surface "this needs attention." Not full
intervention-policy system — just monitoring and notification.

**Context:** The spec's manager loop handler (Section 4.11) is a starting point. The
supervisor watches runs by querying the run DB and classifies state: healthy, degraded
(retrying), blocked (needs human), failed (terminal).

**Done when:**
- Supervisor monitors active runs by polling the run DB
- Classifies run state: healthy, degraded, blocked, failed
- Blocked detection: same node failing repeatedly, no progress for N minutes
- Surfaces blocked runs via: CLI output, HTTP API endpoint, structured event
- `kilroy attractor status --run <id>` shows supervisor assessment
- Per-node timing visible in status output

### 3.5 CXDB as Hook

**What:** Move CXDB integration out of engine core into an optional hook. If no CXDB
hook registered, zero CXDB code runs.

**Context:** CXDB is already mostly nil-safe in the engine but still wired directly.
Extracting into a hook makes the nil-checking unnecessary.

**Done when:**
- Engine has zero direct CXDB imports
- CXDBHook implements the hook interface
- Runs with CXDBHook behave identically to current behavior
- Runs without CXDBHook have zero CXDB overhead

## Phase 4: Prove It Works

Goal: Build real workflow packages that demonstrate the platform end-to-end.

### 4.1 PR Review Workflow Package

**What:** A complete, portable PR review workflow as a workflow package.

**Context:** We've been iterating on this manually. Now package it properly using the
platform's input/output contracts, workspace abstraction, and workflow package format.

**Done when:**
- `workflows/pr-review/` contains graph.dot, scripts/, prompts/
- `kilroy attractor run --package workflows/pr-review/ --workspace /path/to/repo --input '{"pr_number": 74, "pr_repo": "danshapiro/kilroy"}'`
- Graph declares inputs (pr_number, pr_repo) and outputs (review-report.md)
- Works against any repo without modification
- Results visible via `status --run <id>` including output location and per-node timing
- Run recorded in DB with labels and inputs

### 4.2 Implement-from-Spec Workflow Package

**What:** A workflow that takes a specification and produces an implementation.

**Context:** This is the core attractor use case — the "generative SDLC." Plan → implement
→ verify → iterate. The graph structure gives the agent deterministic guardrails while
the LLM provides adaptability.

**Done when:**
- `workflows/implement-spec/` contains the package
- Accepts inputs: spec file path, target language/framework, acceptance criteria
- Graph: plan → implement → test → verify → iterate-or-complete
- Uses agent nodes for planning and implementation, tool nodes for testing
- Loops on test failure with context about what went wrong
- Produces: implementation code, test results, implementation notes

### 4.3 Build-and-Test Workflow Package

**What:** A simple, non-LLM workflow package that demonstrates Layer 0 standalone value.
Runs a project's build and test suite, reports results.

**Context:** Proves Layer 0 works without any LLM involvement. Pure tool_command nodes.
Also useful as a building block for other workflows.

**Done when:**
- `workflows/build-test/` contains the package
- All nodes are tool_command (shell scripts)
- Detects build system (make, go, npm, cargo, etc.) and runs appropriate commands
- Conditional routing: build fail → report, test fail → report, all pass → report
- Produces: build-report.json with status, timing, output
- Runs with Layer 0 only (no L1/L2 handlers registered)

## Naming

Rename "codergen" to "agent" throughout the codebase. The handler invokes an LLM agent
to perform a task — it's not generating code. This rename happens as part of Phase 1.1
(package restructure) to avoid a separate rename pass.

## Transition Strategy

Feature branch (`feat/platform-reframe`). Both old and new architectures exist during
development. No backward compatibility constraints blocking progress. When the new
architecture is proven (Phase 4 workflows run successfully), merge to main.

## Test Strategy

- Layer 0 tested with pure tool_command graphs (already have the test suite from Phase 0.1)
- Layer 1 tested with fake CLI scripts that mimic real tool output (existing pattern)
- Layer 2 tested with integration tests against real repos
- Each phase must pass: `go test ./...`, all test graphs execute correctly, specific
  behavior change demonstrated
- Phase 4 workflows are the end-to-end proof

## What We're Not Doing (Yet)

- Routing simplification (5-step algorithm stays — it's the LLM-influenced routing mechanism)
- Full supervisor intervention policies (prototype only)
- Graph authoring tooling (visual editor, templates)
- Dynamic iteration patterns (loop over collections)
- Configuration surface simplification (zero-config is good enough for now)
- Remove API backend entirely (deprecate first, remove later)
