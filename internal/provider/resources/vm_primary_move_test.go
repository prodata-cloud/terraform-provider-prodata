package resources

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// The documentation of prodata_vm quotes both numbers ("the default is 30m", "refused when shorter
// than 20m"), and a plan that sets no timeouts.update is judged by the default: it has to be enough
// for a move, or every such plan would be refused.
func TestUpdateTimeouts(t *testing.T) {
	def, least := vmDefaultUpdateTime, vmMoveMinTimeout
	if def != 30*time.Minute {
		t.Errorf("vmDefaultUpdateTime = %s, want 30m: docs/resources/vm.md says so", def)
	}
	if least != 20*time.Minute {
		t.Errorf("vmMoveMinTimeout = %s, want 20m: docs/resources/vm.md says so", least)
	}
	if def < least {
		t.Errorf("the default update timeout (%s) is shorter than a move needs (%s)", def, least)
	}
}

// privateIPFromState is called on its own: what it does to the plan value of private_ip, for each
// way the plan, the state and the configuration can differ.
func TestPrivateIPFromState(t *testing.T) {
	p := newVMPlanner(t)
	s := vmSchema(t)
	stateOf := func(v vmValues) tfsdk.State { return tfsdk.State{Schema: s, Raw: p.object(v)} }
	planOf := func(v vmValues) tfsdk.Plan { return tfsdk.Plan{Schema: s, Raw: p.object(v)} }
	nothing := tftypes.NewValue(p.typ, nil)

	null, unknown := types.StringNull(), types.StringUnknown()
	address, other := types.StringValue("192.168.0.5"), types.StringValue("192.168.0.9")
	// What a plan of a rename looks like for private_ip: left unknown, the way the framework marks a
	// computed attribute whose configuration is null.
	renamed := vmState().with("name", vmStr("web-2")).with("private_ip", vmUnkS)

	cases := []struct {
		name    string
		state   tfsdk.State
		plan    tfsdk.Plan
		config  types.String // private_ip in the configuration
		planned types.String // private_ip in the plan when the modifier is called
		inState types.String // private_ip in the state
		want    types.String
	}{
		{"the VM stays in its network: the address of the state", stateOf(vmState()), planOf(renamed),
			null, unknown, address, address},
		{"the state has no address: left for apply", stateOf(vmState().with("private_ip", vmNullS)), planOf(renamed),
			null, unknown, null, unknown},
		{"the network changes: left for apply", stateOf(vmState()),
			planOf(renamed.with("local_network_id", vmNum(vmNetNew))), null, unknown, address, unknown},
		{"the network is not known yet: left for apply", stateOf(vmState()),
			planOf(renamed.with("local_network_id", vmUnkN)), null, unknown, address, unknown},
		{"an address in the configuration is kept", stateOf(vmState()),
			planOf(renamed.with("private_ip", vmStr("192.168.0.9"))), other, other, address, other},
		{"an address that is known already is kept", stateOf(vmState()),
			planOf(renamed.with("private_ip", vmStr("192.168.0.9"))), null, other, address, other},
		{"creating", tfsdk.State{Schema: s, Raw: nothing}, planOf(renamed), null, unknown, null, unknown},
		{"destroying", stateOf(vmState()), tfsdk.Plan{Schema: s, Raw: nothing}, null, unknown, address, unknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := planmodifier.StringRequest{
				Path:        path.Root("private_ip"),
				State:       tc.state,
				Plan:        tc.plan,
				ConfigValue: tc.config,
				PlanValue:   tc.planned,
				StateValue:  tc.inState,
			}
			resp := &planmodifier.StringResponse{PlanValue: tc.planned}

			privateIPFromState().PlanModifyString(context.Background(), req, resp)

			if resp.Diagnostics.HasError() {
				t.Fatalf("diagnostics: %v", resp.Diagnostics)
			}
			if !resp.PlanValue.Equal(tc.want) {
				t.Errorf("private_ip is planned as %s, want %s", resp.PlanValue, tc.want)
			}
		})
	}
}

// movePrimaryNetwork is called on its own, on a VM that is stopped, with the forecast of a move that
// is due. Nothing else of the update is in the way.
func newDirectMove(t *testing.T) (*updateHarness, *VmResourceModel, *client.MovedPrimaryLocalNetwork) {
	t.Helper()
	h := newUpdateHarness(t, "STOPPED")
	plan := &VmResourceModel{LocalNetworkID: types.Int64Value(vmNetNew)}
	forecast := &client.MovedPrimaryLocalNetwork{NetworkID: vmNetNew, PendingRecovery: true, RequiresGuestConfiguration: true}
	return h, plan, forecast
}

