package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danshapiro/kilroy/internal/cxdb"
)

func TestLoadCXDBManifest_ParsesManifest(t *testing.T) {
	logs := t.TempDir()
	manifest := map[string]any{
		"run_id": "test-run-1",
		"goal":   "Test the system",
		"cxdb": map[string]any{
			"http_base_url": "http://127.0.0.1:9010",
			"context_id":    "42",
		},
	}
	b, _ := json.Marshal(manifest)
	_ = os.WriteFile(filepath.Join(logs, "manifest.json"), b, 0o644)

	m, err := loadCXDBManifest(logs)
	if err != nil {
		t.Fatalf("loadCXDBManifest: %v", err)
	}
	if m.RunID != "test-run-1" {
		t.Fatalf("run_id=%q want test-run-1", m.RunID)
	}
	if m.Goal != "Test the system" {
		t.Fatalf("goal=%q want 'Test the system'", m.Goal)
	}
	if m.CXDB.HTTPBaseURL != "http://127.0.0.1:9010" {
		t.Fatalf("http_base_url=%q", m.CXDB.HTTPBaseURL)
	}
	if m.CXDB.ContextID != "42" {
		t.Fatalf("context_id=%q", m.CXDB.ContextID)
	}
}

func TestLoadCXDBManifest_ErrorsOnMissingCXDB(t *testing.T) {
	logs := t.TempDir()
	manifest := map[string]any{"run_id": "test-run-1"}
	b, _ := json.Marshal(manifest)
	_ = os.WriteFile(filepath.Join(logs, "manifest.json"), b, 0o644)

	_, err := loadCXDBManifest(logs)
	if err == nil {
		t.Fatal("expected error for missing cxdb config")
	}
}

func TestLoadCXDBManifest_ErrorsOnMissingFile(t *testing.T) {
	_, err := loadCXDBManifest(t.TempDir())
	if err == nil {
		t.Fatal("expected error for missing manifest.json")
	}
}

func TestFormatCXDBTurn_RunStarted(t *testing.T) {
	turn := cxdb.Turn{
		TypeID:      "com.kilroy.attractor.RunStarted",
		TypeVersion: 1,
		Depth:       1,
		Payload: map[string]any{
			"timestamp_ms": float64(1739163625000),
			"goal":         "Implement authentication",
		},
	}
	got := formatCXDBTurn(turn)
	if !strings.Contains(got, "RUN_STARTED") {
		t.Fatalf("expected RUN_STARTED: %s", got)
	}
	if !strings.Contains(got, "Implement authentication") {
		t.Fatalf("expected goal: %s", got)
	}
}

func TestFormatCXDBTurn_StageStarted(t *testing.T) {
	turn := cxdb.Turn{
		TypeID:      "com.kilroy.attractor.StageStarted",
		TypeVersion: 1,
		Depth:       2,
		Payload: map[string]any{
			"timestamp_ms": float64(1739163625000),
			"node_id":      "implement_feature",
			"handler_type": "codergen",
			"attempt":      "2",
		},
	}
	got := formatCXDBTurn(turn)
	if !strings.Contains(got, "STAGE_STARTED") {
		t.Fatalf("expected STAGE_STARTED: %s", got)
	}
	if !strings.Contains(got, "implement_feature") {
		t.Fatalf("expected node_id: %s", got)
	}
	if !strings.Contains(got, "codergen") {
		t.Fatalf("expected handler_type: %s", got)
	}
	if !strings.Contains(got, "attempt 2") {
		t.Fatalf("expected attempt: %s", got)
	}
}

func TestFormatCXDBTurn_StageFinished(t *testing.T) {
	turn := cxdb.Turn{
		TypeID:      "com.kilroy.attractor.StageFinished",
		TypeVersion: 1,
		Depth:       3,
		Payload: map[string]any{
			"timestamp_ms":   float64(1739163625000),
			"node_id":        "verify_feature",
			"status":         "fail",
			"failure_reason": "Lint errors in modified files",
			"notes":          "Found 3 eslint errors",
		},
	}
	got := formatCXDBTurn(turn)
	if !strings.Contains(got, "STAGE_FINISHED") {
		t.Fatalf("expected STAGE_FINISHED: %s", got)
	}
	if !strings.Contains(got, "fail") {
		t.Fatalf("expected status: %s", got)
	}
	if !strings.Contains(got, "Lint errors") {
		t.Fatalf("expected failure_reason: %s", got)
	}
	if !strings.Contains(got, "notes: Found 3") {
		t.Fatalf("expected notes: %s", got)
	}
}

