package engine

import (
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestAutoApproveInterviewer_SelectsFirstOption(t *testing.T) {
	i := &AutoApproveInterviewer{}
	ans := i.Ask(Question{
		Type:  QuestionSingleSelect,
		Text:  "choose",
		Stage: "s",
		Options: []Option{
			{Key: "A", Label: "Approve", To: "a"},
			{Key: "F", Label: "Fix", To: "f"},
		},
	})
	if ans.Value != "A" {
		t.Fatalf("value: got %q want %q", ans.Value, "A")
	}
}

func TestAutoApproveInterviewer_NoOptions_DefaultsToYES(t *testing.T) {
	i := &AutoApproveInterviewer{}
	ans := i.Ask(Question{Type: QuestionConfirm, Text: "ok?", Stage: "s"})
	if ans.Value != "YES" {
		t.Fatalf("value: got %q want %q", ans.Value, "YES")
	}
}

func TestCallbackInterviewer_DelegatesToFn(t *testing.T) {
	i := &CallbackInterviewer{Fn: func(q Question) Answer {
		_ = q
		return Answer{Value: "X"}
	}}
	ans := i.Ask(Question{Type: QuestionSingleSelect, Text: "t", Stage: "s"})
	if ans.Value != "X" {
		t.Fatalf("value: got %q want %q", ans.Value, "X")
	}
}

func TestQueueInterviewer_PopsAnswersInOrder(t *testing.T) {
	i := &QueueInterviewer{Answers: []Answer{{Value: "A"}, {Value: "B"}}}
	if got := i.Ask(Question{}).Value; got != "A" {
		t.Fatalf("first: %q", got)
	}
	if got := i.Ask(Question{}).Value; got != "B" {
		t.Fatalf("second: %q", got)
	}
}

func TestConsoleInterviewer_SingleSelect_ReadsInputAndWritesPrompt(t *testing.T) {
	rIn, wIn, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rIn.Close() }()
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rOut.Close() }()

	go func() {
		_, _ = wIn.Write([]byte("F\n"))
		_ = wIn.Close()
	}()

	i := &ConsoleInterviewer{In: rIn, Out: wOut}
	ans := i.Ask(Question{
		Type:  QuestionSingleSelect,
		Text:  "choose",
		Stage: "gate",
		Options: []Option{
			{Key: "A", Label: "Approve"},
			{Key: "F", Label: "Fix"},
		},
	})
	_ = wOut.Close()

	outBytes, _ := io.ReadAll(rOut)
	outText := string(outBytes)
	if !strings.Contains(outText, "[gate] choose") {
		t.Fatalf("expected prompt to include stage/text; got:\n%s", outText)
	}
	if ans.Value != "F" {
		t.Fatalf("answer: got %q want %q", ans.Value, "F")
	}
}

func TestConsoleInterviewer_FreeText_ReadsText(t *testing.T) {
	rIn, wIn, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rIn.Close() }()
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rOut.Close() }()

	go func() {
		_, _ = wIn.Write([]byte("hello world\n"))
		_ = wIn.Close()
	}()

	i := &ConsoleInterviewer{In: rIn, Out: wOut}
	ans := i.Ask(Question{
		Type:  QuestionFreeText,
		Text:  "type something",
		Stage: "s",
	})
	_ = wOut.Close()

	if got := ans.Text; got != "hello world" {
		t.Fatalf("answer text: got %q want %q", got, "hello world")
	}
}

func TestConsoleInterviewer_Confirm_ParsesYesNo(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  string
	}{
		{input: "y\n", want: "YES"},
		{input: "yes\n", want: "YES"},
		{input: "n\n", want: "NO"},
		{input: "\n", want: "NO"},
	} {
		rIn, wIn, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		rOut, wOut, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}

		go func() {
			_, _ = wIn.Write([]byte(tc.input))
			_ = wIn.Close()
		}()

		i := &ConsoleInterviewer{In: rIn, Out: wOut}
		ans := i.Ask(Question{
			Type:  QuestionConfirm,
			Text:  "ok?",
			Stage: "s",
		})
		_ = wOut.Close()
		_, _ = io.ReadAll(rOut)
		_ = rIn.Close()
		_ = rOut.Close()

		if got := ans.Value; got != tc.want {
			t.Fatalf("confirm(%q): got %q want %q", strings.TrimSpace(tc.input), got, tc.want)
		}
	}
}

