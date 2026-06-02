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

// RetryOnBusy retries fn while it returns a genuinely transient API error —
// an HTTP 503 (Service Unavailable), e.g. the panel's "no compute capacity"
// (744) or "no IP pool in region" (743) — using exponential backoff with ±25%
// jitter (2s → 4s → 8s → 15s cap).
//
// It deliberately does NOT retry code 627 ("Unhandled error. Please contact
// support."): 627 is the panel's generic HTTP 500 catch-all for any unmapped
// failure (e.g. a downstream provisioning service returning 5xx), not a
// transient/busy condition. Retrying it would only hang `terraform apply` for
// the full timeout and mask the real cause, so 627 now surfaces immediately.
func RetryOnBusy[T any](ctx context.Context, timeout time.Duration, fn func() (T, error)) (T, error) {
	deadline := time.Now().Add(timeout)
	interval := retryInitialInterval

	for {
		result, err := fn()
		if err == nil || !IsRetryableTransient(err) {
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