func (h *updateHarness) move(plan *VmResourceModel, forecast *client.MovedPrimaryLocalNetwork) (*client.MovedPrimaryLocalNetwork, error) {
	h.t.Helper()
	return h.resource.movePrimaryNetwork(context.Background(), vmPanelVMID, plan, &client.RequestOpts{}, forecast)
}

// The panel's 627 after a move that went through: the answer is what the panel says of the VM when
// it is asked again, with the move marked as made. The forecast was made before the VM was stopped
// and is not the VM: a VM with an earlier move that was never settled has none (no address, and no
// need for a guest configured whatever the VM is), and the address forecast may have been taken by
// another VM since.
func TestMovePrimaryNetwork_A627AfterAMoveThatWentThroughIsAnsweredByTheVM(t *testing.T) {
	text := func(s string) *string { return &s }
	previous := vmNetOld
	cases := []struct {
		name        string
		noCloudInit bool
		realIP      string
		forecast    client.MovedPrimaryLocalNetwork
		wantGuest   bool
		wantIP      string
	}{
		{"no forecast of an earlier move that was never settled, a guest without cloud-init", true, "",
			client.MovedPrimaryLocalNetwork{PendingRecovery: true, PreviousNetworkID: &previous}, true, vmNetNewIP},
		{"a forecast that says the guest needs it, a guest with cloud-init", false, "",
			client.MovedPrimaryLocalNetwork{RequiresGuestConfiguration: true, PendingRecovery: true, PreviousNetworkID: &previous}, false, vmNetNewIP},
		{"an address that is not the one forecast", true, "192.168.20.77",
			client.MovedPrimaryLocalNetwork{IP: text(vmNetNewIP), PreviousNetworkID: &previous}, true, "192.168.20.77"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, plan, _ := newDirectMove(t)
			forecast := tc.forecast
			forecast.NetworkID = vmNetNew
			h.panel.scriptMoves(nil, []moveReply{{fail: &answer{http.StatusInternalServerError, 627}, applyFirst: true}})
			if tc.noCloudInit {
				h.panel.setNoCloudInit()
			}
			if tc.realIP != "" {
				h.panel.setRealIP(tc.realIP)
			}

			moved, err := h.move(plan, &forecast)

			if err != nil {
				t.Fatalf("movePrimaryNetwork: %v: the VM is in the new network", err)
			}
			if !moved.Changed || moved.NetworkID != vmNetNew {
				t.Errorf("the answer is %+v, want a move that was made to network %d", moved, vmNetNew)
			}
			if moved.RequiresGuestConfiguration != tc.wantGuest {
				t.Errorf("RequiresGuestConfiguration = %v, want %v: it is the VM that decides, not the forecast", moved.RequiresGuestConfiguration, tc.wantGuest)
			}
			if moved.IP == nil || *moved.IP != tc.wantIP {
				t.Errorf("IP = %v, want %s: the address the VM has", moved.IP, tc.wantIP)
			}
			if moved.PreviousNetworkID == nil || *moved.PreviousNetworkID != previous {
				t.Errorf("PreviousNetworkID = %v, want %d: the network the VM left", moved.PreviousNetworkID, previous)
			}
			if forecast.Changed {
				t.Errorf("the forecast was changed in place")
			}
			wantCalls(t, h.panel.calls(), []string{"move", "move"})
		})
	}
}

// When the panel cannot be asked again what the move did, the answer is made of what is known: the
// address the VM has, and a guest that has to be configured when the forecast says so or says nothing
// (pendingRecovery): a warning that was not needed costs less than one that was left out.
func TestMovePrimaryNetwork_A627AndNoAnswerWhenAskedAgainFallsBackOnTheVM(t *testing.T) {
	text := func(s string) *string { return &s }
	cases := []struct {
		name      string
		forecast  client.MovedPrimaryLocalNetwork
		wantGuest bool
	}{
		{"the forecast says the guest needs it", client.MovedPrimaryLocalNetwork{RequiresGuestConfiguration: true}, true},
		{"the forecast says nothing", client.MovedPrimaryLocalNetwork{PendingRecovery: true}, true},
		{"the forecast says the guest does not", client.MovedPrimaryLocalNetwork{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, plan, _ := newDirectMove(t)
			forecast := tc.forecast
			forecast.NetworkID, forecast.IP = vmNetNew, text(vmNetNewIP)
			h.panel.setRealIP("192.168.20.77")
			h.panel.scriptMoves(nil, []moveReply{
				{fail: &answer{http.StatusInternalServerError, 627}, applyFirst: true},
				moveFails(http.StatusForbidden, 714),
			})

			moved, err := h.move(plan, &forecast)

			if err != nil {
				t.Fatalf("movePrimaryNetwork: %v: the VM is in the new network", err)
			}
			if !moved.Changed || moved.RequiresGuestConfiguration != tc.wantGuest {
				t.Errorf("the answer is %+v, want a move that was made, and RequiresGuestConfiguration = %v", moved, tc.wantGuest)
			}
			if moved.IP == nil || *moved.IP != "192.168.20.77" {
				t.Errorf("IP = %v, want 192.168.20.77: the address the VM has, not the one forecast", moved.IP)
			}
			wantCalls(t, h.panel.calls(), []string{"move", "move"})
		})
	}
}

