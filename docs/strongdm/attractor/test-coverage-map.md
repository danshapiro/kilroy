# Kilroy Attractor: Spec Requirement to Test Coverage Map

This document maps spec requirements (line-numbered) to concrete Go tests in this repo. Tests are written to the spec contract (artifacts, observable behavior, and external interfaces) rather than internal implementation details where feasible.

Legend:
- `Covered (unit)`: behavior validated via unit tests.
- `Covered (integration)`: behavior validated via end-to-end-ish tests (git repo + logs layout + fake CXDB / fake provider servers).
- `Partial`: implemented but not strongly/fully asserted by tests, or test coverage is indirect.
- `Missing`: no meaningful test (and often no implementation).

Line numbers are from the current in-repo markdown specs; they will drift if the docs change.

---

## docs/strongdm/attractor/kilroy-metaspec.md

| Line | Requirement (summary) | Coverage | Test(s) / Evidence |
|---:|---|---|---|
| 60 | No implicit backend defaults; missing `backend(P)` MUST fail fast | Covered (unit) | `internal/attractor/engine/run_with_config_test.go` |
| 64 | Model metadata MUST come from OpenRouter model info | Covered (unit) | `internal/attractor/modeldb/openrouter_test.go`, `internal/attractor/modeldb/catalog_resolve_test.go` |
| 65 | Catalog is metadata-only; MUST NOT be used as provider call path | Covered (integration) | `internal/attractor/engine/model_catalog_metadata_only_test.go` |
| 69 | Repo SHOULD include a pinned snapshot of OpenRouter model info | N/A (repo content policy) | Not a runtime behavior test |
| 71 | On run start MUST materialize catalog to `{logs_root}/modeldb/openrouter_models.json` | Covered (unit) | `internal/attractor/modeldb/catalog_resolve_test.go` (snapshot file); `internal/attractor/engine/resume_catalog_test.go` (requires snapshot on resume) |
| 72 | Resume MUST use run’s snapshotted catalog | Covered (integration) | `internal/attractor/engine/resume_catalog_test.go` |
| 73 | Catalog differs from pinned snapshot MUST be recorded as warning | Covered (unit) | `internal/attractor/modeldb/catalog_resolve_warning_test.go` |
| 97 | `git` MUST be present and repo MUST be a git repo; otherwise fail fast | Covered (integration) | `internal/attractor/engine/git_requirements_test.go` |
| 98 | Dedicated per-run branch; MUST commit after each node completes | Covered (integration) | `internal/attractor/engine/engine_test.go` |
| 99 | CLI backend MUST run in isolated git worktree | Covered (integration) | `internal/attractor/engine/run_with_config_integration_test.go` |
| 103 | Each run maps to one CXDB context | Covered (integration) | `internal/attractor/engine/run_with_config_integration_test.go` (manifest has one `cxdb.context_id`, turns exist) |
| 104 | Parallel branches map to CXDB context forks (DAG), not separate logs | Covered (integration) | `internal/attractor/engine/parallel_cxdb_test.go` |
| 124-127 | Outputs: run branch `attractor/run/<run_id>`, per-node commits, logs layout, commit message format | Covered (integration) | `internal/attractor/engine/engine_test.go` |
| 132-135 | `{logs_root}/final.json` MUST include final status, final git SHA, CXDB ids | Covered (integration) | `internal/attractor/engine/engine_test.go`, `internal/attractor/engine/run_with_config_integration_test.go` |
| 139-176 | Run config file schema (YAML/JSON) | Covered (unit) | `internal/attractor/engine/config_test.go` |
| 180-182 | CLI exit codes | Covered (integration) | `cmd/kilroy/main_exit_codes_test.go` |
| 188 | `run_id` MUST be globally unique and filesystem-safe; recommended ULID | Covered (unit) | `internal/attractor/engine/runid_test.go` |
| 189-190 | Default `{logs_root}` SHOULD be outside git repo | Covered (unit) | `internal/attractor/engine/run_options_test.go` |
| 191 | `{logs_root}` MUST be recorded in CXDB | Covered (integration) | `internal/attractor/engine/run_with_config_integration_test.go` (RunStarted payload `logs_root`) |
| 195 | Run MUST refuse to start if repo has uncommitted changes | Covered (integration) | `internal/attractor/engine/git_requirements_test.go` |
| 210-215 | Per-node checkpoint commit; empty commits allowed; SHA recorded in checkpoint.json + CXDB | Covered (integration) | `internal/attractor/engine/engine_test.go` (checkpoint.json SHA), `internal/attractor/engine/run_with_config_integration_test.go` (GitCheckpoint/CheckpointSaved turns) |
| 218-223 | Resume MUST be possible from filesystem checkpoint, CXDB head, and git branch chain | Covered (integration) | `internal/attractor/engine/resume_test.go`, `internal/attractor/engine/resume_cxdb_test.go` |
| 226-227 | Resume MUST reset worktree to last checkpoint SHA | Covered (integration) | `internal/attractor/engine/resume_test.go` |
| 228 | Resume: if previous hop depended on `full` fidelity, MUST downgrade to `summary:high` for first resumed node unless session can be restored | Covered (integration) | `internal/attractor/engine/resume_fidelity_degrade_test.go` |
| 232-243 | Outcome/status canonicalization (lowercase) | Covered (unit) | `internal/attractor/runtime/status_test.go`, `internal/attractor/runtime/status_test.go` |
| 250 | Context variable `outcome` MUST be canonical lowercase | Covered (unit) | `internal/attractor/cond/cond_test.go`, `internal/attractor/engine/edge_selection_test.go` |
| 254-275 | `status.json` contract, failure_reason required, omitted optionals treated as zero values, engine sets built-in context keys | Covered (unit/integration) | `internal/attractor/engine/status_json_test.go`, `internal/attractor/runtime/status_test.go` |
| 284 | Missing `llm_provider` after stylesheet resolution MUST fail | Covered (unit) | `internal/attractor/validate/validate_test.go` |
| 295-304 | CLI backend MUST capture stdout/stderr, JSON/JSONL streams, and persist artifacts to CXDB | Covered (integration) | `internal/attractor/engine/run_with_config_integration_test.go` |
| 307-310 | Store CLI JSON/JSONL in `jj`-friendly `events.ndjson` + `events.json` | Covered (integration) | `internal/attractor/engine/run_with_config_integration_test.go` |
| 313-323 | CLI adapter MUST be non-interactive and capture replayable invocation info | Covered (integration) | `internal/attractor/engine/run_with_config_integration_test.go` (invocation json fields) |
| 324 | If CLI provides final JSON output + event stream, MUST capture both | Covered (integration) | `internal/attractor/engine/run_with_config_integration_test.go` (OpenAI CLI: `output.json` + `events.*`) |
| 362-365 | API backend codergen MUST support `one_shot` + `agent_loop` | Covered (integration) | `internal/attractor/engine/run_with_config_integration_test.go` |
| 381 | Normalized events MUST be appended to CXDB as turns | Covered (integration) | `internal/attractor/engine/run_with_config_integration_test.go` (turns exist + required type ids observed) |
| 386-398 | MUST publish CXDB type registry bundle with required types | Covered (unit) | `internal/cxdb/kilroy_registry_test.go` |
| 399-403 | Field tags MUST be numeric/unique; versions monotonic | Covered (unit) | `internal/cxdb/kilroy_registry_test.go` |
| 413-420 | Required filesystem artifacts MUST also be stored as CXDB blobs + `Artifact` turns | Covered (integration) | `internal/attractor/engine/run_with_config_integration_test.go` (Artifact names present for run + stage files) |
| 431-436 | Edge selection algorithm + stable tie-breaks | Covered (unit) | `internal/attractor/engine/edge_selection_test.go` |
| 439 | DOT parsing MUST accept quoted + unquoted values | Covered (unit) | `internal/attractor/dot/parser_test.go` |
| 443 | Failure routing uses lowercase `outcome=fail` | Covered (unit) | `internal/attractor/cond/cond_test.go`, `internal/attractor/engine/edge_selection_test.go` |
| 451-452 | Parallel branches MUST use isolated git branch/worktree and fork CXDB context | Covered (integration) | `internal/attractor/engine/parallel_test.go`, `internal/attractor/engine/parallel_cxdb_test.go` |
| 458-460 | Fan-in MUST select single winner and ff-only fast-forward main branch | Covered (integration) | `internal/attractor/engine/parallel_test.go` |

