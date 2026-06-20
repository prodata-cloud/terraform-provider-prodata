package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// flakyRoundTripper returns a transport error for its first `failures` calls, then a
// canned HTTP response — simulating a transient network blip.
type flakyRoundTripper struct {
	failures int
	calls    int
	status   int
	body     string
}

func (f *flakyRoundTripper) RoundTrip(_ *http.Request) (*http.Response, error) {
	f.calls++
	if f.calls <= f.failures {
		return nil, fmt.Errorf("simulated connection reset")
	}
	return &http.Response{
		StatusCode: f.status,
		Body:       io.NopCloser(strings.NewReader(f.body)),
		Header:     make(http.Header),
	}, nil
}

func newClientWithTransport(t *testing.T, rt http.RoundTripper) *Client {
	t.Helper()
	c, err := New(Config{APIBaseURL: "https://example.test", APIKeyID: "k", APISecretKey: "s"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.httpClient.Transport = rt
	return c
}

// TestDoRequest_RetriesGetOnTransportError: a GET succeeds after transient transport
// failures, so a momentary network blip during a refresh does not abort the plan.
func TestDoRequest_RetriesGetOnTransportError(t *testing.T) {
	rt := &flakyRoundTripper{failures: 2, status: 200, body: `{"success":true,"data":null}`}
	c := newClientWithTransport(t, rt)
	status, _, err := c.doRequest(context.Background(), http.MethodGet, "/api/v2/x", nil, nil)
	if err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if status != 200 {
		t.Errorf("status = %d, want 200", status)
	}
	if rt.calls != 3 {
		t.Errorf("calls = %d, want 3 (2 fail + 1 ok)", rt.calls)
	}
}

// TestDoRequest_DoesNotRetryPostOnTransportError: a POST must not be retried — it may
// already have reached the server, and re-sending could double-apply.
func TestDoRequest_DoesNotRetryPostOnTransportError(t *testing.T) {
	rt := &flakyRoundTripper{failures: 1, status: 200, body: `{}`}
	c := newClientWithTransport(t, rt)
	_, _, err := c.doRequest(context.Background(), http.MethodPost, "/api/v2/x", nil, nil)
	if err == nil {
		t.Fatal("expected error: POST must not retry on a transport error")
	}
	if rt.calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry)", rt.calls)
	}
}

// TestDoRequest_GivesUpAfterMaxTransportRetries: a GET that keeps failing gives up after
// the bounded number of retries rather than looping forever.
func TestDoRequest_GivesUpAfterMaxTransportRetries(t *testing.T) {
	rt := &flakyRoundTripper{failures: 99, status: 200, body: `{}`}
	c := newClientWithTransport(t, rt)
	_, _, err := c.doRequest(context.Background(), http.MethodGet, "/api/v2/x", nil, nil)
	if err == nil {
		t.Fatal("expected error after exhausting transport retries")
	}
	if rt.calls != transportMaxRetries+1 {
		t.Errorf("calls = %d, want %d (initial + %d retries)", rt.calls, transportMaxRetries+1, transportMaxRetries)
	}
}
