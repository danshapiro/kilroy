package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/strongdm/kilroy/internal/attractor/procutil"
	"github.com/strongdm/kilroy/internal/cxdb"
)

// cxdbManifest is the subset of manifest.json we need for CXDB follow.
type cxdbManifest struct {
	RunID string `json:"run_id"`
	Goal  string `json:"goal"`
	CXDB  struct {
		HTTPBaseURL string `json:"http_base_url"`
		ContextID   string `json:"context_id"`
	} `json:"cxdb"`
}

// loadCXDBManifest reads the manifest.json from logs_root.
func loadCXDBManifest(logsRoot string) (*cxdbManifest, error) {
	b, err := os.ReadFile(filepath.Join(logsRoot, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var m cxdbManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m.CXDB.HTTPBaseURL == "" || m.CXDB.ContextID == "" {
		return nil, fmt.Errorf("manifest.json missing cxdb.http_base_url or cxdb.context_id")
	}
	return &m, nil
}

// runFollowCXDB polls CXDB for new turns and prints them formatted.
// Falls back to progress.ndjson if CXDB is unavailable.
func runFollowCXDB(logsRoot string, w io.Writer, raw bool) int {
	manifest, err := loadCXDBManifest(logsRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cxdb: %v (falling back to progress.ndjson)\n", err)
		return runFollowProgress(logsRoot, w, raw)
	}

	client := cxdb.New(manifest.CXDB.HTTPBaseURL)
	ctx := context.Background()

	// Check CXDB health.
	if err := client.Health(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "cxdb: unreachable at %s (falling back to progress.ndjson)\n", manifest.CXDB.HTTPBaseURL)
		return runFollowProgress(logsRoot, w, raw)
	}

	fmt.Fprintf(w, "connected to CXDB at %s (context %s)\n", manifest.CXDB.HTTPBaseURL, manifest.CXDB.ContextID)
	if manifest.Goal != "" {
		fmt.Fprintf(w, "goal: %s\n", manifest.Goal)
	}
	fmt.Fprintln(w, strings.Repeat("-", 80))

	finalPath := filepath.Join(logsRoot, "final.json")
	pidPath := filepath.Join(logsRoot, "run.pid")

	// Track last seen turn depth to avoid re-printing.
	lastDepth := 0

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		// Fetch all turns and sort by depth ascending so we process in order
		// regardless of CXDB's return ordering. This also ensures we never
		// skip events when the response is capped by a server-side limit.
		turns, err := client.ListTurns(ctx, manifest.CXDB.ContextID, cxdb.ListTurnsOptions{})
		if err != nil {
			// CXDB may have gone down — just wait and retry.
			<-ticker.C
			continue
		}

		sort.Slice(turns, func(i, j int) bool { return turns[i].Depth < turns[j].Depth })

		// Print turns we haven't seen yet.
		for _, turn := range turns {
			if turn.Depth <= lastDepth {
				continue
			}
			if raw {
				b, _ := json.Marshal(turn)
				fmt.Fprintln(w, string(b))
			} else {
				line := formatCXDBTurn(turn)
				if line != "" {
					fmt.Fprintln(w, line)
				}
			}
			lastDepth = turn.Depth
		}

		// Check for terminal state.
		if isTerminal(finalPath) {
			printFinalSummary(finalPath, w)
			return 0
		}

		// Check if PID is still alive.
		if pid := readPID(pidPath); pid > 0 && !procutil.PIDAlive(pid) {
			fmt.Fprintf(w, "\nrun process (pid %d) is no longer alive\n", pid)
			return 1
		}

		<-ticker.C
	}
}

