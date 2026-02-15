package server

import (
	"fmt"
	"sync"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/engine"
)

// WebInterviewer satisfies engine.Interviewer by parking questions until an
// HTTP client answers them. The engine goroutine blocks on Ask() until an
// answer is posted via Answer() or the timeout expires.
//
// Multiple questions can be pending concurrently — this happens when parallel
// branches in the engine each hit a human gate simultaneously.
type WebInterviewer struct {
	mu       sync.Mutex
	pending  map[string]*pendingQuestion // keyed by question ID
	timeout  time.Duration
	qidSeq   uint64
	cancelCh chan struct{}
}

type pendingQuestion struct {
	ID       string
	Question engine.Question
	AskedAt  time.Time
	answerCh chan engine.Answer
}

// NewWebInterviewer creates a new WebInterviewer with the given timeout.
// If timeout <= 0, defaults to 30 minutes.
func NewWebInterviewer(timeout time.Duration) *WebInterviewer {
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	return &WebInterviewer{
		timeout:  timeout,
		cancelCh: make(chan struct{}),
		pending:  make(map[string]*pendingQuestion),
	}
}

// Ask implements engine.Interviewer. It blocks until an answer is posted or timeout.
// Safe for concurrent use — each call gets its own question ID.
func (wi *WebInterviewer) Ask(q engine.Question) engine.Answer {
	wi.mu.Lock()
	wi.qidSeq++
	qid := fmt.Sprintf("q-%d", wi.qidSeq)
	ch := make(chan engine.Answer, 1)
	pq := &pendingQuestion{
		ID:       qid,
		Question: q,
		AskedAt:  time.Now().UTC(),
		answerCh: ch,
	}
	wi.pending[qid] = pq
	wi.mu.Unlock()

	defer func() {
		wi.mu.Lock()
		delete(wi.pending, qid)
		wi.mu.Unlock()
	}()

	timer := time.NewTimer(wi.timeout)
	defer timer.Stop()

	select {
	case ans := <-ch:
		return ans
	case <-timer.C:
		return engine.Answer{TimedOut: true}
	case <-wi.cancelCh:
		return engine.Answer{TimedOut: true}
	}
}

// Pending returns all currently pending questions (may be more than one when
// parallel branches hit human gates concurrently). Returns empty slice if none.
func (wi *WebInterviewer) Pending() []PendingQuestion {
	wi.mu.Lock()
	defer wi.mu.Unlock()
	out := make([]PendingQuestion, 0, len(wi.pending))
	for _, pq := range wi.pending {
		opts := make([]QuestionOption, len(pq.Question.Options))
		for i, o := range pq.Question.Options {
			opts[i] = QuestionOption{Key: o.Key, Label: o.Label, To: o.To}
		}
		out = append(out, PendingQuestion{
			QuestionID: pq.ID,
			Type:       string(pq.Question.Type),
			Text:       pq.Question.Text,
			Stage:      pq.Question.Stage,
			Options:    opts,
			AskedAt:    pq.AskedAt,
		})
	}
	return out
}

// AskMultiple implements engine.Interviewer. Asks each question sequentially.
func (wi *WebInterviewer) AskMultiple(questions []engine.Question) []engine.Answer {
	answers := make([]engine.Answer, len(questions))
	for idx, q := range questions {
		answers[idx] = wi.Ask(q)
	}
	return answers
}

// Inform implements engine.Interviewer. No-op for web interviewer — informational
// messages are delivered via the SSE progress stream instead.
func (wi *WebInterviewer) Inform(message string, stage string) {}

// Cancel unblocks all in-flight Ask() calls, causing them to return TimedOut answers.
// Safe to call multiple times.
func (wi *WebInterviewer) Cancel() {
	wi.mu.Lock()
	defer wi.mu.Unlock()
	select {
	case <-wi.cancelCh:
		// already closed
	default:
		close(wi.cancelCh)
	}
}

// Answer delivers an answer to a pending question by ID. Returns false if qid
// doesn't match any pending question or is already answered.
func (wi *WebInterviewer) Answer(qid string, ans engine.Answer) bool {
	wi.mu.Lock()
	defer wi.mu.Unlock()
	pq, ok := wi.pending[qid]
	if !ok {
		return false
	}
	select {
	case pq.answerCh <- ans:
		delete(wi.pending, qid) // prevent duplicate answers
		return true
	default:
		return false // already answered
	}
}
