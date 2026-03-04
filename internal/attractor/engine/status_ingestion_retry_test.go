package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

func TestCopyFirstValidFallbackStatus_CanonicalStageStatusWins(t *testing.T) {
	tmp := t.TempDir()
	stageStatusPath := filepath.Join(tmp, "logs", "a", "status.json")
	fallbackPath := filepath.Join(tmp, "status.json")

	if err := runtime.WriteFileAtomic(stageStatusPath, []byte(`{"status":"success","notes":"canonical"}`)); err != nil {
		t.Fatalf("write canonical status: %v", err)
	}
	if err := os.WriteFile(fallbackPath, []byte(`{"status":"fail","failure_reason":"fallback"}`), 0o644); err != nil {
		t.Fatalf("write fallback status: %v", err)
	}

	source, diagnostic, err := copyFirstValidFallbackStatus(stageStatusPath, []fallbackStatusPath{
		{path: fallbackPath, source: statusSourceWorktree},
	})
	if err != nil {
		t.Fatalf("copyFirstValidFallbackStatus: %v", err)
	}
	if source != statusSourceCanonical {
		t.Fatalf("source=%q want %q", source, statusSourceCanonical)
	}
	if strings.TrimSpace(diagnostic) != "" {
		t.Fatalf("diagnostic=%q want empty", diagnostic)
	}

	b, err := os.ReadFile(stageStatusPath)
	if err != nil {
		t.Fatalf("read stage status: %v", err)
	}
	out, err := runtime.DecodeOutcomeJSON(b)
	if err != nil {
		t.Fatalf("decode stage status: %v", err)
	}
	if out.Status != runtime.StatusSuccess {
		t.Fatalf("status=%q want %q", out.Status, runtime.StatusSuccess)
	}
}

func TestCopyFirstValidFallbackStatus_MissingFallbackIsDiagnosed(t *testing.T) {
	tmp := t.TempDir()
	stageStatusPath := filepath.Join(tmp, "logs", "a", "status.json")
	missingPath := filepath.Join(tmp, "missing-status.json")

	source, diagnostic, err := copyFirstValidFallbackStatus(stageStatusPath, []fallbackStatusPath{
		{path: missingPath, source: statusSourceWorktree},
	})
	if err != nil {
		t.Fatalf("copyFirstValidFallbackStatus: %v", err)
	}
	if source != statusSourceNone {
		t.Fatalf("source=%q want empty", source)
	}
	if !strings.Contains(diagnostic, "missing status artifact") {
		t.Fatalf("diagnostic=%q want mention of missing status artifact", diagnostic)
	}
}

func TestCopyFirstValidFallbackStatus_PermanentCorruptFallbackIsDiagnosed(t *testing.T) {
	tmp := t.TempDir()
	stageStatusPath := filepath.Join(tmp, "logs", "a", "status.json")
	fallbackPath := filepath.Join(tmp, "status.json")

	if err := os.WriteFile(fallbackPath, []byte(`{ this is invalid json }`), 0o644); err != nil {
		t.Fatalf("write corrupt fallback: %v", err)
	}

	source, diagnostic, err := copyFirstValidFallbackStatus(stageStatusPath, []fallbackStatusPath{
		{path: fallbackPath, source: statusSourceWorktree},
	})
	if err != nil {
		t.Fatalf("copyFirstValidFallbackStatus: %v", err)
	}
	if source != statusSourceNone {
		t.Fatalf("source=%q want empty", source)
	}
	if !strings.Contains(diagnostic, "corrupt status artifact") {
		t.Fatalf("diagnostic=%q want mention of corrupt status artifact", diagnostic)
	}
}

func TestCopyFirstValidFallbackStatus_RetryDecodeSucceedsAfterTransientCorruption(t *testing.T) {
	tmp := t.TempDir()
	stageStatusPath := filepath.Join(tmp, "logs", "a", "status.json")
	fallbackPath := filepath.Join(tmp, "status.json")

	if err := os.WriteFile(fallbackPath, []byte(`{ this is invalid json }`), 0o644); err != nil {
		t.Fatalf("seed transient corrupt fallback: %v", err)
	}

	go func() {
		time.Sleep(fallbackStatusDecodeBaseDelay + 10*time.Millisecond)
		_ = os.WriteFile(fallbackPath, []byte(`{"status":"fail","failure_reason":"transient decode retry success"}`), 0o644)
	}()

	source, diagnostic, err := copyFirstValidFallbackStatus(stageStatusPath, []fallbackStatusPath{
		{path: fallbackPath, source: statusSourceWorktree},
	})
	if err != nil {
		t.Fatalf("copyFirstValidFallbackStatus: %v", err)
	}
	if source != statusSourceWorktree {
		t.Fatalf("source=%q want %q", source, statusSourceWorktree)
	}
	if strings.TrimSpace(diagnostic) != "" {
		t.Fatalf("diagnostic=%q want empty", diagnostic)
	}

	b, err := os.ReadFile(stageStatusPath)
	if err != nil {
		t.Fatalf("read copied stage status: %v", err)
	}
	out, err := runtime.DecodeOutcomeJSON(b)
	if err != nil {
		t.Fatalf("decode copied stage status: %v", err)
	}
	if out.Status != runtime.StatusFail {
		t.Fatalf("status=%q want %q", out.Status, runtime.StatusFail)
	}
	if out.FailureReason != "transient decode retry success" {
		t.Fatalf("failure_reason=%q want %q", out.FailureReason, "transient decode retry success")
	}
}
