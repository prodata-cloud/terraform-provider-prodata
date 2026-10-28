package client

import (
	"context"
	"net/http"
	"testing"
)

func TestGetVmDNS(t *testing.T) {
	server, seen := recordingServer(t, `{"success":true,"data":{"nameserver":"192.0.2.53 198.51.100.53","configured":true,"pending":true,"reconcileRequired":false,"requiresGuestConfiguration":true}}`)
	c := newTestClient(t, server)

	dns, err := c.GetVmDNS(context.Background(), 118, &RequestOpts{Region: "UZ5", ProjectTag: "proj"})
	if err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 1 || (*seen)[0].method != http.MethodGet || (*seen)[0].path != "/panel-main/api/v2/vms/118/dns" || (*seen)[0].query != "projectTag=proj&region=UZ5" {
		t.Fatalf("unexpected request: %+v", *seen)
	}
	if dns.Nameserver == nil || *dns.Nameserver != "192.0.2.53 198.51.100.53" || !dns.Pending || dns.ReconcileRequired || !dns.RequiresGuestConfiguration {
		t.Fatalf("unexpected DNS: %+v", dns)
	}
}

func TestGetVmDNS_AllowsAbsentNameserver(t *testing.T) {
	server, _ := recordingServer(t, `{"success":true,"data":{"nameserver":null,"configured":false,"pending":false,"reconcileRequired":false,"requiresGuestConfiguration":false}}`)
	dns, err := newTestClient(t, server).GetVmDNS(context.Background(), 118, nil)
	if err != nil || dns.Nameserver != nil {
		t.Fatalf("got dns=%+v, err=%v; want a successful read of an unset nameserver", dns, err)
	}
}

func TestGetVmDNS_RefusesIncompleteResponse(t *testing.T) {
	server, _ := recordingServer(t, `{"success":true,"data":{"nameserver":"192.0.2.53"}}`)
	if _, err := newTestClient(t, server).GetVmDNS(context.Background(), 118, nil); err == nil {
		t.Fatal("an incomplete response must not be interpreted as no pending change")
	}
}

func TestGetVmDNS_RefusesMissingConfiguredNameserver(t *testing.T) {
	server, _ := recordingServer(t, `{"success":true,"data":{"configured":true,"pending":false,"reconcileRequired":false,"requiresGuestConfiguration":false}}`)
	if _, err := newTestClient(t, server).GetVmDNS(context.Background(), 118, nil); err == nil {
		t.Fatal("configured DNS without its value must not be interpreted as unset")
	}
}

func TestChangeVmDNS_UsesScopedPreflightAndValidatesReply(t *testing.T) {
	server, seen := recordingServer(t, `{"success":true,"data":{"nameserver":"192.0.2.53","changed":true,"requiresGuestConfiguration":false}}`)
	got, err := newTestClient(t, server).ChangeVmDNS(context.Background(), 118, "192.0.2.53", true, &RequestOpts{Region: "UZ5", ProjectTag: "proj"})
	if err != nil || !got.Changed {
		t.Fatalf("got %+v, err %v", got, err)
	}
	if len(*seen) != 1 || (*seen)[0].method != http.MethodPut || (*seen)[0].path != "/panel-main/api/v2/vms/118/dns" ||
		(*seen)[0].query != "projectTag=proj&region=UZ5&dryRun=true" || (*seen)[0].body != `{"nameserver":"192.0.2.53"}` {
		t.Fatalf("unexpected request: %+v", *seen)
	}
}

func TestChangeVmDNS_RejectsIncompleteOrMismatchedReply(t *testing.T) {
	for _, body := range []string{
		`{"success":true,"data":{"nameserver":"192.0.2.53"}}`,
		`{"success":true,"data":{"nameserver":"192.0.2.54","changed":false,"requiresGuestConfiguration":false}}`,
	} {
		server, _ := recordingServer(t, body)
		if _, err := newTestClient(t, server).ChangeVmDNS(context.Background(), 118, "192.0.2.53", false, nil); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}