---

## docs/strongdm/attractor/attractor-spec.md

### MUST/SHOULD Requirements Outside DoD

| Line | Requirement (summary) | Coverage | Test(s) / Evidence |
|---:|---|---|---|
| 988 | Handlers MUST be stateless or synchronized | N/A | Design constraint; not directly testable in general |
| 989 | Handler panics MUST be caught by engine and converted to FAIL | Covered (unit) | `internal/attractor/engine/handler_panic_test.go` |
| 990 | Handlers SHOULD NOT embed provider-specific logic | N/A | Design constraint; not directly testable in general |

### Definition of Done (Checklist)

#### 11.1 DOT Parsing (lines 1782-1791)

| Line | DoD item | Coverage | Test(s) |
|---:|---|---|---|
| 1782 | Parser accepts supported DOT subset | Covered (unit) | `internal/attractor/dot/parser_test.go`, `internal/attractor/dot/parser_strict_test.go`, `internal/attractor/dot/syntax_errors_test.go` |
| 1783 | Graph-level attrs (`goal`, `label`, `model_stylesheet`) extracted | Covered (unit) | `internal/attractor/dot/parser_test.go` |
| 1784 | Multi-line node attributes parsed | Covered (unit) | `internal/attractor/dot/parser_test.go` |
| 1785 | Edge attributes parsed (`label`, `condition`, `weight`) | Covered (unit) | `internal/attractor/engine/edge_selection_test.go` (condition/label/weight behavior) |
| 1786 | Chained edges expand (`A -> B -> C`) | Covered (unit) | `internal/attractor/dot/parser_test.go` |
| 1787 | Node/edge default blocks apply | Covered (unit) | `internal/attractor/dot/parser_test.go` |
| 1788 | Subgraphs flattened | Covered (unit) | `internal/attractor/dot/parser_test.go` |
| 1789 | `class` merges stylesheet attributes | Covered (unit) | `internal/attractor/style/stylesheet_test.go` |
| 1790 | Quoted + unquoted values work | Covered (unit) | `internal/attractor/dot/parser_test.go` |
| 1791 | Comments stripped before parsing | Covered (unit) | `internal/attractor/dot/parser_test.go` |

#### 11.2 Validation and Linting (lines 1795-1805)

