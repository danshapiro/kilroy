// Layer 2 handler registration for human-in-the-loop gates.
// Implementation lives in engine/ until Phase 3.2 extraction.
package workflows

import "github.com/danshapiro/kilroy/internal/attractor/engine"

// HumanGateHandler blocks execution until a human responds via an interviewer backend.
// Type alias to engine.WaitHumanHandler — the implementation moves here in Phase 3.2.
type HumanGateHandler = engine.WaitHumanHandler
