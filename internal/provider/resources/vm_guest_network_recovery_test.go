package resources

import (
	"context"
	"fmt"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"net/http"
	"strings"
	"terraform-provider-prodata/internal/client"
	"testing"
	"time"
)

const guestUUID = "11111111-1111-4111-8111-111111111111"

func guestWire(status string) map[string]any {
	return map[string]any{"contract": client.GuestNetworkContract, "recoverableSubmission": true, "recoverablePowerChildren": true, "operationUuid": guestUUID, "status": status, "pending": status == "RESERVED" || status == "UNVERIFIED", "supported": true, "configured": status == "SUCCESS", "driver": "netplan-v1", "verified": status == "SUCCESS", "cloudInitDisabled": false, "released": false}
}
func TestGuestLostPostAckOnlyObservesPersistedOperation(t *testing.T) {
	posts, reads := 0, 0
	c := protectionClient(t, func(w http.ResponseWriter, q *http.Request) {
		if strings.HasSuffix(q.URL.Path, "/guest-network-capabilities") {
			protectionReply(w, guestWire("SUCCESS"))
			return
		}
		if q.Method == "POST" {
			posts++
			w.WriteHeader(503)
			fmt.Fprint(w, `{"success":false,"error":627}`)
			return
		}
		reads++
		status := "UNVERIFIED"
		if reads > 1 {
			status = "SUCCESS"
		}
		protectionReply(w, guestWire(status))
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := applyGuestNetwork(ctx, c, 7, 91, client.ConfigureGuestNetworkRequest{ExpectedMAC: lnaMAC, ExpectedIP: lnaAddress}, nil, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if posts != 1 || reads != 2 {
		t.Fatalf("posts=%d reads=%d", posts, reads)
	}
}
func TestGuestPermanentPreflightRefusalIsNotPolled(t *testing.T) {
	for _, code := range []int{403, 633, 703} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			calls := 0
			c := protectionClient(t, func(w http.ResponseWriter, q *http.Request) {
				calls++
				w.WriteHeader(403)
				fmt.Fprintf(w, `{"success":false,"errors":[{"code":%d,"message":"permanent refusal"}]}`, code)
			})
			err := applyGuestNetwork(context.Background(), c, 7, 91, client.ConfigureGuestNetworkRequest{}, nil, time.Millisecond)
			if err == nil || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}
func withGatewayGuestReads(t *testing.T, p *gatewayTestPanel, result map[string]any, fail bool) {
	t.Helper()
	base := p.vm.extra
	p.vm.extra = func(w http.ResponseWriter, q *http.Request) bool {
		if q.URL.Path == vmPanelBase+"/local-networks" {
			network := int64(42)
			ip, mac := "192.0.2.5", lnaMAC
			p.vm.ok(w, []client.VmLocalNetwork{{ID: 91, NetworkID: &network, IP: &ip, MAC: &mac}})
			return true
		}
		if strings.Contains(q.URL.Path, "guest-configuration") || strings.HasSuffix(q.URL.Path, "guest-network-capabilities") {
			if fail {
				p.vm.fail(w, 503, 627)
			} else {
				p.vm.ok(w, result)
			}
			return true
		}
		return base(w, q)
	}
}
func TestGatewayUnverifiedGuestReadPreservesLastVerifiedState(t *testing.T) {
	for _, mode := range []string{"stopped", "busy", "read_error"} {
		t.Run(mode, func(t *testing.T) {
			gw := "192.0.2.1"
			c, p := newGatewayTestPanel(t, "RUNNING", &gw)
			wire := guestWire("UNVERIFIED")
			wire["pending"] = false
			wire["reason"] = "unverified guest observation"
			if mode == "stopped" {
				wire["supported"] = false
			}
			withGatewayGuestReads(t, p, wire, mode == "read_error")
			state := gatewayState(t, gw, gw)
			for _, key := range []string{"configure_guest", "guest_configuration_applied"} {
				if d := state.SetAttribute(context.Background(), path.Root(key), true); d.HasError() {
					t.Fatal(d)
				}
			}
			out := resource.ReadResponse{State: state}
			r := &VmGatewayResource{client: c}
			r.Read(context.Background(), resource.ReadRequest{State: state}, &out)
			var got VmGatewayResourceModel
			if d := out.State.Get(context.Background(), &got); d.HasError() {
				t.Fatal(d)
			}
			if out.Diagnostics.HasError() || out.Diagnostics.WarningsCount() == 0 || !got.GuestConfigurationApplied.ValueBool() {
				t.Fatalf("state=%+v diagnostics=%v", got, out.Diagnostics)
			}
		})
	}
}
func TestGatewayTargetDryRunRefusalPreventsPowerAndGatewayMutation(t *testing.T) {
	c, p := newGatewayTestPanel(t, "RUNNING", nil)
	base := p.vm.extra
	p.vm.extra = func(w http.ResponseWriter, q *http.Request) bool {
		if strings.HasSuffix(q.URL.Path, "guest-network-capabilities") {
			p.vm.ok(w, guestWire("SUCCESS"))
			return true
		}
		if strings.HasSuffix(q.URL.Path, "guest-configuration/dry-run") {
			wire := guestWire("UNVERIFIED")
			wire["pending"] = false
			wire["supported"] = false
			wire["reason"] = "additional custom Netplan file"
			p.vm.ok(w, wire)
			return true
		}
		if q.URL.Path == vmPanelBase+"/local-networks" {
			network := int64(42)
			ip, mac := "192.0.2.5", lnaMAC
			p.vm.ok(w, []client.VmLocalNetwork{{ID: 91, NetworkID: &network, IP: &ip, MAC: &mac}})
			return true
		}
		return base(w, q)
	}
	r := &VmGatewayResource{client: c, poll: time.Millisecond}
	plan, empty := gatewayRaw(t, map[string]tftypes.Value{"configure_guest": tftypes.NewValue(tftypes.Bool, true)})
	out := resource.CreateResponse{State: empty}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &out)
	if !out.Diagnostics.HasError() || len(p.requests) != 0 {
		t.Fatalf("gateway=%v diag=%v", p.requests, out.Diagnostics)
	}
	for _, q := range p.vm.requests {
		if strings.Contains(q, "/stop") || strings.Contains(q, "/start") {
			t.Fatalf("power before dry-run: %s", q)
		}
	}
}
