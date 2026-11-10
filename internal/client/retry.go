package client

import (
	"context"
	"fmt"
	"math/rand"
	"time"
)

const (
	RetryTimeoutShort = 2 * time.Minute
	RetryTimeoutLong  = 5 * time.Minute

	retryInitialInterval = 2 * time.Second
	retryMaxInterval     = 15 * time.Second
)

// RetryOnBusy replays a mutation only after an explicit API pre-write refusal.
// An HTTP 503 or capacity error alone cannot establish that an upstream mutation
// did not happen. The LB fence proof is deliberately separate from ambiguous AWX error 806.
func RetryOnBusy[T any](ctx context.Context, timeout time.Duration, fn func() (T, error)) (T, error) {
	return retryOnBusy(ctx, timeout, fn, isRetrySafeMutationRefusal)
}

// RetryReadOnBusy is for read-only operations, including the primary-move dry-run.
func RetryReadOnBusy[T any](ctx context.Context, timeout time.Duration, fn func() (T, error)) (T, error) {
	return retryOnBusy(ctx, timeout, fn, IsRetryableTransient)
}

func isRetrySafeMutationRefusal(err error) bool {
	return IsAPIError(err, 806) && IsRetryableTransient(err)
}

func retryOnBusy[T any](ctx context.Context, timeout time.Duration, fn func() (T, error), retryable func(error) bool) (T, error) {
	deadline := time.Now().Add(timeout)
	interval := retryInitialInterval

	for {
		result, err := fn()
		if err == nil || !retryable(err) {
			return result, err
		}

		if time.Now().After(deadline) {
			var zero T
			return zero, fmt.Errorf("timed out (service unavailable): %w", err)
		}

		// Add ±25% jitter to avoid thundering herd
		jitter := time.Duration(rand.Int63n(int64(interval)/2)) - interval/4
		sleepDuration := interval + jitter

		select {
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		case <-time.After(sleepDuration):
		}

		if interval *= 2; interval > retryMaxInterval {
			interval = retryMaxInterval
		}
	}
}

// RetryVoidOnBusy is RetryOnBusy for functions that return only an error.
func RetryVoidOnBusy(ctx context.Context, timeout time.Duration, fn func() error) error {
	_, err := RetryOnBusy(ctx, timeout, func() (struct{}, error) {
		return struct{}{}, fn()
	})
	return err
}
