package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/agent"
)

func TestThrottledEmitter_ShouldBatchTextDeltasWithinFlushInterval(t *testing.T) {
	var mu sync.Mutex
	var captured []map[string]any
	eng := &Engine{
		progressSink: func(ev map[string]any) {
			mu.Lock()
			defer mu.Unlock()
			captured = append(captured, ev)
		},
		Options: RunOptions{RunID: "run-1"},
	}

	em := newStreamProgressEmitter(eng, "node_1", "run-1")
	em.interval = 50 * time.Millisecond

	em.appendDelta("Hello")
	em.appendDelta(" wor")
	em.appendDelta("ld")

	// Before flush interval elapses, nothing should be emitted.
	mu.Lock()
	count := len(captured)
	mu.Unlock()
	if count != 0 {
		t.Fatalf("expected 0 events before flush interval, got %d", count)
	}

	// Wait for the flush timer to fire.
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	count = len(captured)
	mu.Unlock()
	if count != 1 {
		t.Fatalf("expected 1 batched event after flush, got %d", count)
	}

	mu.Lock()
	ev := captured[0]
	mu.Unlock()
	if ev["event"] != "llm_text_delta" {
		t.Fatalf("expected llm_text_delta event, got %v", ev["event"])
	}
	if ev["delta"] != "Hello world" {
		t.Fatalf("expected batched delta 'Hello world', got %q", ev["delta"])
	}
	if ev["backend"] != "api" {
		t.Fatalf("expected backend 'api', got %v", ev["backend"])
	}
	em.close()
}

func TestThrottledEmitter_ShouldForceFlushOnTurnEnd(t *testing.T) {
	var mu sync.Mutex
	var captured []map[string]any
	eng := &Engine{
		progressSink: func(ev map[string]any) {
			mu.Lock()
			defer mu.Unlock()
			captured = append(captured, ev)
		},
		Options: RunOptions{RunID: "run-1"},
	}

	em := newStreamProgressEmitter(eng, "node_1", "run-1")
	em.interval = 5 * time.Second // Long interval to prove force-flush works.

	em.appendDelta("partial response")
	em.emitTurnEnd(16, 0)

	mu.Lock()
	defer mu.Unlock()
	if len(captured) != 2 {
		t.Fatalf("expected 2 events (delta + turn_end), got %d", len(captured))
	}
	if captured[0]["event"] != "llm_text_delta" {
		t.Fatalf("first event should be llm_text_delta, got %v", captured[0]["event"])
	}
	if captured[0]["delta"] != "partial response" {
		t.Fatalf("expected flushed delta, got %q", captured[0]["delta"])
	}
	if captured[1]["event"] != "llm_turn_end" {
		t.Fatalf("second event should be llm_turn_end, got %v", captured[1]["event"])
	}
}

func TestThrottledEmitter_ShouldFlushOnClose(t *testing.T) {
	var mu sync.Mutex
	var captured []map[string]any
	eng := &Engine{
		progressSink: func(ev map[string]any) {
			mu.Lock()
			defer mu.Unlock()
			captured = append(captured, ev)
		},
		Options: RunOptions{RunID: "run-1"},
	}

	em := newStreamProgressEmitter(eng, "node_1", "run-1")
	em.interval = 5 * time.Second

	em.appendDelta("buffered data")
	em.close()

	mu.Lock()
	defer mu.Unlock()
	if len(captured) != 1 {
		t.Fatalf("expected 1 flushed event on close, got %d", len(captured))
	}
	if captured[0]["delta"] != "buffered data" {
		t.Fatalf("expected flushed delta, got %q", captured[0]["delta"])
	}
}

func TestThrottledEmitter_ShouldEmitToolCallEventsImmediately(t *testing.T) {
	var mu sync.Mutex
	var captured []map[string]any
	eng := &Engine{
		progressSink: func(ev map[string]any) {
			mu.Lock()
			defer mu.Unlock()
			captured = append(captured, ev)
		},
		Options: RunOptions{RunID: "run-1"},
	}

	em := newStreamProgressEmitter(eng, "node_1", "run-1")
	em.emitToolCallStart("write_file", "call_1")
	em.emitToolCallEnd("write_file", "call_1", false)

	mu.Lock()
	defer mu.Unlock()
	if len(captured) != 2 {
		t.Fatalf("expected 2 events, got %d", len(captured))
	}
	if captured[0]["event"] != "llm_tool_call_start" {
		t.Fatalf("expected llm_tool_call_start, got %v", captured[0]["event"])
	}
	if captured[0]["tool_name"] != "write_file" {
		t.Fatalf("expected tool_name write_file, got %v", captured[0]["tool_name"])
	}
	if captured[1]["event"] != "llm_tool_call_end" {
		t.Fatalf("expected llm_tool_call_end, got %v", captured[1]["event"])
	}
	if captured[1]["is_error"] != false {
		t.Fatalf("expected is_error false, got %v", captured[1]["is_error"])
	}
	em.close()
}

