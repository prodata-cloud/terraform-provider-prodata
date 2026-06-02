package client

import (
	"errors"
	"fmt"
	"testing"
)

func TestAPIError_Error(t *testing.T) {
	tests := []struct {
		name string
		err  APIError
		want string
	}{
		{
			name: "with codes",
			err:  APIError{StatusCode: 500, Codes: []int{627}, Message: "busy"},
			want: "api error [627] (http 500): busy",
		},
		{
			name: "multiple codes",
			err:  APIError{StatusCode: 400, Codes: []int{666, 601}, Message: "conflict; not found"},
			want: "api error [666 601] (http 400): conflict; not found",
		},
		{
			name: "no codes",
			err:  APIError{StatusCode: 502, Message: "Bad Gateway"},
			want: "api error (http 502): Bad Gateway",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.err.Error()
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAPIError_HasCode(t *testing.T) {
	err := APIError{Codes: []int{627, 666}}

	if !err.HasCode(627) {
		t.Error("HasCode(627) should be true")
	}
	if !err.HasCode(666) {
		t.Error("HasCode(666) should be true")
	}
	if err.HasCode(601) {
		t.Error("HasCode(601) should be false")
	}
}

func TestIsAPIError(t *testing.T) {
	apiErr := &APIError{StatusCode: 500, Codes: []int{627}, Message: "busy"}

	// Direct APIError
	if !IsAPIError(apiErr, 627) {
		t.Error("direct APIError: IsAPIError(err, 627) should be true")
	}
	if IsAPIError(apiErr, 666) {
		t.Error("direct APIError: IsAPIError(err, 666) should be false")
	}

	// Wrapped APIError
	wrapped := fmt.Errorf("something went wrong: %w", apiErr)
	if !IsAPIError(wrapped, 627) {
		t.Error("wrapped: IsAPIError(err, 627) should be true")
	}

	// Non-APIError
	plainErr := errors.New("plain error")
	if IsAPIError(plainErr, 627) {
		t.Error("plain error: IsAPIError should be false")
	}

	// Nil error
	if IsAPIError(nil, 627) {
		t.Error("nil: IsAPIError should be false")
	}
}

func TestIsNotFound(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"http 404", &APIError{StatusCode: 404, Message: "not found"}, true},
		{"code 601", &APIError{StatusCode: 403, Codes: []int{601}, Message: "vm not found"}, true},
		{"code 703", &APIError{StatusCode: 400, Codes: []int{703}, Message: "volume not found"}, true},
		{"code 628 bucket gone", &APIError{StatusCode: 400, Codes: []int{628}, Message: "bucket not found"}, true},
		{"code 712 cross-project NOT not-found", &APIError{StatusCode: 403, Codes: []int{712}, Message: "not yours"}, false},
		{"code 627", &APIError{StatusCode: 500, Codes: []int{627}, Message: "busy"}, false},
		{"http 500 no codes", &APIError{StatusCode: 500, Message: "server error"}, false},
		{"wrapped 601", fmt.Errorf("wrap: %w", &APIError{Codes: []int{601}}), true},
		{"plain error", errors.New("not found"), false},
		{"nil", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsNotFound(tt.err)
			if got != tt.want {
				t.Errorf("IsNotFound() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsRetryableTransient(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"http 503 no capacity (744)", &APIError{StatusCode: 503, Codes: []int{744}, Message: "no capacity"}, true},
		{"http 503 no ip pool (743)", &APIError{StatusCode: 503, Codes: []int{743}, Message: "no ip pool"}, true},
		{"code 627 http 500 NOT transient", &APIError{StatusCode: 500, Codes: []int{627}, Message: "Unhandled error"}, false},
		{"http 502 provisioning (747) NOT transient", &APIError{StatusCode: 502, Codes: []int{747}, Message: "provisioning failed"}, false},
		{"http 400 NOT transient", &APIError{StatusCode: 400, Codes: []int{666}, Message: "conflict"}, false},
		{"wrapped 503", fmt.Errorf("wrap: %w", &APIError{StatusCode: 503, Codes: []int{744}}), true},
		{"plain error", errors.New("boom"), false},
		{"nil", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsRetryableTransient(tt.err)
			if got != tt.want {
				t.Errorf("IsRetryableTransient() = %v, want %v", got, tt.want)
			}
		})
	}
}
