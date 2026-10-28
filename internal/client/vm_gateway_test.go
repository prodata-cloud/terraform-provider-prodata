package client

import (
	"context"
	"net/http"
	"net/url"
	"testing"
)

func TestGetVmGateway(t *testing.T) {
	server, seen := recordingServer(t, `{"success":true,"data":{"interfaceId":9071,"networkId":412,"gateway":"192.0.2.1","present":true,"pending":false,"reconcileRequired":true,"otherGatewayActive":false,"requiresGuestConfiguration":true}}`)
	c := newTestClient(t, server)
	got, err := c.GetVmGateway(context.Background(), 118, 412, &RequestOpts{Region: "UZ5", ProjectTag: "proj"})
	if err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 1 || (*seen)[0].method != http.MethodGet || (*seen)[0].path != "/panel-main/api/v2/vms/118/gateway" {
		t.Fatalf("unexpected request: %+v", *seen)
	}
	query, err := url.ParseQuery((*seen)[0].query)
	if err != nil || query.Get("networkId") != "412" || query.Get("projectTag") != "proj" || query.Get("region") != "UZ5" {
		t.Fatalf("unexpected query: %q, %v", (*seen)[0].query, err)
	}
	if got.InterfaceID != 9071 || got.NetworkID != 412 || got.Gateway == nil || *got.Gateway != "192.0.2.1" || got.Pending || !got.ReconcileRequired || got.OtherGatewayActive || !got.RequiresGuestConfiguration {
		t.Fatalf("unexpected gateway: %+v", got)
	}
}

func TestGetVmGateway_AllowsNoGatewayOnTheSelectedInterface(t *testing.T) {
	server, _ := recordingServer(t, `{"success":true,"data":{"interfaceId":9071,"networkId":412,"gateway":null,"present":false,"pending":false,"reconcileRequired":false,"otherGatewayActive":true,"requiresGuestConfiguration":false}}`)
	got, err := newTestClient(t, server).GetVmGateway(context.Background(), 118, 412, nil)
	if err != nil || got.Gateway != nil || !got.OtherGatewayActive {
		t.Fatalf("got gateway=%+v, err=%v", got, err)
	}
}

func TestGetVmGateway_RefusesIncompleteOrMismatchedResponses(t *testing.T) {
	for _, body := range []string{
		`{"success":true,"data":{"interfaceId":9071,"networkId":412}}`,
		`{"success":true,"data":{"interfaceId":9071,"networkId":413,"gateway":null,"present":false,"pending":false,"reconcileRequired":false,"otherGatewayActive":false,"requiresGuestConfiguration":false}}`,
		`{"success":true,"data":{"interfaceId":9071,"networkId":412,"present":true,"pending":false,"reconcileRequired":false,"otherGatewayActive":false,"requiresGuestConfiguration":false}}`,
	} {
		server, _ := recordingServer(t, body)
		if _, err := newTestClient(t, server).GetVmGateway(context.Background(), 118, 412, nil); err == nil {
			t.Fatalf("accepted incomplete or mismatched reply %s", body)
		}
	}
}

func TestChangeVmGateway_SendsInterfaceSelectionAndConditionalClear(t *testing.T) {
	server, seen := recordingServer(t, `{"success":true,"data":{"interfaceId":9071,"networkId":412,"gateway":null,"changed":true,"previousGatewayOnAnotherInterface":false,"requiresGuestConfiguration":false}}`)
	expected := "192.0.2.1"
	got, err := newTestClient(t, server).ChangeVmGateway(context.Background(), 118,
		ChangeVmGatewayRequest{NetworkID: 412, ExpectedGateway: &expected, Clear: true}, true, &RequestOpts{Region: "UZ5"})
	if err != nil || !got.Changed || got.Gateway != nil {
		t.Fatalf("got %+v, err %v", got, err)
	}
	if len(*seen) != 1 || (*seen)[0].method != http.MethodPut || (*seen)[0].query != "region=UZ5&dryRun=true" ||
		(*seen)[0].body != `{"networkId":412,"expectedGateway":"192.0.2.1","clear":true}` {
		t.Fatalf("unexpected request: %+v", *seen)
	}
}

func TestChangeVmGateway_RejectsAnIncompleteOrMismatchedReply(t *testing.T) {
	gateway := "192.0.2.1"
	for _, body := range []string{
		`{"success":true,"data":{"interfaceId":9071,"networkId":412,"gateway":"192.0.2.1"}}`,
		`{"success":true,"data":{"interfaceId":9071,"networkId":412,"gateway":"192.0.2.2","changed":true,"previousGatewayOnAnotherInterface":false,"requiresGuestConfiguration":false}}`,
	} {
		server, _ := recordingServer(t, body)
		_, err := newTestClient(t, server).ChangeVmGateway(context.Background(), 118, ChangeVmGatewayRequest{NetworkID: 412, Gateway: &gateway}, false, nil)
		if err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}