| Line | DoD item | Coverage | Test(s) |
|---:|---|---|---|
| 1795 | Exactly one start node required | Covered (unit) | `internal/attractor/validate/validate_test.go` |
| 1796 | Exactly one exit node required | Covered (unit) | `internal/attractor/validate/validate_test.go` |
| 1797 | Start node has no incoming edges | Covered (unit) | `internal/attractor/validate/validate_test.go` |
| 1798 | Exit node has no outgoing edges | Covered (unit) | `internal/attractor/validate/validate_test.go` |
| 1799 | All nodes reachable from start | Covered (unit) | `internal/attractor/validate/validate_test.go` |
| 1800 | All edges reference valid node IDs | Covered (unit) | `internal/attractor/validate/validate_test.go` |
| 1801 | Codergen nodes missing `prompt` are a warning | Covered (unit) | `internal/attractor/validate/validate_test.go` |
| 1802 | Condition expressions parse without errors | Covered (unit) | `internal/attractor/validate/validate_test.go`, `internal/attractor/cond/cond_test.go` |
| 1803 | `validate_or_raise()` throws on error-severity violations | Covered (unit) | `internal/attractor/engine/prepare_validation_test.go` |
| 1804 | Lint results include rule/severity + node/edge IDs + message | Covered (unit) | `internal/attractor/validate/validate_test.go` |

#### 11.3 Execution Engine (lines 1808-1815)

| Line | DoD item | Coverage | Test(s) |
|---:|---|---|---|
| 1808 | Engine resolves start node and begins there | Covered (integration) | `internal/attractor/engine/engine_test.go` |
| 1809 | Handler resolved via shape-to-type mapping | Covered (integration) | `internal/attractor/engine/engine_test.go` |
| 1810 | Handler called and returns Outcome | Covered (unit/integration) | `internal/attractor/engine/engine_test.go`, `internal/attractor/engine/handler_panic_test.go` |
| 1811 | Outcome written to `{logs_root}/{node_id}/status.json` | Covered (integration) | `internal/attractor/engine/engine_test.go` |
| 1812 | Edge selection follows 5-step priority | Covered (unit) | `internal/attractor/engine/edge_selection_test.go` |
| 1813 | Engine loops execute/select/advance | Covered (integration) | `internal/attractor/engine/engine_test.go` |
| 1814 | Terminal node stops execution | Covered (integration) | `internal/attractor/engine/engine_test.go` |
| 1815 | Pipeline outcome success iff all goal gates succeeded | Covered (integration) | `internal/attractor/engine/goal_gate_test.go` |

#### 11.4 Goal Gate Enforcement (lines 1819-1822)

| Line | DoD item | Coverage | Test(s) |
|---:|---|---|---|
| 1819 | `goal_gate=true` nodes are tracked | Covered (integration) | `internal/attractor/engine/goal_gate_test.go` |
| 1820 | Exit checks all goal gates succeeded | Covered (integration) | `internal/attractor/engine/goal_gate_test.go` |
| 1821 | Unsatisfied goal gate routes to `retry_target` | Covered (integration) | `internal/attractor/engine/goal_gate_test.go` |
| 1822 | No retry target -> pipeline fails | Covered (integration) | `internal/attractor/engine/goal_gate_test.go` |

#### 11.5 Retry Logic (lines 1826-1831)

| Line | DoD item | Coverage | Test(s) |
|---:|---|---|---|
| 1826 | `max_retries>0` retried on RETRY or FAIL | Covered (integration) | `internal/attractor/engine/retry_policy_test.go`, `internal/attractor/engine/retry_on_retry_status_test.go` |
| 1827 | Retry count tracked per-node and respects limit | Covered (integration) | `internal/attractor/engine/retry_on_retry_status_test.go`, `internal/attractor/engine/retry_exhaustion_routing_test.go` (asserts checkpoint `node_retries`) |
| 1828 | Backoff works (constant/linear/exponential as configured) | Covered (unit) | `internal/attractor/engine/backoff_test.go` (`retry.backoff.*` config + delay calculation) |
| 1829 | Jitter applied when configured | Covered (unit) | `internal/attractor/engine/backoff_test.go` |
| 1830 | After retry exhaustion, final outcome used for edge selection | Covered (integration) | `internal/attractor/engine/retry_exhaustion_routing_test.go` |

#### 11.6 Node Handlers (lines 1834-1842)

| Line | DoD item | Coverage | Test(s) |
|---:|---|---|---|
| 1834 | Start handler returns SUCCESS immediately | Covered (integration) | `internal/attractor/engine/engine_test.go` (start/status.json) |
| 1835 | Exit handler returns SUCCESS immediately | Covered (integration) | `internal/attractor/engine/engine_test.go` (exit/status.json) |
| 1836 | Codergen handler expands `$goal`, writes prompt/response | Covered (integration) | `internal/attractor/engine/engine_test.go`, `internal/attractor/engine/prepare_test.go` |
| 1837 | Wait.human presents choices, returns preferred_label | Covered (integration) | `internal/attractor/engine/wait_human_test.go`, `internal/attractor/engine/interviewer_test.go` |
| 1838 | Conditional handler pass-through | Covered (integration) | `internal/attractor/engine/conditional_passthrough_test.go` |
| 1839 | Parallel fan-out handler | Covered (integration) | `internal/attractor/engine/parallel_test.go` |
| 1840 | Fan-in waits/consolidates | Covered (integration) | `internal/attractor/engine/parallel_test.go` |
| 1841 | Tool handler executes command and returns result | Covered (integration) | `internal/attractor/engine/status_json_test.go`, `internal/attractor/engine/retry_policy_test.go` |
| 1842 | Custom handlers can be registered by type string | Covered (unit/integration) | `internal/attractor/engine/handler_panic_test.go`, `internal/attractor/engine/context_updates_test.go` |

