package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// recordedRequest is what a recordingServer saw of a request.
type recordedRequest struct {
	method, path, query, body string
}

// recordingServer answers every request with 200 and body and keeps what it was sent.
func recordingServer(t *testing.T, body string) (*httptest.Server, *[]recordedRequest) {
	t.Helper()
	var seen []recordedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = append(seen, recordedRequest{r.Method, r.URL.Path, r.URL.RawQuery, string(b)})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server, &seen
}

func TestGetVmLocalNetworks(t *testing.T) {
	server, seen := recordingServer(t, `{"success":true,"data":[
		{"id":9070,"networkId":411,"networkName":"main","ip":"192.168.10.5","mac":"BC:24:11:00:00:01","gateway":"192.168.10.1","primary":true},
		{"id":9071,"networkId":412,"networkName":"backend","ip":"192.168.20.15","mac":null,"gateway":null,"primary":false},
		{"id":9072,"networkId":null,"networkName":null,"ip":null,"primary":false}
	],"errors":null}`)
	c := newTestClient(t, server)

	nics, err := c.GetVmLocalNetworks(context.Background(), 118, &RequestOpts{Region: "UZ5", ProjectTag: "proj"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*seen) != 1 || (*seen)[0].method != http.MethodGet || (*seen)[0].path != "/panel-main/api/v2/vms/118/local-networks" {
		t.Errorf("requests = %+v, want one GET of /panel-main/api/v2/vms/118/local-networks", *seen)
	}
	if got, want := (*seen)[0].query, "projectTag=proj&region=UZ5"; got != want {
		t.Errorf("query = %q, want %q", got, want)
	}
	if len(nics) != 3 {
		t.Fatalf("got %d interfaces, want 3: %+v", len(nics), nics)
	}

	main := nics[0]
	if main.ID != 9070 || !main.Primary || main.NetworkID == nil || *main.NetworkID != 411 ||
		main.NetworkName == nil || *main.NetworkName != "main" ||
		main.IP == nil || *main.IP != "192.168.10.5" ||
		main.MAC == nil || *main.MAC != "BC:24:11:00:00:01" ||
		main.Gateway == nil || *main.Gateway != "192.168.10.1" {
		t.Errorf("primary interface not decoded: %+v", main)
	}

	second := nics[1]
	if second.ID != 9071 || second.Primary || second.NetworkID == nil || *second.NetworkID != 412 || second.IP == nil || *second.IP != "192.168.20.15" {
		t.Errorf("second interface not decoded: %+v", second)
	}
	if second.MAC != nil || second.Gateway != nil {
		t.Errorf("null mac and gateway must stay nil: mac=%v gateway=%v", second.MAC, second.Gateway)
	}

	legacy := nics[2]
	if legacy.NetworkID != nil || legacy.NetworkName != nil || legacy.IP != nil || legacy.MAC != nil || legacy.Gateway != nil {
		t.Errorf("a legacy row with nulls and missing keys must decode to nils: %+v", legacy)
	}
}

func TestGetVmLocalNetworks_NoScopeNoQuery(t *testing.T) {
	server, seen := recordingServer(t, `{"success":true,"data":[]}`)
	c := newTestClient(t, server)

	nics, err := c.GetVmLocalNetworks(context.Background(), 118, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nics) != 0 {
		t.Errorf("got %+v, want no interfaces", nics)
	}
	if (*seen)[0].query != "" {
		t.Errorf("query = %q, want none: the client's own region and project travel in the headers", (*seen)[0].query)
	}
}

func TestAttachLocalNetwork(t *testing.T) {
	server, seen := recordingServer(t, `{"success":true,"data":{"id":9071,"networkId":412,"networkName":"backend",
		"ip":"192.168.20.15","mac":"BC:24:11:AA:BB:CC","gateway":null,"primary":false,
		"requiresRestart":true,"requiresGuestConfiguration":false},"errors":null}`)
	c := newTestClient(t, server)

	attached, err := c.AttachLocalNetwork(context.Background(), 118, AttachLocalNetworkRequest{NetworkID: 412, IP: "192.168.20.15"}, &RequestOpts{Region: "UZ5", ProjectTag: "proj"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := (*seen)[0]
	if got.method != http.MethodPost || got.path != "/panel-main/api/v2/vms/118/local-networks" || got.query != "projectTag=proj&region=UZ5" {
		t.Errorf("request = %+v, want a POST of /panel-main/api/v2/vms/118/local-networks?projectTag=proj&region=UZ5", got)
	}
	if want := `{"networkId":412,"ip":"192.168.20.15"}`; got.body != want {
		t.Errorf("body = %s, want %s", got.body, want)
	}
	if attached.ID != 9071 || attached.NetworkID == nil || *attached.NetworkID != 412 ||
		attached.IP == nil || *attached.IP != "192.168.20.15" || attached.MAC == nil || *attached.MAC != "BC:24:11:AA:BB:CC" {
		t.Errorf("interface not decoded: %+v", attached)
	}
	if !attached.RequiresRestart || attached.RequiresGuestConfiguration {
		t.Errorf("requiresRestart=%v requiresGuestConfiguration=%v, want true and false", attached.RequiresRestart, attached.RequiresGuestConfiguration)
	}
}

func TestAttachLocalNetwork_WithoutAnAddressTheBodyHasNone(t *testing.T) {
	server, seen := recordingServer(t, `{"success":true,"data":{"id":9071,"networkId":412,"ip":"192.168.20.2","primary":false,
		"requiresRestart":false,"requiresGuestConfiguration":true}}`)
	c := newTestClient(t, server)

	attached, err := c.AttachLocalNetwork(context.Background(), 118, AttachLocalNetworkRequest{NetworkID: 412}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := `{"networkId":412}`; (*seen)[0].body != want {
		t.Errorf("body = %s, want %s: an empty ip must not be sent, the panel would read it as an address", (*seen)[0].body, want)
	}
	if attached.RequiresRestart || !attached.RequiresGuestConfiguration {
		t.Errorf("requiresRestart=%v requiresGuestConfiguration=%v, want false and true", attached.RequiresRestart, attached.RequiresGuestConfiguration)
	}
}

func TestAttachLocalNetwork_RefusalsKeepTheirCodes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		code   int
	}{
		{"the VM is busy", 503, 961},
		{"the network is already attached", 409, 955},
		{"the address is taken", 409, 956},
		{"the interface limit is reached", 409, 957},
		{"the VM is in the wrong status", 409, 959},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newTestServer(tc.status, `{"success":false,"data":null,"errors":[{"code":`+strconv.Itoa(tc.code)+`,"message":"refused"}]}`)
			defer server.Close()
			c := newTestClient(t, server)

			_, err := c.AttachLocalNetwork(context.Background(), 118, AttachLocalNetworkRequest{NetworkID: 412}, nil)
			if !IsAPIError(err, tc.code) {
				t.Fatalf("err = %v, want API error %d", err, tc.code)
			}
			if got := IsRetryableTransient(err); got != (tc.status == 503) {
				t.Errorf("IsRetryableTransient = %v for HTTP %d: only the busy answer may be retried", got, tc.status)
			}
		})
	}
}

