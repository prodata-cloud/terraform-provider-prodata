package client

import (
	"net/http"
	"strings"
	"testing"
)

// TestParseResponse_RedactsNonEnvelopedBody verifies that an error body which is NOT
// the standard {success,errors[]} envelope is never echoed into the user-facing error
// message — such a body (an infra page or an unexpected payload) can carry secrets like
// kube_config credentials or VM passwords. The full body must still be available in
// RawBody (which is never rendered) for programmatic inspection.
func TestParseResponse_RedactsNonEnvelopedBody(t *testing.T) {
	const secret = "bearer-token-SUPERSECRET-kubeconfig"

	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"json-but-not-error-envelope", http.StatusInternalServerError, `{"unexpected":"` + secret + `"}`},
		{"non-json-infra-page", http.StatusBadGateway, "<html>nginx 502 " + secret + "</html>"},
		{"success-false-no-errors", http.StatusOK, `{"success":false,"data":{"token":"` + secret + `"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := parseResponse(tc.status, []byte(tc.body), nil)
			if err == nil {
				t.Fatal("expected an error")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("secret leaked into error message: %q", err.Error())
			}
			apiErr, ok := err.(*APIError)
			if !ok {
				t.Fatalf("want *APIError, got %T", err)
			}
			if !strings.Contains(apiErr.RawBody, secret) {
				t.Errorf("RawBody must retain the full body for inspection; got %q", apiErr.RawBody)
			}
		})
	}
}

// TestParseResponse_EnvelopeErrorsStillSurface confirms the redaction does not hide the
// safe, DB-backed error messages and codes that come through the proper error envelope.
func TestParseResponse_EnvelopeErrorsStillSurface(t *testing.T) {
	body := `{"success":false,"errors":[{"code":714,"message":"Permission denied"}]}`
	err := parseResponse(http.StatusForbidden, []byte(body), nil)
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("want *APIError, got %T (%v)", err, err)
	}
	if !strings.Contains(apiErr.Message, "Permission denied") {
		t.Errorf("envelope message should surface; got %q", apiErr.Message)
	}
	if !apiErr.HasCode(714) {
		t.Errorf("envelope code 714 should be preserved; got %v", apiErr.Codes)
	}
}

// TestParseResponse_SuccessWithoutData covers a success envelope that omits "data": it
// must succeed and leave result at its zero value, not fail with "unexpected end of JSON
// input" from unmarshalling an empty payload.
func TestParseResponse_SuccessWithoutData(t *testing.T) {
	var out struct {
		ID int64 `json:"id"`
	}
	for _, body := range []string{`{"success":true}`, `{"success":true,"data":null}`} {
		if err := parseResponse(http.StatusOK, []byte(body), &out); err != nil {
			t.Fatalf("body %q: unexpected error: %v", body, err)
		}
		if out.ID != 0 {
			t.Fatalf("body %q: result should stay zero, got %d", body, out.ID)
		}
	}
}
