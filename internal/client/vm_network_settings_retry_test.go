package client

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestNetworkSettingsRetriesOnlyExplicitPrewriteRefusals(t *testing.T) {
	for _, code := range []int{961, 370971} {
		calls := 0
		err := retryNetworkSettingsBusy(context.Background(), func() error {
			calls++
			if calls == 1 {
				return &APIError{StatusCode: http.StatusServiceUnavailable, Codes: []int{code}}
			}
			return nil
		})
		if err != nil || calls != 2 {
			t.Fatalf("code %d: calls=%d error=%v", code, calls, err)
		}
	}
}

func TestNetworkSettingsNeverRetriesAnUnknownWriteOutcome(t *testing.T) {
	for _, failure := range []error{
		errors.New("connection lost"),
		&APIError{StatusCode: http.StatusServiceUnavailable},
		&APIError{StatusCode: http.StatusServiceUnavailable, Codes: []int{961, 370970}},
		&APIError{StatusCode: http.StatusServiceUnavailable, Codes: []int{977}},
		&APIError{StatusCode: http.StatusConflict, Codes: []int{370970}},
	} {
		calls := 0
		err := retryNetworkSettingsBusy(context.Background(), func() error {
			calls++
			return failure
		})
		if err != failure || calls != 1 {
			t.Fatalf("failure %v: calls=%d error=%v", failure, calls, err)
		}
	}
}