#### 11.7 State and Context (lines 1846-1852)

| Line | DoD item | Coverage | Test(s) |
|---:|---|---|---|
| 1846 | Context is key-value store | Covered (unit) | `internal/attractor/runtime/context_test.go` |
| 1847 | Handlers read context and return `context_updates` | Covered (unit) | `internal/attractor/engine/context_updates_test.go` |
| 1848 | Context updates merged after node execution | Covered (integration) | `internal/attractor/engine/context_updates_test.go` |
| 1849 | Checkpoint saved after each node completion | Covered (integration) | `internal/attractor/engine/engine_test.go` |
| 1850 | Resume from checkpoint works | Covered (integration) | `internal/attractor/engine/resume_test.go` |
| 1851 | Artifacts written to `{logs_root}/{node_id}/` | Covered (integration) | `internal/attractor/engine/engine_test.go` |

#### 11.8 Human-in-the-Loop (lines 1855-1861)

| Line | DoD item | Coverage | Test(s) |
|---:|---|---|---|
| 1855 | Interviewer interface `ask(question)->Answer` | Covered (unit) | `internal/attractor/engine/interviewer_test.go` |
| 1856 | Question types: SINGLE/MULTI/FREE_TEXT/CONFIRM | Covered (unit) | `internal/attractor/engine/interviewer_test.go` |
| 1857 | AutoApprove selects first option | Covered (unit) | `internal/attractor/engine/interviewer_test.go` |
| 1858 | ConsoleInterviewer prompts + reads stdin | Covered (unit) | `internal/attractor/engine/interviewer_test.go` |
| 1859 | CallbackInterviewer delegates to fn | Covered (unit) | `internal/attractor/engine/interviewer_test.go` |
| 1860 | QueueInterviewer reads from answer queue | Covered (unit/integration) | `internal/attractor/engine/interviewer_test.go`, `internal/attractor/engine/wait_human_test.go` |

#### 11.9 Condition Expressions (lines 1864-1871)

| Line | DoD item | Coverage | Test(s) |
|---:|---|---|---|
| 1864 | `=` string equals works | Covered (unit) | `internal/attractor/cond/cond_test.go` |
| 1865 | `!=` works | Covered (unit) | `internal/attractor/cond/cond_test.go` |
| 1866 | `&&` conjunction works | Covered (unit) | `internal/attractor/cond/cond_test.go` |
| 1867 | `outcome` resolves to current status | Covered (unit) | `internal/attractor/cond/cond_test.go` |
| 1868 | `preferred_label` resolves correctly | Covered (unit) | `internal/attractor/engine/edge_selection_test.go` |
| 1869 | `context.*` variables resolve (missing -> empty) | Covered (unit) | `internal/attractor/cond/cond_test.go` |
| 1870 | Empty condition evaluates to true | Covered (unit) | `internal/attractor/cond/cond_test.go` |

#### 11.10 Model Stylesheet (lines 1874-1880)

| Line | DoD item | Coverage | Test(s) |
|---:|---|---|---|
| 1874 | Stylesheet parsed from `model_stylesheet` | Covered (unit) | `internal/attractor/style/stylesheet_test.go` |
| 1875 | Shape selectors work | Covered (unit) | `internal/attractor/style/stylesheet_test.go` |
| 1876 | Class selectors work | Covered (unit) | `internal/attractor/style/stylesheet_test.go` |
| 1877 | ID selectors work | Covered (unit) | `internal/attractor/style/stylesheet_test.go` |
| 1878 | Specificity order correct | Covered (unit) | `internal/attractor/style/stylesheet_test.go` |
| 1879 | Explicit node attrs override stylesheet | Covered (unit) | `internal/attractor/style/stylesheet_test.go` |

#### 11.11 Transforms and Extensibility (lines 1883-1888)

| Line | DoD item | Coverage | Test(s) |
|---:|---|---|---|
| 1883 | AST transforms can modify Graph between parse and validate | Covered (unit) | `internal/attractor/engine/transforms_test.go` |
| 1884 | Transform interface exists | Covered (unit) | `internal/attractor/engine/transforms_test.go` |
| 1885 | Built-in `$goal` expansion transform | Covered (unit) | `internal/attractor/engine/prepare_test.go` |
| 1886 | Custom transforms can be registered/run in order | Covered (unit) | `internal/attractor/engine/transforms_test.go` |
| 1887 | HTTP server mode (if implemented) | N/A | Not implemented in this repo |

---

## docs/strongdm/attractor/coding-agent-loop-spec.md

### MUST Requirements Outside DoD

