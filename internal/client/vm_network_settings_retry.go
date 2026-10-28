package client

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// Only the panel's explicit pre-write refusals may be repeated. A transport error, an arbitrary
// 503, or the unresolved-outcome code does not prove that a network write was never sent.
func retryNetworkSettingsBusy(ctx context.Context, fn func() error) error {
	deadline := time.Now().Add(RetryTimeoutShort)
	delay := retryInitialInterval
	for {
		err := fn()
		var apiErr *APIError
		if err == nil || !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusServiceUnavailable ||
			len(apiErr.Codes) != 1 || (apiErr.Codes[0] != 961 && apiErr.Codes[0] != 370971) {
			return err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return err
		}
		wait := min(delay, remaining)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
		delay = min(delay*2, retryMaxInterval)
	}
}
