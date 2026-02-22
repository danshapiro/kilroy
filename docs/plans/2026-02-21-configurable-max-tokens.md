# Configurable `max_tokens` for LLM API Calls

**Date:** 2026-02-21
**Status:** Proposed
**Problem:** The Anthropic provider adapter hardcodes `max_tokens: 4096`. When an agent
emits a large tool call (e.g., `write_file` with 80KB+ content), the output is truncated
mid-JSON, causing schema validation failures (`missing properties: 'content'`). This
creates unrecoverable error loops.

## Goal

Make `max_tokens` configurable at three levels (highest priority wins):

1. **Node attribute** in the pipeline DOT file (per-node override)
2. **Model stylesheet** in the DOT graph attributes (per-class/selector)
3. **Provider adapter default** (bumped from 4096 to 65536)

This follows the exact same pattern as `reasoning_effort`, which is already configurable
at levels 1 and 2.

## Design

### Level of effort: Small

All plumbing already exists. `llm.Request.MaxTokens` is defined and respected by every
provider adapter. The only missing piece is reading it from node attributes and the
stylesheet, then passing it through to the request.

## Changes

### 1. Add `max_tokens` to stylesheet whitelist

**File:** `internal/attractor/style/stylesheet.go` ~line 148

The `props` slice controls which properties are applied from the stylesheet to nodes.
Add `"max_tokens"`:

```go
// Before
props := []string{"llm_model", "llm_provider", "reasoning_effort"}

// After
props := []string{"llm_model", "llm_provider", "reasoning_effort", "max_tokens"}
```

### 2. Read `max_tokens` from node attrs in CoderGen router

**File:** `internal/attractor/engine/codergen_router.go` ~line 172

After the existing `reasoning_effort` extraction, add `max_tokens` extraction:

```go
// Existing pattern for reasoning_effort (around line 172):
reasoning := node.Attr("reasoning_effort", "")

// Add after:
maxTokensStr := node.Attr("max_tokens", "")
var maxTokensPtr *int
if maxTokensStr != "" {
    if v, err := strconv.Atoi(maxTokensStr); err == nil && v > 0 {
        maxTokensPtr = &v
    }
}
```

Then pass it to the request in the **one_shot path** (~line 180):

```go
req := llm.Request{
    Provider:        prov,
    Model:           mid,
    Messages:        []llm.Message{llm.User(prompt)},
    ReasoningEffort: reasoningPtr,
    MaxTokens:       maxTokensPtr,  // <-- add this
}
```

And in the **agent_loop path** — find where `SessionConfig` is built (~line 235) and
add `MaxTokens` there too. This requires a small addition to `SessionConfig` (see step 3).

### 3. Add `MaxTokens` to agent SessionConfig

**File:** `internal/agent/session.go`

Add a `MaxTokens *int` field to the session config struct (find the struct definition
that holds `ReasoningEffort`). Then apply it when building the `llm.Request` (~line 466):

```go
// Existing pattern:
if strings.TrimSpace(s.cfg.ReasoningEffort) != "" {
    v := strings.TrimSpace(s.cfg.ReasoningEffort)
    req.ReasoningEffort = &v
}

// Add after:
if s.cfg.MaxTokens != nil {
    req.MaxTokens = s.cfg.MaxTokens
}
```

### 4. Wire it up in CoderGen agent_loop session creation

**File:** `internal/attractor/engine/codergen_router.go`

Where `SessionConfig` is populated for agent_loop execution (~line 235):

```go
sessCfg.ReasoningEffort = reasoning
sessCfg.MaxTokens = maxTokensPtr  // <-- add this
```

### 5. Bump Anthropic provider adapter default to 64K

**File:** `internal/llm/providers/anthropic/adapter.go` lines 97 and 270

Both the synchronous and streaming paths hardcode `maxTokens := 4096`. Change both to
`maxTokens := 65536` so that all Anthropic API calls get a generous default, eliminating
truncation for large tool calls even without per-node configuration:

```go
// Before (lines 97 and 270)
maxTokens := 4096

// After
maxTokens := 65536
```

Both paths already check `req.MaxTokens` and use it when non-nil, so per-node overrides
still take priority. Google's adapter (`adapter.go:97-101`) is left unchanged (its 2048
default is appropriate for its usage pattern).

## Usage

### Per-node in DOT file

```dot
expand_spec [
    shape=box,
    max_tokens=32768
]
```

### Via model stylesheet (per-class)

```dot
graph [
    model_stylesheet="
        * { llm_model: claude-sonnet-4-6; llm_provider: anthropic; }
        .hard { llm_model: claude-opus-4-6; max_tokens: 32768; }
    "
]
```

### Priority order

1. Explicit node attribute (`max_tokens=32768` on the node) — wins
2. Stylesheet match (`.hard { max_tokens: 32768; }`) — applied only if node attr missing
3. Provider adapter default (65536 for Anthropic) — fallback

This matches the existing stylesheet semantics: `ApplyStylesheet` only sets properties
that are **missing** from node attrs (see `stylesheet.go` line 50).

## Files to modify

| File | Change |
|------|--------|
| `internal/attractor/style/stylesheet.go` | Add `"max_tokens"` to props whitelist |
| `internal/attractor/engine/codergen_router.go` | Read `max_tokens` attr, pass to Request and SessionConfig |
| `internal/agent/session.go` (or wherever SessionConfig is defined) | Add `MaxTokens *int` field, apply to Request |
| `internal/llm/providers/anthropic/adapter.go` | Bump default `maxTokens` from 4096 to 65536 (lines 97, 270) |

## Testing

1. Unit test: stylesheet with `max_tokens` property is parsed and applied to nodes
2. Unit test: `max_tokens` node attr is read and converted to `*int` correctly
3. Integration: run a pipeline with `max_tokens=32768` on a node and verify the API
   request includes the correct value (check CXDB turn data or provider logs)

