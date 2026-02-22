package runstate

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/procutil"
)

type finalOutcomeDoc struct {
	Status        string `json:"status"`
	RunID         string `json:"run_id"`
	FailureReason string `json:"failure_reason"`
}

// LoadSnapshot reads run artifacts in logsRoot and returns a compact run snapshot.
func LoadSnapshot(logsRoot string) (*Snapshot, error) {
	root := strings.TrimSpace(logsRoot)
	if root == "" {
		return nil, fmt.Errorf("logs root is required")
	}

	s := &Snapshot{
		LogsRoot: root,
		State:    StateUnknown,
	}

	if err := applyFinalOutcome(s); err != nil {
		return nil, err
	}
	terminal := s.State == StateSuccess || s.State == StateFail

	// terminal final.json is authoritative for status/current node; live/progress
	// are best-effort activity feeds and must not override terminal state.
	if !terminal {
		if err := applyLiveOrProgress(s); err != nil {
			return nil, err
		}
	}

	if err := applyPIDFile(s, terminal); err != nil {
		return nil, err
	}
	if s.State == StateUnknown && s.PIDAlive {
		s.State = StateRunning
	}

	return s, nil
}

func applyFinalOutcome(s *Snapshot) error {
	path := filepath.Join(s.LogsRoot, "final.json")
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	var doc finalOutcomeDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}

	if rid := strings.TrimSpace(doc.RunID); rid != "" {
		s.RunID = rid
	}
	switch strings.ToLower(strings.TrimSpace(doc.Status)) {
	case string(StateSuccess):
		s.State = StateSuccess
	case string(StateFail):
		s.State = StateFail
		if reason := strings.TrimSpace(doc.FailureReason); reason != "" {
			s.FailureReason = reason
		}
	}
	return nil
}

func applyLiveOrProgress(s *Snapshot) error {
	live, found, err := readLiveEvent(filepath.Join(s.LogsRoot, "live.json"))
	if err != nil {
		return err
	}
	if !found {
		live, found, err = readLastProgressEvent(filepath.Join(s.LogsRoot, "progress.ndjson"))
		if err != nil {
			return err
		}
	}
	if !found {
		return nil
	}

	if rid := eventString(live["run_id"]); rid != "" && s.RunID == "" {
		s.RunID = rid
	}
	s.LastEvent = eventString(live["event"])
	s.CurrentNodeID = eventString(live["node_id"])
	if ts := parseEventTime(live["ts"]); !ts.IsZero() {
		s.LastEventAt = ts
	}
	if reason := eventString(live["failure_reason"]); reason != "" {
		s.FailureReason = reason
	}
	return nil
}

func applyPIDFile(s *Snapshot, terminalState bool) error {
	path := filepath.Join(s.LogsRoot, "run.pid")
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	raw := strings.TrimSpace(string(b))
	if raw == "" {
		if terminalState {
			return nil
		}
		return fmt.Errorf("parse %s: empty pid", path)
	}
	pid, err := strconv.Atoi(raw)
	if err != nil || pid <= 0 {
		if terminalState {
			return nil
		}
		return fmt.Errorf("parse %s: invalid pid %q", path, raw)
	}
	s.PID = pid
	s.PIDAlive = pidAlive(pid)
	return nil
}

func pidAlive(pid int) bool {
	return procutil.PIDAlive(pid)
}

func readLiveEvent(path string) (map[string]any, bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	var ev map[string]any
	if err := json.Unmarshal(b, &ev); err != nil {
		return nil, false, fmt.Errorf("decode %s: %w", path, err)
	}
	return ev, true, nil
}

func readLastProgressEvent(path string) (map[string]any, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)

	last := ""
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" {
			last = line
		}
	}
	if err := sc.Err(); err != nil {
		return nil, false, err
	}
	if last == "" {
		return nil, false, nil
	}

	var ev map[string]any
	if err := json.Unmarshal([]byte(last), &ev); err != nil {
		return nil, false, fmt.Errorf("decode %s: %w", path, err)
	}
	return ev, true, nil
}

// ApplyVerbose enriches a Snapshot with data from checkpoint, final, progress,
// and worktree artifact files. Missing files are silently skipped.
func ApplyVerbose(s *Snapshot) error {
	if err := applyCheckpointVerbose(s); err != nil {
		return err
	}
	if err := applyFinalVerbose(s); err != nil {
		return err
	}
	if err := applyStageTrace(s); err != nil {
		return err
	}
	applyWorktreeArtifacts(s)
	return nil
}

type checkpointDoc struct {
	CompletedNodes []string       `json:"completed_nodes"`
	NodeRetries    map[string]int `json:"node_retries"`
}

func applyCheckpointVerbose(s *Snapshot) error {
	path := filepath.Join(s.LogsRoot, "checkpoint.json")
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var doc checkpointDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	s.CompletedNodes = doc.CompletedNodes
	s.RetryCounts = doc.NodeRetries
	return nil
}

type finalVerboseDoc struct {
	FinalCommitSHA string `json:"final_git_commit_sha"`
	CXDBContextID  string `json:"cxdb_context_id"`
}

func applyFinalVerbose(s *Snapshot) error {
	path := filepath.Join(s.LogsRoot, "final.json")
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var doc finalVerboseDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	s.FinalCommitSHA = strings.TrimSpace(doc.FinalCommitSHA)
	s.CXDBContextID = strings.TrimSpace(doc.CXDBContextID)
	return nil
}

func applyStageTrace(s *Snapshot) error {
	path := filepath.Join(s.LogsRoot, "progress.ndjson")
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		switch eventString(ev["event"]) {
		case "stage_attempt_end":
			sa := StageAttempt{
				NodeID:        eventString(ev["node_id"]),
				Status:        eventString(ev["status"]),
				Attempt:       eventInt(ev["attempt"]),
				MaxAttempts:   eventInt(ev["max"]),
				FailureReason: eventString(ev["failure_reason"]),
			}
			s.StageTrace = append(s.StageTrace, sa)
		case "edge_selected":
			et := EdgeTransition{
				From:      eventString(ev["from_node"]),
				To:        eventString(ev["to_node"]),
				Condition: eventString(ev["condition"]),
			}
			s.EdgeTrace = append(s.EdgeTrace, et)
		}
	}
	return sc.Err()
}

func applyWorktreeArtifacts(s *Snapshot) {
	if b, err := os.ReadFile(filepath.Join(s.LogsRoot, "worktree", ".ai", "postmortem_latest.md")); err == nil {
		s.PostmortemText = string(b)
	}
	if b, err := os.ReadFile(filepath.Join(s.LogsRoot, "worktree", ".ai", "review_final.md")); err == nil {
		s.ReviewText = string(b)
	}
}

func eventInt(v any) int {
	switch t := v.(type) {
	case nil:
		return 0
	case float64:
		return int(t)
	case string:
		n, _ := strconv.Atoi(t)
		return n
	default:
		return 0
	}
}

func eventString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	default:
		return strings.TrimSpace(fmt.Sprint(t))
	}
}

func parseEventTime(v any) time.Time {
	raw := eventString(v)
	if raw == "" {
		return time.Time{}
	}
	if ts, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return ts
	}
	if ts, err := time.Parse(time.RFC3339, raw); err == nil {
		return ts
	}
	return time.Time{}
}
