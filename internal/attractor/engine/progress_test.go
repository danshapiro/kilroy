package engine

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEngine_appendProgress_WritesNDJSONAndLiveSnapshot(t *testing.T) {
	dir := t.TempDir()

	e := &Engine{
		LogsRoot: dir,
		Options:  RunOptions{RunID: "r1"},
	}

	e.appendProgress(map[string]any{"event": "first"})
	e.appendProgress(map[string]any{"event": "second"})

	// progress.ndjson should have 2 lines.
	nd := filepath.Join(dir, "progress.ndjson")
	f, err := os.Open(nd)
	if err != nil {
		t.Fatalf("open progress.ndjson: %v", err)
	}
	defer func() { _ = f.Close() }()

	var events []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("unmarshal ndjson: %v (line=%q)", err, line)
		}
		events = append(events, ev)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan ndjson: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("ndjson lines: got %d want %d", len(events), 2)
	}
	if events[0]["event"] != "first" || events[1]["event"] != "second" {
		t.Fatalf("events: %#v", events)
	}
	if events[0]["run_id"] != "r1" || events[1]["run_id"] != "r1" {
		t.Fatalf("run_id: %#v", events)
	}
	if strings.TrimSpace(events[0]["ts"].(string)) == "" || strings.TrimSpace(events[1]["ts"].(string)) == "" {
		t.Fatalf("ts should be set: %#v", events)
	}

	// live.json should match the last event.
	liveBytes, err := os.ReadFile(filepath.Join(dir, "live.json"))
	if err != nil {
		t.Fatalf("read live.json: %v", err)
	}
	var live map[string]any
	if err := json.Unmarshal(liveBytes, &live); err != nil {
		t.Fatalf("unmarshal live.json: %v", err)
	}
	if live["event"] != "second" {
		t.Fatalf("live event: %#v", live["event"])
	}
	if live["run_id"] != "r1" {
		t.Fatalf("live run_id: %#v", live["run_id"])
	}
}

func TestProgressIncludesStatusIngestionDecisionEvent(t *testing.T) {
	events := runStatusIngestionProgressFixture(t)
	if !hasEvent(events, "status_ingestion_decision") {
		t.Fatal("missing status_ingestion_decision event")
	}
}

func TestProgressIncludesSubgraphCycleBreakEvent(t *testing.T) {
	events := runSubgraphCycleProgressFixture(t)
	if !hasEvent(events, "subgraph_deterministic_failure_cycle_breaker") {
		t.Fatal("missing subgraph cycle breaker event")
	}
}

func TestProgressIncludesCancellationExitEvent(t *testing.T) {
	events := runSubgraphCancelProgressFixture(t)
	if !hasEvent(events, "subgraph_canceled_exit") {
		t.Fatal("missing subgraph cancellation exit event")
	}
}

func TestCopyMap_DeepCopiesNestedCollections(t *testing.T) {
	input := map[string]any{
		"event":      "copy-check",
		"nested":     map[string]any{"k": "v"},
		"list":       []any{map[string]any{"id": "1"}, "x"},
		"typed_map":  map[string]string{"alpha": "one"},
		"typed_list": []string{"a", "b"},
	}

	got := copyMap(input)

	input["nested"].(map[string]any)["k"] = "mutated"
	input["list"].([]any)[0].(map[string]any)["id"] = "9"
	input["typed_map"].(map[string]string)["alpha"] = "changed"
	input["typed_list"].([]string)[0] = "z"

	if gotNested := got["nested"].(map[string]any)["k"]; gotNested != "v" {
		t.Fatalf("nested map was aliased: got %v want %v", gotNested, "v")
	}
	if gotID := got["list"].([]any)[0].(map[string]any)["id"]; gotID != "1" {
		t.Fatalf("nested list map was aliased: got %v want %v", gotID, "1")
	}
	if gotAlpha := got["typed_map"].(map[string]string)["alpha"]; gotAlpha != "one" {
		t.Fatalf("typed map was aliased: got %v want %v", gotAlpha, "one")
	}
	if gotFirst := got["typed_list"].([]string)[0]; gotFirst != "a" {
		t.Fatalf("typed list was aliased: got %v want %v", gotFirst, "a")
	}
}
