package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestR05V1ResponseRedactsUnknownBodiesAndRejectsNonSuccessStatus(t *testing.T) {
	for _, body := range []string{`<html>secret-kubeconfig-token</html>`, `{"unexpected":"secret-kubeconfig-token"}`} {
		_, err := parseV1Response[*lbDTO](502, []byte(body))
		if err == nil || strings.Contains(err.Error(), "secret-kubeconfig-token") {
			t.Fatalf("unredacted error: %v", err)
		}
		if !strings.Contains(err.RawBody, "secret-kubeconfig-token") {
			t.Fatal("raw body was lost")
		}
	}
	for _, status := range []int{400, 500, 503} {
		got, err := parseV1Response[*lbDTO](status, []byte(`{"error":0,"data":{"id":42}}`))
		if err == nil || got != nil {
			t.Fatalf("HTTP%d success-shaped body accepted: %+v %v", status, got, err)
		}
	}
}

func TestR05LoadBalancerGetRejectsForeignOrZeroIdentity(t *testing.T) {
	for _, body := range []string{`{"error":0,"data":{}}`, `{"error":0,"data":{"id":43}}`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		c, err := New(Config{APIBaseURL: srv.URL, APIKeyID: "fake", APISecretKey: "fake"})
		if err != nil {
			t.Fatal(err)
		}
		lb, err := c.GetLoadBalancer(context.Background(), 42, nil)
		srv.Close()
		if err == nil || lb != nil {
			t.Fatalf("foreign identity accepted: %+v %v", lb, err)
		}
	}
}

func TestR05Ambiguous806NeverRetriesWithoutExplicitFenceProof(t *testing.T) {
	for _, status := range []int{500, 503} {
		if IsRetryableTransient(&APIError{StatusCode: status, Codes: []int{806}}) {
			t.Fatalf("ambiguous806/HTTP%d is retryable", status)
		}
	}
}

func TestR05Fenced806RetryRequiresProofOnActualMutationResponse(t *testing.T) {
	for _, tc := range []struct {
		status int
		proof  bool
	}{{503, true}, {503, false}, {500, true}} {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if calls == 1 {
				if tc.proof {
					w.Header().Set(LBSelectorFenceRetryHeader, LBSelectorFenceRetryProof)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":806,"message":"Configuration was not applied"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"error":0,"data":{"id":42,"status":{"name":"SUCCESS"}}}`))
		}))
		c, err := New(Config{APIBaseURL: srv.URL, APIKeyID: "fake", APISecretKey: "fake"})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err = RetryOnBusy(ctx, 4*time.Second, func() (*LoadBalancer, error) {
			return c.ConfigureLoadBalancerFrontend(ctx, 42, LoadBalancerRequest{}, nil)
		})
		cancel()
		srv.Close()
		if tc.status == 503 && tc.proof {
			if err != nil || calls != 2 {
				t.Fatalf("safe fence not retried once: calls=%d err=%v", calls, err)
			}
		} else if err == nil || calls != 1 {
			t.Fatalf("ambiguous mutation retried: status=%d proof=%v calls=%d err=%v", tc.status, tc.proof, calls, err)
		}
	}
}

func TestR05MutationTransportNeverReplaysRedirectOr429(t *testing.T) {
	for _, status := range []int{307, 308, 429} {
		originCalls, redirectCalls := 0, 0
		redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			redirectCalls++
			_, _ = w.Write([]byte(`{"error":0,"data":{"id":43}}`))
		}))
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			originCalls++
			w.Header().Set("Location", redirect.URL+"/other-operation")
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":0,"data":{"id":42}}`))
		}))
		c, err := New(Config{APIBaseURL: origin.URL, APIKeyID: "fake-key", APISecretKey: "fake-secret"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.CreateLoadBalancerFrontend(context.Background(), LoadBalancerRequest{Name: "only-once"}, nil)
		origin.Close()
		redirect.Close()
		if err == nil || originCalls != 1 || redirectCalls != 0 {
			t.Errorf("HTTP%d replayed mutation/forwarded credentials: origin=%d redirect=%d err=%v", status, originCalls, redirectCalls, err)
		}
	}
}

func TestR05Ambiguous503CannotReplayMutation(t *testing.T) {
	calls := 0
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	_, err := RetryOnBusy(ctx, 4*time.Second, func() (string, error) {
		calls++
		return "", &APIError{StatusCode: 503, Codes: []int{744}, Message: "unverified proxy outcome"}
	})
	if err == nil || calls != 1 {
		t.Fatalf("ambiguous503 replayed mutation: calls=%d err=%v", calls, err)
	}
}

func TestR05ReadOnly429AndSameOriginRedirectRemainSafe(t *testing.T) {
	for _, status := range []int{307, 429} {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if calls == 1 {
				w.Header().Set("Location", "/canonical-read")
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(status)
				return
			}
			if r.Header.Get("X-API-KEY") != "fake" {
				t.Error("same-origin read lost auth")
			}
			_, _ = w.Write([]byte(`{"error":0,"data":{"id":42}}`))
		}))
		c, err := New(Config{APIBaseURL: srv.URL, APIKeyID: "fake", APISecretKey: "fake"})
		if err != nil {
			t.Fatal(err)
		}
		lb, err := c.GetLoadBalancer(context.Background(), 42, nil)
		srv.Close()
		if err != nil || lb == nil || calls != 2 {
			t.Fatalf("read handling changed for%d: calls=%d err=%v", status, calls, err)
		}
	}
}

func TestR05CrossOriginReadCannotForwardAPIKeyHeaders(t *testing.T) {
	received := 0
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received++
		_, _ = w.Write([]byte(`{"error":0,"data":{"id":42}}`))
	}))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", destination.URL+"/other-origin")
		w.WriteHeader(307)
	}))
	defer origin.Close()
	c, err := New(Config{APIBaseURL: origin.URL, APIKeyID: "fake", APISecretKey: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.GetLoadBalancer(context.Background(), 42, nil)
	if err == nil || received != 0 {
		t.Fatalf("API request followed another origin: received=%d err=%v", received, err)
	}
}

func TestR05KnownIDMutationsAndBucketReadCannotChangeIdentity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/buckets/") {
			_, _ = w.Write([]byte(`{"success":true,"data":{"name":"other-bucket"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"error":0,"data":{"id":43}}`))
	}))
	defer srv.Close()
	c, err := New(Config{APIBaseURL: srv.URL, APIKeyID: "fake", APISecretKey: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	for _, configure := range []func(context.Context, int64, LoadBalancerRequest, *RequestOpts) (*LoadBalancer, error){c.ConfigureLoadBalancerFrontend, c.ConfigureLoadBalancerCCM} {
		lb, err := configure(context.Background(), 42, LoadBalancerRequest{}, nil)
		if err == nil || lb != nil {
			t.Fatalf("mutation changed owned LB identity: %+v %v", lb, err)
		}
	}
	if bucket, err := c.GetBucket(context.Background(), "owned-bucket", nil); err == nil || bucket != nil {
		t.Fatalf("read changed owned bucket identity: %+v %v", bucket, err)
	}
}
