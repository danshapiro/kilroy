package engine

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// ConsoleInterviewer prompts on stdin/stdout. Designed for single-threaded
// interactive use (one human at the terminal). If concurrent callers (e.g.
// parallel pipeline branches) share the same ConsoleInterviewer, additional
// callers receive timeout responses rather than deadlocking.
// In non-interactive environments, prefer AutoApproveInterviewer or QueueInterviewer.
type ConsoleInterviewer struct {
	In  *os.File
	Out *os.File

	// reader and readerMu protect the shared bufio.Reader across calls.
	// On timeout, the goroutine is stranded but sends its eventual result
	// to pendingResult so the next call can recover it.
	readerMu sync.Mutex
	reader   *bufio.Reader

	// pendingResult captures the result from a stranded read goroutine.
	// Buffered (capacity 1). When a timeout fires, the stranded goroutine
	// sends its eventual result here instead of being silently lost.
	// The next readLineWithTimeout call drains this first (non-blocking),
	// returning cached input immediately without spawning a new goroutine.
	// This bounds stranded goroutines to at most 1 at any time.
	pendingResult chan string
	pendingOnce   sync.Once
}

func (i *ConsoleInterviewer) Ask(q Question) Answer {
	in := i.In
	out := i.Out
	if in == nil {
		in = os.Stdin
	}
	if out == nil {
		out = os.Stdout
	}

	_, _ = fmt.Fprintf(out, "\n[%s] %s\n", q.Stage, strings.TrimSpace(q.Text))

	// Spec §6.4: ConsoleInterviewer supports timeout via non-blocking read.
	timeout := time.Duration(q.TimeoutSeconds * float64(time.Second))

	switch q.Type {
	case QuestionFreeText:
		_, _ = fmt.Fprint(out, "> ")
		s, ok := i.readLineWithTimeout(in, timeout)
		if !ok {
			return Answer{TimedOut: true}
		}
		return Answer{Text: strings.TrimSpace(s)}
	case QuestionYesNo, QuestionConfirm:
		_, _ = fmt.Fprint(out, "(y/n)> ")
		s, ok := i.readLineWithTimeout(in, timeout)
		if !ok {
			return Answer{TimedOut: true}
		}
		s = strings.TrimSpace(strings.ToLower(s))
		if s == "y" || s == "yes" {
			return Answer{Value: "YES"}
		}
		return Answer{Value: "NO"}
	case QuestionMultiSelect:
		for _, o := range q.Options {
			_, _ = fmt.Fprintf(out, "  [%s] %s\n", o.Key, o.Label)
		}
		_, _ = fmt.Fprint(out, "comma-separated> ")
		s, ok := i.readLineWithTimeout(in, timeout)
		if !ok {
			return Answer{TimedOut: true}
		}
		raw := strings.TrimSpace(s)
		if raw == "" {
			return Answer{}
		}
		parts := strings.Split(raw, ",")
		var vals []string
		for _, p := range parts {
			if v := strings.TrimSpace(p); v != "" {
				vals = append(vals, v)
			}
		}
		return Answer{Values: vals}
	default:
		// SINGLE_SELECT (default)
		for _, o := range q.Options {
			_, _ = fmt.Fprintf(out, "  [%s] %s\n", o.Key, o.Label)
		}
		_, _ = fmt.Fprint(out, "> ")
		s, ok := i.readLineWithTimeout(in, timeout)
		if !ok {
			return Answer{TimedOut: true}
		}
		return Answer{Value: strings.TrimSpace(s)}
	}
}

func (i *ConsoleInterviewer) AskMultiple(questions []Question) []Answer {
	answers := make([]Answer, len(questions))
	for idx, q := range questions {
		answers[idx] = i.Ask(q)
	}
	return answers
}

func (i *ConsoleInterviewer) Inform(message string, stage string) {
	out := i.Out
	if out == nil {
		out = os.Stdout
	}
	_, _ = fmt.Fprintf(out, "\n[%s] %s\n", stage, message)
}

// initPendingResult lazily initializes the pendingResult channel.
// Uses sync.Once to prevent data race when concurrent Ask() calls
// hit the nil-check simultaneously.
func (i *ConsoleInterviewer) initPendingResult() {
	i.pendingOnce.Do(func() {
		i.pendingResult = make(chan string, 1)
	})
}

