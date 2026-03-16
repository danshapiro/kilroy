package llm

import (
	"fmt"
	"strings"
	"time"
)

type nonHTTPErrorBase struct {
	provider   string
	message    string
	retryable  bool
	retryAfter *time.Duration
}

func (e *nonHTTPErrorBase) Error() string {
	msg := strings.TrimSpace(e.message)
	if msg == "" {
		msg = "request failed"
	}
	if strings.TrimSpace(e.provider) == "" {
		return msg
	}
	return fmt.Sprintf("%s error: %s", e.provider, msg)
}
func (e *nonHTTPErrorBase) Provider() string           { return e.provider }
func (e *nonHTTPErrorBase) StatusCode() int            { return 0 }
func (e *nonHTTPErrorBase) Retryable() bool            { return e.retryable }
func (e *nonHTTPErrorBase) RetryAfter() *time.Duration { return e.retryAfter }

type AbortError struct{ nonHTTPErrorBase }
type NetworkError struct{ nonHTTPErrorBase }
type StreamError struct{ nonHTTPErrorBase }
type InvalidToolCallError struct{ nonHTTPErrorBase }
type NoObjectGeneratedError struct {
	nonHTTPErrorBase
	RawText string
}
type UnsupportedToolChoiceError struct{ nonHTTPErrorBase }

func NewAbortError(message string) error {
	return &AbortError{nonHTTPErrorBase{message: message, retryable: false}}
}

func NewNetworkError(provider, message string) error {
	return &NetworkError{nonHTTPErrorBase{provider: provider, message: message, retryable: true}}
}

func NewStreamError(provider, message string) error {
	return &StreamError{nonHTTPErrorBase{provider: provider, message: message, retryable: true}}
}

func NewInvalidToolCallError(message string) error {
	return &InvalidToolCallError{nonHTTPErrorBase{message: message, retryable: false}}
}

func NewNoObjectGeneratedError(message string, rawText string) error {
	return &NoObjectGeneratedError{nonHTTPErrorBase: nonHTTPErrorBase{message: message, retryable: false}, RawText: rawText}
}

func NewUnsupportedToolChoiceError(provider, mode string) error {
	msg := strings.TrimSpace(mode)
	if msg == "" {
		msg = "(empty)"
	}
	return &UnsupportedToolChoiceError{nonHTTPErrorBase{provider: provider, message: fmt.Sprintf("unsupported tool_choice mode: %s", msg), retryable: false}}
}
