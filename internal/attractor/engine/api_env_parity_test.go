package engine

import (
	"os"
	"testing"
)

func TestBuildAgentLoopOverrides_UsesBaseNodeEnvContract(t *testing.T) {
	origTarget, hadTarget := os.LookupEnv("CARGO_TARGET_DIR")
	_ = os.Unsetenv("CARGO_TARGET_DIR")
	t.Cleanup(func() {
		if hadTarget {
			_ = os.Setenv("CARGO_TARGET_DIR", origTarget)
			return
		}
		_ = os.Unsetenv("CARGO_TARGET_DIR")
	})
	worktree := t.TempDir()
	env := buildAgentLoopOverrides(worktree, map[string]string{"KILROY_STAGE_STATUS_PATH": "/tmp/status.json"})

	if env["CARGO_TARGET_DIR"] == "" {
		t.Fatal("CARGO_TARGET_DIR must be present for API agent_loop path")
	}
	if env["KILROY_STAGE_STATUS_PATH"] != "/tmp/status.json" {
		t.Fatal("stage status env must be preserved")
	}
}
