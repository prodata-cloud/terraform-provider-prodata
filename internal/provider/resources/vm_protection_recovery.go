package resources

import (
	"context"
	"errors"
	"fmt"
	"terraform-provider-prodata/internal/client"
	"time"
)

// Only the panel's versioned, known-ID submit is replayed. Its transactional claim
// makes repeated calls safe; the underlying non-idempotent upstream POST is never replayed here.
func submitKnownReservation[T any](ctx context.Context, submit, read func(context.Context) (*T, error), status func(*T) string, delay time.Duration) (*T, error) {
	if delay <= 0 {
		delay = time.Second
	}
	var current *T
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		var err error
		current, err = submit(ctx)
		lastErr = err
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && (apiErr.StatusCode == 401 || apiErr.StatusCode == 403 || apiErr.StatusCode < 500 && len(apiErr.Codes) > 0 && !apiErr.HasCode(627)) {
			return current, err
		}
		if err == nil && current != nil && status(current) != "RESERVED" {
			return current, nil
		}
		verified, readErr := read(ctx)
		if readErr != nil {
			if err != nil {
				return current, err
			}
			return current, readErr
		}
		current = verified
		if current == nil {
			return nil, fmt.Errorf("the panel did not confirm the known reservation")
		}
		if status(current) != "RESERVED" {
			return current, nil
		}
		if ctx.Err() != nil {
			return current, ctx.Err()
		}
		if attempt == 2 {
			break
		}
		select {
		case <-ctx.Done():
			return current, ctx.Err()
		case <-time.After(delay):
		}
	}
	if lastErr != nil {
		return current, fmt.Errorf("the known reservation remains RESERVED after bounded submit retries; its identity is retained: %w", lastErr)
	}
	return current, fmt.Errorf("the known reservation remains RESERVED after bounded submit retries; its identity is retained")
}
