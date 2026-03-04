package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/procutil"
)

func TestAcquireRunOwnership_RejectsLiveOwner(t *testing.T) {
	t.Parallel()

	logsRoot := t.TempDir()
	lockPath := filepath.Join(logsRoot, runOwnershipLockFile)
	ownerPID, ownerStart := startSleepingProcess(t)
	owner := runOwnershipRecord{
		PID:          ownerPID,
		PIDStartTime: ownerStart,
		RunID:        "existing-run",
	}
	if err := writeOwnershipRecord(lockPath, owner); err != nil {
		t.Fatalf("writeOwnershipRecord: %v", err)
	}

	_, err := acquireRunOwnership(logsRoot, "new-run")
	if err == nil {
		t.Fatalf("expected ownership conflict error, got nil")
	}
	var ownershipErr *runOwnershipConflictError
	if !errors.As(err, &ownershipErr) {
		t.Fatalf("expected runOwnershipConflictError, got %T (%v)", err, err)
	}
	if ownershipErr.ExistingPID != ownerPID {
		t.Fatalf("existing pid: got %d want %d", ownershipErr.ExistingPID, ownerPID)
	}
}

func TestAcquireRunOwnership_ReclaimsStaleOwner(t *testing.T) {
	t.Parallel()

	logsRoot := t.TempDir()
	lockPath := filepath.Join(logsRoot, runOwnershipLockFile)
	owner := runOwnershipRecord{
		PID:          999999, // best-effort stale PID
		PIDStartTime: 123456,
		RunID:        "stale-run",
	}
	if err := writeOwnershipRecord(lockPath, owner); err != nil {
		t.Fatalf("writeOwnershipRecord: %v", err)
	}

	release, err := acquireRunOwnership(logsRoot, "new-run")
	if err != nil {
		t.Fatalf("acquireRunOwnership: %v", err)
	}
	defer release()

	got, err := readRunOwnership(lockPath)
	if err != nil {
		t.Fatalf("readRunOwnership: %v", err)
	}
	if got.PID != os.Getpid() {
		t.Fatalf("owner pid: got %d want %d", got.PID, os.Getpid())
	}
	if got.RunID != "new-run" {
		t.Fatalf("owner run_id: got %q want %q", got.RunID, "new-run")
	}
}

func TestAcquireRunOwnership_UnreadableLockConflicts(t *testing.T) {
	t.Parallel()

	logsRoot := t.TempDir()
	lockPath := filepath.Join(logsRoot, runOwnershipLockFile)
	if err := os.WriteFile(lockPath, []byte("{"), 0o644); err != nil {
		t.Fatalf("write unreadable lock: %v", err)
	}

	_, err := acquireRunOwnership(logsRoot, "new-run")
	if err != nil {
		var ownershipErr *runOwnershipConflictError
		if !errors.As(err, &ownershipErr) {
			t.Fatalf("expected runOwnershipConflictError, got %T (%v)", err, err)
		}
		if ownershipErr.Reason == "" {
			t.Fatalf("expected conflict reason for unreadable lock, got empty reason")
		}
	} else {
		t.Fatalf("expected ownership conflict for unreadable lock, got nil")
	}
	if _, statErr := os.Stat(lockPath); statErr != nil {
		t.Fatalf("expected unreadable lock to be preserved, stat err: %v", statErr)
	}
}

func TestAcquireRunOwnership_RetriesUnreadableLockUntilConflict(t *testing.T) {
	t.Parallel()

	logsRoot := t.TempDir()
	lockPath := filepath.Join(logsRoot, runOwnershipLockFile)
	owner := ownerRecordForCurrentPID("existing-run")

	created := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		f, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			close(created)
			done <- err
			return
		}
		close(created)
		defer func() {
			_ = f.Close()
		}()
		time.Sleep(120 * time.Millisecond)
		b, err := json.Marshal(owner)
		if err != nil {
			done <- err
			return
		}
		_, err = f.Write(b)
		done <- err
	}()
	<-created

	_, err := acquireRunOwnership(logsRoot, "new-run")
	if err == nil {
		t.Fatalf("expected ownership conflict error, got nil")
	}
	if !isRunOwnershipConflict(err) {
		t.Fatalf("expected run ownership conflict, got: %v", err)
	}
	if writeErr := <-done; writeErr != nil {
		t.Fatalf("slow owner write failed: %v", writeErr)
	}
}

