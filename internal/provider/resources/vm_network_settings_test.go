package resources

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func installDNSPanel(m *movePanel, initial string, failPreflight bool) *[]string {
	dns, events := initial, []string{}
	previous := m.extra
	m.extra = func(w http.ResponseWriter, req *http.Request) bool {
		if req.URL.Path != vmPanelBase+"/dns" {
			return previous(w, req)
		}
		if req.Method == http.MethodGet {
			m.ok(w, map[string]any{"nameserver": dns, "configured": true, "pending": false, "reconcileRequired": false, "requiresGuestConfiguration": false})
			return true
		}
		var body struct {
			Nameserver string `json:"nameserver"`
		}
		_ = json.NewDecoder(req.Body).Decode(&body)
		dry := req.URL.Query().Get("dryRun") == "true"
		if dry {
			events = append(events, "check")
		} else {
			events = append(events, "write:"+m.status)
		}
		if dry && failPreflight {
			m.fail(w, http.StatusConflict, 627)
			return true
		}
		changed := body.Nameserver != dns
		if !dry {
			dns = body.Nameserver
		}
		m.ok(w, map[string]any{"nameserver": body.Nameserver, "changed": changed, "requiresGuestConfiguration": false})
		return true
	}
	return &events
}

func TestVmDNSUpdate_RunningVmChecksThenWritesWhileStopped(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	events := installDNSPanel(h.panel, "192.0.2.53", false)
	state := vmState().with("dns", vmStr("192.0.2.53"))
	config := vmConfig().with("dns", vmStr("192.0.2.54"))
	result := h.apply(state, config)
	result.wantOK(t)
	if !reflect.DeepEqual(*events, []string{"check", "write:STOPPED"}) {
		t.Fatalf("DNS events %v", *events)
	}
	h.wantPanelCalls("stop", "start")
	if result.state.DNS.ValueString() != "192.0.2.54" {
		t.Fatalf("DNS in state %v", result.state.DNS)
	}
}

func TestVmDNSUpdate_RestartFailureIsAnErrorAndNextApplyRestoresVm(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	installDNSPanel(h.panel, "192.0.2.53", false)
	h.panel.broken["start"] = [2]int{http.StatusForbidden, 714}
	result := h.apply(vmState().with("dns", vmStr("192.0.2.53")), vmConfig().with("dns", vmStr("192.0.2.54")))
	if !result.failed() || !result.state.RestartPending.ValueBool() {
		t.Fatalf("expected an error with a pending restart, got %+v", result)
	}
	h.wantStatus("STOPPED")
	delete(h.panel.broken, "start")
	prior := vmState().with("dns", vmStr("192.0.2.54")).with("status", vmStr("STOPPED"))
	prior = prior.with("restart_pending", tftypes.NewValue(tftypes.Bool, true))
	recovered := h.apply(prior, vmConfig().with("dns", vmStr("192.0.2.54")))
	if recovered.failed() || recovered.state.RestartPending.ValueBool() {
		t.Fatalf("restart was not recovered: %+v", recovered)
	}
	h.wantStatus("RUNNING")
}

func TestVmDelete_RefusesToReplaceAStillUnconfirmedCreatedVm(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	schema := vmSchema(t)
	prior := vmState().with("dns_confirmation_pending", tftypes.NewValue(tftypes.Bool, true))
	state := tfsdk.State{Schema: schema, Raw: h.planner.object(prior)}
	response := resource.DeleteResponse{State: state}
	h.resource.Delete(context.Background(), resource.DeleteRequest{State: state}, &response)
	if !response.Diagnostics.HasError() {
		t.Fatal("delete must be refused until the created VM and DNS are confirmed")
	}
	if len(h.panel.posts()) != 0 {
		t.Fatalf("delete touched the panel: %v", h.panel.posts())
	}
}

