package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// newTestServer creates an httptest server that always returns the given status and body.
func newTestServer(status int, body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

// newTestClient creates a Client pointing at the given test server.
func newTestClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	c, err := New(Config{
		APIBaseURL:   server.URL,
		APIKeyID:     "test-key",
		APISecretKey: "test-secret",
		Region:       "TEST",
		ProjectTag:   "test-project",
	})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	return c
}

func TestDo_SuccessWithData(t *testing.T) {
	server := newTestServer(200, `{"success":true,"data":{"id":42,"name":"test-vm","status":"RUNNING"}}`)
	defer server.Close()

	c := newTestClient(t, server)
	var vm Vm
	err := c.Do(context.Background(), http.MethodGet, "/api/v2/vms/42", nil, &vm, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vm.ID != 42 {
		t.Errorf("vm.ID = %d, want 42", vm.ID)
	}
	if vm.Name != "test-vm" {
		t.Errorf("vm.Name = %q, want %q", vm.Name, "test-vm")
	}
	if vm.Status != "RUNNING" {
		t.Errorf("vm.Status = %q, want %q", vm.Status, "RUNNING")
	}
}

func TestDo_SuccessNoData(t *testing.T) {
	server := newTestServer(200, `{"success":true,"data":null}`)
	defer server.Close()

	c := newTestClient(t, server)
	err := c.Do(context.Background(), http.MethodPost, "/api/v2/vms/1/stop", nil, nil, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDo_HTTP500WithStructuredErrors(t *testing.T) {
	body := `{"success":false,"data":null,"errors":[{"code":627,"message":"Необработанная ошибка. Обратитесь в службу поддержки."}]}`
	server := newTestServer(500, body)
	defer server.Close()

	c := newTestClient(t, server)
	err := c.Do(context.Background(), http.MethodPost, "/api/v2/vms", nil, nil, nil)

	if err == nil {
		t.Fatal("expected error")
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != 500 {
		t.Errorf("StatusCode = %d, want 500", apiErr.StatusCode)
	}
	if !apiErr.HasCode(627) {
		t.Errorf("expected code 627, got %v", apiErr.Codes)
	}
	if !IsAPIError(err, 627) {
		t.Error("IsAPIError(err, 627) should be true")
	}
}

func TestDo_HTTP500WithMultipleErrors(t *testing.T) {
	body := `{"success":false,"data":null,"errors":[{"code":627,"message":"busy"},{"code":601,"message":"not found"}]}`
	server := newTestServer(500, body)
	defer server.Close()

	c := newTestClient(t, server)
	err := c.Do(context.Background(), http.MethodGet, "/api/v2/vms/1", nil, nil, nil)

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if len(apiErr.Codes) != 2 {
		t.Errorf("expected 2 codes, got %v", apiErr.Codes)
	}
	if !apiErr.HasCode(627) || !apiErr.HasCode(601) {
		t.Errorf("expected codes 627 and 601, got %v", apiErr.Codes)
	}
	if apiErr.Message != "busy; not found" {
		t.Errorf("Message = %q, want %q", apiErr.Message, "busy; not found")
	}
}

func TestDo_HTTP502PlainText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502)
		_, _ = w.Write([]byte("Bad Gateway"))
	}))
	defer server.Close()

	c := newTestClient(t, server)
	err := c.Do(context.Background(), http.MethodGet, "/api/v2/vms", nil, nil, nil)

	if err == nil {
		t.Fatal("expected error")
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != 502 {
		t.Errorf("StatusCode = %d, want 502", apiErr.StatusCode)
	}
	// No structured codes — should have raw body as message
	if len(apiErr.Codes) != 0 {
		t.Errorf("expected no codes, got %v", apiErr.Codes)
	}
}

func TestDo_APIFailureWith200(t *testing.T) {
	body := `{"success":false,"errors":[{"code":666,"message":"VM name already exists"}]}`
	server := newTestServer(200, body)
	defer server.Close()

	c := newTestClient(t, server)
	err := c.Do(context.Background(), http.MethodPost, "/api/v2/vms", nil, nil, nil)

	if err == nil {
		t.Fatal("expected error")
	}
	if !IsAPIError(err, 666) {
		t.Errorf("expected code 666: %v", err)
	}
}

