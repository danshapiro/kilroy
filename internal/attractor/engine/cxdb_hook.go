// CXDB integration boundary documentation.
//
// CXDB (context exchange database) provides optional telemetry, event
// storage, and artifact management. The engine already treats it as
// optional via nil-safe methods — all cxdbXxx methods on *Engine check
// for nil CXDB before operating.
//
// Current state: 6 engine files import the cxdb package directly.
// The CXDBSink type wraps the cxdb.Client and is set on Engine during
// bootstrap. When Engine.CXDB is nil, all event emission is skipped.
//
// The DisableCXDB RunOptions flag controls whether CXDB is initialized.
// When true, Engine.CXDB remains nil and all CXDB operations are no-ops.
//
// Full extraction into a separate package (removing cxdb imports from
// engine/) would require:
// 1. Define CXDBOps interface mirroring CXDBSink methods
// 2. Move CXDBSink creation to cmd/kilroy/ or a bootstrap package
// 3. Pass CXDBOps through RunOptions (similar to GitOps pattern)
// 4. Move cxdb_events.go, cxdb_bootstrap.go, cxdb_helpers.go out
// 5. Update resume_sources.go CXDB-based resume path
//
// This is deferred because the nil-safe pattern already provides the
// "zero CXDB overhead when disabled" behavior, and the extraction
// surface is large (30+ event methods, streaming integration, resume).
package engine
