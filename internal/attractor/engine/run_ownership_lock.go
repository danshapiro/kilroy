package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/procutil"
)

const (
	runOwnershipLockFile        = "run.lock.json"
	runOwnershipAcquireAttempts = 20
	runOwnershipRetryDelay      = 25 * time.Millisecond
)

type runOwnershipRecord struct {
	PID          int    `json:"pid"`
	PIDStartTime uint64 `json:"pid_start_time,omitempty"`
	RunID        string `json:"run_id,omitempty"`
	AcquiredAt   string `json:"acquired_at,omitempty"`
}

type runOwnershipConflictError struct {
	LogsRoot    string
	LockPath    string
	ExistingPID int
	ExistingRun string
	Reason      string
}

func (e *runOwnershipConflictError) Error() string {
	if e == nil {
		return "run ownership conflict"
	}
	msg := fmt.Sprintf("logs_root %q is already owned", e.LogsRoot)
	if e.ExistingPID > 0 {
		msg += fmt.Sprintf(" by a live process (pid=%d)", e.ExistingPID)
	} else {
		msg += " by another process"
	}
	if strings.TrimSpace(e.ExistingRun) != "" {
		msg += fmt.Sprintf(" run_id=%q", e.ExistingRun)
	}
	if strings.TrimSpace(e.LockPath) != "" {
		msg += fmt.Sprintf(" lock=%q", e.LockPath)
	}
	if strings.TrimSpace(e.Reason) != "" {
		msg += fmt.Sprintf(" (%s)", strings.TrimSpace(e.Reason))
	}
	return msg
}

func isRunOwnershipConflict(err error) bool {
	var ownershipErr *runOwnershipConflictError
	return errors.As(err, &ownershipErr)
}

func acquireRunOwnership(logsRoot, runID string) (func(), error) {
	root := strings.TrimSpace(logsRoot)
	if root == "" {
		return nil, fmt.Errorf("logs_root is required")
	}
	lockPath := filepath.Join(root, runOwnershipLockFile)
	record := runOwnershipRecord{
		PID:        os.Getpid(),
		RunID:      strings.TrimSpace(runID),
		AcquiredAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if startTime, err := procutil.ReadPIDStartTime(record.PID); err == nil && startTime > 0 {
		record.PIDStartTime = startTime
	}
	payload, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode run ownership: %w", err)
	}
	// Keep payload newline-friendly for manual inspection.
	payload = append(payload, '\n')

	var lastReadErr error
	for attempts := 0; attempts < runOwnershipAcquireAttempts; attempts++ {
		f, openErr := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if openErr == nil {
			if _, writeErr := f.Write(payload); writeErr != nil {
				_ = f.Close()
				_ = os.Remove(lockPath)
				return nil, fmt.Errorf("write run ownership lock %q: %w", lockPath, writeErr)
			}
			if closeErr := f.Close(); closeErr != nil {
				_ = os.Remove(lockPath)
				return nil, fmt.Errorf("close run ownership lock %q: %w", lockPath, closeErr)
			}
			pid := record.PID
			start := record.PIDStartTime
			return func() {
				releaseRunOwnership(lockPath, pid, start)
			}, nil
		}
		if !errors.Is(openErr, os.ErrExist) {
			return nil, fmt.Errorf("create run ownership lock %q: %w", lockPath, openErr)
		}

		existing, readErr := readRunOwnership(lockPath)
		if readErr != nil {
			lastReadErr = readErr
			time.Sleep(runOwnershipRetryDelay)
			continue
		}

		if runOwnershipMatchesLiveProcess(existing) {
			return nil, &runOwnershipConflictError{
				LogsRoot:    root,
				LockPath:    lockPath,
				ExistingPID: existing.PID,
				ExistingRun: strings.TrimSpace(existing.RunID),
			}
		}
		// Stale ownership record, best-effort cleanup and retry.
		if removeErr := os.Remove(lockPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return nil, fmt.Errorf("remove stale run ownership lock %q: %w", lockPath, removeErr)
		}
	}
	if lastReadErr != nil {
		return nil, &runOwnershipConflictError{
			LogsRoot: root,
			LockPath: lockPath,
			Reason:   fmt.Sprintf("ownership lock unreadable after retries: %v", lastReadErr),
		}
	}
	return nil, fmt.Errorf("acquire run ownership lock %q: exhausted retries", lockPath)
}

func readRunOwnership(lockPath string) (runOwnershipRecord, error) {
	b, err := os.ReadFile(lockPath)
	if err != nil {
		return runOwnershipRecord{}, err
	}
	if strings.TrimSpace(string(b)) == "" {
		return runOwnershipRecord{}, fmt.Errorf("empty lock payload")
	}
	var rec runOwnershipRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return runOwnershipRecord{}, err
	}
	if rec.PID <= 0 {
		return runOwnershipRecord{}, fmt.Errorf("invalid pid %d", rec.PID)
	}
	return rec, nil
}

func runOwnershipMatchesLiveProcess(rec runOwnershipRecord) bool {
	if rec.PID <= 0 || !procutil.PIDAlive(rec.PID) {
		return false
	}
	if rec.PIDStartTime == 0 {
		return true
	}
	start, err := procutil.ReadPIDStartTime(rec.PID)
	if err != nil || start == 0 {
		// Cannot disambiguate; keep conservative conflict behavior.
		return true
	}
	return start == rec.PIDStartTime
}

func releaseRunOwnership(lockPath string, ownerPID int, ownerStartTime uint64) {
	if strings.TrimSpace(lockPath) == "" || ownerPID <= 0 {
		return
	}
	rec, err := readRunOwnership(lockPath)
	if err != nil {
		return
	}
	if rec.PID != ownerPID {
		return
	}
	if ownerStartTime > 0 && rec.PIDStartTime > 0 && rec.PIDStartTime != ownerStartTime {
		return
	}
	_ = os.Remove(lockPath)
}