func TestEmitStreamProgress_ShouldForwardAgentSessionEvents(t *testing.T) {
	var mu sync.Mutex
	var captured []map[string]any
	eng := &Engine{
		progressSink: func(ev map[string]any) {
			mu.Lock()
			defer mu.Unlock()
			captured = append(captured, ev)
		},
		Options: RunOptions{RunID: "run-1"},
	}

	em := newStreamProgressEmitter(eng, "node_1", "run-1")
	em.interval = 5 * time.Second

	events := []agent.SessionEvent{
		{Kind: agent.EventAssistantTextDelta, Data: map[string]any{"delta": "Hello"}, Timestamp: time.Now()},
		{Kind: agent.EventAssistantTextDelta, Data: map[string]any{"delta": " world"}, Timestamp: time.Now()},
		{Kind: agent.EventToolCallStart, Data: map[string]any{"tool_name": "read_file", "call_id": "c1"}, Timestamp: time.Now()},
		{Kind: agent.EventToolCallEnd, Data: map[string]any{"tool_name": "read_file", "call_id": "c1", "is_error": false}, Timestamp: time.Now()},
		{Kind: agent.EventAssistantTextEnd, Data: map[string]any{"text": "Hello world"}, Timestamp: time.Now()},
	}

	for _, ev := range events {
		emitStreamProgress(em, ev)
	}

	mu.Lock()
	defer mu.Unlock()

	var eventTypes []string
	for _, ev := range captured {
		eventTypes = append(eventTypes, ev["event"].(string))
	}

	has := func(eventType string) bool {
		for _, et := range eventTypes {
			if et == eventType {
				return true
			}
		}
		return false
	}
	for _, want := range []string{"llm_text_delta", "llm_tool_call_start", "llm_tool_call_end", "llm_turn_end"} {
		if !has(want) {
			t.Errorf("missing event type %q in %v", want, eventTypes)
		}
	}

	// All events should have backend: "api".
	for _, ev := range captured {
		if ev["backend"] != "api" {
			t.Errorf("expected backend 'api', got %v for event %v", ev["backend"], ev["event"])
		}
	}
}

func TestEmitCXDBToolTurns_ShouldRecordAssistantMessageOnTextEnd(t *testing.T) {
	srv := newCXDBTestServer(t)
	eng := newTestEngineWithCXDB(t, srv)

	ev := agent.SessionEvent{
		Kind:      agent.EventAssistantTextEnd,
		Timestamp: time.Now(),
		Data:      map[string]any{"text": "Here is the implementation of your feature."},
	}
	emitCXDBToolTurns(context.Background(), eng, "codegen_1", ev)

	ctxIDs := srv.ContextIDs()
	if len(ctxIDs) == 0 {
		t.Fatal("expected at least one CXDB context")
	}
	turns := srv.Turns(ctxIDs[0])
	found := false
	for _, turn := range turns {
		if turn["type_id"] == "com.kilroy.attractor.AssistantMessage" {
			found = true
			payload, _ := turn["payload"].(map[string]any)
			if payload == nil {
				t.Fatal("expected payload in turn")
			}
			if payload["node_id"] != "codegen_1" {
				t.Fatalf("expected node_id codegen_1, got %v", payload["node_id"])
			}
			text, _ := payload["text"].(string)
			if text != "Here is the implementation of your feature." {
				t.Fatalf("expected full text, got %q", text)
			}
		}
	}
	if !found {
		t.Fatal("expected AssistantMessage turn in CXDB")
	}
}

func TestEmitCXDBToolTurns_ShouldSkipEmptyAssistantText(t *testing.T) {
	srv := newCXDBTestServer(t)
	eng := newTestEngineWithCXDB(t, srv)

	ev := agent.SessionEvent{
		Kind:      agent.EventAssistantTextEnd,
		Timestamp: time.Now(),
		Data:      map[string]any{"text": "  "},
	}
	emitCXDBToolTurns(context.Background(), eng, "codegen_1", ev)

	ctxIDs := srv.ContextIDs()
	if len(ctxIDs) == 0 {
		t.Fatal("expected at least one CXDB context")
	}
	turns := srv.Turns(ctxIDs[0])
	for _, turn := range turns {
		if turn["type_id"] == "com.kilroy.attractor.AssistantMessage" {
			t.Fatal("should not record AssistantMessage for empty text")
		}
	}
}
