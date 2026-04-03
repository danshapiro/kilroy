// Layer 1 handler registration for LLM agent nodes.
// Implementation lives in engine/ until Phase 2.1 extraction.
package agents

import "github.com/danshapiro/kilroy/internal/attractor/engine"

// AgentHandler invokes an LLM agent to perform a task (renamed from CodergenHandler).
// Type alias to engine.CodergenHandler — the implementation moves here in Phase 2.1.
type AgentHandler = engine.CodergenHandler