func TestConsoleInterviewer_MultiSelect_ParsesCommaSeparatedValues(t *testing.T) {
	rIn, wIn, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rIn.Close() }()
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rOut.Close() }()

	go func() {
		_, _ = wIn.Write([]byte("A, C ,D\n"))
		_ = wIn.Close()
	}()

	i := &ConsoleInterviewer{In: rIn, Out: wOut}
	ans := i.Ask(Question{
		Type:  QuestionMultiSelect,
		Text:  "choose many",
		Stage: "gate",
		Options: []Option{
			{Key: "A", Label: "Apple"},
			{Key: "C", Label: "Carrot"},
			{Key: "D", Label: "Donut"},
		},
	})
	_ = wOut.Close()

	if got := strings.Join(ans.Values, ","); got != "A,C,D" {
		t.Fatalf("values: got %q want %q", got, "A,C,D")
	}
}

// --- V6.1: AskMultiple and Inform ---

func TestAutoApproveInterviewer_AskMultiple(t *testing.T) {
	i := &AutoApproveInterviewer{}
	questions := []Question{
		{Type: QuestionConfirm, Text: "ok?", Stage: "s1"},
		{Type: QuestionSingleSelect, Text: "choose", Stage: "s2", Options: []Option{
			{Key: "A", Label: "Alpha"},
			{Key: "B", Label: "Bravo"},
		}},
	}
	answers := i.AskMultiple(questions)
	if len(answers) != 2 {
		t.Fatalf("expected 2 answers, got %d", len(answers))
	}
	if answers[0].Value != "YES" {
		t.Fatalf("first answer: got %q want %q", answers[0].Value, "YES")
	}
	if answers[1].Value != "A" {
		t.Fatalf("second answer: got %q want %q", answers[1].Value, "A")
	}
}

func TestCallbackInterviewer_AskMultiple(t *testing.T) {
	calls := 0
	i := &CallbackInterviewer{Fn: func(q Question) Answer {
		calls++
		return Answer{Value: q.Stage}
	}}
	answers := i.AskMultiple([]Question{
		{Stage: "a"},
		{Stage: "b"},
	})
	if calls != 2 {
		t.Fatalf("expected 2 callback calls, got %d", calls)
	}
	if answers[0].Value != "a" || answers[1].Value != "b" {
		t.Fatalf("answers: %v", answers)
	}
}

func TestConsoleInterviewer_Inform_WritesToOutput(t *testing.T) {
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rOut.Close() }()

	i := &ConsoleInterviewer{Out: wOut}
	i.Inform("Pipeline started", "init")
	_ = wOut.Close()

	outBytes, _ := io.ReadAll(rOut)
	outText := string(outBytes)
	if !strings.Contains(outText, "[init] Pipeline started") {
		t.Fatalf("expected Inform output; got: %q", outText)
	}
}

// --- V6.3: YES_NO QuestionType ---

func TestConsoleInterviewer_YesNo_ParsesLikeConfirm(t *testing.T) {
	rIn, wIn, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rIn.Close() }()
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rOut.Close() }()

	go func() {
		_, _ = wIn.Write([]byte("y\n"))
		_ = wIn.Close()
	}()

	i := &ConsoleInterviewer{In: rIn, Out: wOut}
	ans := i.Ask(Question{
		Type:  QuestionYesNo,
		Text:  "proceed?",
		Stage: "s",
	})
	_ = wOut.Close()

	if ans.Value != "YES" {
		t.Fatalf("YES_NO with y input: got %q want %q", ans.Value, "YES")
	}
}

