package engine

import (
	"sync"
	"time"
)

// streamProgressEmitter batches LLM text deltas and flushes them
// as progress events at a capped rate (default 100ms). Tool-call
// and turn-end events are forwarded immediately.
type streamProgressEmitter struct {
	eng    *Engine
	nodeID string
	runID  string

	mu         sync.Mutex
	buf        string
	flushTimer *time.Timer
	interval   time.Duration
	closed     bool
}

func newStreamProgressEmitter(eng *Engine, nodeID, runID string) *streamProgressEmitter {
	return &streamProgressEmitter{
		eng:      eng,
		nodeID:   nodeID,
		runID:    runID,
		interval: 100 * time.Millisecond,
	}
}

// appendDelta buffers a text delta and schedules a flush.
func (e *streamProgressEmitter) appendDelta(delta string) {
	if delta == "" || e.eng == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	e.buf += delta
	if e.flushTimer == nil {
		e.flushTimer = time.AfterFunc(e.interval, e.timerFlush)
	}
}

// emitToolCallStart forwards a tool-call-start event immediately.
func (e *streamProgressEmitter) emitToolCallStart(toolName, callID string) {
	if e.eng == nil {
		return
	}
	e.flushDelta()
	e.eng.appendProgress(map[string]any{
		"event":     "llm_tool_call_start",
		"node_id":   e.nodeID,
		"run_id":    e.runID,
		"backend":   "api",
		"tool_name": toolName,
		"call_id":   callID,
	})
}

// emitToolCallEnd forwards a tool-call-end event immediately.
func (e *streamProgressEmitter) emitToolCallEnd(toolName, callID string, isError bool) {
	if e.eng == nil {
		return
	}
	e.flushDelta()
	e.eng.appendProgress(map[string]any{
		"event":     "llm_tool_call_end",
		"node_id":   e.nodeID,
		"run_id":    e.runID,
		"backend":   "api",
		"tool_name": toolName,
		"call_id":   callID,
		"is_error":  isError,
	})
}

// emitTurnEnd flushes any pending delta and emits a turn-end event.
func (e *streamProgressEmitter) emitTurnEnd(textLen int, toolCallCount int) {
	if e.eng == nil {
		return
	}
	e.flushDelta()
	e.eng.appendProgress(map[string]any{
		"event":           "llm_turn_end",
		"node_id":         e.nodeID,
		"run_id":          e.runID,
		"backend":         "api",
		"text_length":     textLen,
		"tool_call_count": toolCallCount,
	})
}

// close stops the timer and flushes remaining data.
func (e *streamProgressEmitter) close() {
	e.mu.Lock()
	e.closed = true
	if e.flushTimer != nil {
		e.flushTimer.Stop()
		e.flushTimer = nil
	}
	pending := e.buf
	e.buf = ""
	e.mu.Unlock()
	if pending != "" && e.eng != nil {
		e.eng.appendProgress(map[string]any{
			"event":   "llm_text_delta",
			"node_id": e.nodeID,
			"run_id":  e.runID,
			"backend": "api",
			"delta":   pending,
		})
	}
}

func (e *streamProgressEmitter) timerFlush() {
	e.flushDelta()
}

func (e *streamProgressEmitter) flushDelta() {
	e.mu.Lock()
	if e.flushTimer != nil {
		e.flushTimer.Stop()
		e.flushTimer = nil
	}
	pending := e.buf
	e.buf = ""
	e.mu.Unlock()
	if pending != "" && e.eng != nil {
		e.eng.appendProgress(map[string]any{
			"event":   "llm_text_delta",
			"node_id": e.nodeID,
			"run_id":  e.runID,
			"backend": "api",
			"delta":   pending,
		})
	}
}
