package llm

import (
	"context"
	"sync"
)

type chanStream struct {
	events   chan StreamEvent
	cancel   context.CancelFunc
	once     sync.Once
	sendOnce sync.Once
	closing  chan struct{}
	done     chan struct{}
}

type ChanStream struct{ chanStream }

func NewChanStream(cancel context.CancelFunc) *ChanStream {
	return &ChanStream{chanStream{
		events:  make(chan StreamEvent, 128),
		cancel:  cancel,
		closing: make(chan struct{}),
		done:    make(chan struct{}),
	}}
}

func (s *ChanStream) Events() <-chan StreamEvent { return s.events }

func (s *ChanStream) Close() error {
	s.once.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		// Signal senders to stop blocking and drop events.
		close(s.closing)
	})
	<-s.done
	return nil
}

// CloseSend closes the event channel and marks the stream as finished. Provider adapters
// should call this exactly once when the underlying stream finishes.
func (s *ChanStream) CloseSend() {
	s.sendOnce.Do(func() {
		close(s.done)
		close(s.events)
	})
}

// Send publishes a stream event, dropping it if the stream is already closed.
func (s *ChanStream) Send(ev StreamEvent) {
	// Send can race with CloseSend; sending on a closed channel panics.
	// v1 semantics are best-effort delivery, so treat this as a drop.
	defer func() { _ = recover() }()
	select {
	case <-s.done:
		return
	case <-s.closing:
		return
	default:
	}
	select {
	case s.events <- ev:
	case <-s.closing:
	case <-s.done:
	}
}
