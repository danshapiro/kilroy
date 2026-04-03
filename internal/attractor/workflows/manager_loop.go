// Layer 2 handler registration for supervisor/manager loop nodes.
// Implementation lives in engine/ until Phase 3.4 extraction.
package workflows

import "github.com/danshapiro/kilroy/internal/attractor/engine"

// ManagerLoopHandler runs an observe/wait loop that monitors a child pipeline.
// Type alias to engine.ManagerLoopHandler — the implementation moves here in Phase 3.4.
type ManagerLoopHandler = engine.ManagerLoopHandler