func TestAcquireRunOwnership_ReclaimsPIDStartTimeMismatch(t *testing.T) {
	t.Parallel()

	currentStart, err := procutil.ReadPIDStartTime(os.Getpid())
	if err != nil || currentStart == 0 {
		t.Skip("pid start time unavailable on this platform")
	}

	logsRoot := t.TempDir()
	lockPath := filepath.Join(logsRoot, runOwnershipLockFile)
	owner := runOwnershipRecord{
		PID:          os.Getpid(),
		PIDStartTime: currentStart + 1, // force mismatch with current process identity
		RunID:        "stale-owner",
	}
	if err := writeOwnershipRecord(lockPath, owner); err != nil {
		t.Fatalf("writeOwnershipRecord: %v", err)
	}

	release, err := acquireRunOwnership(logsRoot, "new-run")
	if err != nil {
		t.Fatalf("acquireRunOwnership: %v", err)
	}
	defer release()

	got, err := readRunOwnership(lockPath)
	if err != nil {
		t.Fatalf("readRunOwnership: %v", err)
	}
	if got.RunID != "new-run" {
		t.Fatalf("owner run_id: got %q want %q", got.RunID, "new-run")
	}
}

func TestReleaseRunOwnership_DoesNotRemoveForeignOwner(t *testing.T) {
	t.Parallel()

	logsRoot := t.TempDir()
	lockPath := filepath.Join(logsRoot, runOwnershipLockFile)
	owner := ownerRecordForCurrentPID("owner-run")
	if err := writeOwnershipRecord(lockPath, owner); err != nil {
		t.Fatalf("writeOwnershipRecord: %v", err)
	}

	releaseRunOwnership(lockPath, os.Getpid()+1, 0)
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("expected lock to remain for foreign owner, stat err: %v", err)
	}
}

func TestReleaseRunOwnership_DoesNotRemoveMismatchedStartTime(t *testing.T) {
	t.Parallel()

	currentStart, err := procutil.ReadPIDStartTime(os.Getpid())
	if err != nil || currentStart == 0 {
		t.Skip("pid start time unavailable on this platform")
	}

	logsRoot := t.TempDir()
	lockPath := filepath.Join(logsRoot, runOwnershipLockFile)
	owner := runOwnershipRecord{
		PID:          os.Getpid(),
		PIDStartTime: currentStart + 1,
		RunID:        "owner-run",
	}
	if err := writeOwnershipRecord(lockPath, owner); err != nil {
		t.Fatalf("writeOwnershipRecord: %v", err)
	}

	releaseRunOwnership(lockPath, os.Getpid(), currentStart)
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("expected lock to remain when start time mismatches, stat err: %v", err)
	}
}

func TestRun_OwnershipConflict_DoesNotWriteFinalJSON(t *testing.T) {
	t.Parallel()

	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	lockPath := filepath.Join(logsRoot, runOwnershipLockFile)
	ownerPID, ownerStart := startSleepingProcess(t)
	owner := runOwnershipRecord{
		PID:          ownerPID,
		PIDStartTime: ownerStart,
		RunID:        "existing-run",
	}
	if err := writeOwnershipRecord(lockPath, owner); err != nil {
		t.Fatalf("writeOwnershipRecord: %v", err)
	}

	dot := []byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  start -> exit
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := Run(ctx, dot, RunOptions{
		RepoPath: repo,
		RunID:    "ownership-conflict",
		LogsRoot: logsRoot,
	})
	if err == nil {
		t.Fatalf("expected ownership conflict error, got nil")
	}
	if !isRunOwnershipConflict(err) {
		t.Fatalf("expected ownership conflict, got: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(logsRoot, "final.json")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected no final.json for ownership conflict, stat err=%v", statErr)
	}
}

func writeOwnershipRecord(path string, rec runOwnershipRecord) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func ownerRecordForCurrentPID(runID string) runOwnershipRecord {
	rec := runOwnershipRecord{
		PID:   os.Getpid(),
		RunID: runID,
	}
	if start, err := procutil.ReadPIDStartTime(rec.PID); err == nil && start > 0 {
		rec.PIDStartTime = start
	}
	return rec
}

func startSleepingProcess(t *testing.T) (int, uint64) {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep process: %v", err)
	}
	pid := cmd.Process.Pid
	var start uint64
	if s, err := procutil.ReadPIDStartTime(pid); err == nil {
		start = s
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return pid, start
}
