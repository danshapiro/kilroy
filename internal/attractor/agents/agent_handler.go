// Layer 1 handler registration for LLM agent nodes.
// Two implementations available: AgentHandler (subprocess/API, existing) and
// TmuxAgentHandler (tmux sessions, new). cmd/kilroy/ selects based on config.
package agents

import "github.com/danshapiro/kilroy/internal/attractor/engine"

// AgentHandler invokes an LLM agent via the existing subprocess/API backend.
// Retained for backward compatibility with run configs that specify API backends.
type AgentHandler = engine.CodergenHandler