func TestVmDelete_RefusesWhileRestartRecoveryIsPending(t *testing.T) {
	h := newUpdateHarness(t, "STOPPED")
	schema := vmSchema(t)
	prior := vmState().with("restart_pending", tftypes.NewValue(tftypes.Bool, true))
	state := tfsdk.State{Schema: schema, Raw: h.planner.object(prior)}
	response := resource.DeleteResponse{State: state}
	h.resource.Delete(context.Background(), resource.DeleteRequest{State: state}, &response)
	if !response.Diagnostics.HasError() || len(h.panel.posts()) != 0 {
		t.Fatalf("pending restart must prevent VM deletion: %v", response.Diagnostics)
	}
}

func TestVmDNSRecoveryChecksActualValueEvenWithoutRefresh(t *testing.T) {
	h := newUpdateHarness(t, "STOPPED")
	events := installDNSPanel(h.panel, "192.0.2.53", false)
	prior := vmState().with("dns", vmStr("192.0.2.54")).with("status", vmStr("STOPPED"))
	prior = prior.with("dns_confirmation_pending", tftypes.NewValue(tftypes.Bool, true))
	result := h.apply(prior, vmConfig().with("dns", vmStr("192.0.2.54")))
	result.wantOK(t)
	if !reflect.DeepEqual(*events, []string{"check", "write:STOPPED"}) {
		t.Fatalf("recovery must reconcile the actual DNS value; events %v", *events)
	}
	if result.state.DNSConfirmationPending.ValueBool() {
		t.Fatal("confirmation remains pending after reconciliation")
	}
}

func TestVmDNSUpdate_FailedCheckDoesNotStopOrModifyVm(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	events := installDNSPanel(h.panel, "192.0.2.53", true)
	result := h.apply(vmState().with("dns", vmStr("192.0.2.53")), vmConfig().with("dns", vmStr("192.0.2.54")))
	if !result.failed() {
		t.Fatal("expected preflight failure")
	}
	if !reflect.DeepEqual(*events, []string{"check"}) {
		t.Fatalf("DNS events %v", *events)
	}
	h.wantPanelCalls()
	h.wantStatus("RUNNING")
}

func TestVmDNSUpdate_WithMoveAndResizeUsesOneStop(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	events := installDNSPanel(h.panel, "192.0.2.53", false)
	state := vmState().with("dns", vmStr("192.0.2.53"))
	config := moveConfig().with("dns", vmStr("192.0.2.54")).with("cpu_cores", vmNum(4))
	result := h.apply(state, config)
	result.wantOK(t)
	if !reflect.DeepEqual(*events, []string{"check", "write:STOPPED"}) {
		t.Fatalf("DNS events %v", *events)
	}
	h.wantPanelCalls("check", "stop", "resources", "move", "start")
}

func TestVmDNSUpdate_RemovingTheAttributeRelinquishesManagement(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	events := installDNSPanel(h.panel, "192.0.2.53", false)
	result := h.apply(vmState().with("dns", vmStr("192.0.2.53")), vmConfig())
	result.wantOK(t)
	if len(*events) != 0 {
		t.Fatalf("unexpected DNS operations %v", *events)
	}
	h.wantPanelCalls()
}

func TestDNSNameserversValidator(t *testing.T) {
	for _, tc := range []struct {
		value string
		valid bool
	}{
		{"1.1.1.1", true}, {"1.1.1.1 8.8.8.8", true}, {"", false},
		{"01.1.1.1", false}, {"1.1.1.1  8.8.8.8", false}, {"256.0.0.1", false},
		{"1.1.1.1 2.2.2.2 3.3.3.3 4.4.4.4 5.5.5.5", false},
	} {
		var resp validator.StringResponse
		DNSNameservers().ValidateString(context.Background(), validator.StringRequest{Path: path.Root("dns"), ConfigValue: types.StringValue(tc.value)}, &resp)
		if resp.Diagnostics.HasError() == tc.valid {
			t.Errorf("value %q: diagnostics %v", tc.value, resp.Diagnostics)
		}
	}
}

type gatewayTestPanel struct {
	vm          *vmPanel
	gateway     *string
	interfaceID int64
	reconcile   bool
	failCheck   bool
	failWrite   bool
	requests    []client.ChangeVmGatewayRequest
}

