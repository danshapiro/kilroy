# Canonical Run Log

Date: 2026-04-07

## Goal

A single, chronological, verbose log file per run that captures everything that
happened — engine lifecycle, script output, agent tool calls, file changes, context
mutations. The REST API serves from this log. It is the single source of truth for
"what happened during this run."

## Design

### Log file

Each run produces `{logs_root}/run.log` — a newline-delimited JSON file where each
line is a timestamped event. Human-readable when piped through `jq`, machine-parseable
for the API and UI.

```json
{"ts":"2026-04-07T12:06:07.001Z","level":"info","source":"engine","node":"","event":"run.started","msg":"Run started: build_test","data":{"workspace":"/path/to/repo","inputs":{"pr_number":"77"}}}
{"ts":"2026-04-07T12:06:07.015Z","level":"info","source":"engine","node":"detect","event":"node.started","msg":"Executing: sh .kilroy/package/scripts/detect-build-system.sh","data":{"handler":"tool","attempt":1}}
{"ts":"2026-04-07T12:06:07.080Z","level":"info","source":"tool","node":"detect","event":"stdout","msg":"Detected build system: go"}
{"ts":"2026-04-07T12:06:07.081Z","level":"info","source":"tool","node":"detect","event":"stdout","msg":"Build command: go build ./cmd/kilroy/"}
{"ts":"2026-04-07T12:06:07.090Z","level":"info","source":"engine","node":"detect","event":"node.completed","msg":"Node detect: success (75ms)","data":{"status":"success","duration_ms":75}}
{"ts":"2026-04-07T12:06:07.091Z","level":"info","source":"engine","node":"","event":"edge.selected","msg":"detect → build (only_edge)","data":{"from":"detect","to":"build","reason":"only_edge","condition":""}}
{"ts":"2026-04-07T12:08:15.000Z","level":"info","source":"engine","node":"code_review","event":"node.started","msg":"Agent started (claude, model: claude-sonnet-4.6)","data":{"handler":"agent","attempt":1,"tool":"claude","model":"claude-sonnet-4.6"}}
{"ts":"2026-04-07T12:08:22.000Z","level":"info","source":"agent","node":"code_review","event":"tool_call","msg":"Read(.ai/pr-data/pr-diff.patch)","data":{"tool":"read","args":{"path":".ai/pr-data/pr-diff.patch"}}}
{"ts":"2026-04-07T12:08:30.000Z","level":"info","source":"agent","node":"code_review","event":"tool_call","msg":"Write(.ai/pr-data/code-review-findings.md)","data":{"tool":"write","args":{"path":".ai/pr-data/code-review-findings.md"}}}
{"ts":"2026-04-07T12:08:31.000Z","level":"info","source":"engine","node":"code_review","event":"node.completed","msg":"Agent completed (16s)","data":{"status":"success","duration_ms":16000}}
{"ts":"2026-04-07T12:08:31.001Z","level":"info","source":"engine","node":"code_review","event":"files_changed","msg":"1 file added","data":{"files":[{"path":".ai/pr-data/code-review-findings.md","status":"added"}]}}
```

### Event schema

Every log line has:
- `ts` — ISO 8601 UTC timestamp with millisecond precision
- `level` — info, warn, error
- `source` — who emitted: `engine`, `tool`, `agent`, `git`
- `node` — which node this relates to (empty for run-level events)
- `event` — dot-notation event type
- `msg` — human-readable one-liner
- `data` — structured payload (optional, event-specific)

### Event types

**Engine lifecycle:**
- `run.started` — run begins, includes inputs, workspace, graph name
- `run.completed` — run finished, includes final status, duration
- `node.started` — node execution begins, includes handler type, attempt
- `node.completed` — node finished, includes status, duration, outcome
- `edge.selected` — routing decision, includes from/to, condition, reason
- `context.updated` — context key-value changes from node outcome
- `checkpoint.saved` — checkpoint written
- `kilroy.created` — .kilroy/ directory and files written
- `output.validated` — output contract check result

**Tool node output:**
- `stdout` — line of stdout from tool_command execution (source=tool)
- `stderr` — line of stderr from tool_command execution (source=tool)
- `exit` — tool process exit code

**Agent activity (parsed from CLI jsonl):**
- `tool_call` — agent invoked a tool (read, write, edit, bash, etc.)
- `tool_result` — tool returned a result (truncated if large)
- `thinking` — agent thinking/reasoning block (optional, configurable)
- `text` — agent text output
- `agent.started` — CLI tool session started
- `agent.completed` — CLI tool session finished