// --- V6.5: QueueInterviewer returns SKIPPED when empty ---

func TestQueueInterviewer_EmptyQueue_ReturnsSkipped(t *testing.T) {
	i := &QueueInterviewer{}
	ans := i.Ask(Question{Text: "anything", Stage: "s"})
	if !ans.Skipped {
		t.Fatal("expected Skipped=true when queue is empty")
	}
}

func TestQueueInterviewer_AskMultiple_ReturnsSKIPPEDWhenExhausted(t *testing.T) {
	i := &QueueInterviewer{Answers: []Answer{{Value: "A"}}}
	answers := i.AskMultiple([]Question{
		{Stage: "s1"},
		{Stage: "s2"},
	})
	if len(answers) != 2 {
		t.Fatalf("expected 2 answers, got %d", len(answers))
	}
	if answers[0].Value != "A" {
		t.Fatalf("first: got %q want %q", answers[0].Value, "A")
	}
	if !answers[1].Skipped {
		t.Fatal("second: expected Skipped=true when queue exhausted")
	}
}

// --- V6.6: RecordingInterviewer ---

func TestRecordingInterviewer_RecordsQAPairs(t *testing.T) {
	inner := &AutoApproveInterviewer{}
	rec := &RecordingInterviewer{Inner: inner}

	q := Question{Type: QuestionConfirm, Text: "deploy?", Stage: "deploy"}
	ans := rec.Ask(q)

	if ans.Value != "YES" {
		t.Fatalf("expected YES, got %q", ans.Value)
	}
	if len(rec.Recordings) != 1 {
		t.Fatalf("expected 1 recording, got %d", len(rec.Recordings))
	}
	if rec.Recordings[0].Question.Text != "deploy?" {
		t.Fatalf("recorded question text: %q", rec.Recordings[0].Question.Text)
	}
	if rec.Recordings[0].Answer.Value != "YES" {
		t.Fatalf("recorded answer value: %q", rec.Recordings[0].Answer.Value)
	}
}

func TestRecordingInterviewer_AskMultiple_RecordsAll(t *testing.T) {
	inner := &AutoApproveInterviewer{}
	rec := &RecordingInterviewer{Inner: inner}

	questions := []Question{
		{Type: QuestionConfirm, Text: "q1", Stage: "s1"},
		{Type: QuestionConfirm, Text: "q2", Stage: "s2"},
	}
	answers := rec.AskMultiple(questions)

	if len(answers) != 2 {
		t.Fatalf("expected 2 answers, got %d", len(answers))
	}
	if len(rec.Recordings) != 2 {
		t.Fatalf("expected 2 recordings, got %d", len(rec.Recordings))
	}
	if rec.Recordings[0].Question.Text != "q1" || rec.Recordings[1].Question.Text != "q2" {
		t.Fatal("recorded questions don't match input")
	}
}

func TestRecordingInterviewer_DelegatesToInner(t *testing.T) {
	called := false
	inner := &CallbackInterviewer{Fn: func(q Question) Answer {
		called = true
		return Answer{Value: "CALLBACK"}
	}}
	rec := &RecordingInterviewer{Inner: inner}

	ans := rec.Ask(Question{Stage: "s"})
	if !called {
		t.Fatal("expected inner.Ask to be called")
	}
	if ans.Value != "CALLBACK" {
		t.Fatalf("expected CALLBACK, got %q", ans.Value)
	}
}

// --- V6.7: ConsoleInterviewer timeout ---

func TestConsoleInterviewer_Timeout_ReturnsTimedOut(t *testing.T) {
	// Create a pipe but never write to it — the read will block.
	rIn, wIn, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rIn.Close(); _ = wIn.Close() }()
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rOut.Close() }()

	i := &ConsoleInterviewer{In: rIn, Out: wOut}
	start := time.Now()
	ans := i.Ask(Question{
		Type:           QuestionSingleSelect,
		Text:           "choose quickly",
		Stage:          "s",
		TimeoutSeconds: 0.1, // 100ms timeout
		Options: []Option{
			{Key: "A", Label: "Alpha"},
		},
	})
	_ = wOut.Close()
	elapsed := time.Since(start)

	if !ans.TimedOut {
		t.Fatal("expected TimedOut=true when input blocks beyond timeout")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("timeout took too long: %v (expected ~100ms)", elapsed)
	}
}