| Line | Requirement (summary) | Coverage | Test(s) / Evidence |
|---:|---|---|---|
| 843 | Tool output MUST be truncated before sending to LLM; full output available via `TOOL_CALL_END` | Covered (unit/integration) | `internal/agent/tool_registry_test.go`, `internal/agent/session_test.go` |
| 889 | Character truncation MUST run first; line truncation second | Covered (unit) | `internal/agent/tool_registry_test.go` |
| 960-971 | Context window awareness warning at ~80% usage | Covered (unit) | `internal/agent/session_dod_test.go` |
| 978-989 | System prompt is layered; user override appended last | Covered (unit) | `internal/agent/session_test.go` (override appended last) |
| 1001-1018 | Environment context block included in every system prompt | Covered (unit) | `internal/agent/session_test.go` |
| 1020-1027 | Git snapshot at session start included in prompts | Covered (unit) | `internal/agent/session_test.go` |
| 1031-1046 | Project doc discovery from git root to cwd; provider filtering; 32KB budget | Covered (unit) | `internal/agent/project_docs_test.go`, `internal/agent/session_test.go` |

### Definition of Done (Checklist)

#### 9.1 Core Loop (lines 1141-1149)

| Line | DoD item | Coverage | Test(s) |
|---:|---|---|---|
| 1141 | Session can be created with ProviderProfile and ExecutionEnvironment | Covered (unit) | `internal/agent/session_test.go`, `internal/agent/session_dod_test.go` |
| 1142 | `process_input()` runs loop (LLM -> tools -> completion) | Covered (unit) | `internal/agent/session_test.go` |
| 1143 | Natural completion exits on text-only response | Covered (unit) | `internal/agent/session_test.go` |
| 1144 | Round limits stop the loop | Covered (unit) | `internal/agent/session_dod_test.go` |
| 1145 | Session turn limits stop across inputs | Covered (unit) | `internal/agent/session_dod_test.go` |
| 1146 | Abort signal cancels loop and closes session | Covered (unit) | `internal/agent/session_dod_test.go` |
| 1147 | Loop detection injects warning SteeringTurn | Covered (unit) | `internal/agent/session_test.go` (inject + event + SteeringTurn history) |
| 1148 | Multiple sequential inputs work | Covered (unit) | `internal/agent/session_dod_test.go` |

#### 9.2 Provider Profiles (lines 1152-1158)

| Line | DoD item | Coverage | Test(s) |
|---:|---|---|---|
| 1152 | OpenAI profile toolset includes `apply_patch` | Covered (unit) | `internal/agent/profile_test.go`, `internal/agent/apply_patch_test.go` |
| 1153 | Anthropic profile toolset includes `edit_file` | Covered (unit) | `internal/agent/profile_test.go` |
| 1154 | Gemini profile provides gemini-cli-aligned tools | Covered (unit) | `internal/agent/profile_test.go` (exact tool list asserted vs spec Section 3.6) |
| 1155 | Provider-specific system prompts exist | Covered (unit) | `internal/agent/profile_test.go` |
| 1156 | Custom tools can be registered on top of any profile | Covered (unit) | `internal/agent/session_dod_test.go` |
| 1157 | Tool name collisions: custom overrides defaults | Covered (unit) | `internal/agent/session_dod_test.go` |

#### 9.3 Tool Execution (lines 1161-1165)

| Line | DoD item | Coverage | Test(s) |
|---:|---|---|---|
| 1161 | Tool calls dispatched through ToolRegistry | Covered (unit) | `internal/agent/session_test.go` |
| 1162 | Unknown tool -> error result | Covered (unit) | `internal/agent/tool_registry_test.go` |
| 1163 | Tool argument JSON parsed + schema validated | Covered (unit) | `internal/agent/tool_registry_test.go` |
| 1164 | Tool exec errors returned as `is_error=true` | Covered (unit) | `internal/agent/tool_registry_test.go` |
| 1165 | Parallel tool execution when supported | Covered (unit) | `internal/agent/session_test.go` |

#### 9.4 Execution Environment (lines 1169-1175)

| Line | DoD item | Coverage | Test(s) |
|---:|---|---|---|
| 1169 | LocalExecutionEnvironment implements file + command ops | Covered (unit) | `internal/agent/env_local_test.go` |
| 1170 | Default command timeout 10s | Covered (unit) | `internal/agent/session_dod_test.go` |
| 1171 | Per-call timeout override via `timeout_ms` | Covered (unit) | `internal/agent/session_dod_test.go` |
| 1172 | Timeout sends SIGTERM then SIGKILL after 2s | Covered (unit) | `internal/agent/env_local_test.go` |
| 1173 | Sensitive env vars filtered | Covered (unit) | `internal/agent/env_local_test.go` |
| 1174 | ExecutionEnvironment is implementable | Covered (unit) | Tests use custom env fakes in `internal/agent/session_dod_test.go` |

#### 9.5 Tool Output Truncation (lines 1178-1184)

| Line | DoD item | Coverage | Test(s) |
|---:|---|---|---|
| 1178 | Character truncation runs first | Covered (unit) | `internal/agent/tool_registry_test.go` |
| 1179 | Line truncation runs second where configured | Covered (unit) | `internal/agent/tool_registry_test.go`, `internal/agent/session_test.go` |
| 1180 | Truncation inserts visible warning marker | Covered (unit) | `internal/agent/tool_registry_test.go` |
| 1181 | Full output available via TOOL_CALL_END | Covered (unit) | `internal/agent/session_test.go` |
| 1182 | Default limits match table | Covered (unit) | `internal/agent/tool_registry_test.go` |
| 1183 | Limits overridable via SessionConfig | Covered (unit) | `internal/agent/session_test.go` |

#### 9.6 Steering (lines 1187-1191)

