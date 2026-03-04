package codexappserver

import (
	"context"
	"testing"
	"time"
)

func TestNewTransport_DefaultRequestTimeoutIsDisabled(t *testing.T) {
	transport := NewTransport(TransportOptions{})
	if transport.opts.RequestTimeout != 0 {
		t.Fatalf("request timeout: got %v want 0 (disabled)", transport.opts.RequestTimeout)
	}
}

func TestContextWithRequestTimeout_DisabledDoesNotInjectDeadline(t *testing.T) {
	ctx := context.Background()

	derivedCtx, cancel := contextWithRequestTimeout(ctx, 0)
	defer cancel()

	if _, ok := derivedCtx.Deadline(); ok {
		t.Fatalf("expected no deadline when timeout is disabled")
	}
}

func TestContextWithRequestTimeout_PositiveTimeoutInjectsDeadline(t *testing.T) {
	ctx := context.Background()

	derivedCtx, cancel := contextWithRequestTimeout(ctx, 500*time.Millisecond)
	defer cancel()

	deadline, ok := derivedCtx.Deadline()
	if !ok {
		t.Fatalf("expected derived context deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > time.Second {
		t.Fatalf("derived deadline remaining=%v, expected within (0, 1s]", remaining)
	}
}

func TestContextWithRequestTimeout_ParentDeadlineTakesPrecedence(t *testing.T) {
	parentCtx, cancelParent := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancelParent()

	derivedCtx, cancelDerived := contextWithRequestTimeout(parentCtx, 5*time.Second)
	defer cancelDerived()

	deadline, ok := derivedCtx.Deadline()
	if !ok {
		t.Fatalf("expected derived context deadline from parent")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > time.Second {
		t.Fatalf("derived deadline remaining=%v, expected parent-sized deadline", remaining)
	}
}

func TestInterruptTimeout_DefaultsWhenRequestTimeoutDisabled(t *testing.T) {
	transport := NewTransport(TransportOptions{})
	if got := transport.interruptTimeout(); got != defaultInterruptTimeout {
		t.Fatalf("interrupt timeout: got %v want %v", got, defaultInterruptTimeout)
	}
}

func TestInterruptTimeout_UsesRequestTimeoutWhenSet(t *testing.T) {
	transport := NewTransport(TransportOptions{RequestTimeout: 3 * time.Second})
	if got := transport.interruptTimeout(); got != 3*time.Second {
		t.Fatalf("interrupt timeout: got %v want %v", got, 3*time.Second)
	}
}

func TestInterruptTimeout_ClampsToShutdownTimeout(t *testing.T) {
	transport := NewTransport(TransportOptions{
		RequestTimeout:  7 * time.Second,
		ShutdownTimeout: 1 * time.Second,
	})
	if got := transport.interruptTimeout(); got != 1*time.Second {
		t.Fatalf("interrupt timeout: got %v want %v", got, 1*time.Second)
	}
}