func TestFormatCXDBTurn_ToolCall(t *testing.T) {
	turn := cxdb.Turn{
		TypeID:      "com.kilroy.attractor.ToolCall",
		TypeVersion: 1,
		Depth:       4,
		Payload: map[string]any{
			"timestamp_ms":   float64(1739163625000),
			"tool_name":      "shell",
			"call_id":        "call_1",
			"arguments_json": `{"command":"npm run build"}`,
		},
	}
	got := formatCXDBTurn(turn)
	if !strings.Contains(got, "TOOL_CALL") {
		t.Fatalf("expected TOOL_CALL: %s", got)
	}
	if !strings.Contains(got, "shell") {
		t.Fatalf("expected tool_name: %s", got)
	}
	if !strings.Contains(got, "npm run build") {
		t.Fatalf("expected arguments: %s", got)
	}
}

func TestFormatCXDBTurn_ToolResult(t *testing.T) {
	turn := cxdb.Turn{
		TypeID:      "com.kilroy.attractor.ToolResult",
		TypeVersion: 1,
		Depth:       5,
		Payload: map[string]any{
			"timestamp_ms": float64(1739163625000),
			"tool_name":    "shell",
			"call_id":      "call_1",
			"output":       "Build succeeded",
			"is_error":     "false",
		},
	}
	got := formatCXDBTurn(turn)
	if !strings.Contains(got, "TOOL_RESULT") {
		t.Fatalf("expected TOOL_RESULT: %s", got)
	}
	if !strings.Contains(got, "ok") {
		t.Fatalf("expected ok status: %s", got)
	}
	if !strings.Contains(got, "Build succeeded") {
		t.Fatalf("expected output: %s", got)
	}
}

func TestFormatCXDBTurn_ToolResultError(t *testing.T) {
	turn := cxdb.Turn{
		TypeID:      "com.kilroy.attractor.ToolResult",
		TypeVersion: 1,
		Depth:       5,
		Payload: map[string]any{
			"timestamp_ms": float64(1739163625000),
			"tool_name":    "shell",
			"call_id":      "call_2",
			"output":       "command not found",
			"is_error":     "true",
		},
	}
	got := formatCXDBTurn(turn)
	if !strings.Contains(got, "ERROR") {
		t.Fatalf("expected ERROR status: %s", got)
	}
}

func TestFormatCXDBTurn_GitCheckpoint(t *testing.T) {
	turn := cxdb.Turn{
		TypeID:      "com.kilroy.attractor.GitCheckpoint",
		TypeVersion: 1,
		Depth:       6,
		Payload: map[string]any{
			"timestamp_ms":   float64(1739163625000),
			"node_id":        "implement_feature",
			"status":         "success",
			"git_commit_sha": "abcdef1234567890",
		},
	}
	got := formatCXDBTurn(turn)
	if !strings.Contains(got, "GIT_CHECKPOINT") {
		t.Fatalf("expected GIT_CHECKPOINT: %s", got)
	}
	if !strings.Contains(got, "abcdef12") {
		t.Fatalf("expected truncated SHA: %s", got)
	}
}

func TestFormatCXDBTurn_Artifact(t *testing.T) {
	turn := cxdb.Turn{
		TypeID:      "com.kilroy.attractor.Artifact",
		TypeVersion: 1,
		Depth:       7,
		Payload: map[string]any{
			"timestamp_ms": float64(1739163625000),
			"node_id":      "implement_feature",
			"name":         "diff.patch",
			"mime":         "text/x-diff",
		},
	}
	got := formatCXDBTurn(turn)
	if !strings.Contains(got, "ARTIFACT") {
		t.Fatalf("expected ARTIFACT: %s", got)
	}
	if !strings.Contains(got, "diff.patch") {
		t.Fatalf("expected artifact name: %s", got)
	}
}

func TestFormatCXDBTurn_RunCompleted(t *testing.T) {
	turn := cxdb.Turn{
		TypeID:      "com.kilroy.attractor.RunCompleted",
		TypeVersion: 1,
		Depth:       20,
		Payload: map[string]any{
			"timestamp_ms":         float64(1739163625000),
			"final_status":         "success",
			"final_git_commit_sha": "deadbeef12345678",
		},
	}
	got := formatCXDBTurn(turn)
	if !strings.Contains(got, "RUN_COMPLETED") {
		t.Fatalf("expected RUN_COMPLETED: %s", got)
	}
	if !strings.Contains(got, "success") {
		t.Fatalf("expected success: %s", got)
	}
	if !strings.Contains(got, "deadbeef") {
		t.Fatalf("expected truncated SHA: %s", got)
	}
}