| Line | DoD item | Coverage | Test(s) |
|---:|---|---|---|
| 1187 | `steer()` injected after current tool round | Covered (unit) | `internal/agent/session_dod_test.go` |
| 1188 | `follow_up()` processed after input completes | Covered (unit) | `internal/agent/session_test.go` |
| 1189 | Steering messages appear as SteeringTurn in history | Covered (unit) | `internal/agent/session_dod_test.go` |
| 1190 | SteeringTurns converted to user-role messages for LLM | Covered (unit) | `internal/agent/session_dod_test.go` (injected into next request) |

#### 9.7 Reasoning Effort (lines 1194-1197)

| Line | DoD item | Coverage | Test(s) |
|---:|---|---|---|
| 1194 | reasoning_effort passed through | Covered (unit) | `internal/agent/session_dod_test.go` |
| 1195 | Changing reasoning_effort takes effect next call | Covered (unit) | `internal/agent/session_dod_test.go` |

#### 9.8 System Prompts (lines 1200-1206)

| Line | DoD item | Coverage | Test(s) |
|---:|---|---|---|
| 1200 | Provider-specific base instructions included | Covered (unit) | `internal/agent/profile_test.go` |
| 1201 | Environment context included | Covered (unit) | `internal/agent/session_test.go` |
| 1202 | Tool descriptions included | Covered (unit) | `internal/agent/session_test.go` |
| 1203 | Project docs discovered + included | Covered (unit) | `internal/agent/project_docs_test.go`, `internal/agent/session_test.go` |
| 1204 | User overrides appended last | Covered (unit) | `internal/agent/session_test.go` |
| 1205 | Only relevant provider docs loaded | Covered (unit) | `internal/agent/session_test.go` |

#### 9.9 Subagents (lines 1209-1215)

| Line | DoD item | Coverage | Test(s) |
|---:|---|---|---|
| 1209 | spawn_agent works | Covered (unit) | `internal/agent/session_dod_test.go` |
| 1210 | Subagents share parent env | Covered (unit) | `internal/agent/session_dod_test.go` |
| 1211 | Subagents have independent history | Covered (unit) | `internal/agent/session_dod_test.go` |
| 1212 | Depth limiting prevents recursion | Covered (unit) | `internal/agent/session_dod_test.go` |
| 1213 | Subagent results returned as tool results | Covered (unit) | `internal/agent/session_dod_test.go` |
| 1214 | send_input/wait/close_agent work | Covered (unit) | `internal/agent/session_dod_test.go` |

#### 9.10 Event System (lines 1218-1222)

| Line | DoD item | Coverage | Test(s) |
|---:|---|---|---|
| 1218 | All event kinds emitted at correct times | Covered (unit) | `internal/agent/session_dod_test.go`, `internal/agent/session_test.go` |
| 1219 | Events delivered via async iterator equivalent | Covered (unit) | All tests consume `Session.Events()` channel |
| 1220 | TOOL_CALL_END carries full untruncated tool output | Covered (unit) | `internal/agent/session_test.go` |
| 1221 | SESSION_START/END bracket session | Covered (unit) | `internal/agent/session_dod_test.go` |

#### 9.11 Error Handling (lines 1225-1229)

| Line | DoD item | Coverage | Test(s) |
|---:|---|---|---|
| 1225 | Tool errors -> error result to model | Covered (unit) | `internal/agent/tool_registry_test.go` |
| 1226 | LLM transient errors -> retry/backoff (Unified LLM layer) | Covered (unit) | `internal/llm/client_test.go` |
| 1227 | Auth errors surface immediately; session closes | Covered (unit) | `internal/agent/session_dod_test.go` |
| 1228 | Context overflow emits warning (no compaction) | Covered (unit) | `internal/agent/session_dod_test.go` |
| 1229 | Graceful shutdown on abort | Covered (unit) | `internal/agent/session_dod_test.go`, `internal/agent/env_local_test.go` |

---

## docs/strongdm/attractor/unified-llm-spec.md

### MUST Requirements Outside DoD

| Line | Requirement (summary) | Coverage | Test(s) / Evidence |
|---:|---|---|---|
| 212 | Provider adapters MUST use provider native APIs (not compatibility shim) | Covered (integration-ish unit) | `internal/llm/providers/openai/adapter_test.go`, `internal/llm/providers/anthropic/adapter_test.go`, `internal/llm/providers/google/adapter_test.go` |

### Definition of Done (Checklist)

#### 8.1 Core Infrastructure (lines 1973-1981)

| Line | DoD item | Coverage | Test(s) |
|---:|---|---|---|
| 1973 | Client constructable from env vars | Covered (unit) | `internal/llmclient/env_test.go` |
| 1974 | Client constructable with explicit adapters | Covered (unit) | `internal/llm/client_test.go` |
| 1975 | Provider routing by request.provider | Covered (unit) | `internal/llm/client_test.go` |
| 1976 | Default provider used when provider omitted | Covered (unit) | `internal/llm/client_test.go` |
| 1977 | ConfigurationError when no/default provider configured | Covered (unit) | `internal/llm/client_test.go` |
| 1978 | Middleware chain order | Covered (unit) | `internal/llm/client_test.go` |
| 1979 | Module-level default client (`set_default_client`) | Covered (unit) | `internal/llm/env_registry_test.go` |
| 1980 | Model catalog + `get_model_info` / `list_models` | Covered (unit) | `internal/llm/model_catalog_test.go` |

#### 8.2 Provider Adapters (lines 1986-1996)

