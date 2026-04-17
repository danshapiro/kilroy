# Platform Reframe Phase 1.1 - Lab Notes

Date: 2026-04-03

## Goal
Create layered package structure: engine/ (L0), agents/ (L1), workflows/ (L2).
Extract L1/L2 handlers from engine. Wire registration in cmd/kilroy/. Rename codergen→agent.

## Key Coupling Points Discovered

### CodergenHandler.Execute() accesses (from handlers.go:415):
- `exec.Engine.lastResolvedFidelity` (unexported field)
- `exec.Engine.Options.RunID`
- `exec.Engine.CodergenBackend` (field)
- `exec.Engine.cxdbPrompt()` (unexported method)
- `exec.Engine.appendProgress()` (unexported method)
- `buildFidelityPreamble()` (unexported function in fidelity_preamble.go)
- `buildStageRuntimeEnv()` (unexported in node_env.go)
- `mustRenderInputMaterializationPromptPreamble()` (unexported)
- `mustRenderFailureDossierPromptPreamble()` (unexported)
- `buildManualBoxFanInPromptPreamble()` (unexported)
- `classifyAPIError()` (unexported)
- `buildStageStatusContract()` (unexported in stage_status_contract.go)
- `copyFirstValidFallbackStatus()` (unexported)
- `decodeParallelResults()` (unexported)
- `gitutil.CopyIgnoredFiles()` (direct git dependency)

### WaitHumanHandler.Execute() accesses (from handlers.go:658):
- `exec.Engine.Interviewer` (field)
- `exec.Engine.cxdbInterviewStarted()` (unexported)
- `exec.Engine.cxdbInterviewTimeout()` (unexported)
- `exec.Engine.cxdbInterviewCompleted()` (unexported)

### ManagerLoopHandler.Execute() accesses (from manager_loop.go:26):
- `exec.Engine.appendProgress()` (unexported)
- `runChildPipeline()` (unexported, runs sub-engine)

## Strategy
1. Export engine methods needed by external handler packages
2. Move handler types to agents/ and workflows/ (they import engine/)
3. Move associated helpers that are L1/L2-specific
4. Keep L0 utilities (fidelity preamble etc) in engine as exported functions
5. Registration moves to cmd/kilroy/
6. Rename codergen → agent throughout

## What Stays in engine/ (L0)
- Handler, HandlerRegistry, Execution, Engine types
- StartHandler, ExitHandler, ConditionalHandler, ToolHandler
- ParallelHandler, FanInHandler (graph traversal primitives)
- CodergenBackend interface (renamed to AgentBackend)
- Interviewer interface + Question/Option/Answer types
- All engine execution logic (run loop, retry, edge selection)
- Fidelity preamble builder (used by agent handler but is L0 utility)
- CXDB sink (extracted in Phase 3.5)
- Git operations (extracted in Phase 3.1)

## What Moves to agents/ (L1)
- AgentHandler (renamed from CodergenHandler)
- CodergenRouter (renamed to AgentRouter)

## What Moves to workflows/ (L2)
- WaitHumanHandler (renamed to HumanGateHandler)
- Interviewer implementations (Console, Queue, Callback, Recording, AutoApprove)
- ManagerLoopHandler