func TestDetachLocalNetwork(t *testing.T) {
	server, seen := recordingServer(t, `{"success":true,"data":null,"errors":null}`)
	c := newTestClient(t, server)

	if err := c.DetachLocalNetwork(context.Background(), 118, 9071, &RequestOpts{Region: "UZ5", ProjectTag: "proj"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := (*seen)[0]
	if got.method != http.MethodDelete || got.path != "/panel-main/api/v2/vms/118/local-networks/9071" || got.query != "projectTag=proj&region=UZ5" {
		t.Errorf("request = %+v, want a DELETE of /panel-main/api/v2/vms/118/local-networks/9071?projectTag=proj&region=UZ5", got)
	}
	if got.body != "" {
		t.Errorf("body = %q, want none", got.body)
	}
}

func TestDetachLocalNetwork_RefusalsKeepTheirCodes(t *testing.T) {
	for _, code := range []int{703, 958, 959, 627} {
		server := newTestServer(409, `{"success":false,"data":null,"errors":[{"code":`+strconv.Itoa(code)+`,"message":"refused"}]}`)
		c := newTestClient(t, server)

		err := c.DetachLocalNetwork(context.Background(), 118, 9071, nil)
		if !IsAPIError(err, code) {
			t.Errorf("err = %v, want API error %d", err, code)
		}
		server.Close()
	}
}

func TestLocalNetworkCalls_SendTheScopeOfTheirOptionsInTheHeadersToo(t *testing.T) {
	// The region and the project of a call are named in the query and in the headers. Options that
	// were not passed on to Do would leave the headers at the provider's defaults, and the call
	// would act in a project other than the one it asks about.
	var got http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":null,"errors":null}`))
	}))
	t.Cleanup(server.Close)
	c := newTestClient(t, server)
	opts := &RequestOpts{Region: "UZ5", ProjectTag: "proj"}

	for _, tc := range []struct {
		name string
		call func(ctx context.Context)
	}{
		{"list", func(ctx context.Context) { _, _ = c.GetVmLocalNetworks(ctx, 118, opts) }},
		{"attach", func(ctx context.Context) {
			_, _ = c.AttachLocalNetwork(ctx, 118, AttachLocalNetworkRequest{NetworkID: 412}, opts)
		}},
		{"detach", func(ctx context.Context) { _ = c.DetachLocalNetwork(ctx, 118, 9071, opts) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got = nil
			tc.call(context.Background())

			if got == nil {
				t.Fatal("no request reached the server")
			}
			if v := got.Get("X-Region"); v != "UZ5" {
				t.Errorf("X-Region = %q, want UZ5, not the provider's default", v)
			}
			if v := got.Get("X-Project-Tag"); v != "proj" {
				t.Errorf("X-Project-Tag = %q, want proj, not the provider's default", v)
			}
		})
	}
}

func TestScopedPath(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts *RequestOpts
		want string
	}{
		{"no options leave the path alone", nil, "/p"},
		{"empty options leave the path alone, with no bare question mark", &RequestOpts{}, "/p"},
		{"only the region", &RequestOpts{Region: "UZ5"}, "/p?region=UZ5"},
		{"only the project", &RequestOpts{ProjectTag: "proj"}, "/p?projectTag=proj"},
		{"both, in a stable order", &RequestOpts{Region: "UZ5", ProjectTag: "proj"}, "/p?projectTag=proj&region=UZ5"},
		{"values are escaped", &RequestOpts{ProjectTag: "a b&c=d/é"}, "/p?projectTag=a+b%26c%3Dd%2F%C3%A9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := scopedPath("/p", tc.opts); got != tc.want {
				t.Errorf("scopedPath = %q, want %q", got, tc.want)
			}
		})
	}
}