| Line | DoD item | Coverage | Test(s) |
|---:|---|---|---|
| 1986 | Native APIs (OpenAI Responses, Anthropic Messages, Gemini API) | Covered (unit) | `internal/llm/providers/*/adapter_test.go` |
| 1988 | `complete()` returns populated Response | Covered (unit) | `internal/llm/providers/*/adapter_test.go` |
| 1989 | `stream()` yields StreamEvent iterator | Covered (unit) | `internal/llm/providers/*/adapter_test.go` |
| 1990 | System messages handled per provider | Covered (unit) | `internal/llm/providers/*/adapter_test.go` |
| 1991 | Roles translated (system/user/assistant/tool/developer) | Covered (unit) | `internal/llm/providers/*/adapter_test.go` |
| 1992 | provider_options escape hatch | Covered (unit) | `internal/llm/providers/*/adapter_test.go` |
| 1993 | Beta headers supported (Anthropic) | Covered (unit) | `internal/llm/providers/anthropic/adapter_test.go` |
| 1994 | HTTP errors map to error hierarchy | Covered (unit) | `internal/llm/errors_test.go`, `internal/llm/providers/*/adapter_test.go` |
| 1995 | Retry-After parsed and set on error | Covered (unit) | `internal/llm/errors_test.go`, `internal/llm/providers/openai/adapter_test.go`, `internal/llm/providers/google/adapter_test.go` |

#### 8.3 Message & Content Model (lines 1999-2005)

| Line | DoD item | Coverage | Test(s) |
|---:|---|---|---|
| 1999 | Text-only messages work across all providers | Covered (unit) | `internal/llm/providers/openai/adapter_test.go`, `internal/llm/providers/anthropic/adapter_test.go`, `internal/llm/providers/google/adapter_test.go` |
| 2000 | Image input works (URL, base64, local path) | Covered (unit) | `internal/llm/providers/openai/adapter_test.go`, `internal/llm/providers/anthropic/adapter_test.go`, `internal/llm/providers/google/adapter_test.go` (`TestAdapter_Complete_ImageInput_URL_Data_AndFilePath`) |
| 2001 | Audio/document parts handled or rejected | Covered (unit) | `internal/llm/providers/openai/adapter_test.go`, `internal/llm/providers/anthropic/adapter_test.go`, `internal/llm/providers/google/adapter_test.go` (`TestAdapter_Complete_RejectsAudioAndDocumentParts`) |
| 2002 | Tool call parts round-trip correctly | Covered (unit) | `internal/llm/generate_test.go` (`TestGenerate_ToolLoop_ExecutesToolsAndContinues`) |
| 2003 | Thinking blocks preserved and round-tripped with signatures | Covered (unit) | `internal/llm/providers/anthropic/adapter_test.go` (`TestAdapter_ThinkingBlocks_RoundTripIncludingRedacted`) |
| 2004 | Redacted thinking passed through verbatim | Covered (unit) | `internal/llm/providers/anthropic/adapter_test.go` (`TestAdapter_ThinkingBlocks_RoundTripIncludingRedacted`) |
| 2005 | Multimodal messages (text + images) work | Covered (unit) | `internal/llm/providers/*/adapter_test.go` (`TestAdapter_Complete_ImageInput_URL_Data_AndFilePath`) |

#### 8.4 Generation (lines 2009-2019)

| Line | DoD item | Coverage | Test(s) |
|---:|---|---|---|
| 2009 | `generate()` works with simple text `prompt` | Covered (unit) | `internal/llm/generate_test.go` (`TestGenerate_SimplePrompt`) |
| 2010 | `generate()` works with full `messages` list | Covered (unit) | `internal/llm/generate_test.go` (`TestGenerate_MessagesList`) |
| 2011 | `generate()` rejects when both `prompt` and `messages` provided | Covered (unit) | `internal/llm/generate_test.go` (`TestGenerate_RejectsPromptAndMessagesTogether`) |
| 2012 | Streaming yields `TEXT_DELTA` that concatenates to full response | Covered (unit) | `internal/llm/stream_generate_test.go` (`TestStreamGenerate_SimpleStreaming_YieldsDeltasAndFinish`) |
| 2013 | Streaming yields `STREAM_START` and `FINISH` events | Covered (unit) | `internal/llm/stream_generate_test.go`, `internal/llm/providers/*/adapter_test.go` |
| 2014 | Streaming follows start/delta/end pattern for text segments | Covered (unit) | `internal/llm/stream_generate_test.go`, `internal/llm/providers/*/adapter_test.go` |
| 2015 | `generate_object()` returns parsed, validated structured output | Covered (unit) | `internal/llm/generate_object_test.go` |
| 2016 | `generate_object()` raises `NoObjectGeneratedError` on parse/validation failure | Covered (unit) | `internal/llm/generate_object_test.go` |
| 2017 | Cancellation works for `generate()` and streaming | Covered (unit) | `internal/llm/generate_test.go` (`TestGenerate_Cancellation_ReturnsAbortError`), `internal/llm/stream_generate_test.go` (`TestStreamGenerate_Cancellation_EmitsAbortError`) |
| 2018 | Timeouts work (total + per-step) | Covered (unit) | `internal/llm/generate_test.go` (`TestGenerate_Timeout*`), `internal/llm/stream_generate_test.go` (`TestStreamGenerate_Timeout*`) |

#### 8.5 Reasoning Tokens (lines 2022-2028)

