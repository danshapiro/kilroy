package agent

import "time"

type EventKind string

const (
	EventSessionStart        EventKind = "SESSION_START"
	EventSessionEnd          EventKind = "SESSION_END"
	EventUserInput           EventKind = "USER_INPUT"
	EventAssistantTextStart  EventKind = "ASSISTANT_TEXT_START"
	EventAssistantTextDelta  EventKind = "ASSISTANT_TEXT_DELTA"
	EventAssistantTextEnd    EventKind = "ASSISTANT_TEXT_END"
	EventToolCallStart       EventKind = "TOOL_CALL_START"
	EventToolCallOutputDelta EventKind = "TOOL_CALL_OUTPUT_DELTA"
	EventToolCallEnd         EventKind = "TOOL_CALL_END"
	EventSteeringInjected    EventKind = "STEERING_INJECTED"
	EventTurnLimit           EventKind = "TURN_LIMIT"
	EventLoopDetection       EventKind = "LOOP_DETECTION"
	EventWarning             EventKind = "WARNING"
	EventError               EventKind = "ERROR"
)

type SessionEvent struct {
	Kind      EventKind      `json:"kind"`
	Timestamp time.Time      `json:"timestamp"`
	SessionID string         `json:"session_id"`
	Data      map[string]any `json:"data,omitempty"`
}
