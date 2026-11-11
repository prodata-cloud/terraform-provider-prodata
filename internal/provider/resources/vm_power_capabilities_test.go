package resources

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"terraform-provider-prodata/internal/client"
)

func TestGuestNewActionsRequireRecoverablePowerChildren(t *testing.T) {
	for _, mode := range []string{"missing", "false"} {
		t.Run(mode, func(t *testing.T) {
			mutations := 0
			c := protectionClient(t, func(w http.ResponseWriter, q *http.Request) {
				wire := guestWire("SUCCESS")
				if mode == "missing" {
					delete(wire, "recoverablePowerChildren")
				} else {
					wire["recoverablePowerChildren"] = false
				}
				if q.Method != http.MethodGet {
					mutations++
				}
				protectionReply(w, wire)
			})
			err := applyGuestNetwork(context.Background(), c, 7, 91, client.ConfigureGuestNetworkRequest{}, nil, time.Millisecond)
			if err == nil || mutations != 0 {
				t.Fatalf("new guest action passed incomplete power support: mutations=%d err=%v", mutations, err)
			}
		})
	}
}

func TestRestartRefusedPowerCapabilityPreventsStopAndHardwareHook(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	p.broken["power-capabilities"] = [2]int{503, 627}
	h := &hookRecorder{p: p}
	_, err := p.restartWithHook(t, c, generousTime, h)
	if err == nil || h.calls != 0 || len(p.posts()) != 0 || p.current() != "RUNNING" {
		t.Fatalf("unavailable power capability changed VM: hook=%d posts=%v err=%v", h.calls, p.posts(), err)
	}
}

func TestResizeRefusedPowerCapabilityPreventsStop(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	p.broken["power-capabilities"] = [2]int{503, 627}
	r := &VmResource{client: c, poll: time.Millisecond}
	_, err := r.stopIfRunning(context.Background(), 7, nil)
	if err == nil || len(p.posts()) != 0 || p.current() != "RUNNING" {
		t.Fatalf("unavailable power capability stopped VM: posts=%v err=%v", p.posts(), err)
	}
}

func TestNicRebootRefusedPowerCapabilityPreventsPhysicalSubmit(t *testing.T) {
	c, p := newNICPanel(t, "RUNNING")
	p.vp.broken["power-capabilities"] = [2]int{503, 627}
	out := lnaCreate(t, c, lnaCreatePlan(t, map[string]tftypes.Value{"reboot_after_attach": lnaFlag("true")}))
	p.mu.Lock()
	phase := p.protocol[lnaInterfaceID]
	p.mu.Unlock()
	if !out.Diagnostics.HasError() || phase != "RESERVED" || len(p.vp.posts()) != 0 {
		t.Fatalf("NIC physically submitted without durable restart: phase=%s power=%v diag=%v", phase, p.vp.posts(), out.Diagnostics)
	}
}

func TestPublicIPRebootRefusedPowerCapabilityPreventsAttach(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	p.broken["power-capabilities"] = [2]int{503, 627}
	out := pipCreate(t, c, pipCreatePlan(t, "true", ""))
	if !out.Diagnostics.HasError() || len(p.posts()) != 0 {
		t.Fatalf("public IP attached without durable restart: posts=%v diag=%v", p.posts(), out.Diagnostics)
	}
}

func TestGuestKnownProofAndReleaseDoNotRequireNewPowerCapability(t *testing.T) {
	deletes := 0
	c := protectionClient(t, func(w http.ResponseWriter, q *http.Request) {
		wire := guestWire("SUCCESS")
		delete(wire, "recoverablePowerChildren")
		if q.Method == http.MethodDelete {
			deletes++
			wire["released"] = true
		}
		protectionReply(w, wire)
	})
	request := client.ConfigureGuestNetworkRequest{}
	state, err := c.GetVmGuestNetworkState(context.Background(), 7, 91, request, nil)
	if err != nil || !confirmedGuestNetwork(state) {
		t.Fatalf("known verified proof was gated: state=%+v err=%v", state, err)
	}
	if err := releaseGuestNetwork(context.Background(), c, 7, 91, request, nil, time.Millisecond); err != nil || deletes != 1 {
		t.Fatalf("known guest release was gated: deletes=%d err=%v", deletes, err)
	}
}