**Git activity (source=git):**
- `commit` — per-node commit, includes SHA, files changed
- `worktree.created` — worktree set up
- `worktree.cleaned` — worktree removed

## Implementation

### 1. RunLog writer

New type in `engine/` — `RunLog` that wraps a file handle and provides typed
emit methods. Set on the Engine alongside the existing progress sink.

```go
type RunLog struct {
    f   *os.File
    mu  sync.Mutex
    run string  // run ID for context
}

func (l *RunLog) Emit(level, source, node, event, msg string, data map[string]any)
func (l *RunLog) Info(source, node, event, msg string, data ...map[string]any)
func (l *RunLog) Warn(source, node, event, msg string, data ...map[string]any)
```

The engine creates the RunLog at run start and passes it through the execution.
The RunLog writes to `{logs_root}/run.log`.

### 2. Engine lifecycle events

Wire `RunLog.Info(...)` calls at every lifecycle point in the engine:
- Run start/complete (already have progress events — add RunLog calls alongside)
- Node start/complete
- Edge selection
- Retry decisions
- Context updates
- Checkpoint saves
- .kilroy/ file writes
- Output contract validation

These replace nothing — progress.ndjson continues to work. The RunLog is additive.

### 3. Tool node stdout/stderr capture

The tool handler (`ToolHandler.Execute`) currently captures stdout as a single
string. Change it to stream line-by-line through the RunLog:
- Pipe stdout/stderr through a scanner
- Each line gets a RunLog event with source=tool
- Also accumulate for the outcome (existing behavior preserved)

### 4. Agent CLI log parsing

This is the per-CLI-tool work. Each CLI tool writes its own log format:

**Claude:** Writes JSONL to a conversation file. Each line has a `type` field:
`assistant`, `tool_use`, `tool_result`, `thinking`. Parse these and emit as
RunLog events with source=agent.

**Codex:** Writes to its own log format. Parse similarly.

**OpenCode:** May use a different format. Parse similarly.

The agent handler (TmuxAgentHandler) needs to:
1. Know where the CLI tool writes its log (varies by tool)
2. After the agent completes, read the log file
3. Parse tool calls, results, and text output
4. Emit each as a RunLog event

For real-time streaming (while the agent is running), the handler could tail
the CLI log file and emit events as they appear. Start with post-completion
parsing; add real-time tailing later.

Implementation approach: each invocation template gets a `LogParser` interface:
```go
type LogParser interface {
    // ParseLog reads a CLI tool's log file and returns structured events.
    ParseLog(logPath string) ([]AgentEvent, error)
}
```

Templates define where their tool's log lives and how to parse it.

### 5. Git activity events

The git hook emits RunLog events for commits and worktree operations.
The hook already has access to the engine (via the GitOps interface) —
it needs access to the RunLog to emit events.

### 6. REST API endpoint

`GET /runs/{id}/log` — serves the canonical run.log file.

Query params:
- `?node=code_review` — filter to events for a specific node
- `?source=agent` — filter by source
- `?event=tool_call` — filter by event type
- `?since=2026-04-07T12:08:00Z` — events after timestamp
- `?tail=100` — last N events
- `?stream=true` — SSE mode: stream events as they're written (for live runs)

The streaming mode tails run.log with fsnotify and pushes new lines as SSE
events. This solves the "can't watch CLI-launched runs" gap — the dashboard
subscribes to `GET /runs/{id}/log?stream=true` and gets real-time updates
regardless of how the run was launched.

### 7. Backward compatibility

- `progress.ndjson` continues to be written (existing consumers don't break)
- `run.log` is additive
- The RunLog can optionally replace progress.ndjson as the canonical source
  once proven reliable, but that's a future decision

## Implementation Order

1. RunLog writer type
2. Engine lifecycle events
3. Tool stdout/stderr streaming
4. REST API endpoint (including SSE tail)
5. Claude JSONL parser
6. Codex log parser
7. OpenCode log parser
8. Git activity events

## Test Strategy

- Build after every change
- Run a tool-only graph, verify run.log contains engine + tool events
- Run a claude agent graph, verify run.log contains agent tool calls
- Curl the log endpoint with filters, verify correct results
- Test SSE streaming against a live run
- Verify progress.ndjson still works alongside run.log
