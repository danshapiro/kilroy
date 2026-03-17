# Fix: Treat `exec.ErrWaitDelay` as success in setup commands

## Context

When Kilroy runs setup commands for the hangar pipeline, `bin/setup` starts a background Postgres daemon via `pg_ctl start`. The daemon inherits the stdout/stderr pipes from its parent process. After `bin/setup` exits successfully (exit code 0), Go's `cmd.Wait()` waits for the pipes to close — but Postgres keeps them open. After 3 seconds (`WaitDelay`), Go forcefully closes the pipes and returns `exec.ErrWaitDelay`. Kilroy treats this as a failure, aborting the pipeline.

The setup command itself succeeded. A grandchild daemon holding a pipe open is not an error.

## Change

**File:** `internal/attractor/engine/setup_commands.go`

Add `"errors"` to the import block.

Replace the error handling block (lines 52-70) with:

```go
err := cmd.Run()
if errors.Is(err, exec.ErrWaitDelay) {
    e.appendProgress(map[string]any{
        "event":   "setup_command_ok",
        "index":   i,
        "command": cmdStr,
        "stdout":  strings.TrimSpace(stdout.String()),
        "warning": "child process held I/O pipes open past WaitDelay; treated as success",
    })
} else if err != nil {
    e.appendProgress(map[string]any{
        "event":   "setup_command_failed",
        "index":   i,
        "command": cmdStr,
        "error":   err.Error(),
        "stdout":  strings.TrimSpace(stdout.String()),
        "stderr":  strings.TrimSpace(stderr.String()),
    })
    return fmt.Errorf("setup command [%d] %q failed: %w", i, cmdStr, err)
} else {
    e.appendProgress(map[string]any{
        "event":   "setup_command_ok",
        "index":   i,
        "command": cmdStr,
        "stdout":  strings.TrimSpace(stdout.String()),
    })
}
```

## Test

**File:** `internal/attractor/engine/setup_commands_test.go`

Add a test that runs a setup command which spawns a background process holding pipes open, and verify it succeeds rather than returning an error. For example:

```go
func TestSetupCommands_BackgroundDaemonDoesNotFail(t *testing.T) {
    // command that exits 0 but leaves a child holding stdout open
    // e.g.: "sh -c 'sleep 60 &'"
}
```

## Verification

1. Run existing tests: `go test ./internal/attractor/engine/ -run TestSetupCommands`
2. Run the new test
3. Re-run `/kilroy:run hangar` from the orchestration repo to confirm the pipeline gets past setup
