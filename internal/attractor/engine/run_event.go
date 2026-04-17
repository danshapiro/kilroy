// Canonical event envelope for all engine lifecycle events.
// Standardizes structure, IDs, timestamps, and dot-notation event names.
package engine

import (
	"crypto/rand"
	"fmt"
	"time"
)

// RunEvent is the canonical envelope for all engine events. Every event
// emitted by the engine flows through this type, providing consistent
// structure for progress.ndjson, SSE streaming, and run DB storage.
type RunEvent struct {
	// ID is a unique identifier for this event (UUIDv4).
	ID string `json:"id"`

	// Timestamp is when the event was emitted (UTC).
	Timestamp time.Time `json:"ts"`

	// RunID identifies the run that produced this event.
	RunID string `json:"run_id,omitempty"`

	// Event is the dot-notation event name (e.g. "node.started", "run.completed").
	Event string `json:"event"`

	// NodeID is the node context for this event (empty for run-level events).
	NodeID string `json:"node_id,omitempty"`

	// Properties contains event-specific structured data.
	Properties map[string]any `json:"properties,omitempty"`
}

// NewRunEvent creates a RunEvent with a generated ID and current timestamp.
func NewRunEvent(eventName, runID, nodeID string, props map[string]any) RunEvent {
	return RunEvent{
		ID:         newEventID(),
		Timestamp:  time.Now().UTC(),
		RunID:      runID,
		Event:      eventName,
		NodeID:     nodeID,
		Properties: props,
	}
}

// ToMap converts the RunEvent to a map for backward-compatible serialization.
// This bridges the canonical envelope to the existing map[string]any progress system.
func (e RunEvent) ToMap() map[string]any {
	m := map[string]any{
		"id":    e.ID,
		"ts":    e.Timestamp.Format(time.RFC3339Nano),
		"event": e.Event,
	}
	if e.RunID != "" {
		m["run_id"] = e.RunID
	}
	if e.NodeID != "" {
		m["node_id"] = e.NodeID
	}
	for k, v := range e.Properties {
		if k == "id" || k == "ts" || k == "event" || k == "run_id" || k == "node_id" {
			// Properties don't override envelope fields.
			m["prop."+k] = v
			continue
		}
		m[k] = v
	}
	return m
}

// FromMap creates a RunEvent from a legacy map[string]any progress event.
// Used to wrap existing ad-hoc events in the canonical envelope.
func FromMap(m map[string]any) RunEvent {
	ev := RunEvent{
		Properties: map[string]any{},
	}
	if id, ok := m["id"].(string); ok {
		ev.ID = id
	}
	if ev.ID == "" {
		ev.ID = newEventID()
	}
	if ts, ok := m["ts"].(string); ok {
		ev.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	if runID, ok := m["run_id"].(string); ok {
		ev.RunID = runID
	}
	if event, ok := m["event"].(string); ok {
		ev.Event = event
	}
	if nodeID, ok := m["node_id"].(string); ok {
		ev.NodeID = nodeID
	}
	// Everything else goes into properties.
	for k, v := range m {
		switch k {
		case "id", "ts", "event", "run_id", "node_id":
			continue
		}
		ev.Properties[k] = v
	}
	return ev
}

// newEventID generates a short unique event ID (UUIDv4-style, 8 hex chars).
func newEventID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}
