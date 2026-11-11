package resources

import (
	"context"
	"fmt"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"net/http"
	"net/http/httptest"
	"strings"
	"terraform-provider-prodata/internal/client"
	"testing"
	"time"
)

func TestVmReadAbsenceRequiresVmDomainProof(t *testing.T) {
	for _, code := range []int{0, 601, 703, 628, 736, 714} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				w.WriteHeader(404)
				if code > 0 {
					fmt.Fprintf(w, `{"success":false,"errors":[{"code":%d,"message":"fixture refusal"}]}`, code)
				} else {
					fmt.Fprint(w, "<html>route not found</html>")
				}
			}))
			defer srv.Close()
			c, err := client.New(client.Config{APIBaseURL: srv.URL, APIKeyID: "fake", APISecretKey: "fake"})
			if err != nil {
				t.Fatal(err)
			}
			planner := newVMPlanner(t)
			s := vmSchema(t)
			prior := tfsdk.State{Schema: s, Raw: planner.object(vmState())}
			out := resource.ReadResponse{State: prior}
			r := &VmResource{client: c}
			r.Read(context.Background(), resource.ReadRequest{State: prior}, &out)
			if out.State.Raw.IsNull() != (code == 601) || out.Diagnostics.HasError() == (code == 601) {
				t.Fatalf("code=%d state=%v diag=%v", code, out.State.Raw, out.Diagnostics)
			}
		})
	}
}
func TestKnownPendingVolumeCannotDisappearThroughProxy404Fallback(t *testing.T) {
	for _, knownStatus := range []bool{false, true} {
		t.Run(fmt.Sprint(knownStatus), func(t *testing.T) {
			reads := 0
			c := volumeMoveClient(t, func(w http.ResponseWriter, q *http.Request) {
				reads++
				if strings.Contains(q.URL.Path, "volume-attachments") {
					w.WriteHeader(404)
					return
				}
				protectionReply(w, []any{})
			})
			r := &VolumeAttachmentResource{client: c}
			inputs := attachmentInputs()
			inputs["attached_volume_id"] = int64(515)
			if knownStatus {
				inputs["status"] = "QUEUED"
			}
			plan, _ := protectionPlan(t, r, inputs)
			prior := tfsdk.State(plan)
			out := resource.ReadResponse{State: prior}
			r.Read(context.Background(), resource.ReadRequest{State: prior}, &out)
			if out.State.Raw.IsNull() || !out.Diagnostics.HasError() || knownStatus && reads != 1 {
				t.Fatalf("known=%v reads=%d diag=%v", knownStatus, reads, out.Diagnostics)
			}
		})
	}
}
func TestGatewayGuestReservationCheckpointPreservesPriorRouteOnPrewriteFailure(t *testing.T) {
	old := "192.0.2.1"
	c, p := newGatewayTestPanel(t, "RUNNING", &old)
	base := p.vm.extra
	guestStatus := "RESERVED"
	uuid := guestUUID
	p.vm.extra = func(w http.ResponseWriter, q *http.Request) bool {
		wire := func(status string) map[string]any { v := guestWire(status); v["operationUuid"] = uuid; return v }
		if strings.HasSuffix(q.URL.Path, "guest-network-capabilities") || strings.HasSuffix(q.URL.Path, "guest-configuration/dry-run") {
			p.vm.ok(w, wire("SUCCESS"))
			return true
		}
		if q.URL.Path == vmPanelBase+"/local-networks" {
			network := int64(42)
			ip, mac := "192.0.2.5", lnaMAC
			p.vm.ok(w, []client.VmLocalNetwork{{ID: 91, NetworkID: &network, IP: &ip, MAC: &mac}})
			return true
		}
		if strings.Contains(q.URL.Path, "guest-configuration/reservations/") && q.Method == "DELETE" {
			guestStatus = "NOT_SUBMITTED"
			p.vm.ok(w, wire(guestStatus))
			return true
		}
		if strings.HasSuffix(q.URL.Path, "guest-configuration/reservations") {
			if guestStatus == "NOT_SUBMITTED" {
				uuid = "22222222-2222-4222-8222-222222222222"
			}
			guestStatus = "RESERVED"
			p.vm.ok(w, wire(guestStatus))
			return true
		}
		if strings.HasSuffix(q.URL.Path, "guest-configuration") {
			if q.Method == "POST" {
				guestStatus = "SUCCESS"
			}
			p.vm.ok(w, wire(guestStatus))
			return true
		}
		return base(w, q)
	}
	p.failCheck = true
	r := &VmGatewayResource{client: c, poll: time.Millisecond}
	prior := gatewayState(t, old, old)
	for _, key := range []string{"configure_guest", "guest_configuration_applied"} {
		if d := prior.SetAttribute(context.Background(), path.Root(key), true); d.HasError() {
			t.Fatal(d)
		}
	}
	plan, _ := gatewayRaw(t, map[string]tftypes.Value{"gateway": vmStr("192.0.2.2"), "interface_id": vmNum(91), "owned_gateway": vmUnkS, "configure_guest": tftypes.NewValue(tftypes.Bool, true), "guest_configuration_applied": tftypes.NewValue(tftypes.Bool, tftypes.UnknownValue), "restart_pending": tftypes.NewValue(tftypes.Bool, tftypes.UnknownValue)})
	out := resource.UpdateResponse{State: prior}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: prior}, &out)
	var got VmGatewayResourceModel
	if d := out.State.Get(context.Background(), &got); d.HasError() {
		t.Fatal(d)
	}
	if !out.Diagnostics.HasError() || !out.State.Raw.IsFullyKnown() || got.OwnedGateway.ValueString() != old || got.Gateway.ValueString() != old || got.GuestOperationUUID.ValueString() != guestUUID {
		t.Fatalf("state=%+v diag=%v", got, out.Diagnostics)
	}
	p.failCheck = false
	deleted := resource.DeleteResponse{State: out.State}
	r.Delete(context.Background(), resource.DeleteRequest{State: out.State}, &deleted)
	if deleted.Diagnostics.HasError() || p.gateway != nil {
		t.Fatalf("gateway=%v diag=%v", p.gateway, deleted.Diagnostics)
	}
}
