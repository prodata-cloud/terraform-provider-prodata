package client

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// Only the explicit pending result may be polled: the panel/core contract deduplicates it.
// A transport failure or generic error retains state and needs reconciliation, not a retry.
func pollVmDeletion(ctx context.Context, poll func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	for {
		err := poll(ctx)
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusConflict ||
			len(apiErr.Codes) != 1 || apiErr.Codes[0] != 370972 {
			return err
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
