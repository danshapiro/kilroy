// Tail a JSONL file and emit parsed events as they appear.
// Tool-agnostic: works with any CLI agent that writes structured output.
package agentlog

import (
	"bufio"
	"context"
	"io"
	"os"
	"time"
)

// TailConfig controls the tailer's polling behavior.
type TailConfig struct {
	PollInterval time.Duration // how often to check for new lines (default 500ms)
}

// EventCallback is called for each parsed event during tailing.
type EventCallback func(event AgentEvent)

// TailJSONL watches a JSONL file and calls onEvent for each parsed line.
// Blocks until ctx is canceled or the file is closed. The caller starts
// this in a goroutine before the agent runs and cancels ctx when the
// agent finishes.
func TailJSONL(ctx context.Context, path string, lineParse LineParseFunc, onEvent EventCallback, cfg TailConfig) {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 500 * time.Millisecond
	}

	// Wait for the file to appear (agent may not have started writing yet).
	var f *os.File
	for {
		var err error
		f, err = os.Open(path)
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(cfg.PollInterval):
		}
	}
	defer f.Close()

	reader := bufio.NewReaderSize(f, 256*1024)
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			raw, ok := ParseJSONLine(line)
			if ok {
				for _, ev := range lineParse(raw) {
					onEvent(ev)
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				// No more data — wait and retry.
				select {
				case <-ctx.Done():
					return
				case <-time.After(cfg.PollInterval):
				}
				continue
			}
			// Read error — stop.
			return
		}
	}
}
