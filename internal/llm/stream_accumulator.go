package llm

import (
	"encoding/json"
	"strconv"
	"strings"
)

// StreamAccumulator collects StreamEvent values and produces a complete Response.
// It primarily exists to bridge streaming mode back to code that expects a Response.
type StreamAccumulator struct {
	textByID   map[string]*strings.Builder
	toolByID   map[string]*toolCallStreamState
	contentLog []streamContentRef
	seenParts  map[string]struct{}

	finish     *FinishReason
	usage      *Usage
	final      *Response
	partial    *Response
	nextToolID int
}

type streamContentRef struct {
	kind string
	id   string
}

type toolCallStreamState struct {
	id       string
	name     string
	typ      string
	args     strings.Builder
	sawDelta bool
}

func NewStreamAccumulator() *StreamAccumulator {
	return &StreamAccumulator{
		textByID:   map[string]*strings.Builder{},
		toolByID:   map[string]*toolCallStreamState{},
		contentLog: nil,
		seenParts:  map[string]struct{}{},
		nextToolID: 1,
	}
}

func (a *StreamAccumulator) Process(ev StreamEvent) {
	if a == nil {
		return
	}
	switch ev.Type {
	case StreamEventTextStart:
		_ = a.ensureText(strings.TrimSpace(ev.TextID))
	case StreamEventTextDelta:
		b := a.ensureText(strings.TrimSpace(ev.TextID))
		if ev.Delta != "" {
			b.WriteString(ev.Delta)
			a.partial = a.buildResponse()
		}
	case StreamEventToolCallStart:
		tc := a.ensureToolCall(ev.ToolCall)
		if tc == nil {
			return
		}
		if !tc.sawDelta && tc.args.Len() == 0 && len(ev.ToolCall.Arguments) > 0 {
			tc.args.Write(ev.ToolCall.Arguments)
		}
		a.partial = a.buildResponse()
	case StreamEventToolCallDelta:
		tc := a.ensureToolCall(ev.ToolCall)
		if tc == nil {
			return
		}
		if len(ev.ToolCall.Arguments) > 0 {
			tc.sawDelta = true
			tc.args.Write(ev.ToolCall.Arguments)
		}
		a.partial = a.buildResponse()
	case StreamEventToolCallEnd:
		tc := a.ensureToolCall(ev.ToolCall)
		if tc == nil {
			return
		}
		if !tc.sawDelta && tc.args.Len() == 0 && len(ev.ToolCall.Arguments) > 0 {
			tc.args.Write(ev.ToolCall.Arguments)
		}
		a.partial = a.buildResponse()
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
	content := make([]ContentPart, 0, len(a.contentLog))
	for _, ref := range a.contentLog {
		switch ref.kind {
		case string(ContentText):
			if tb := a.textByID[ref.id]; tb != nil {
				txt := tb.String()
				if txt != "" {
					content = append(content, ContentPart{Kind: ContentText, Text: txt})
				}
			}
		case string(ContentToolCall):
			tc := a.toolByID[ref.id]
			if tc == nil || strings.TrimSpace(tc.id) == "" || strings.TrimSpace(tc.name) == "" {
				continue
			}
			args := strings.TrimSpace(tc.args.String())
			var raw json.RawMessage
			if args != "" {
				raw = json.RawMessage(args)
			}
			typ := strings.TrimSpace(tc.typ)
			if typ == "" {
				typ = "function"
			}
			call := ToolCallData{
				ID:        tc.id,
				Name:      tc.name,
				Type:      typ,
				Arguments: raw,
			}
			content = append(content, ContentPart{Kind: ContentToolCall, ToolCall: &call})
		}
	}
	if len(content) == 0 {
		content = []ContentPart{{Kind: ContentText, Text: ""}}
	}
	msg := Message{Role: RoleAssistant, Content: content}
	r := &Response{Message: msg}
	if a.finish != nil {
		r.Finish = *a.finish
	}
	if a.usage != nil {
		r.Usage = *a.usage
	}
	return r
}

func (a *StreamAccumulator) ensureText(id string) *strings.Builder {
	if strings.TrimSpace(id) == "" {
		id = "text_0"
	}
	b, ok := a.textByID[id]
	if !ok {
		b = &strings.Builder{}
		a.textByID[id] = b
		a.recordContent(string(ContentText), id)
	}
	return b
}

func (a *StreamAccumulator) ensureToolCall(call *ToolCallData) *toolCallStreamState {
	if call == nil {
		return nil
	}
	id := strings.TrimSpace(call.ID)
	if id == "" {
		id = "tool_call_" + strconv.Itoa(a.nextToolID)
		a.nextToolID++
	}
	tc, ok := a.toolByID[id]
	if !ok {
		tc = &toolCallStreamState{id: id}
		a.toolByID[id] = tc
		a.recordContent(string(ContentToolCall), id)
	}
	if name := strings.TrimSpace(call.Name); name != "" {
		tc.name = name
	}
	if typ := strings.TrimSpace(call.Type); typ != "" {
		tc.typ = typ
	}
	return tc
}

func (a *StreamAccumulator) recordContent(kind, id string) {
	key := kind + ":" + id
	if _, ok := a.seenParts[key]; ok {
		return
	}
	a.seenParts[key] = struct{}{}
	a.contentLog = append(a.contentLog, streamContentRef{kind: kind, id: id})
}