func TestDo_EmptyBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
	}))
	defer server.Close()

	c := newTestClient(t, server)
	err := c.Do(context.Background(), http.MethodDelete, "/api/v2/vms/1", nil, nil, nil)

	if err != nil {
		t.Fatalf("unexpected error for 204 with no body: %v", err)
	}
}

func TestDo_SetsHeaders(t *testing.T) {
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"success":true,"data":null}`))
	}))
	defer server.Close()

	c := newTestClient(t, server)
	opts := &RequestOpts{Region: "UZ5", ProjectTag: "my-project"}
	_ = c.Do(context.Background(), http.MethodGet, "/api/v2/vms", nil, nil, opts)

	checks := map[string]string{
		"X-Api-Key":     "test-key",
		"X-Api-Secret":  "test-secret",
		"X-Region":      "UZ5",
		"X-Project-Tag": "my-project",
		"Content-Type":  "application/json",
	}
	for header, want := range checks {
		got := gotHeaders.Get(header)
		if got != want {
			t.Errorf("header %s = %q, want %q", header, got, want)
		}
	}
}

func TestDo_UsesProviderDefaultsWhenNoOpts(t *testing.T) {
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"success":true,"data":null}`))
	}))
	defer server.Close()

	c := newTestClient(t, server)
	_ = c.Do(context.Background(), http.MethodGet, "/api/v2/vms", nil, nil, nil)

	if got := gotHeaders.Get("X-Region"); got != "TEST" {
		t.Errorf("X-Region = %q, want provider default %q", got, "TEST")
	}
	if got := gotHeaders.Get("X-Project-Tag"); got != "test-project" {
		t.Errorf("X-Project-Tag = %q, want provider default %q", got, "test-project")
	}
}

func TestGetVm_NotFound(t *testing.T) {
	body := `{"success":false,"errors":[{"code":601,"message":"VM not found"}]}`
	server := newTestServer(403, body)
	defer server.Close()

	c := newTestClient(t, server)
	_, err := c.GetVm(context.Background(), 999, nil)

	if err == nil {
		t.Fatal("expected error")
	}
	if !IsNotFound(err) {
		t.Errorf("expected IsNotFound=true, got error: %v", err)
	}
}

func TestWaitForVmStatus_ImmediateSuccess(t *testing.T) {
	server := newTestServer(200, `{"success":true,"data":{"id":1,"name":"vm","status":"STOPPED"}}`)
	defer server.Close()

	c := newTestClient(t, server)
	err := c.WaitForVmStatus(context.Background(), 1, "STOPPED", 5*time.Second, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWaitForVmStatus_Timeout(t *testing.T) {
	server := newTestServer(200, `{"success":true,"data":{"id":1,"name":"vm","status":"STOPPING"}}`)
	defer server.Close()

	c := newTestClient(t, server)
	err := c.WaitForVmStatus(context.Background(), 1, "STOPPED", 1*time.Second, nil)

	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestWaitForVmStatus_TransientErrors(t *testing.T) {
	var callCount int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&callCount, 1)
		if n <= 2 {
			// First 2 calls: transient error
			w.WriteHeader(500)
			_, _ = w.Write([]byte("Internal Server Error"))
			return
		}
		// 3rd call: success
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"success":true,"data":{"id":1,"name":"vm","status":"STOPPED"}}`))
	}))
	defer server.Close()

	c := newTestClient(t, server)
	err := c.WaitForVmStatus(context.Background(), 1, "STOPPED", 30*time.Second, nil)

	if err != nil {
		t.Fatalf("expected success after transient errors, got: %v", err)
	}
	if n := atomic.LoadInt64(&callCount); n != 3 {
		t.Errorf("expected 3 calls (2 transient + 1 success), got %d", n)
	}
}

func TestWaitForVmStatus_TooManyTransientErrors(t *testing.T) {
	server := newTestServer(500, "Internal Server Error")
	defer server.Close()

	c := newTestClient(t, server)
	err := c.WaitForVmStatus(context.Background(), 1, "STOPPED", 30*time.Second, nil)

	if err == nil {
		t.Fatal("expected error after too many transient failures")
	}
}