func TestFormatCXDBTurn_AssistantMessage(t *testing.T) {
	turn := cxdb.Turn{
		TypeID:      "com.kilroy.attractor.AssistantMessage",
		TypeVersion: 1,
		Depth:       8,
		Payload: map[string]any{
			"timestamp_ms":   float64(1739163625000),
			"model":          "claude-sonnet-4-5-20250929",
			"input_tokens":   float64(1500),
			"output_tokens":  float64(42),
			"tool_use_count": float64(2),
			"text":           "Let me read the file and check the tests.",
		},
	}
	got := formatCXDBTurn(turn)
	if !strings.Contains(got, "ASSISTANT_MSG") {
		t.Fatalf("expected ASSISTANT_MSG: %s", got)
	}
	if !strings.Contains(got, "claude-sonnet-4-5-20250929") {
		t.Fatalf("expected model: %s", got)
	}
	if !strings.Contains(got, "in=1500") {
		t.Fatalf("expected input tokens: %s", got)
	}
	if !strings.Contains(got, "out=42") {
		t.Fatalf("expected output tokens: %s", got)
	}
	if !strings.Contains(got, "2 tools") {
		t.Fatalf("expected tool count: %s", got)
	}
	if !strings.Contains(got, "Let me read") {
		t.Fatalf("expected text preview: %s", got)
	}
}

func TestFormatCXDBTurn_AssistantMessageTextOnly(t *testing.T) {
	turn := cxdb.Turn{
		TypeID:      "com.kilroy.attractor.AssistantMessage",
		TypeVersion: 1,
		Depth:       9,
		Payload: map[string]any{
			"timestamp_ms":   float64(1739163625000),
			"model":          "claude-sonnet-4-5-20250929",
			"input_tokens":   float64(500),
			"output_tokens":  float64(10),
			"tool_use_count": float64(0),
			"text":           "Done.",
		},
	}
	got := formatCXDBTurn(turn)
	if !strings.Contains(got, "ASSISTANT_MSG") {
		t.Fatalf("expected ASSISTANT_MSG: %s", got)
	}
	// No "(0 tools)" when count is 0
	if strings.Contains(got, "tools") {
		t.Fatalf("expected no tools mention for 0 tools: %s", got)
	}
}

func TestRunFollowCXDB_FallsBackWithoutManifest(t *testing.T) {
	logs := t.TempDir()
	// No manifest.json, but has progress.ndjson and final.json
	_ = os.WriteFile(filepath.Join(logs, "progress.ndjson"),
		[]byte(`{"ts":"2026-02-10T04:00:25Z","event":"stage_attempt_start","node_id":"start","attempt":1,"max":1}`+"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(logs, "final.json"), []byte(`{"status":"success"}`), 0o644)

	var buf bytes.Buffer
	code := runFollowCXDB(logs, &buf, false)
	if code != 0 {
		t.Fatalf("expected fallback to succeed, got exit code %d", code)
	}
	// Should fall back to progress.ndjson and print the event
	if !strings.Contains(buf.String(), "start") {
		t.Fatalf("expected fallback output: %s", buf.String())
	}
}

func TestRunAttractorStatus_CXDBFlagAutoDetect(t *testing.T) {
	// When --follow is used and manifest.json exists but CXDB is unreachable,
	// it should fall back to progress.ndjson gracefully.
	logs := t.TempDir()

	manifest := map[string]any{
		"run_id": "test-run-1",
		"cxdb": map[string]any{
			"http_base_url": "http://127.0.0.1:99999", // unreachable
			"context_id":    "42",
		},
	}
	b, _ := json.Marshal(manifest)
	_ = os.WriteFile(filepath.Join(logs, "manifest.json"), b, 0o644)
	_ = os.WriteFile(filepath.Join(logs, "progress.ndjson"),
		[]byte(`{"ts":"2026-02-10T04:00:25Z","event":"stage_attempt_start","node_id":"start","attempt":1,"max":1}`+"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(logs, "final.json"), []byte(`{"status":"success"}`), 0o644)

	var stdout, stderr bytes.Buffer
	code := runAttractorStatus([]string{"--follow", "--logs-root", logs}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected fallback success, got exit code %d; stderr: %s", code, stderr.String())
	}
	// Should have fallen back and printed progress events
	if !strings.Contains(stdout.String(), "start") {
		t.Fatalf("expected progress output after CXDB fallback: %s", stdout.String())
	}
}