func newGatewayTestPanel(t *testing.T, status string, gateway *string) (*client.Client, *gatewayTestPanel) {
	c, vm := newVMPanel(t, status)
	p := &gatewayTestPanel{vm: vm, gateway: gateway, interfaceID: 91}
	vm.extra = func(w http.ResponseWriter, req *http.Request) bool {
		if req.URL.Path != vmPanelBase+"/gateway" {
			return false
		}
		if req.Method == http.MethodGet {
			vm.ok(w, map[string]any{"interfaceId": p.interfaceID, "networkId": 42, "gateway": p.gateway, "present": p.gateway != nil, "pending": false, "reconcileRequired": p.reconcile, "otherGatewayActive": false, "requiresGuestConfiguration": false})
			return true
		}
		var body client.ChangeVmGatewayRequest
		_ = json.NewDecoder(req.Body).Decode(&body)
		p.requests = append(p.requests, body)
		if body.ExpectedInterfaceID != nil && *body.ExpectedInterfaceID != p.interfaceID {
			vm.fail(w, 404, 703)
			return true
		}
		dry := req.URL.Query().Get("dryRun") == "true"
		if dry && p.failCheck {
			vm.fail(w, 409, 627)
			return true
		}
		if !dry && p.failWrite {
			vm.fail(w, 409, 370970)
			return true
		}
		changed := false
		if body.Clear {
			changed = p.gateway != nil && body.ExpectedGateway != nil && *p.gateway == *body.ExpectedGateway
		} else {
			changed = p.gateway == nil || *p.gateway != *body.Gateway
		}
		answer := p.gateway
		if !body.Clear {
			answer = body.Gateway
		} else if changed {
			answer = nil
		}
		if !dry {
			if vm.status != "STOPPED" {
				vm.fail(w, 409, 959)
				return true
			}
			if changed {
				p.gateway = answer
			}
			p.reconcile = false
		}
		vm.ok(w, map[string]any{"interfaceId": p.interfaceID, "networkId": body.NetworkID, "gateway": answer, "changed": changed, "previousGatewayOnAnotherInterface": false, "requiresGuestConfiguration": false})
		return true
	}
	return c, p
}
func gatewaySchema(t *testing.T) (tftypes.Object, resource.SchemaResponse) {
	t.Helper()
	var response resource.SchemaResponse
	NewVmGatewayResource().Schema(context.Background(), resource.SchemaRequest{}, &response)
	if response.Diagnostics.HasError() {
		t.Fatal(response.Diagnostics)
	}
	obj, ok := response.Schema.Type().TerraformType(context.Background()).(tftypes.Object)
	if !ok {
		t.Fatal("gateway schema is not an object")
	}
	return obj, response
}
func gatewayState(t *testing.T, gateway, owned string) tfsdk.State {
	t.Helper()
	obj, sch := gatewaySchema(t)
	values := map[string]tftypes.Value{}
	for name, typ := range obj.AttributeTypes {
		values[name] = tftypes.NewValue(typ, nil)
	}
	values["vm_id"], values["network_id"], values["interface_id"] = vmNum(7), vmNum(42), vmNum(91)
	values["gateway"], values["owned_gateway"] = vmStr(gateway), vmStr(owned)
	values["region"], values["project_tag"] = vmStr("TEST"), vmStr("proj")
	return tfsdk.State{Schema: sch.Schema, Raw: tftypes.NewValue(obj, values)}
}
func gatewayModel() VmGatewayResourceModel {
	return VmGatewayResourceModel{VmID: types.Int64Value(7), NetworkID: types.Int64Value(42), Region: types.StringValue("TEST"), ProjectTag: types.StringValue("proj")}
}
func gatewayRequests(vm *vmPanel) []string {
	vm.mu.Lock()
	defer vm.mu.Unlock()
	var out []string
	for i, req := range vm.requests {
		switch {
		case strings.HasSuffix(req, "/gateway") && strings.HasPrefix(req, "PUT"):
			if strings.Contains(vm.queries[i], "dryRun=true") {
				out = append(out, "check")
			} else {
				out = append(out, "write")
			}
		case strings.HasSuffix(req, "/gateway") && strings.HasPrefix(req, "GET"):
			out = append(out, "read")
		case strings.HasSuffix(req, "/stop"):
			out = append(out, "stop")
		case strings.HasSuffix(req, "/start"):
			out = append(out, "start")
		}
	}
	return out
}

