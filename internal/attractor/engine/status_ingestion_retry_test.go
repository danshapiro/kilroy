package engine

import (
	"errors"
	"io"
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

	source, diagnostic, err := CopyFirstValidFallbackStatus(stageStatusPath, []FallbackStatusPath{
		{Path: fallbackPath, Source: StatusSourceWorktree},
	})
	if err != nil {
		t.Fatalf("CopyFirstValidFallbackStatus: %v", err)
	}
	if source != StatusSourceCanonical {
		t.Fatalf("source=%q want %q", source, StatusSourceCanonical)
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

	source, diagnostic, err := CopyFirstValidFallbackStatus(stageStatusPath, []FallbackStatusPath{
		{Path: missingPath, Source: StatusSourceWorktree},
	})
	if err != nil {
		t.Fatalf("CopyFirstValidFallbackStatus: %v", err)
	}
	if source != StatusSourceNone {
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

	source, diagnostic, err := CopyFirstValidFallbackStatus(stageStatusPath, []FallbackStatusPath{
		{Path: fallbackPath, Source: StatusSourceWorktree},
	})
	if err != nil {
		t.Fatalf("CopyFirstValidFallbackStatus: %v", err)
	}
	if source != StatusSourceNone {
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

	source, diagnostic, err := CopyFirstValidFallbackStatus(stageStatusPath, []FallbackStatusPath{
		{Path: fallbackPath, Source: StatusSourceWorktree},
	})
	if err != nil {
		t.Fatalf("CopyFirstValidFallbackStatus: %v", err)
	}
	if source != StatusSourceWorktree {
		t.Fatalf("source=%q want %q", source, StatusSourceWorktree)
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

func TestCopyFirstValidFallbackStatus_TypeMismatchIsInvalidPayload(t *testing.T) {
	tmp := t.TempDir()
	stageStatusPath := filepath.Join(tmp, "logs", "a", "status.json")
	fallbackPath := filepath.Join(tmp, "status.json")

	// Deterministic schema/type violation: status must be a string.
	if err := os.WriteFile(fallbackPath, []byte(`{"status":123}`), 0o644); err != nil {
		t.Fatalf("write invalid payload fallback: %v", err)
	}

	source, diagnostic, err := CopyFirstValidFallbackStatus(stageStatusPath, []FallbackStatusPath{
		{Path: fallbackPath, Source: StatusSourceWorktree},
	})
	if err != nil {
		t.Fatalf("CopyFirstValidFallbackStatus: %v", err)
	}
	if source != StatusSourceNone {
		t.Fatalf("source=%q want empty", source)
	}
	if !strings.Contains(diagnostic, "invalid status payload") {
		t.Fatalf("diagnostic=%q want mention of invalid status payload", diagnostic)
	}
	if strings.Contains(diagnostic, "corrupt status artifact") {
		t.Fatalf("diagnostic=%q should not classify type mismatch as corrupt artifact", diagnostic)
	}
}

func TestShouldRetryFallbackRead_DoesNotRetryMissing(t *testing.T) {
	if shouldRetryFallbackRead(fallbackFailureModeMissing, os.ErrNotExist) {
		t.Fatalf("missing fallback paths should not be retried")
	}
}

func TestShouldRetryFallbackRead_RetriesUnexpectedEOF(t *testing.T) {
	if !shouldRetryFallbackRead(fallbackFailureModeUnreadable, io.ErrUnexpectedEOF) {
		t.Fatalf("unexpected EOF should remain retryable for unreadable fallback artifacts")
	}
}

func TestShouldRetryFallbackRead_DoesNotRetryRegularUnreadable(t *testing.T) {
	if shouldRetryFallbackRead(fallbackFailureModeUnreadable, errors.New("permission denied")) {
		t.Fatalf("regular unreadable fallback artifacts should not be retried")
	}
}
