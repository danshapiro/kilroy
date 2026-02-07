package llm

import (
	"context"
	"errors"
)

func wrapContextError(provider string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return NewAbortError(err.Error())
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return NewRequestTimeoutError(provider, err.Error())
	}
	return err
}

// WrapContextError converts context cancellation/deadline errors into the SDK
// error hierarchy (AbortError/RequestTimeoutError). Other errors are returned
// unchanged.
func WrapContextError(provider string, err error) error {
	return wrapContextError(provider, err)
}