func TestConsoleInterviewer_ZeroTimeout_BlocksNormally(t *testing.T) {
	// Zero timeout means no timeout — normal blocking behavior.
	rIn, wIn, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rIn.Close() }()
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rOut.Close() }()

	go func() {
		_, _ = wIn.Write([]byte("hello\n"))
		_ = wIn.Close()
	}()

	i := &ConsoleInterviewer{In: rIn, Out: wOut}
	ans := i.Ask(Question{
		Type:           QuestionFreeText,
		Text:           "type",
		Stage:          "s",
		TimeoutSeconds: 0, // no timeout
	})
	_ = wOut.Close()

	if ans.TimedOut {
		t.Fatal("expected no timeout with TimeoutSeconds=0")
	}
	if ans.Text != "hello" {
		t.Fatalf("text: got %q want %q", ans.Text, "hello")
	}
}

// --- V6.2: Question Default field ---

func TestQuestion_DefaultField(t *testing.T) {
	defaultAns := Answer{Value: "YES"}
	q := Question{
		Type:           QuestionConfirm,
		Text:           "proceed?",
		Default:        &defaultAns,
		TimeoutSeconds: 5.0,
		Stage:          "deploy",
		Metadata:       map[string]any{"priority": "high"},
	}
	if q.Default == nil || q.Default.Value != "YES" {
		t.Fatal("expected Default to be populated")
	}
	if q.TimeoutSeconds != 5.0 {
		t.Fatalf("TimeoutSeconds: got %v want 5.0", q.TimeoutSeconds)
	}
	if q.Metadata["priority"] != "high" {
		t.Fatalf("Metadata: %v", q.Metadata)
	}
}

// --- V6.4: Answer SelectedOption field ---

func TestAnswer_SelectedOptionField(t *testing.T) {
	opt := Option{Key: "A", Label: "Alpha", To: "alpha"}
	ans := Answer{
		Value:          "A",
		SelectedOption: &opt,
	}
	if ans.SelectedOption == nil {
		t.Fatal("expected SelectedOption to be populated")
	}
	if ans.SelectedOption.Key != "A" || ans.SelectedOption.Label != "Alpha" {
		t.Fatalf("SelectedOption: %+v", ans.SelectedOption)
	}
}

// --- Regression: ConsoleInterviewer timeout-retry goroutine leak ---

// TestConsoleInterviewer_TimeoutRecovery_PendingResultCaptured verifies that
// when a read times out, the stranded goroutine's eventual result is captured
// in pendingResult and returned by the next Ask call — no input is lost.
func TestConsoleInterviewer_TimeoutRecovery_PendingResultCaptured(t *testing.T) {
	rIn, wIn, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rIn.Close() }()
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rOut.Close() }()

	ci := &ConsoleInterviewer{In: rIn, Out: wOut}

	// First Ask: times out (no input written yet).
	ans1 := ci.Ask(Question{
		Type:           QuestionFreeText,
		Text:           "prompt1",
		Stage:          "s",
		TimeoutSeconds: 0.05, // 50ms — will time out
	})
	if !ans1.TimedOut {
		t.Fatal("expected first Ask to time out")
	}

	// Now write input after the timeout. The stranded goroutine will read it
	// and deposit it in pendingResult.
	time.Sleep(20 * time.Millisecond) // let stranded goroutine settle on ReadString
	_, _ = wIn.Write([]byte("delayed answer\n"))

	// Give the stranded goroutine time to read and deposit to pendingResult.
	time.Sleep(100 * time.Millisecond)

	// Second Ask: should recover the pending input immediately.
	ans2 := ci.Ask(Question{
		Type:           QuestionFreeText,
		Text:           "prompt2",
		Stage:          "s",
		TimeoutSeconds: 1.0, // generous timeout — should return instantly
	})
	_ = wOut.Close()

	if ans2.TimedOut {
		t.Fatal("expected second Ask to return pending result, not time out")
	}
	if ans2.Text != "delayed answer" {
		t.Fatalf("expected recovered text %q, got %q", "delayed answer", ans2.Text)
	}
}