func formatCXDBTurn(turn cxdb.Turn) string {
	p := turn.Payload
	if p == nil {
		return ""
	}

	ts := formatCXDBTimestamp(p)
	nodeID := payloadStr(p, "node_id")

	switch turn.TypeID {
	case "com.kilroy.attractor.RunStarted":
		goal := payloadStr(p, "goal")
		if len(goal) > 80 {
			goal = goal[:77] + "..."
		}
		return fmt.Sprintf("%s | RUN_STARTED            | %s", ts, goal)

	case "com.kilroy.attractor.StageStarted":
		handler := payloadStr(p, "handler_type")
		attempt := payloadStr(p, "attempt")
		if attempt != "" {
			return fmt.Sprintf("%s | STAGE_STARTED          | %s (%s, attempt %s)", ts, nodeID, handler, attempt)
		}
		return fmt.Sprintf("%s | STAGE_STARTED          | %s (%s)", ts, nodeID, handler)

	case "com.kilroy.attractor.StageFinished":
		status := payloadStr(p, "status")
		line := fmt.Sprintf("%s | STAGE_FINISHED         | %s | %s", ts, nodeID, status)
		if reason := payloadStr(p, "failure_reason"); reason != "" {
			if len(reason) > 100 {
				reason = reason[:97] + "..."
			}
			line += " | " + reason
		}
		if notes := payloadStr(p, "notes"); notes != "" {
			if len(notes) > 100 {
				notes = notes[:97] + "..."
			}
			line += "\n" + strings.Repeat(" ", 11) + "notes: " + notes
		}
		return line

	case "com.kilroy.attractor.ToolCall":
		toolName := payloadStr(p, "tool_name")
		callID := payloadStr(p, "call_id")
		argsJSON := payloadStr(p, "arguments_json")
		if len(argsJSON) > 120 {
			argsJSON = argsJSON[:117] + "..."
		}
		if argsJSON != "" {
			return fmt.Sprintf("%s | TOOL_CALL              | %s [%s] %s", ts, toolName, callID, argsJSON)
		}
		return fmt.Sprintf("%s | TOOL_CALL              | %s [%s]", ts, toolName, callID)

	case "com.kilroy.attractor.ToolResult":
		toolName := payloadStr(p, "tool_name")
		callID := payloadStr(p, "call_id")
		isErr := payloadStr(p, "is_error")
		output := payloadStr(p, "output")
		if len(output) > 120 {
			output = output[:117] + "..."
		}
		status := "ok"
		if isErr == "true" {
			status = "ERROR"
		}
		if output != "" {
			return fmt.Sprintf("%s | TOOL_RESULT            | %s [%s] %s: %s", ts, toolName, callID, status, output)
		}
		return fmt.Sprintf("%s | TOOL_RESULT            | %s [%s] %s", ts, toolName, callID, status)

	case "com.kilroy.attractor.GitCheckpoint":
		sha := payloadStr(p, "git_commit_sha")
		status := payloadStr(p, "status")
		if len(sha) > 8 {
			sha = sha[:8]
		}
		return fmt.Sprintf("%s | GIT_CHECKPOINT         | %s | %s (%s)", ts, nodeID, sha, status)

	case "com.kilroy.attractor.CheckpointSaved":
		return fmt.Sprintf("%s | CHECKPOINT_SAVED       | context=%s", ts, payloadStr(p, "cxdb_context_id"))

	case "com.kilroy.attractor.Artifact":
		name := payloadStr(p, "name")
		mime := payloadStr(p, "mime")
		return fmt.Sprintf("%s | ARTIFACT               | %s/%s (%s)", ts, nodeID, name, mime)

	case "com.kilroy.attractor.BackendTraceRef":
		return fmt.Sprintf("%s | BACKEND_TRACE          | %s", ts, nodeID)

	case "com.kilroy.attractor.RunCompleted":
		status := payloadStr(p, "final_status")
		sha := payloadStr(p, "final_git_commit_sha")
		if len(sha) > 8 {
			sha = sha[:8]
		}
		return fmt.Sprintf("%s | RUN_COMPLETED          | %s (commit %s)", ts, status, sha)

	case "com.kilroy.attractor.AssistantMessage":
		model := payloadStr(p, "model")
		inTok := payloadStr(p, "input_tokens")
		outTok := payloadStr(p, "output_tokens")
		toolCount := payloadStr(p, "tool_use_count")
		text := payloadStr(p, "text")
		if len(text) > 120 {
			text = text[:117] + "..."
		}
		line := fmt.Sprintf("%s | ASSISTANT_MSG          | %s [in=%s out=%s]", ts, model, inTok, outTok)
		if toolCount != "" && toolCount != "0" {
			line += fmt.Sprintf(" (%s tools)", toolCount)
		}
		if text != "" {
			line += "\n" + strings.Repeat(" ", 11) + text
		}
		return line

	case "com.kilroy.attractor.Prompt":
		text := payloadStr(p, "text")
		if len(text) > 120 {
			text = text[:117] + "..."
		}
		return fmt.Sprintf("%s | PROMPT                 | %s\n%s%s", ts, nodeID, strings.Repeat(" ", 11), text)

	case "com.kilroy.attractor.RunFailed":
		reason := payloadStr(p, "reason")
		return fmt.Sprintf("%s | RUN_FAILED             | %s | %s", ts, nodeID, reason)

	default:
		return fmt.Sprintf("%s | %-22s | depth=%d", ts, turn.TypeID, turn.Depth)
	}
}

func formatCXDBTimestamp(p map[string]any) string {
	// CXDB events use timestamp_ms (unix milliseconds).
	if v, ok := p["timestamp_ms"]; ok {
		var ms int64
		switch t := v.(type) {
		case float64:
			ms = int64(t)
		case json.Number:
			ms, _ = t.Int64()
		}
		if ms > 0 {
			return time.UnixMilli(ms).UTC().Format("15:04:05")
		}
	}
	// Fallback to ts field.
	if ts := payloadStr(p, "ts"); ts != "" {
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			return t.Format("15:04:05")
		}
	}
	return "         "
}

func payloadStr(p map[string]any, key string) string {
	v, ok := p[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprint(t)
	}
}
