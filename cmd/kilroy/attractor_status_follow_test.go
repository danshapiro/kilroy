package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFollowProgress_EmitsFormattedEvents(t *testing.T) {
	logs := t.TempDir()
	ndjson := filepath.Join(logs, "progress.ndjson")

	f, err := os.Create(ndjson)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"ts":"2026-02-10T04:00:25Z","event":"stage_attempt_start","node_id":"pick_feature","attempt":1,"max":4}` + "\n")
	_, _ = f.WriteString(`{"ts":"2026-02-10T04:00:30Z","event":"branch_heartbeat","branch_key":"impl_a","branch_event":"branch_active","branch_idle_ms":250}` + "\n")
	_, _ = f.WriteString(`{"ts":"2026-02-10T04:00:40Z","event":"stage_attempt_end","node_id":"pick_feature","status":"success","attempt":1,"max":4}` + "\n")
	_, _ = f.WriteString(`{"ts":"2026-02-10T04:00:41Z","event":"branch_progress","branch_key":"impl_a","branch_event":"stage_attempt_start","branch_node_id":"impl_a","branch_status":"success"}` + "\n")
	_, _ = f.WriteString(`{"ts":"2026-02-10T04:00:40Z","event":"edge_selected","from_node":"pick_feature","to_node":"check_pick"}` + "\n")
	_ = f.Close()

	// Write final.json so follow exits immediately.
	_ = os.WriteFile(filepath.Join(logs, "final.json"), []byte(`{"status":"success","run_id":"test1"}`), 0o644)

	var buf bytes.Buffer
	code := runFollowProgress(logs, &buf, false)
	if code != 0 {
		t.Fatalf("exit code %d; output: %s", code, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "pick_feature") {
		t.Fatalf("expected node name in output: %s", out)
	}
	if !strings.Contains(out, "stage_attempt_start") {
		t.Fatalf("expected stage_attempt_start in output: %s", out)
	}
	if !strings.Contains(out, "stage_attempt_end") {
		t.Fatalf("expected stage_attempt_end in output: %s", out)
	}
	if !strings.Contains(out, "edge_selected") {
		t.Fatalf("expected edge_selected in output: %s", out)
	}
	if !strings.Contains(out, "branch_progress") {
		t.Fatalf("expected branch_progress in output: %s", out)
	}
	if strings.Contains(out, "branch_heartbeat") {
		t.Fatalf("did not expect branch_heartbeat in default output: %s", out)
	}
	if !strings.Contains(out, "pick_feature -> check_pick") {
		t.Fatalf("expected edge routing in output: %s", out)
	}
	if !strings.Contains(out, "run completed: success") {
		t.Fatalf("expected final summary in output: %s", out)
	}
}

func TestFollowProgress_RawPassthrough(t *testing.T) {
	logs := t.TempDir()
	ndjson := filepath.Join(logs, "progress.ndjson")
	line := `{"ts":"2026-02-10T04:00:25Z","event":"warning","message":"test warning"}`
	_ = os.WriteFile(ndjson, []byte(line+"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(logs, "final.json"), []byte(`{"status":"success"}`), 0o644)

	var buf bytes.Buffer
	code := runFollowProgress(logs, &buf, true)
	if code != 0 {
		t.Fatalf("exit code %d", code)
	}
	if !strings.Contains(buf.String(), `"event":"warning"`) {
		t.Fatalf("expected raw JSON passthrough: %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"message":"test warning"`) {
		t.Fatalf("expected raw message field: %s", buf.String())
	}
}