func TestStoppedVmWithoutRestartDoesNotProbePowerCapability(t *testing.T) {
	c, p := newVMPanel(t, "STOPPED")
	p.broken["power-capabilities"] = [2]int{503, 627}
	if cycled, err := p.restartWithin(t, c, time.Second); err != nil || cycled {
		t.Fatalf("stopped VM required restart support: cycled=%t err=%v", cycled, err)
	}
	for _, q := range p.requests {
		if strings.Contains(q, "power-operations") {
			t.Fatalf("stopped VM probed an unneeded capability: %s", q)
		}
	}
}

func TestKnownPendingStartDrainsWithoutNewPowerAdmission(t *testing.T) {
	c, p := newVMPanel(t, "STOPPED")
	p.broken["power-capabilities"] = [2]int{503, 627}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := resumePendingVM(ctx, c, 7, nil, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, p.posts(), []string{"start"})
	for _, q := range p.requests {
		if strings.Contains(q, "power-operations") {
			t.Fatalf("known pending start required new admission: %s", q)
		}
	}
}

func TestNicWithoutRestartDoesNotRequirePowerCapability(t *testing.T) {
	for _, mode := range []struct{ vm, reboot string }{{"RUNNING", "false"}, {"STOPPED", "true"}} {
		t.Run(mode.vm+"/"+mode.reboot, func(t *testing.T) {
			c, p := newNICPanel(t, mode.vm)
			p.vp.broken["power-capabilities"] = [2]int{503, 627}
			out := lnaCreate(t, c, lnaCreatePlan(t, map[string]tftypes.Value{"reboot_after_attach": lnaFlag(mode.reboot)}))
			if out.Diagnostics.HasError() {
				t.Fatal(out.Diagnostics)
			}
			for _, q := range p.vp.requests {
				if strings.Contains(q, "power-operations") {
					t.Fatalf("no-restart NIC required power support: %s", q)
				}
			}
		})
	}
}

func TestGatewayRouteNoopDoesNotRequirePowerCapability(t *testing.T) {
	gateway := "192.0.2.1"
	c, p := newGatewayTestPanel(t, "RUNNING", &gateway)
	p.vm.broken["power-capabilities"] = [2]int{503, 627}
	plan, empty := gatewayRaw(t, nil)
	out := resource.CreateResponse{State: empty}
	r := &VmGatewayResource{client: c, poll: time.Millisecond}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &out)
	if out.Diagnostics.HasError() {
		t.Fatal(out.Diagnostics)
	}
	for _, q := range p.vm.requests {
		if strings.Contains(q, "power-operations") || strings.Contains(q, "/stop") || strings.Contains(q, "/start") {
			t.Fatalf("route noop unexpectedly required power: %s", q)
		}
	}
}

func TestExactOwnedGuestRouteCleanupDrainsWithoutNewPowerAdmission(t *testing.T) {
	posts := 0
	c := protectionClient(t, func(w http.ResponseWriter, q *http.Request) {
		wire := guestWire("SUCCESS")
		wire["recoverablePowerChildren"] = false
		if q.Method == http.MethodPost {
			posts++
		}
		protectionReply(w, wire)
	})
	owned := "192.0.2.1"
	request := client.ConfigureGuestNetworkRequest{ConfigureDefaultRoute: true, ExpectedGuestGateway: &owned}
	if err := applyGuestNetwork(context.Background(), c, 7, 91, request, nil, time.Millisecond); err != nil || posts != 1 {
		t.Fatalf("owned cleanup was blocked: posts=%d err=%v", posts, err)
	}
	request.ExpectedGuestGateway = nil
	if err := applyGuestNetwork(context.Background(), c, 7, 91, request, nil, time.Millisecond); err == nil || posts != 1 {
		t.Fatalf("unowned new route mutation bypassed admission: posts=%d err=%v", posts, err)
	}
}
