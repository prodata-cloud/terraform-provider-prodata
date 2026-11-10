package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"net/http"
	"net/http/httptest"
	"strings"
	"terraform-provider-prodata/internal/client"
	"testing"
	"time"
)

func localIntentWire(status, operation string, exists bool) map[string]any {
	return map[string]any{"contract": client.LocalNetworkAttachmentContract, "id": lnaInterfaceID, "vmId": vmPanelVMID, "networkId": lnaNetworkID, "ip": lnaAddress, "mac": lnaMAC, "exists": exists, "status": status, "operation": operation, "canRelease": status == "NOT_SUBMITTED", "netIndex": 1, "pveFullName": "net1", "requiresRestart": false, "requiresGuestConfiguration": false}
}
func TestLocalInterfaceLostAckUsesCheckpointAndPendingReadDoesNotSubmit(t *testing.T) {
	status, operation, exists := "RESERVED", "ATTACH_LOCAL_NIC", true
	reserves, submits, attaches, detaches, power, legacy := 0, 0, 0, 0, 0, 0
	caps := true
	out := resource.CreateResponse{State: lnaNullState(t)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		ok := func(v any) { _ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": v}) }
		if strings.HasSuffix(q.URL.Path, "/capabilities") {
			if !caps {
				w.WriteHeader(503)
				return
			}
			ok(map[string]any{"contract": client.LocalNetworkAttachmentContract, "reserveBeforeSubmit": true, "recoverableSubmission": true, "strictInterfaceIdentity": true})
			return
		}
		if strings.HasSuffix(q.URL.Path, "/stop") || strings.HasSuffix(q.URL.Path, "/start") {
			power++
			w.WriteHeader(500)
			return
		}
		if strings.Contains(q.URL.Path, "/local-networks") {
			legacy++
			w.WriteHeader(500)
			return
		}
		switch {
		case strings.HasSuffix(q.URL.Path, "/reservations"):
			reserves++
			ok(localIntentWire(status, operation, exists))
		case strings.HasSuffix(q.URL.Path, "/submit"):
			var id int64
			if d := out.State.GetAttribute(context.Background(), path.Root("interface_id"), &id); d.HasError() || id != lnaInterfaceID {
				t.Errorf("submit preceded committed checkpoint: id=%d diag=%v", id, d)
			}
			submits++
			attaches++
			status = "SUCCESS"
			w.WriteHeader(502)
		case q.Method == "DELETE":
			if operation != "DETACH_LOCAL_NIC" {
				detaches++
			}
			operation = "DETACH_LOCAL_NIC"
			status = "UNVERIFIED"
			w.WriteHeader(502)
		default:
			ok(localIntentWire(status, operation, exists))
		}
	}))
	defer server.Close()
	c, err := client.New(client.Config{APIBaseURL: server.URL, APIKeyID: "fake", APISecretKey: "fake", Region: "TEST", ProjectTag: "proj"})
	if err != nil {
		t.Fatal(err)
	}
	r := &LocalNetworkAttachmentResource{client: c, restartPoll: time.Millisecond}
	r.Create(context.Background(), resource.CreateRequest{Plan: lnaCreatePlan(t, nil)}, &out)
	if out.Diagnostics.HasError() || reserves != 1 || submits != 1 || attaches != 1 {
		t.Fatalf("reserve=%d submit=%d attach=%d diag=%v", reserves, submits, attaches, out.Diagnostics)
	}
	status = "UNVERIFIED"
	read := resource.ReadResponse{State: out.State}
	r.Read(context.Background(), resource.ReadRequest{State: out.State}, &read)
	if read.Diagnostics.HasError() || read.Diagnostics.WarningsCount() == 0 || submits != 1 || read.State.Raw.IsNull() {
		t.Fatalf("pending read lost identity: %v", read.Diagnostics)
	}
	for i := 0; i < 2; i++ {
		deleted := resource.DeleteResponse{State: read.State}
		r.Delete(context.Background(), resource.DeleteRequest{State: read.State}, &deleted)
		if !deleted.Diagnostics.HasError() {
			t.Fatal("unknown physical detach was reported complete")
		}
	}
	if detaches != 1 || attaches != 1 || legacy != 0 || power != 0 {
		t.Fatalf("attach=%d detach=%d legacy=%d power=%d", attaches, detaches, legacy, power)
	}
	status = "DELETED"
	exists = false
	caps = false
	deleted := resource.DeleteResponse{State: read.State}
	r.Delete(context.Background(), resource.DeleteRequest{State: read.State}, &deleted)
	if deleted.Diagnostics.HasError() {
		t.Fatalf("positive tombstone could not drain disabled capability: %v", deleted.Diagnostics)
	}
}
func TestLocalInterfaceOldServerRefusesBeforeAnyReservationOrPower(t *testing.T) {
	mutations := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.Method != "GET" {
			mutations++
		}
		w.WriteHeader(404)
	}))
	defer server.Close()
	c, err := client.New(client.Config{APIBaseURL: server.URL, APIKeyID: "fake", APISecretKey: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	r := &LocalNetworkAttachmentResource{client: c}
	out := resource.CreateResponse{State: lnaNullState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: lnaCreatePlan(t, nil)}, &out)
	if !out.Diagnostics.HasError() || !out.State.Raw.IsNull() || mutations != 0 {
		t.Fatalf("mutations=%d diag=%v", mutations, out.Diagnostics)
	}
}
func TestKnownLocalInterfaceProxy404CannotFallBackAndForget(t *testing.T) {
	reads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) { reads++; w.WriteHeader(404) }))
	defer server.Close()
	c, err := client.New(client.Config{APIBaseURL: server.URL, APIKeyID: "fake", APISecretKey: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	state := lnaStored(t, nil)
	if d := state.SetAttribute(context.Background(), path.Root("status"), "QUEUED"); d.HasError() {
		t.Fatal(d)
	}
	r := &LocalNetworkAttachmentResource{client: c}
	out := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &out)
	if !out.Diagnostics.HasError() || out.State.Raw.IsNull() || reads != 1 {
		t.Fatalf("reads=%d diag=%v", reads, out.Diagnostics)
	}
}

func TestLocalInterfaceDeleteDomainErrorIsNotPhysicalAbsence(t *testing.T) {
	for _, code := range []int{633, 703, 601} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			mutations := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				if strings.HasSuffix(q.URL.Path, "/capabilities") {
					protectionReply(w, map[string]any{"contract": client.LocalNetworkAttachmentContract, "reserveBeforeSubmit": true, "recoverableSubmission": true, "strictInterfaceIdentity": true})
					return
				}
				if q.Method == "DELETE" {
					mutations++
					w.WriteHeader(403)
					fmt.Fprintf(w, `{"success":false,"errors":[{"code":%d,"message":"refused"}]}`, code)
					return
				}
				protectionReply(w, localIntentWire("SUCCESS", "ATTACH_LOCAL_NIC", true))
			}))
			defer server.Close()
			c, err := client.New(client.Config{APIBaseURL: server.URL, APIKeyID: "fake", APISecretKey: "fake"})
			if err != nil {
				t.Fatal(err)
			}
			r := &LocalNetworkAttachmentResource{client: c}
			state := lnaStored(t, nil)
			out := resource.DeleteResponse{State: state}
			r.Delete(context.Background(), resource.DeleteRequest{State: state}, &out)
			if !out.Diagnostics.HasError() || out.State.Raw.IsNull() || mutations != 1 {
				t.Fatalf("mutations=%d diag=%v", mutations, out.Diagnostics)
			}
		})
	}
}
