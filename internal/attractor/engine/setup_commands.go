package engine

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// executeSetupCommands runs the configured setup commands sequentially in the
// worktree directory before the first pipeline node executes. Commands are run
// via "sh -c" and fail fast on the first error.
func (e *Engine) executeSetupCommands(ctx context.Context) error {
	if e == nil || e.RunConfig == nil || len(e.RunConfig.Setup.Commands) == 0 {
		return nil
	}

	timeoutMS := e.RunConfig.Setup.TimeoutMS
	if timeoutMS <= 0 {
		timeoutMS = 300000
	}
	timeout := time.Duration(timeoutMS) * time.Millisecond
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for i, cmdStr := range e.RunConfig.Setup.Commands {
		cmdStr = strings.TrimSpace(cmdStr)
		if cmdStr == "" {
			continue
		}

		e.appendProgress(map[string]any{
			"event":   "setup_command_start",
			"index":   i,
			"command": cmdStr,
		})

		cmd := exec.CommandContext(ctx, "sh", "-c", cmdStr)
		cmd.Dir = e.WorktreeDir
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err := cmd.Run()
		if err != nil {
			e.appendProgress(map[string]any{
				"event":   "setup_command_failed",
				"index":   i,
				"command": cmdStr,
				"error":   err.Error(),
				"stdout":  strings.TrimSpace(stdout.String()),
				"stderr":  strings.TrimSpace(stderr.String()),
			})
			return fmt.Errorf("setup command [%d] %q failed: %w", i, cmdStr, err)
		}

		e.appendProgress(map[string]any{
			"event":   "setup_command_ok",
			"index":   i,
			"command": cmdStr,
			"stdout":  strings.TrimSpace(stdout.String()),
		})
	}

	return nil
}