func TestVmGatewayApply_RunningVmChangesOnlyWhileStopped(t *testing.T) {
	c, panel := newGatewayTestPanel(t, "RUNNING", nil)
	r := &VmGatewayResource{client: c, poll: time.Millisecond}
	data := gatewayModel()
	desired := "192.0.2.1"
	result, diags := r.apply(context.Background(), &data, client.ChangeVmGatewayRequest{NetworkID: 42, Gateway: &desired})
	if diags.HasError() || result == nil || !result.Changed {
		t.Fatalf("result %+v diagnostics %v", result, diags)
	}
	if got := gatewayRequests(panel.vm); !reflect.DeepEqual(got, []string{"check", "stop", "write", "start"}) {
		t.Fatalf("calls %v", got)
	}
	if len(panel.requests) != 2 || panel.requests[1].ExpectedInterfaceID == nil || *panel.requests[1].ExpectedInterfaceID != 91 {
		t.Fatalf("requests %+v", panel.requests)
	}
}

func TestVmGatewayApply_NoOpDoesNotStopVm(t *testing.T) {
	desired := "192.0.2.1"
	c, panel := newGatewayTestPanel(t, "RUNNING", &desired)
	r := &VmGatewayResource{client: c, poll: time.Millisecond}
	data := gatewayModel()
	_, diags := r.apply(context.Background(), &data, client.ChangeVmGatewayRequest{NetworkID: 42, Gateway: &desired})
	if diags.HasError() {
		t.Fatal(diags)
	}
	if got := gatewayRequests(panel.vm); !reflect.DeepEqual(got, []string{"check", "read"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestVmGatewayReadAndDeleteDoNotAdoptOrClearSomeoneElsesRoute(t *testing.T) {
	foreign := "192.0.2.2"
	c, panel := newGatewayTestPanel(t, "RUNNING", &foreign)
	r := &VmGatewayResource{client: c, poll: time.Millisecond}
	state := gatewayState(t, "192.0.2.1", "192.0.2.1")
	read := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &read)
	if read.Diagnostics.HasError() {
		t.Fatal(read.Diagnostics)
	}
	var after VmGatewayResourceModel
	if d := read.State.Get(context.Background(), &after); d.HasError() {
		t.Fatal(d)
	}
	if !after.Gateway.IsNull() || after.OwnedGateway.ValueString() != "192.0.2.1" {
		t.Fatalf("state %+v", after)
	}
	deleted := resource.DeleteResponse{State: read.State}
	r.Delete(context.Background(), resource.DeleteRequest{State: read.State}, &deleted)
	if deleted.Diagnostics.HasError() {
		t.Fatal(deleted.Diagnostics)
	}
	if got := gatewayRequests(panel.vm); !reflect.DeepEqual(got, []string{"read", "check", "read"}) {
		t.Fatalf("calls %v", got)
	}
	if panel.gateway == nil || *panel.gateway != foreign || panel.requests[0].ExpectedGateway == nil || *panel.requests[0].ExpectedGateway != "192.0.2.1" {
		t.Fatalf("unexpected delete: %+v", panel.requests)
	}
}

func TestVmGatewayDeleteReconcilesPanelWithoutClearingForeignRoute(t *testing.T) {
	foreign := "192.0.2.2"
	c, panel := newGatewayTestPanel(t, "STOPPED", &foreign)
	panel.reconcile = true
	r := &VmGatewayResource{client: c, poll: time.Millisecond}
	state := gatewayState(t, "192.0.2.1", "192.0.2.1")
	deleted := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleted)
	if deleted.Diagnostics.HasError() {
		t.Fatal(deleted.Diagnostics)
	}
	if panel.reconcile || panel.gateway == nil || *panel.gateway != foreign {
		t.Fatalf("panel was not reconciled or foreign route changed: %+v", panel)
	}
	if got := gatewayRequests(panel.vm); !reflect.DeepEqual(got, []string{"check", "read", "write"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestVmGatewayReadRefusesAReplacedInterface(t *testing.T) {
	gateway := "192.0.2.1"
	c, panel := newGatewayTestPanel(t, "STOPPED", &gateway)
	panel.interfaceID = 92
	r := &VmGatewayResource{client: c, poll: time.Millisecond}
	state := gatewayState(t, gateway, gateway)
	resp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("replaced interface accepted")
	}
}

func TestVmGatewayReadDoesNotForgetPendingRestartWhenInterfaceIsMissing(t *testing.T) {
	gateway := "192.0.2.1"
	c, panel := newGatewayTestPanel(t, "STOPPED", &gateway)
	panel.vm.extra = func(w http.ResponseWriter, req *http.Request) bool {
		if req.Method == http.MethodGet && req.URL.Path == vmPanelBase+"/gateway" {
			panel.vm.fail(w, http.StatusNotFound, 703)
			return true
		}
		return false
	}
	r := &VmGatewayResource{client: c}
	state := gatewayState(t, gateway, gateway)
	if d := state.SetAttribute(context.Background(), path.Root("restart_pending"), true); d.HasError() {
		t.Fatal(d)
	}
	resp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
	if !resp.Diagnostics.HasError() || resp.State.Raw.IsNull() {
		t.Fatalf("missing interface must preserve pending restart state: %v", resp.Diagnostics)
	}
}

func TestVmGatewayPreflightFailureDoesNotStopVm(t *testing.T) {
	c, panel := newGatewayTestPanel(t, "RUNNING", nil)
	panel.failCheck = true
	r := &VmGatewayResource{client: c, poll: time.Millisecond}
	data := gatewayModel()
	desired := "192.0.2.1"
	_, diags := r.apply(context.Background(), &data, client.ChangeVmGatewayRequest{NetworkID: 42, Gateway: &desired})
	if !diags.HasError() {
		t.Fatal("expected preflight failure")
	}
	if got := gatewayRequests(panel.vm); !reflect.DeepEqual(got, []string{"check"}) {
		t.Fatalf("calls %v", got)
	}
}

func gatewayRaw(t *testing.T, overrides map[string]tftypes.Value) (tfsdk.Plan, tfsdk.State) {
	t.Helper()
	obj, sch := gatewaySchema(t)
	values := map[string]tftypes.Value{}
	for name, typ := range obj.AttributeTypes {
		values[name] = tftypes.NewValue(typ, nil)
	}
	values["vm_id"], values["network_id"], values["gateway"] = vmNum(7), vmNum(42), vmStr("192.0.2.1")
	values["region"], values["project_tag"] = vmStr("TEST"), vmStr("proj")
	values["interface_id"], values["owned_gateway"] = vmUnkN, vmUnkS
	for k, v := range overrides {
		values[k] = v
	}
	raw := tftypes.NewValue(obj, values)
	return tfsdk.Plan{Schema: sch.Schema, Raw: raw}, tfsdk.State{Schema: sch.Schema, Raw: tftypes.NewValue(obj, nil)}
}

func TestVmGatewayCreate_RecordsOwnershipAndInterface(t *testing.T) {
	c, panel := newGatewayTestPanel(t, "STOPPED", nil)
	r := &VmGatewayResource{client: c, poll: time.Millisecond}
	plan, empty := gatewayRaw(t, nil)
	resp := resource.CreateResponse{State: empty}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var state VmGatewayResourceModel
	if d := resp.State.Get(context.Background(), &state); d.HasError() {
		t.Fatal(d)
	}
	if state.InterfaceID.ValueInt64() != 91 || state.Gateway.ValueString() != "192.0.2.1" || state.OwnedGateway.ValueString() != "192.0.2.1" {
		t.Fatalf("state %+v", state)
	}
	if got := gatewayRequests(panel.vm); !reflect.DeepEqual(got, []string{"check", "write"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestVmGatewayCreate_RestartFailureKeepsStateAndBlocksTaintedDelete(t *testing.T) {
	c, panel := newGatewayTestPanel(t, "RUNNING", nil)
	panel.vm.broken["start"] = [2]int{http.StatusForbidden, 714}
	r := &VmGatewayResource{client: c, poll: time.Millisecond}
	plan, empty := gatewayRaw(t, nil)
	created := resource.CreateResponse{State: empty}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &created)
	if !created.Diagnostics.HasError() {
		t.Fatal("expected restart failure")
	}
	var state VmGatewayResourceModel
	if d := created.State.Get(context.Background(), &state); d.HasError() || !state.RestartPending.ValueBool() || state.InterfaceID.ValueInt64() != 91 {
		t.Fatalf("state %+v, diagnostics %v", state, d)
	}
	deleted := resource.DeleteResponse{State: created.State}
	r.Delete(context.Background(), resource.DeleteRequest{State: created.State}, &deleted)
	if !deleted.Diagnostics.HasError() || panel.gateway == nil || *panel.gateway != "192.0.2.1" {
		t.Fatalf("pending recovery must prevent route deletion: %+v", deleted.Diagnostics)
	}
	delete(panel.vm.broken, "start")
	recoverPlan, _ := gatewayRaw(t, map[string]tftypes.Value{
		"interface_id": vmNum(91), "owned_gateway": vmStr("192.0.2.1"),
		"restart_pending": tftypes.NewValue(tftypes.Bool, tftypes.UnknownValue),
	})
	recovered := resource.UpdateResponse{State: created.State}
	r.Update(context.Background(), resource.UpdateRequest{State: created.State, Plan: recoverPlan}, &recovered)
	if recovered.Diagnostics.HasError() || panel.vm.current() != "RUNNING" {
		t.Fatalf("restart was not recovered: %+v", recovered.Diagnostics)
	}
	if d := recovered.State.Get(context.Background(), &state); d.HasError() || state.RestartPending.ValueBool() {
		t.Fatalf("recovered state %+v, diagnostics %v", state, d)
	}
}

func TestVmGatewayUpdate_PlanLeavesNewOwnershipForApply(t *testing.T) {
	c, _ := newGatewayTestPanel(t, "STOPPED", nil)
	r := &VmGatewayResource{client: c, poll: time.Millisecond}
	state := gatewayState(t, "192.0.2.1", "192.0.2.1")
	plan, _ := gatewayRaw(t, map[string]tftypes.Value{"gateway": vmStr("192.0.2.2"), "interface_id": vmNum(91), "owned_gateway": vmStr("192.0.2.1")})
	resp := resource.ModifyPlanResponse{Plan: plan}
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{Plan: plan, State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var model VmGatewayResourceModel
	if d := resp.Plan.Get(context.Background(), &model); d.HasError() {
		t.Fatal(d)
	}
	if !model.OwnedGateway.IsUnknown() {
		t.Fatalf("ownership plan %v, want unknown", model.OwnedGateway)
	}
}

func TestVmGatewayDestroyPlanWarnsAboutCloudInitAndTheRestart(t *testing.T) {
	r := &VmGatewayResource{}
	state := gatewayState(t, "192.0.2.1", "192.0.2.1")
	plan, empty := gatewayRaw(t, nil)
	plan.Raw = empty.Raw
	resp := resource.ModifyPlanResponse{Plan: plan}
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{Plan: plan, State: state}, &resp)
	_, warnings := summaries(resp.Diagnostics)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "SSH host keys") || !strings.Contains(warnings[0], "restart") {
		t.Fatalf("destroy warning %v", warnings)
	}
}

func TestVmGatewayDelete_ClearsOnlyTheOwnedGateway(t *testing.T) {
	gateway := "192.0.2.1"
	c, panel := newGatewayTestPanel(t, "RUNNING", &gateway)
	r := &VmGatewayResource{client: c, poll: time.Millisecond}
	state := gatewayState(t, gateway, gateway)
	resp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if panel.gateway != nil {
		t.Fatalf("gateway remains %v", *panel.gateway)
	}
	if got := gatewayRequests(panel.vm); !reflect.DeepEqual(got, []string{"check", "stop", "write", "start"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestVmGatewayUnknownWriteDoesNotReportSuccessOrRestartThroughJournalGuard(t *testing.T) {
	c, panel := newGatewayTestPanel(t, "RUNNING", nil)
	panel.failWrite = true
	panel.vm.broken["start"] = [2]int{http.StatusConflict, 370970}
	r := &VmGatewayResource{client: c, poll: time.Millisecond}
	data := gatewayModel()
	desired := "192.0.2.1"
	_, diags := r.apply(context.Background(), &data, client.ChangeVmGatewayRequest{NetworkID: 42, Gateway: &desired})
	if !diags.HasError() {
		t.Fatal("unknown outcome reported as success")
	}
	if panel.vm.current() != "STOPPED" {
		t.Fatalf("VM status %s", panel.vm.current())
	}
	if got := gatewayRequests(panel.vm); !reflect.DeepEqual(got, []string{"check", "stop", "write", "start"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestVmGatewayCreate_UnknownWriteAndFailedRestartCanRecoverWithoutAdoptingForeignRoute(t *testing.T) {
	c, panel := newGatewayTestPanel(t, "RUNNING", nil)
	panel.failWrite = true
	panel.vm.broken["start"] = [2]int{http.StatusConflict, 370970}
	r := &VmGatewayResource{client: c, poll: time.Millisecond}
	plan, empty := gatewayRaw(t, nil)
	created := resource.CreateResponse{State: empty}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &created)
	var state VmGatewayResourceModel
	if !created.Diagnostics.HasError() || created.State.Raw.IsNull() {
		t.Fatalf("failed create must retain recovery state: %v", created.Diagnostics)
	}
	if d := created.State.Get(context.Background(), &state); d.HasError() || !state.RestartPending.ValueBool() || state.InterfaceID.ValueInt64() != 91 || !state.OwnedGateway.IsNull() {
		t.Fatalf("unconfirmed route was treated as owned: state=%+v diagnostics=%v", state, d)
	}
	deleted := resource.DeleteResponse{State: created.State}
	r.Delete(context.Background(), resource.DeleteRequest{State: created.State}, &deleted)
	if !deleted.Diagnostics.HasError() {
		t.Fatal("pending recovery must block deletion")
	}

	delete(panel.vm.broken, "start")
	panel.failWrite = false
	foreign := "192.0.2.2"
	panel.gateway = &foreign
	read := resource.ReadResponse{State: created.State}
	r.Read(context.Background(), resource.ReadRequest{State: created.State}, &read)
	if read.Diagnostics.HasError() {
		t.Fatal(read.Diagnostics)
	}
	if d := read.State.Get(context.Background(), &state); d.HasError() || !state.OwnedGateway.IsNull() || !state.Gateway.IsNull() {
		t.Fatalf("refresh adopted a foreign route: state=%+v diagnostics=%v", state, d)
	}
	recoverPlan, _ := gatewayRaw(t, map[string]tftypes.Value{
		"interface_id": vmNum(91), "restart_pending": tftypes.NewValue(tftypes.Bool, tftypes.UnknownValue),
	})
	recovered := resource.UpdateResponse{State: read.State}
	r.Update(context.Background(), resource.UpdateRequest{State: read.State, Plan: recoverPlan}, &recovered)
	if recovered.Diagnostics.HasError() || panel.vm.current() != "RUNNING" || panel.gateway == nil || *panel.gateway != "192.0.2.1" {
		t.Fatalf("recovery failed: %v", recovered.Diagnostics)
	}
	if d := recovered.State.Get(context.Background(), &state); d.HasError() || state.RestartPending.ValueBool() || state.OwnedGateway.ValueString() != "192.0.2.1" {
		t.Fatalf("incorrect recovered state: %+v, diagnostics %v", state, d)
	}
}

func TestVmDNSRead_ManagedValueReflectsTheHypervisor(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	installDNSPanel(h.panel, "192.0.2.54", false)
	raw := h.planner.object(vmState().with("dns", vmStr("192.0.2.53")))
	state := tfsdk.State{Schema: vmSchema(t), Raw: raw}
	resp := resource.ReadResponse{State: state}
	h.resource.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var after VmResourceModel
	if d := resp.State.Get(context.Background(), &after); d.HasError() {
		t.Fatal(d)
	}
	if after.DNS.ValueString() != "192.0.2.54" {
		t.Fatalf("DNS in state %v", after.DNS)
	}
}

func TestVmReadDoesNotForgetAResourceWithPendingRecovery(t *testing.T) {
	for _, marker := range []string{"dns_confirmation_pending", "restart_pending"} {
		t.Run(marker, func(t *testing.T) {
			h := newUpdateHarness(t, "ERROR")
			h.panel.readFailures = []bool{true}
			h.panel.readAnswer = answer{http.StatusNotFound, 601}
			raw := h.planner.object(vmState().with(marker, tftypes.NewValue(tftypes.Bool, true)))
			state := tfsdk.State{Schema: vmSchema(t), Raw: raw}
			resp := resource.ReadResponse{State: state}
			h.resource.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
			if !resp.Diagnostics.HasError() || resp.State.Raw.IsNull() {
				t.Fatalf("not-found during recovery must preserve state and report an error: %v", resp.Diagnostics)
			}
			if h.panel.requests[0] != "GET /api/v2/vms/7/status" {
				t.Fatalf("read used %v, want status endpoint", h.panel.requests)
			}
		})
	}
}

func TestVmDNSRead_UnmanagedDnsDoesNotCallANewEndpoint(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	raw := h.planner.object(vmState())
	state := tfsdk.State{Schema: vmSchema(t), Raw: raw}
	resp := resource.ReadResponse{State: state}
	h.resource.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	h.panel.mu.Lock()
	defer h.panel.mu.Unlock()
	for _, req := range h.panel.requests {
		if strings.Contains(req, "/dns") {
			t.Fatalf("unexpected DNS call %s", req)
		}
	}
}

func TestVmDNSRead_MissingEndpointDoesNotRemoveTheVm(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	previous := h.panel.extra
	h.panel.extra = func(w http.ResponseWriter, req *http.Request) bool {
		if strings.HasSuffix(req.URL.Path, "/dns") {
			http.NotFound(w, req)
			return true
		}
		return previous(w, req)
	}
	raw := h.planner.object(vmState().with("dns", vmStr("192.0.2.53")))
	state := tfsdk.State{Schema: vmSchema(t), Raw: raw}
	resp := resource.ReadResponse{State: state}
	h.resource.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("missing endpoint must be an error")
	}
	if resp.State.Raw.IsNull() {
		t.Fatal("missing endpoint removed the VM from state")
	}
}

func TestCreateVmRequest_DNSIsSentOnlyWhenSet(t *testing.T) {
	dns := "1.1.1.1 8.8.8.8"
	with, err := json.Marshal(client.CreateVmRequest{Name: "vm", DNS: &dns})
	if err != nil || !strings.Contains(string(with), `"dns":"1.1.1.1 8.8.8.8"`) {
		t.Fatalf("body %s err %v", with, err)
	}
	without, err := json.Marshal(client.CreateVmRequest{Name: "vm"})
	if err != nil || strings.Contains(string(without), `"dns"`) {
		t.Fatalf("body %s err %v", without, err)
	}
}

func TestVmDNSUpdate_UnknownWriteLeavesVmStoppedWhenPanelBlocksStart(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	installDNSPanel(h.panel, "192.0.2.53", false)
	previous := h.panel.extra
	h.panel.extra = func(w http.ResponseWriter, req *http.Request) bool {
		if req.URL.Path == vmPanelBase+"/dns" && req.Method == http.MethodPut && req.URL.Query().Get("dryRun") != "true" {
			h.panel.fail(w, http.StatusConflict, 370970)
			return true
		}
		return previous(w, req)
	}
	h.panel.broken["start"] = [2]int{http.StatusConflict, 370970}
	result := h.apply(vmState().with("dns", vmStr("192.0.2.53")), vmConfig().with("dns", vmStr("192.0.2.54")))
	if !result.failed() {
		t.Fatal("unknown DNS outcome was reported as success")
	}
	h.wantStatus("STOPPED")
	h.wantPanelCalls("stop", "start")
}