func TestFollowProgress_TailsNewEvents(t *testing.T) {
	logs := t.TempDir()
	ndjson := filepath.Join(logs, "progress.ndjson")
	_ = os.WriteFile(ndjson, []byte(""), 0o644)

	// Write events in background after a delay.
	go func() {
		time.Sleep(400 * time.Millisecond)
		f, _ := os.OpenFile(ndjson, os.O_APPEND|os.O_WRONLY, 0o644)
		_, _ = f.WriteString(`{"ts":"2026-02-10T04:01:00Z","event":"stage_attempt_start","node_id":"impl","attempt":1,"max":3}` + "\n")
		_ = f.Close()
		time.Sleep(400 * time.Millisecond)
		_ = os.WriteFile(filepath.Join(logs, "final.json"), []byte(`{"status":"success"}`), 0o644)
	}()

	var buf bytes.Buffer
	code := runFollowProgress(logs, &buf, false)
	if code != 0 {
		t.Fatalf("exit code %d; output: %s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "impl") {
		t.Fatalf("expected tailed event with node_id=impl: %s", buf.String())
	}
}

func TestFollowProgress_ExitsOnDeadPID(t *testing.T) {
	logs := t.TempDir()
	ndjson := filepath.Join(logs, "progress.ndjson")
	_ = os.WriteFile(ndjson, []byte(`{"ts":"2026-02-10T04:00:25Z","event":"stage_attempt_start","node_id":"start","attempt":1,"max":1}`+"\n"), 0o644)
	// Write a PID that doesn't exist (use a very high value).
	_ = os.WriteFile(filepath.Join(logs, "run.pid"), []byte("999999999"), 0o644)

	var buf bytes.Buffer
	code := runFollowProgress(logs, &buf, false)
	if code != 1 {
		t.Fatalf("expected exit code 1 for dead PID, got %d; output: %s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "no longer alive") {
		t.Fatalf("expected dead-PID message: %s", buf.String())
	}
}

func TestFormatProgressEvent_AllEventTypes(t *testing.T) {
	tests := []struct {
		name     string
		event    map[string]any
		contains []string
	}{
		{
			name: "stage_attempt_start",
			event: map[string]any{
				"ts": "2026-02-10T04:00:25Z", "event": "stage_attempt_start",
				"node_id": "pick_feature", "attempt": float64(1), "max": float64(4),
			},
			contains: []string{"04:00:25", "stage_attempt_start", "pick_feature", "1/4"},
		},
		{
			name: "stage_attempt_end_success",
			event: map[string]any{
				"ts": "2026-02-10T04:00:40Z", "event": "stage_attempt_end",
				"node_id": "pick_feature", "status": "success",
			},
			contains: []string{"04:00:40", "stage_attempt_end", "pick_feature", "success"},
		},
		{
			name: "stage_attempt_end_fail",
			event: map[string]any{
				"ts": "2026-02-10T04:31:00Z", "event": "stage_attempt_end",
				"node_id": "implement_feature", "status": "fail",
				"failure_reason": "context deadline exceeded",
			},
			contains: []string{"fail", "context deadline exceeded"},
		},
		{
			name: "edge_selected",
			event: map[string]any{
				"ts": "2026-02-10T04:00:40Z", "event": "edge_selected",
				"from_node": "pick_feature", "to_node": "check_pick",
			},
			contains: []string{"edge_selected", "pick_feature -> check_pick"},
		},
		{
			name: "warning",
			event: map[string]any{
				"ts": "2026-02-10T04:00:26Z", "event": "warning",
				"message": "codex schema validation failed",
			},
			contains: []string{"WARNING", "codex schema validation failed"},
		},
		{
			name: "stage_heartbeat",
			event: map[string]any{
				"ts": "2026-02-10T04:02:00Z", "event": "stage_heartbeat",
				"node_id": "implement_feature", "elapsed_s": float64(60),
			},
			contains: []string{"stage_heartbeat", "implement_feature", "elapsed=60s"},
		},
		{
			name: "branch_progress",
			event: map[string]any{
				"ts": "2026-02-10T04:02:30Z", "event": "branch_progress",
				"branch_key": "impl_a", "branch_event": "stage_attempt_start",
				"branch_node_id": "impl_a", "branch_status": "success",
			},
			contains: []string{"branch_progress", "impl_a", "stage_attempt_start", "status=success"},
		},
		{
			name: "branch_stale_warning",
			event: map[string]any{
				"ts": "2026-02-10T04:02:45Z", "event": "branch_stale_warning",
				"branch_key": "impl_a", "branch_idle_ms": float64(301000), "branch_last_event": "stage_attempt_start",
			},
			contains: []string{"branch_stale_warning", "impl_a", "idle=301000ms", "last=stage_attempt_start"},
		},
		{
			name: "loop_restart",
			event: map[string]any{
				"ts": "2026-02-10T05:00:00Z", "event": "loop_restart",
			},
			contains: []string{"loop_restart", "restarting pipeline"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatProgressEvent(tc.event)
			for _, want := range tc.contains {
				if !strings.Contains(got, want) {
					t.Errorf("formatProgressEvent missing %q in: %s", want, got)
				}
			}
		})
	}
}

func TestFormatProgressEvent_BranchHeartbeatSuppressed(t *testing.T) {
	got := formatProgressEvent(map[string]any{
		"ts":             "2026-02-10T04:02:00Z",
		"event":          "branch_heartbeat",
		"branch_key":     "impl_a",
		"branch_idle_ms": float64(200),
	})
	if strings.TrimSpace(got) != "" {
		t.Fatalf("expected suppressed output for branch_heartbeat, got: %q", got)
	}
}

func TestLatestRunLogsRoot_FindsMostRecent(t *testing.T) {
	tmp := t.TempDir()
	runsDir := filepath.Join(tmp, "kilroy", "attractor", "runs")
	_ = os.MkdirAll(filepath.Join(runsDir, "run-old"), 0o755)
	time.Sleep(50 * time.Millisecond)
	_ = os.MkdirAll(filepath.Join(runsDir, "run-new"), 0o755)

	// Touch the new directory to ensure it's most recent.
	_ = os.WriteFile(filepath.Join(runsDir, "run-new", "progress.ndjson"), []byte(""), 0o644)

	t.Setenv("XDG_STATE_HOME", tmp)
	got, err := latestRunLogsRoot()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "run-new") {
		t.Fatalf("expected run-new, got %s", got)
	}
}

func TestLatestRunLogsRoot_ErrorsOnEmpty(t *testing.T) {
	tmp := t.TempDir()
	_ = os.MkdirAll(filepath.Join(tmp, "kilroy", "attractor", "runs"), 0o755)
	t.Setenv("XDG_STATE_HOME", tmp)

	_, err := latestRunLogsRoot()
	if err == nil {
		t.Fatal("expected error for empty runs directory")
	}
}

func TestRunAttractorStatus_FollowFlag(t *testing.T) {
	logs := t.TempDir()
	_ = os.WriteFile(filepath.Join(logs, "progress.ndjson"),
		[]byte(`{"ts":"2026-02-10T04:00:25Z","event":"stage_attempt_start","node_id":"start","attempt":1,"max":1}`+"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(logs, "final.json"), []byte(`{"status":"success"}`), 0o644)

	var stdout, stderr bytes.Buffer
	code := runAttractorStatus([]string{"--follow", "--logs-root", logs}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "start") {
		t.Fatalf("expected event output: %s", stdout.String())
	}
}

func TestRunAttractorStatus_FollowAndWatchMutuallyExclusive(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runAttractorStatus([]string{"--follow", "--watch", "--logs-root", "/tmp"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "mutually exclusive") {
		t.Fatalf("expected mutual exclusion error: %s", stderr.String())
	}
}

func TestRunAttractorStatus_LatestAndLogsRootMutuallyExclusive(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runAttractorStatus([]string{"--latest", "--logs-root", "/tmp"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "mutually exclusive") {
		t.Fatalf("expected mutual exclusion error: %s", stderr.String())
	}
}
