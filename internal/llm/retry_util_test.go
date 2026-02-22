package llm

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRetry_RetriesRetryableErrors_WithExponentialBackoff(t *testing.T) {
	policy := RetryPolicy{
		MaxRetries:        3,
		BaseDelay:         10 * time.Millisecond,
		MaxDelay:          5 * time.Second,
		BackoffMultiplier: 2.0,
		Jitter:            false,
	}
	var sleeps []time.Duration
	sleep := func(ctx context.Context, d time.Duration) error {
		_ = ctx
		sleeps = append(sleeps, d)
		return nil
	}

	err429 := ErrorFromHTTPStatus("openai", 429, "rate limited", nil, nil)
	attempts := 0
	fn := func() (Response, error) {
		attempts++
		if attempts <= 2 {
			return Response{}, err429
		}
		return Response{Provider: "openai", Model: "m", Message: Assistant("ok")}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := Retry(ctx, policy, sleep, nil, fn)
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if resp.Text() != "ok" {
		t.Fatalf("resp: %q", resp.Text())
	}
	if got, want := len(sleeps), 2; got != want {
		t.Fatalf("sleep calls: got %d want %d (%v)", got, want, sleeps)
	}
	if sleeps[0] != 10*time.Millisecond {
		t.Fatalf("sleep[0]: got %s want %s", sleeps[0], 10*time.Millisecond)
	}
	if sleeps[1] != 20*time.Millisecond {
		t.Fatalf("sleep[1]: got %s want %s", sleeps[1], 20*time.Millisecond)
	}
}

func TestRetry_RetryAfterHeader_OverridesCalculatedBackoff(t *testing.T) {
	policy := RetryPolicy{
		MaxRetries:        1,
		BaseDelay:         10 * time.Millisecond,
		MaxDelay:          5 * time.Second,
		BackoffMultiplier: 2.0,
		Jitter:            false,
	}
	var sleeps []time.Duration
	sleep := func(ctx context.Context, d time.Duration) error {
		_ = ctx
		sleeps = append(sleeps, d)
		return nil
	}

	ra := 3 * time.Second
	err429 := ErrorFromHTTPStatus("openai", 429, "rate limited", nil, &ra)
	attempts := 0
	fn := func() (Response, error) {
		attempts++
		if attempts == 1 {
			return Response{}, err429
		}
		return Response{Provider: "openai", Model: "m", Message: Assistant("ok")}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := Retry(ctx, policy, sleep, nil, fn); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if got, want := len(sleeps), 1; got != want {
		t.Fatalf("sleep calls: got %d want %d (%v)", got, want, sleeps)
	}
	if sleeps[0] != ra {
		t.Fatalf("sleep[0]: got %s want %s", sleeps[0], ra)
	}
}

func TestRetry_RetryAfterHeader_TooLarge_AbortsWithoutRetry(t *testing.T) {
	policy := RetryPolicy{
		MaxRetries:        3,
		BaseDelay:         10 * time.Millisecond,
		MaxDelay:          1 * time.Second,
		BackoffMultiplier: 2.0,
		Jitter:            false,
	}
	sleep := func(ctx context.Context, d time.Duration) error {
		t.Fatalf("unexpected sleep: %s", d)
		return nil
	}

	ra := 5 * time.Second
	err429 := ErrorFromHTTPStatus("openai", 429, "rate limited", nil, &ra)
	attempts := 0
	fn := func() (Response, error) {
		attempts++
		return Response{}, err429
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := Retry(ctx, policy, sleep, nil, fn)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if attempts != 1 {
		t.Fatalf("attempts: got %d want 1", attempts)
	}
}

func TestRetry_DoesNotRetry_NonRetryableErrors(t *testing.T) {
	policy := RetryPolicy{MaxRetries: 3, BaseDelay: 1 * time.Millisecond, Jitter: false, BackoffMultiplier: 2.0, MaxDelay: 1 * time.Second}
	sleep := func(ctx context.Context, d time.Duration) error {
		t.Fatalf("unexpected sleep: %s", d)
		return nil
	}
	err401 := ErrorFromHTTPStatus("openai", 401, "bad key", nil, nil)

	attempts := 0
	fn := func() (Response, error) {
		attempts++
		return Response{}, err401
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := Retry(ctx, policy, sleep, nil, fn)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if attempts != 1 {
		t.Fatalf("attempts: got %d want 1", attempts)
	}
}

func TestRetry_MaxRetriesZero_DisablesRetry(t *testing.T) {
	policy := RetryPolicy{MaxRetries: 0, BaseDelay: 1 * time.Millisecond, Jitter: false, BackoffMultiplier: 2.0, MaxDelay: 1 * time.Second}
	err429 := ErrorFromHTTPStatus("openai", 429, "rate limited", nil, nil)

	attempts := 0
	fn := func() (Response, error) {
		attempts++
		return Response{}, err429
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := Retry(ctx, policy, nil, nil, fn)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if attempts != 1 {
		t.Fatalf("attempts: got %d want 1", attempts)
	}
}

func TestRetry_JitterAppliedWhenEnabled(t *testing.T) {
	policy := RetryPolicy{
		MaxRetries:        1,
		BaseDelay:         10 * time.Millisecond,
		MaxDelay:          1 * time.Second,
		BackoffMultiplier: 2.0,
		Jitter:            true,
	}
	err := ErrorFromHTTPStatus("openai", 429, "rate limited", nil, nil)

	if got, want := func() time.Duration {
		d, ok := retryDelay(policy, func() float64 { return 0.0 }, err, 0) // retry #1 uses n=0
		if !ok {
			t.Fatalf("expected ok=true")
		}
		return d
	}(), 5*time.Millisecond; got != want {
		t.Fatalf("jitter low: got %s want %s", got, want)
	}

	if got, want := func() time.Duration {
		d, ok := retryDelay(policy, func() float64 { return 1.0 }, err, 0)
		if !ok {
			t.Fatalf("expected ok=true")
		}
		return d
	}(), 15*time.Millisecond; got != want {
		t.Fatalf("jitter high: got %s want %s", got, want)
	}
}

func TestRetry_UnknownErrors_DefaultRetryable(t *testing.T) {
	policy := RetryPolicy{
		MaxRetries:        2,
		BaseDelay:         0,
		MaxDelay:          0,
		BackoffMultiplier: 2.0,
		Jitter:            false,
	}
	var attempts int
	fn := func() (Response, error) {
		attempts++
		if attempts < 3 {
			return Response{}, errors.New("network fell over")
		}
		return Response{Provider: "openai", Model: "m", Message: Assistant("ok")}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := Retry(ctx, policy, nil, nil, fn)
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if resp.Text() != "ok" {
		t.Fatalf("resp: %q", resp.Text())
	}
	if attempts != 3 {
		t.Fatalf("attempts: got %d want 3", attempts)
	}
}
