package client

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestDeletionOnlyPollsExplicitPendingResult(t *testing.T) {
	for _, err := range []error{
		errors.New("lost response"),
		&APIError{StatusCode: http.StatusInternalServerError, Codes: []int{627}},
		&APIError{StatusCode: http.StatusConflict, Codes: []int{370970}},
		&APIError{StatusCode: http.StatusConflict, Codes: []int{370972, 627}},
	} {
		calls := 0
		got := pollVmDeletion(context.Background(), func(context.Context) error { calls++; return err })
		if got != err || calls != 1 {
			t.Fatalf("unexpected destructive retry: calls=%d err=%v", calls, got)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	calls := 0
	err := pollVmDeletion(ctx, func(context.Context) error { calls++; return &APIError{StatusCode: 409, Codes: []int{370972}} })
	if !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Fatalf("pending did not retain deadline: %d %v", calls, err)
	}
}

func TestDeletionWaitsForAcceptedTaskBeforeSuccess(t *testing.T) {
	calls := 0
	err := pollVmDeletion(context.Background(), func(context.Context) error {
		calls++
		if calls == 1 {
			return &APIError{StatusCode: 409, Codes: []int{370972}}
		}
		return nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("pending task was not polled to completion: %d %v", calls, err)
	}
}
