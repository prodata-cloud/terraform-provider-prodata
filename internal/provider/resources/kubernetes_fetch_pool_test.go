package resources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"terraform-provider-prodata/internal/client"
)

// kuberResponder routes the two node-pool endpoints fetchPool uses to canned
// responses, mimicking panel-main's V1 kuber envelope.
func kuberResponder(getStatus int, getBody string, listStatus int, listBody string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/getK8SNodePool/"):
			w.WriteHeader(getStatus)
			_, _ = w.Write([]byte(getBody))
		case strings.Contains(r.URL.Path, "/getNodePoolsByClusters/"):
			w.WriteHeader(listStatus)
			_, _ = w.Write([]byte(listBody))
		default:
			w.WriteHeader(http.StatusNotImplemented)
		}
	}
}

func newKuberTestClient(t *testing.T, h http.HandlerFunc) *client.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := client.New(client.Config{
		APIBaseURL:   srv.URL,
		APIKeyID:     "k",
		APISecretKey: "s",
		Region:       "TEST",
		ProjectTag:   "test",
	})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	return c
}

// TestFetchPool_GoneVsInconclusive is the regression guard for B2: fetchPool must
// distinguish a CONFIRMED-absent default node pool (the server affirmatively reported it
// gone) from an INCONCLUSIVE refresh (a transient API error). The caller turns a
// confirmed absence into a cluster REPLACEMENT, so a transient blip must report
// gone=false and never trigger that.
func TestFetchPool_GoneVsInconclusive(t *testing.T) {
	const emptyList = `{"error":0,"errMessage":"","data":[]}`

	tests := []struct {
		name       string
		poolID     int64
		poolName   string
		getStatus  int
		getBody    string
		listStatus int
		listBody   string
		wantGone   bool
	}{
		{"by-id: 404 is confirmed gone", 7, "", http.StatusNotFound, "", http.StatusOK, emptyList, true},
		{"by-id: transient get + transient list is inconclusive", 7, "", http.StatusInternalServerError, "boom", http.StatusInternalServerError, "boom", false},
		{"by-id: transient get + empty list is confirmed gone", 7, "", http.StatusInternalServerError, "boom", http.StatusOK, emptyList, true},
		{"by-name: transient list is inconclusive", 0, "tfacc", http.StatusOK, "", http.StatusInternalServerError, "boom", false},
		{"by-name: empty list is confirmed absent", 0, "tfacc", http.StatusOK, "", http.StatusOK, emptyList, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newKuberTestClient(t, kuberResponder(tc.getStatus, tc.getBody, tc.listStatus, tc.listBody))
			r := &K8sClusterResource{c: c}
			pool, gone := r.fetchPool(context.Background(), tc.poolID, tc.poolName, 99, "TEST", "test")
			if pool != nil {
				t.Fatalf("expected nil pool, got %+v", pool)
			}
			if gone != tc.wantGone {
				t.Fatalf("gone = %v, want %v", gone, tc.wantGone)
			}
		})
	}
}
