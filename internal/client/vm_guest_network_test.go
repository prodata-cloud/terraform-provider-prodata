package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestGuestNetworkIdentityScopeAndExplicitRouteIntent(t *testing.T) {
	server, seen := recordingServer(t, `{"success":true,"data":{"supported":true,"configured":true,"driver":"netplan-v1","contract":"guest-network-operation-v1","recoverableSubmission":true,"verified":true,"cloudInitDisabled":false}}`)
	c := newTestClient(t, server)
	gw := "192.0.2.1"
	req := ConfigureGuestNetworkRequest{ExpectedMAC: "BC:24:11:AA:BB:CC", ExpectedIP: "192.0.2.5", ConfigureDefaultRoute: true, ExpectedGuestGateway: &gw}
	opts := &RequestOpts{Region: "TEST", ProjectTag: "chosen"}
	if _, err := c.GetVmGuestNetworkCapabilities(context.Background(), 7, opts); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetVmGuestNetworkState(context.Background(), 7, 91, req, opts); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ConfigureVmGuestNetwork(context.Background(), 7, 91, req, opts); err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 3 || (*seen)[0].method != http.MethodGet || !strings.HasSuffix((*seen)[0].path, "/7/guest-network-capabilities") {
		t.Fatalf("unexpected preflight requests: %+v", *seen)
	}
	query, err := url.ParseQuery((*seen)[1].query)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{"expectedMac": req.ExpectedMAC, "expectedIp": req.ExpectedIP, "configureDefaultRoute": "true", "expectedGuestGateway": gw, "region": "TEST", "projectTag": "chosen"} {
		if query.Get(key) != value {
			t.Errorf("query %s = %q, want %q", key, query.Get(key), value)
		}
	}
	var body ConfigureGuestNetworkRequest
	if err := json.Unmarshal([]byte((*seen)[2].body), &body); err != nil {
		t.Fatal(err)
	}
	if (*seen)[2].method != http.MethodPost || body.ExpectedMAC != req.ExpectedMAC || body.ExpectedIP != req.ExpectedIP || !body.ConfigureDefaultRoute || body.Gateway != nil || body.ExpectedGuestGateway == nil || *body.ExpectedGuestGateway != gw {
		t.Fatalf("unsafe configuration identity or route intent: %+v", body)
	}
}

func TestGuestNetworkIncompleteOrContradictoryResponsesFailClosed(t *testing.T) {
	for _, data := range []string{
		`null`, `{}`, `{"supported":true,"driver":"netplan-v1"}`,
		`{"supported":true,"configured":true,"driver":"unknown"}`,
		`{"supported":false,"configured":true,"driver":"netplan-v1","reason":"unsupported"}`,
		`{"supported":false,"configured":false,"driver":"netplan-v1"}`,
	} {
		t.Run(data, func(t *testing.T) {
			server, _ := recordingServer(t, `{"success":true,"data":`+data+`}`)
			c := newTestClient(t, server)
			if _, err := c.GetVmGuestNetworkCapabilities(context.Background(), 7, nil); err == nil {
				t.Fatal("incomplete or inconsistent preflight was accepted")
			}
			if _, err := c.ConfigureVmGuestNetwork(context.Background(), 7, 91, ConfigureGuestNetworkRequest{}, nil); err == nil {
				t.Fatal("incomplete or inconsistent mutation was accepted")
			}
		})
	}
}

func TestGuestNetworkPostRequiresObservedSuccess(t *testing.T) {
	for _, data := range []string{
		`{"supported":true,"configured":false,"driver":"netplan-v1"}`,
		`{"supported":false,"configured":false,"driver":"netplan-v1","reason":"FreeBSD requires manual configuration"}`,
	} {
		server, _ := recordingServer(t, `{"success":true,"data":`+data+`}`)
		c := newTestClient(t, server)
		if _, err := c.GetVmGuestNetworkCapabilities(context.Background(), 7, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := c.ConfigureVmGuestNetwork(context.Background(), 7, 91, ConfigureGuestNetworkRequest{}, nil); err == nil {
			t.Fatal("guest mutation was accepted without observed configuration")
		}
	}
}

func TestGuestNetworkPendingMutationHasKnownIdentityAndReleaseUsesDelete(t *testing.T) {
	response := `{"success":true,"data":{"supported":true,"configured":false,"driver":"netplan-v1","contract":"guest-network-operation-v1","recoverableSubmission":true,"operationUuid":"11111111-1111-4111-8111-111111111111","status":"UNVERIFIED","pending":true,"verified":false}}`
	server, seen := recordingServer(t, response)
	c := newTestClient(t, server)
	req := ConfigureGuestNetworkRequest{ExpectedMAC: "BC:24:11:AA:BB:CC", ExpectedIP: "192.0.2.5"}
	got, err := c.ConfigureVmGuestNetwork(context.Background(), 7, 91, req, nil)
	if err != nil || got.Pending == nil || !*got.Pending {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err = c.ReleaseVmGuestNetwork(context.Background(), 7, 91, req, nil); err != nil {
		t.Fatal(err)
	}
	if (*seen)[0].method != "POST" || (*seen)[1].method != "DELETE" {
		t.Fatalf("%+v", *seen)
	}
}
func TestGuestNetworkOldServerCannotAuthorizeMutation(t *testing.T) {
	server, _ := recordingServer(t, `{"success":true,"data":{"supported":true,"configured":true,"driver":"netplan-v1"}}`)
	c := newTestClient(t, server)
	if _, err := c.ConfigureVmGuestNetwork(context.Background(), 7, 91, ConfigureGuestNetworkRequest{}, nil); err == nil {
		t.Fatal("uncorrelated old guest mutation accepted")
	}
}
