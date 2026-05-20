package client

import (
	"errors"
	"fmt"
)

// APIError is a structured error returned by the ProData API.
// It carries HTTP status code and business-logic error codes,
// enabling callers to match on specific codes without string parsing.
type APIError struct {
	StatusCode int
	Codes      []int
	Message    string
	RawBody    string
}

func (e *APIError) Error() string {
	if len(e.Codes) > 0 {
		return fmt.Sprintf("api error %v (http %d): %s", e.Codes, e.StatusCode, e.Message)
	}
	return fmt.Sprintf("api error (http %d): %s", e.StatusCode, e.Message)
}

// HasCode reports whether this API error contains the given error code.
func (e *APIError) HasCode(code int) bool {
	for _, c := range e.Codes {
		if c == code {
			return true
		}
	}
	return false
}

// IsAPIError reports whether err is (or wraps) an APIError with the given code.
func IsAPIError(err error, code int) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.HasCode(code)
	}
	return false
}

// IsNotFound reports whether err indicates that the resource does not exist
// (HTTP 404, or API codes 601/703/628/736). Code 712 (cross-project — bucket
// exists but is owned by another project) is intentionally NOT treated as
// not-found: silently dropping state for someone else's bucket would be a footgun.
func IsNotFound(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.StatusCode == 404 {
		return true
	}
	return apiErr.HasCode(601) || apiErr.HasCode(703) || apiErr.HasCode(628) || apiErr.HasCode(736)
}

// IsInsufficientFreeIPs reports whether err is the panel's "not enough free IPs
// in the network to allocate a load balancer" error (code 737). It is
// deliberately NOT folded into IsNotFound: this is a create-time validation
// failure, not a missing resource — treating it as not-found would mask it.
func IsInsufficientFreeIPs(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.HasCode(737)
}