// The question settledMove asks again after a 627 is bounded: the VM is stopped meanwhile, and a panel
// that does not answer it must not hold the VM off for the rest of the time the move has. The answer
// is then made of what is known, as when the question fails.
func TestMovePrimaryNetwork_AQuestionThatIsNotAnsweredIsGivenUpOn(t *testing.T) {
	text := func(s string) *string { return &s }
	h, plan, forecast := newDirectMove(t)
	forecast.IP = text(vmNetNewIP)
	h.resource.settledAsk = 200 * time.Millisecond
	h.panel.setRealIP("192.168.20.77")
	h.panel.scriptMoves(nil, []moveReply{{fail: &answer{http.StatusInternalServerError, 627}, applyFirst: true}})
	// The second move is the question. The panel holds it until the client gives up on it, or for
	// longer than the bound could ever be: then the answer would come, and the test says it did not
	// have to be waited for.
	var asked int
	var cutOff bool
	h.panel.onMove = func(r *http.Request) {
		asked++
		if asked < 2 {
			return
		}
		select {
		case <-r.Context().Done():
			cutOff = true
		case <-time.After(5 * time.Second):
		}
	}

	started := time.Now()
	moved, err := h.move(plan, forecast)
	took := time.Since(started)
	calls := h.panel.calls() // the panel's lock: the question has left the handler by now

	if err != nil {
		t.Fatalf("movePrimaryNetwork: %v: the VM is in the new network", err)
	}
	if !cutOff || took > 3*time.Second {
		t.Errorf("the question was given up on = %v after %s, want it cut off by the client within the bound (200ms)", cutOff, took)
	}
	if !moved.Changed || !moved.RequiresGuestConfiguration {
		t.Errorf("the answer is %+v, want a move that was made, and the guest to be configured as the forecast said", moved)
	}
	if moved.IP == nil || *moved.IP != "192.168.20.77" {
		t.Errorf("IP = %v, want 192.168.20.77: the address the VM has, not the one forecast", moved.IP)
	}
	wantCalls(t, calls, []string{"move", "move"})
}

// The question ends with the context of the update: a user who interrupts while it is asked must not
// find the VM held off for the time the question is allowed, since the cycle starts the VM again only
// when the hook returns.
func TestMovePrimaryNetwork_AnInterruptionEndsTheQuestionAtOnce(t *testing.T) {
	text := func(s string) *string { return &s }
	h, plan, forecast := newDirectMove(t)
	forecast.IP = text(vmNetNewIP)
	h.resource.settledAsk = 3 * time.Second
	h.panel.setRealIP("192.168.20.77")
	h.panel.scriptMoves(nil, []moveReply{{fail: &answer{http.StatusInternalServerError, 627}, applyFirst: true}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The second move is the question. The user interrupts as it arrives, and the panel does not answer it.
	var asked int
	h.panel.onMove = func(r *http.Request) {
		asked++
		if asked < 2 {
			return
		}
		cancel()
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}

	started := time.Now()
	moved, err := h.resource.movePrimaryNetwork(ctx, vmPanelVMID, plan, &client.RequestOpts{}, forecast)
	took := time.Since(started)
	calls := h.panel.calls() // the panel's lock: the question has left the handler by now

	if err != nil {
		t.Fatalf("movePrimaryNetwork: %v: the VM is in the new network", err)
	}
	if took > time.Second {
		t.Errorf("the question took %s after the interruption, want it cut off at once (the bound is 3s)", took)
	}
	if !moved.Changed || !moved.RequiresGuestConfiguration {
		t.Errorf("the answer is %+v, want a move that was made, and the guest to be configured as the forecast said", moved)
	}
	wantCalls(t, calls, []string{"move", "move"})
}

// The refusal of the move stays in the error, for whoever looks at its code, whether the VM could not
// be read afterwards or was found in the old network.
func TestMovePrimaryNetwork_TheErrorKeepsThe627OfTheMove(t *testing.T) {
	cases := []struct {
		name  string
		setup func(h *updateHarness)
		want  string
	}{
		{"the VM cannot be read", func(h *updateHarness) {
			h.panel.failReadsLike(true)
			h.panel.readAnswer = answer{http.StatusForbidden, 714}
		}, "reading the VM to find out whether the move happened failed too"},
		{"the VM is still in its network", func(h *updateHarness) {}, "the VM is still in network 10"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, plan, forecast := newDirectMove(t)
			h.panel.scriptMoves(nil, []moveReply{moveFails(http.StatusInternalServerError, 627)})
			tc.setup(h)

			_, err := h.move(plan, forecast)

			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want one with %q", err, tc.want)
			}
			if !client.IsAPIError(err, client.PanelCodePlatformError) {
				t.Errorf("the error does not hold the 627 of the move: %v", err)
			}
			if client.IsAPIError(err, 714) {
				t.Errorf("the error holds the answer of the read, not the one of the move: %v", err)
			}
		})
	}
}

