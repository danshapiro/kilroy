package llm

import "strings"

// StreamAccumulator collects StreamEvent values and produces a complete Response.
// It primarily exists to bridge streaming mode back to code that expects a Response.
type StreamAccumulator struct {
	textByID  map[string]*strings.Builder
	textOrder []string
	finish    *FinishReason
	usage     *Usage
	final     *Response
	partial   *Response
}

func NewStreamAccumulator() *StreamAccumulator {
	return &StreamAccumulator{
		textByID:  map[string]*strings.Builder{},
		textOrder: nil,
	}
}

func (a *StreamAccumulator) Process(ev StreamEvent) {
	if a == nil {
		return
	}
	switch ev.Type {
	case StreamEventTextStart:
		id := strings.TrimSpace(ev.TextID)
		if id == "" {
			id = "text_0"
		}
		if _, ok := a.textByID[id]; !ok {
			a.textByID[id] = &strings.Builder{}
			a.textOrder = append(a.textOrder, id)
		}
	case StreamEventTextDelta:
		id := strings.TrimSpace(ev.TextID)
		if id == "" {
			id = "text_0"
		}
		b, ok := a.textByID[id]
		if !ok {
			b = &strings.Builder{}
			a.textByID[id] = b
			a.textOrder = append(a.textOrder, id)
		}
		if ev.Delta != "" {
			b.WriteString(ev.Delta)
			a.partial = a.buildResponse()
		}
	case StreamEventFinish:
		a.finish = ev.FinishReason
		a.usage = ev.Usage
		if ev.Response != nil {
			cp := *ev.Response
			a.final = &cp
			a.partial = &cp
			return
		}
		r := a.buildResponse()
		a.final = r
		a.partial = r
	default:
		// ignore
	}
}

// Response returns the final accumulated response after FINISH, or nil if the stream
// has not completed.
func (a *StreamAccumulator) Response() *Response {
	if a == nil {
		return nil
	}
	return a.final
}

// PartialResponse returns the best-effort accumulated response so far (may be nil).
func (a *StreamAccumulator) PartialResponse() *Response {
	if a == nil {
		return nil
	}
	if a.partial != nil {
		cp := *a.partial
		return &cp
	}
	return nil
}

func (a *StreamAccumulator) buildResponse() *Response {
	if a == nil {
		return nil
	}
	var b strings.Builder
	for _, id := range a.textOrder {
		if tb := a.textByID[id]; tb != nil {
			b.WriteString(tb.String())
		}
	}
	msg := Message{Role: RoleAssistant, Content: []ContentPart{{Kind: ContentText, Text: b.String()}}}
	r := &Response{Message: msg}
	if a.finish != nil {
		r.Finish = *a.finish
	}
	if a.usage != nil {
		r.Usage = *a.usage
	}
	return r
}
