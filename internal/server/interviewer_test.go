package server

import (
	"sync"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/engine"
)

// waitForPending polls until at least n questions are pending, returning them.
func waitForPending(t *testing.T, wi *WebInterviewer, n int) []PendingQuestion {
	t.Helper()
	for i := 0; i < 100; i++ {
		pqs := wi.Pending()
		if len(pqs) >= n {
			return pqs
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected at least %d pending question(s), got %d", n, len(wi.Pending()))
	return nil
}

func TestWebInterviewer_AskAndAnswer(t *testing.T) {
	wi := NewWebInterviewer(5 * time.Second)

	done := make(chan engine.Answer, 1)
	go func() {
		ans := wi.Ask(engine.Question{
			Type: engine.QuestionSingleSelect,
			Text: "Approve?",
			Options: []engine.Option{
				{Key: "y", Label: "Yes"},
				{Key: "n", Label: "No"},
			},
			Stage: "review",
		})
		done <- ans
	}()

	pqs := waitForPending(t, wi, 1)
	pq := pqs[0]
	if pq.Text != "Approve?" {
		t.Fatalf("unexpected question text: %s", pq.Text)
	}
	if len(pq.Options) != 2 {
		t.Fatalf("expected 2 options, got %d", len(pq.Options))
	}
	if pq.Stage != "review" {
		t.Fatalf("unexpected stage: %s", pq.Stage)
	}

	ok := wi.Answer(pq.QuestionID, engine.Answer{Value: "y"})
	if !ok {
		t.Fatal("answer should have succeeded")
	}

	select {
	case ans := <-done:
		if ans.Value != "y" {
			t.Fatalf("unexpected answer value: %s", ans.Value)
		}
		if ans.TimedOut {
			t.Fatal("answer should not have timed out")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Ask to return")
	}

	if len(wi.Pending()) != 0 {
		t.Fatal("expected no pending questions after answer")
	}
}

func TestWebInterviewer_Timeout(t *testing.T) {
	wi := NewWebInterviewer(50 * time.Millisecond)

	start := time.Now()
	ans := wi.Ask(engine.Question{
		Type: engine.QuestionSingleSelect,
		Text: "Will timeout",
	})
	elapsed := time.Since(start)

	if !ans.TimedOut {
		t.Fatal("expected timeout")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("timeout took too long: %v", elapsed)
	}
}

func TestWebInterviewer_AnswerWrongQID(t *testing.T) {
	wi := NewWebInterviewer(5 * time.Second)

	go func() {
		wi.Ask(engine.Question{Text: "test"})
	}()

	waitForPending(t, wi, 1)

	ok := wi.Answer("wrong-id", engine.Answer{Value: "x"})
	if ok {
		t.Fatal("answer with wrong QID should return false")
	}
}

func TestWebInterviewer_NoPending(t *testing.T) {
	wi := NewWebInterviewer(5 * time.Second)
	if len(wi.Pending()) != 0 {
		t.Fatal("expected no pending questions initially")
	}

	ok := wi.Answer("q-1", engine.Answer{Value: "x"})
	if ok {
		t.Fatal("answer with no pending question should return false")
	}
}

func TestWebInterviewer_Cancel(t *testing.T) {
	wi := NewWebInterviewer(30 * time.Minute)

	done := make(chan engine.Answer, 1)
	go func() {
		done <- wi.Ask(engine.Question{Text: "will be canceled"})
	}()

	waitForPending(t, wi, 1)

	start := time.Now()
	wi.Cancel()

	select {
	case ans := <-done:
		if !ans.TimedOut {
			t.Fatal("expected TimedOut=true on cancel")
		}
		if time.Since(start) > time.Second {
			t.Fatal("Cancel() should unblock Ask() immediately")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Ask() did not unblock after Cancel()")
	}
}

func TestWebInterviewer_CancelIdempotent(t *testing.T) {
	wi := NewWebInterviewer(5 * time.Second)
	wi.Cancel()
	wi.Cancel()
}

func TestWebInterviewer_DuplicateAnswerReturnsFalse(t *testing.T) {
	wi := NewWebInterviewer(5 * time.Second)

	go func() {
		wi.Ask(engine.Question{Text: "dup test"})
	}()

	pqs := waitForPending(t, wi, 1)
	pq := pqs[0]

	ok1 := wi.Answer(pq.QuestionID, engine.Answer{Value: "a"})
	if !ok1 {
		t.Fatal("first answer should succeed")
	}

	ok2 := wi.Answer(pq.QuestionID, engine.Answer{Value: "b"})
	if ok2 {
		t.Fatal("duplicate answer should return false")
	}
}

func TestWebInterviewer_ConcurrentAsk(t *testing.T) {
	wi := NewWebInterviewer(5 * time.Second)

	// Launch 3 concurrent Ask() calls (simulates parallel branches).
	const n = 3
	answers := make([]engine.Answer, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			answers[i] = wi.Ask(engine.Question{
				Text:  "Approve branch?",
				Stage: "gate",
			})
		}()
	}

	// Wait for all 3 to be pending.
	pqs := waitForPending(t, wi, n)
	if len(pqs) != n {
		t.Fatalf("expected %d pending questions, got %d", n, len(pqs))
	}

	// Verify all question IDs are unique.
	ids := make(map[string]bool)
	for _, pq := range pqs {
		if ids[pq.QuestionID] {
			t.Fatalf("duplicate question ID: %s", pq.QuestionID)
		}
		ids[pq.QuestionID] = true
	}

	// Answer each with a distinct value.
	for i, pq := range pqs {
		ok := wi.Answer(pq.QuestionID, engine.Answer{Value: pq.QuestionID})
		if !ok {
			t.Fatalf("answer %d should have succeeded", i)
		}
	}

	// Wait for all Ask() goroutines to return.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent Ask() calls did not all return")
	}

	// Verify each received an answer (not timed out).
	for i, ans := range answers {
		if ans.TimedOut {
			t.Fatalf("answer %d timed out unexpectedly", i)
		}
		if ans.Value == "" {
			t.Fatalf("answer %d has empty value", i)
		}
	}

	if len(wi.Pending()) != 0 {
		t.Fatalf("expected 0 pending after all answered, got %d", len(wi.Pending()))
	}
}

func TestWebInterviewer_CancelUnblocksAllConcurrent(t *testing.T) {
	wi := NewWebInterviewer(30 * time.Minute)

	const n = 3
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			ans := wi.Ask(engine.Question{Text: "blocked"})
			if !ans.TimedOut {
				t.Errorf("expected TimedOut=true on cancel")
			}
		}()
	}

	waitForPending(t, wi, n)

	wi.Cancel()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Cancel() did not unblock all concurrent Ask() calls")
	}
}