// readLineWithTimeout reads a line using i's shared reader with an optional timeout.
// If timeout is <= 0, it blocks indefinitely (no timeout).
// Returns the line and true on success, or ("", false) on timeout.
//
// Bounded single-goroutine reader pattern:
//
// When a timeout fires, the stranded goroutine sends its eventual result to
// pendingResult (buffered channel, capacity 1) instead of being silently lost.
// On the next call, we drain pendingResult first (non-blocking select). If a
// value is available, it's input from the previously-stranded goroutine and we
// return it immediately without starting a new read.
//
// Key properties:
//   - At most 1 stranded goroutine at any time
//   - User input is never lost (stranded goroutine's result is captured)
//   - Next call gets the pending input immediately without spawning a new read
//
// This approach prevents the hang that occurred when wait.human returned RETRY
// on timeout and the engine retried the node: the old design would either block
// on readerMu.Lock() indefinitely, or spawn unbounded fresh readers that race
// for the same FD.
func (i *ConsoleInterviewer) readLineWithTimeout(f *os.File, timeout time.Duration) (string, bool) {
	i.initPendingResult()

	// Drain any result from a previously-stranded goroutine. If present, use
	// it immediately — this is real user input that was read after the previous
	// timeout fired.
	select {
	case line := <-i.pendingResult:
		return line, true
	default:
	}

	// Acquire the mutex. If TryLock fails, a stranded goroutine still holds it
	// (blocked on ReadString). That goroutine will eventually send its result
	// to pendingResult and unlock the mutex. Rather than spawning new
	// goroutines to wait for the mutex (which would accumulate), we wait for
	// pendingResult directly — that's where the stranded goroutine's result
	// will appear.
	acquired := i.readerMu.TryLock()
	if !acquired {
		// Another call holds the mutex — either a stranded goroutine blocked on
		// ReadString, or a concurrent caller doing a blocking read. The stranded
		// goroutine will publish to pendingResult when it completes, but a
		// concurrent blocking reader (timeout=0) will NOT. Use a bounded wait
		// to prevent deadlock: if nothing arrives within the caller's timeout
		// (or 5s max for callers with no timeout), return a timeout response.
		// The caller (e.g. WaitHumanHandler) treats this as a timeout and
		// applies its normal timeout handling (default_choice or RETRY).
		boundedTimeout := timeout
		if boundedTimeout <= 0 {
			boundedTimeout = 5 * time.Second
		}
		select {
		case line := <-i.pendingResult:
			return line, true
		case <-time.After(boundedTimeout):
			return "", false
		}
	}

	if i.reader == nil {
		i.reader = bufio.NewReader(f)
	}
	r := i.reader

	if timeout <= 0 {
		// No timeout — blocking read under mutex.
		s, _ := r.ReadString('\n')
		i.readerMu.Unlock()
		return s, true
	}

	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		s, err := r.ReadString('\n')
		ch <- result{line: s, err: err}
	}()
	select {
	case res := <-ch:
		i.readerMu.Unlock()
		return res.line, true
	case <-time.After(timeout):
		// Goroutine is stranded on ReadString. It will eventually complete
		// when input arrives. Route its result to pendingResult so the next
		// call can recover it, then release the mutex.
		go func() {
			res := <-ch
			// Send to pendingResult (buffered, cap 1). If the channel is
			// already full (shouldn't happen since we drain at call start),
			// discard to avoid blocking.
			select {
			case i.pendingResult <- res.line:
			default:
			}
			i.readerMu.Unlock()
		}()
		return "", false
	}
}

type CallbackInterviewer struct {
	Fn func(Question) Answer
}

func (i *CallbackInterviewer) Ask(q Question) Answer {
	if i == nil || i.Fn == nil {
		return Answer{}
	}
	return i.Fn(q)
}

func (i *CallbackInterviewer) AskMultiple(questions []Question) []Answer {
	answers := make([]Answer, len(questions))
	for idx, q := range questions {
		answers[idx] = i.Ask(q)
	}
	return answers
}

func (i *CallbackInterviewer) Inform(message string, stage string) {
	// No-op for callback interviewer.
}

// QueueInterviewer returns pre-seeded answers in order. Useful for tests.
// Spec §6.4: returns Answer{Skipped: true} when the queue is empty.
type QueueInterviewer struct {
	mu      sync.Mutex
	Answers []Answer
}

func (i *QueueInterviewer) Ask(q Question) Answer {
	_ = q
	i.mu.Lock()
	defer i.mu.Unlock()
	if len(i.Answers) == 0 {
		return Answer{Skipped: true}
	}
	a := i.Answers[0]
	i.Answers = i.Answers[1:]
	return a
}

func (i *QueueInterviewer) AskMultiple(questions []Question) []Answer {
	answers := make([]Answer, len(questions))
	for idx, q := range questions {
		answers[idx] = i.Ask(q)
	}
	return answers
}

func (i *QueueInterviewer) Inform(message string, stage string) {
	// No-op for queue interviewer.
}

// RecordingInterviewer wraps another Interviewer and records all Q&A pairs.
// Spec §6.4: Used for replay, debugging, and audit trails.
type RecordingInterviewer struct {
	Inner      Interviewer
	mu         sync.Mutex
	Recordings []QAPair
}

// QAPair stores a question and its answer for recording purposes.
type QAPair struct {
	Question Question
	Answer   Answer
}

func (i *RecordingInterviewer) Ask(q Question) Answer {
	if i.Inner == nil {
		return Answer{}
	}
	ans := i.Inner.Ask(q)
	i.mu.Lock()
	i.Recordings = append(i.Recordings, QAPair{Question: q, Answer: ans})
	i.mu.Unlock()
	return ans
}

func (i *RecordingInterviewer) AskMultiple(questions []Question) []Answer {
	if i.Inner == nil {
		return make([]Answer, len(questions))
	}
	answers := i.Inner.AskMultiple(questions)
	i.mu.Lock()
	for idx, q := range questions {
		var ans Answer
		if idx < len(answers) {
			ans = answers[idx]
		}
		i.Recordings = append(i.Recordings, QAPair{Question: q, Answer: ans})
	}
	i.mu.Unlock()
	return answers
}

func (i *RecordingInterviewer) Inform(message string, stage string) {
	if i.Inner == nil {
		return
	}
	i.Inner.Inform(message, stage)
}

func acceleratorKey(label string) string {
	s := strings.TrimSpace(label)
	if s == "" {
		return ""
	}
	// Patterns:
	// [K] Label
	// K) Label
	// K - Label
	// Fallback: first character.
	if len(s) >= 4 && s[0] == '[' && s[2] == ']' && s[3] == ' ' {
		return strings.ToUpper(string(s[1]))
	}
	if len(s) >= 3 && s[1] == ')' && s[2] == ' ' {
		return strings.ToUpper(string(s[0]))
	}
	if len(s) >= 4 && s[1] == ' ' && s[2] == '-' && s[3] == ' ' {
		return strings.ToUpper(string(s[0]))
	}
	return strings.ToUpper(string(s[0]))
}