// 961 and 627 in one answer is an answer that does not say whether the move happened: the move is
// asked again, and when there is no time for that, its outcome is unknown. The VM is not read to
// find out, the way it is after a plain 627.
func TestMovePrimaryNetwork_ABusyAnswerThatNamesA627TooIsNotSettledByReading(t *testing.T) {
	h, plan, forecast := newDirectMove(t)
	h.resource.moveOptions = []client.MoveOption{client.WithMovePause(func(int) time.Duration { return time.Hour })}
	h.panel.scriptMoves(nil, []moveReply{{fail: &answer{http.StatusServiceUnavailable, 961}, alsoCode: 627}})

	_, err := h.move(plan, forecast)

	if !client.IsMoveOutcomeUnknown(err) {
		t.Fatalf("error = %v, want the outcome to be unknown", err)
	}
	if got := h.panel.statusReads(); got != 0 {
		t.Errorf("the VM was read %d time(s), want none", got)
	}
}

func TestGuestConfigurationWarning(t *testing.T) {
	text := func(s string) *string { return &s }
	const head = "The VM has no cloud-init, so nothing configured the new network inside the guest."
	const tail = " inside the VM yourself; until then it has no network."

	cases := []struct {
		name  string
		moved client.MovedPrimaryLocalNetwork
		want  string
	}{
		{"all three are known",
			client.MovedPrimaryLocalNetwork{IP: text("192.168.20.15"), NetworkName: text("net-b"), Gateway: text("192.168.20.1")},
			head + ` Set the address 192.168.20.15, the mask of the network "net-b" and the gateway 192.168.20.1` + tail},
		{"no gateway",
			client.MovedPrimaryLocalNetwork{IP: text("192.168.20.15"), NetworkName: text("net-b")},
			head + ` Set the address 192.168.20.15 and the mask of the network "net-b"` + tail},
		{"the address alone",
			client.MovedPrimaryLocalNetwork{IP: text("192.168.20.15")},
			head + " Set the address 192.168.20.15" + tail},
		{"nothing is known", client.MovedPrimaryLocalNetwork{}, head},
		{"empty texts are nothing",
			client.MovedPrimaryLocalNetwork{IP: text(""), NetworkName: text(""), Gateway: text("")}, head},
		{"an empty address is left out",
			client.MovedPrimaryLocalNetwork{IP: text(""), NetworkName: text("net-b"), Gateway: text("192.168.20.1")},
			head + ` Set the mask of the network "net-b" and the gateway 192.168.20.1` + tail},
		{"an empty gateway is left out",
			client.MovedPrimaryLocalNetwork{IP: text("192.168.20.15"), NetworkName: text("net-b"), Gateway: text("")},
			head + ` Set the address 192.168.20.15 and the mask of the network "net-b"` + tail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			summary, detail := guestConfigurationWarning(&tc.moved)

			if summary != "The guest of the virtual machine is not configured" {
				t.Errorf("summary = %q", summary)
			}
			if detail != tc.want {
				t.Errorf("detail = %q, want %q", detail, tc.want)
			}
		})
	}
}

func TestJoinWithAnd(t *testing.T) {
	cases := []struct {
		items []string
		want  string
	}{
		{nil, ""},
		{[]string{"a"}, "a"},
		{[]string{"a", "b"}, "a and b"},
		{[]string{"a", "b", "c"}, "a, b and c"},
		{[]string{"a", "b", "c", "d"}, "a, b, c and d"},
	}
	for _, tc := range cases {
		if got := joinWithAnd(tc.items); got != tc.want {
			t.Errorf("joinWithAnd(%q) = %q, want %q", tc.items, got, tc.want)
		}
	}
}
