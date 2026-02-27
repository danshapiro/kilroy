package llm

// Stream is an asynchronous iterator of StreamEvent values. Implementations must
// be explicitly closed when the consumer is done to avoid leaking connections.
type Stream interface {
	Events() <-chan StreamEvent
	Close() error
}

type StreamEventType string

const (
	StreamEventStreamStart    StreamEventType = "STREAM_START"
	StreamEventTextStart      StreamEventType = "TEXT_START"
	StreamEventTextDelta      StreamEventType = "TEXT_DELTA"
	StreamEventTextEnd        StreamEventType = "TEXT_END"
	StreamEventReasoningStart StreamEventType = "REASONING_START"
	StreamEventReasoningDelta StreamEventType = "REASONING_DELTA"
	StreamEventReasoningEnd   StreamEventType = "REASONING_END"
	StreamEventToolCallStart  StreamEventType = "TOOL_CALL_START"
	StreamEventToolCallDelta  StreamEventType = "TOOL_CALL_DELTA"
	StreamEventToolCallEnd    StreamEventType = "TOOL_CALL_END"
	StreamEventStepFinish     StreamEventType = "STEP_FINISH"
	StreamEventFinish         StreamEventType = "FINISH"
	StreamEventError          StreamEventType = "ERROR"
	StreamEventProviderEvent  StreamEventType = "PROVIDER_EVENT"
)

type StreamEvent struct {
	Type StreamEventType `json:"type"`

	// Stream start metadata
	ID       string    `json:"id,omitempty"`
	Model    string    `json:"model,omitempty"`
	Warnings []Warning `json:"warnings,omitempty"`

	// Text events
	Delta  string `json:"delta,omitempty"`
	TextID string `json:"text_id,omitempty"`

	// Reasoning events
	ReasoningDelta string `json:"reasoning_delta,omitempty"`
	ReasoningID    string `json:"reasoning_id,omitempty"`
	Redacted       *bool  `json:"redacted,omitempty"`

	// Tool call events
	ToolCall *ToolCallData `json:"tool_call,omitempty"`

	// Finish event
	FinishReason *FinishReason `json:"finish_reason,omitempty"`
	Usage        *Usage        `json:"usage,omitempty"`
	Response     *Response     `json:"response,omitempty"`

	// Error event
	Err error `json:"-"`

	// Passthrough
	EventType string         `json:"event_type,omitempty"`
	Raw       map[string]any `json:"raw,omitempty"`
}
