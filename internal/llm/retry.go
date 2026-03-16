package llm

import "time"

type RetryPolicy struct {
	// MaxRetries is the number of retry attempts (not counting the initial attempt).
	MaxRetries int

	// BaseDelay is the delay before the first retry attempt.
	BaseDelay time.Duration

	// MaxDelay caps the delay between retries.
	MaxDelay time.Duration

	// BackoffMultiplier controls exponential backoff growth (2.0 = double each retry).
	BackoffMultiplier float64

	// Jitter adds randomness to delays to reduce thundering-herd retries.
	Jitter bool

	// OnRetry is invoked before sleeping for a retry attempt.
	OnRetry func(err error, attempt int, delay time.Duration)
}

func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxRetries:        2,
		BaseDelay:         1 * time.Second,
		MaxDelay:          60 * time.Second,
		BackoffMultiplier: 2.0,
		Jitter:            true,
	}
}