| Line | DoD item | Coverage | Test(s) |
|---:|---|---|---|
| 2022 | OpenAI reasoning models return `reasoning_tokens` via Responses API | Covered (unit) | `internal/llm/providers/openai/adapter_test.go` (`TestAdapter_Complete_Usage_MapsReasoningAndCacheTokens`) |
| 2023 | `reasoning_effort` passed through correctly | Covered (unit) | `internal/llm/providers/openai/adapter_test.go` |
| 2024-2025 | Anthropic thinking blocks returned/preserved with signatures | Covered (unit) | `internal/llm/providers/anthropic/adapter_test.go` (`TestAdapter_ThinkingBlocks_RoundTripIncludingRedacted`) |
| 2026 | Gemini `thoughtsTokenCount` mapped to `reasoning_tokens` | Covered (unit) | `internal/llm/providers/google/adapter_test.go` (`TestAdapter_Complete_Usage_MapsReasoningAndCacheTokens`) |
| 2027 | `Usage` reports `reasoning_tokens` separate from `output_tokens` | Covered (unit) | `internal/llm/providers/openai/adapter_test.go`, `internal/llm/providers/google/adapter_test.go` |

#### 8.6 Prompt Caching (lines 2031-2039)

| Line | DoD item | Coverage | Test(s) |
|---:|---|---|---|
| 2032 | OpenAI `cache_read_tokens` populated from Responses usage details | Covered (unit) | `internal/llm/providers/openai/adapter_test.go` (`TestAdapter_Complete_Usage_MapsReasoningAndCacheTokens`) |
| 2033-2036 | Anthropic auto-cache injects cache_control + beta header; disable via provider_options | Covered (unit) | `internal/llm/providers/anthropic/adapter_test.go` (`TestAdapter_PromptCaching_AutoCacheDefaultAndDisable`) |
| 2035 | Anthropic `cache_read_tokens` and `cache_write_tokens` populated | Covered (unit) | `internal/llm/providers/anthropic/adapter_test.go` |
| 2038 | Gemini `cache_read_tokens` populated from `cachedContentTokenCount` | Covered (unit) | `internal/llm/providers/google/adapter_test.go` (`TestAdapter_Complete_Usage_MapsReasoningAndCacheTokens`) |
| 2039 | Multi-turn session shows significant cache_read_tokens (>50%) | Manual | Requires real providers; see remaining items note below |

#### 8.7 Tool Calling (lines 2043-2054)

| Line | DoD item | Coverage | Test(s) |
|---:|---|---|---|
| 2043 | Active tools trigger automatic tool execution loops | Covered (unit) | `internal/llm/generate_test.go` (`TestGenerate_ToolLoop_ExecutesToolsAndContinues`) |
| 2044 | Passive tools return tool calls without looping | Covered (unit) | `internal/llm/generate_test.go` (`TestGenerate_PassiveToolCall_ReturnsToolCallsWithoutLooping`), `internal/llm/stream_generate_test.go` (passive tool streaming coverage) |
| 2045-2046 | `max_tool_rounds` respected; 0 disables auto execution | Covered (unit) | `internal/llm/generate_test.go`, `internal/llm/stream_generate_test.go` |
| 2047-2048 | Parallel tool calls executed concurrently and results sent in one continuation | Covered (unit) | `internal/llm/generate_test.go` (`TestGenerate_ParallelToolCalls_ExecuteConcurrently`) |
| 2049-2050 | Tool errors and unknown tools sent as error results (not exceptions) | Covered (unit) | `internal/llm/generate_test.go` (`TestGenerate_UnknownToolCall_SendsErrorResultToModel`, `TestGenerate_ToolArgsSchemaValidationError_*`) |
| 2051 | ToolChoice modes translated per provider | Covered (unit) | `internal/llm/providers/*/adapter_test.go` (`TestAdapter_Complete_ToolChoice_MappedPerSpec`) |
| 2052 | Tool call argument JSON parsed + validated before exec | Covered (unit) | `internal/llm/tool_validation_test.go`, `internal/llm/generate_test.go` |
| 2053 | `StepResult` tracks step tool calls/results/usage | Covered (unit) | `internal/llm/generate_test.go` |

#### 8.8 Error Handling & Retry (lines 2057-2066)

| Line | DoD item | Coverage | Test(s) |
|---:|---|---|---|
| 2057 | Error hierarchy raised for correct status codes | Covered (unit) | `internal/llm/errors_test.go` |
| 2058 | retryable flag correct | Covered (unit) | `internal/llm/errors_test.go` |
| 2059 | Exponential backoff with jitter works | Covered (unit) | `internal/llm/client_test.go` |
| 2060 | Retry-After overrides calculated backoff | Covered (unit) | `internal/llm/client_test.go` |
| 2061 | max_retries=0 disables retries | Covered (unit) | `internal/llm/client_test.go` |
| 2062 | 429 retried transparently | Covered (unit) | `internal/llm/client_test.go` |
| 2063 | Non-retryable errors not retried | Covered (unit) | `internal/llm/client_test.go` |
| 2064 | Retries apply per-step (not whole multi-step op) | Covered (unit) | `internal/llm/generate_test.go` |
| 2065 | Streaming does not retry after partial data delivered | Covered (unit) | `internal/llm/stream_generate_test.go` |

Remaining items that require real-provider integration (not unit-testable offline):
- Unified LLM DoD 8.6 “multi-turn agentic session cache_read_tokens > 50%” across real providers
- Unified LLM DoD 8.10 integration smoke test with real API keys