// TestConsoleInterviewer_RepeatedTimeouts_BoundedGoroutines verifies that
// repeated timeouts do NOT cause unbounded goroutine growth. With the
// pendingResult pattern, at most 1 stranded goroutine should exist at a time.
func TestConsoleInterviewer_RepeatedTimeouts_BoundedGoroutines(t *testing.T) {
	rIn, wIn, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rIn.Close() }()
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rOut.Close(); _ = wOut.Close() }()

	ci := &ConsoleInterviewer{In: rIn, Out: wOut}

	// Record baseline goroutine count.
	runtime.GC()
	time.Sleep(10 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	// Repeatedly time out 10 times with no input.
	for iter := 0; iter < 10; iter++ {
		ans := ci.Ask(Question{
			Type:           QuestionFreeText,
			Text:           "prompt",
			Stage:          "s",
			TimeoutSeconds: 0.05, // 50ms
		})
		if !ans.TimedOut {
			t.Fatalf("iteration %d: expected timeout", iter)
		}
	}

	// Allow goroutines to settle.
	time.Sleep(100 * time.Millisecond)

	current := runtime.NumGoroutine()
	// With the bounded pattern, we expect at most baseline + a small constant
	// (1 stranded reader goroutine + 1 mutex-waiter goroutine at most).
	// The old unbounded design would have baseline + ~10 or more.
	leaked := current - baseline
	// Allow up to 4 extra goroutines (generous margin for runtime jitter,
	// the stranded reader, and the mutex waiter).
	if leaked > 4 {
		t.Fatalf("goroutine leak: baseline=%d current=%d leaked=%d (expected <= 4)",
			baseline, current, leaked)
	}

	// Clean up: write something so the stranded goroutine can exit.
	_, _ = wIn.Write([]byte("cleanup\n"))
	time.Sleep(50 * time.Millisecond)
}

// TestConsoleInterviewer_TimeoutThenImmediateInput verifies the full cycle:
// timeout -> input arrives -> next call gets it without delay.
func TestConsoleInterviewer_TimeoutThenImmediateInput(t *testing.T) {
	rIn, wIn, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rIn.Close() }()
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rOut.Close(); _ = wOut.Close() }()

	ci := &ConsoleInterviewer{In: rIn, Out: wOut}

	// Timeout on first call.
	ans := ci.Ask(Question{
		Type:           QuestionConfirm,
		Text:           "proceed?",
		Stage:          "gate",
		TimeoutSeconds: 0.05,
	})
	if !ans.TimedOut {
		t.Fatal("expected timeout")
	}

	// Write "y\n" — the stranded goroutine picks it up.
	_, _ = wIn.Write([]byte("y\n"))
	time.Sleep(100 * time.Millisecond)

	// Second call should get the pending "y\n" and parse it as YES.
	start := time.Now()
	ans2 := ci.Ask(Question{
		Type:           QuestionConfirm,
		Text:           "proceed again?",
		Stage:          "gate",
		TimeoutSeconds: 2.0,
	})
	elapsed := time.Since(start)

	if ans2.TimedOut {
		t.Fatal("expected pending result, got timeout")
	}
	if ans2.Value != "YES" {
		t.Fatalf("expected YES from recovered input, got %q", ans2.Value)
	}
	// It should return nearly instantly (from pendingResult), not wait 2 seconds.
	if elapsed > 500*time.Millisecond {
		t.Fatalf("second Ask took %v — should have been near-instant from pendingResult", elapsed)
	}
}
