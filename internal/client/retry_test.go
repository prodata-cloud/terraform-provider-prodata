package client

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRetryOnBusy_ImmediateSuccess(t *testing.T) {
	calls := 0
	result, err := RetryOnBusy(context.Background(), 5*time.Second, func() (string, error) {
		calls++
		return "ok", nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "ok" {
		t.Errorf("got %q, want %q", result, "ok")
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

func TestRetryOnBusy_NonRetryableError(t *testing.T) {
	calls := 0
	_, err := RetryOnBusy(context.Background(), 5*time.Second, func() (string, error) {
		calls++
		return "", &APIError{StatusCode: 400, Codes: []int{666}, Message: "name conflict"}
	})

	if err == nil {
		t.Fatal("expected error")
	}
	if !IsAPIError(err, 666) {
		t.Errorf("expected code 666, got: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call (no retry on a non-503 error), got %d", calls)
	}
}

// TestRetryOnBusy_627NotRetried guards the fix: 627 is the panel's generic HTTP
// 500 "Unhandled error" catch-all (e.g. a downstream provisioning service
// returning 5xx), not a transient/busy condition. It must fail fast — one call,
// no backoff loop — so the apply does not hang for the whole timeout.
func TestRetryOnBusy_627NotRetried(t *testing.T) {
	calls := 0
	_, err := RetryOnBusy(context.Background(), 5*time.Second, func() (string, error) {
		calls++
		return "", &APIError{StatusCode: 500, Codes: []int{627}, Message: "Unhandled error"}
	})

	if err == nil {
		t.Fatal("expected error")
	}
	if !IsAPIError(err, 627) {
		t.Errorf("expected code 627 surfaced verbatim, got: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected exactly 1 call (627 must not be retried), got %d", calls)
	}
}

func TestRetryOnBusy_RetryThenSuccess(t *testing.T) {
	calls := 0
	result, err := RetryOnBusy(context.Background(), 30*time.Second, func() (string, error) {
		calls++
		if calls < 3 {
			return "", &APIError{StatusCode: 503, Codes: []int{744}, Message: "no capacity"}
		}
		return "done", nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "done" {
		t.Errorf("got %q, want %q", result, "done")
	}
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
}

func TestRetryOnBusy_Timeout(t *testing.T) {
	calls := 0
	_, err := RetryOnBusy(context.Background(), 1*time.Second, func() (string, error) {
		calls++
		return "", &APIError{StatusCode: 503, Codes: []int{744}, Message: "no capacity"}
	})

	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !errors.Is(err, &APIError{}) {
		// The error wraps the APIError via %w
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Errorf("expected wrapped APIError, got: %v", err)
		}
	}
	if calls < 1 {
		t.Errorf("expected at least 1 call, got %d", calls)
	}
}

func TestRetryOnBusy_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	calls := 0
	_, err := RetryOnBusy(ctx, 30*time.Second, func() (string, error) {
		calls++
		// Cancel context after first call so the select picks it up
		cancel()
		return "", &APIError{StatusCode: 503, Codes: []int{744}, Message: "no capacity"}
	})

	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

func TestRetryVoidOnBusy(t *testing.T) {
	calls := 0
	err := RetryVoidOnBusy(context.Background(), 30*time.Second, func() error {
		calls++
		if calls < 2 {
			return &APIError{StatusCode: 503, Codes: []int{744}, Message: "no capacity"}
		}
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls, got %d", calls)
	}
}

func TestRetryOnBusy_ExponentialBackoff(t *testing.T) {
	var timestamps []time.Time

	_, _ = RetryOnBusy(context.Background(), 10*time.Second, func() (string, error) {
		timestamps = append(timestamps, time.Now())
		if len(timestamps) < 4 {
			return "", &APIError{StatusCode: 503, Codes: []int{744}, Message: "no capacity"}
		}
		return "done", nil
	})

	if len(timestamps) != 4 {
		t.Fatalf("expected 4 calls, got %d", len(timestamps))
	}

	// Verify intervals are increasing (exponential backoff)
	// Expected: ~2s, ~4s, ~8s (with some tolerance)
	for i := 1; i < len(timestamps); i++ {
		gap := timestamps[i].Sub(timestamps[i-1])
		expectedMin := time.Duration(1<<(i-1)) * time.Second // 1s, 2s, 4s (with tolerance)
		if gap < expectedMin {
			t.Errorf("gap %d: %v < expected min %v", i, gap, expectedMin)
		}
	}

	// Verify second gap is larger than first (exponential, not linear)
	gap1 := timestamps[1].Sub(timestamps[0])
	gap2 := timestamps[2].Sub(timestamps[1])
	if gap2 <= gap1 {
		t.Errorf("expected exponential backoff: gap2 (%v) should be > gap1 (%v)", gap2, gap1)
	}
}
