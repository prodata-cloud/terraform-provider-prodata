package resources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"terraform-provider-prodata/internal/client"
)

func TestLbASG_CreateRefreshUpdateImportDelete(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var mu sync.Mutex
	asg, deleted, writes := int64(7), false, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("X-Region") != "TEST" || r.Header.Get("X-Project-Tag") != "project-a" {
			t.Error("scope was lost")
		}
		call := r.Method + " " + r.URL.Path
		switch call {
		case "POST /panel-main/api/loadbalancer/createLoadbalancer", "POST /panel-main/api/loadbalancer/configureLoadbalancer/42":
			var body map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			var backends []map[string]int64
			if err := json.Unmarshal(body["backends"], &backends); err != nil {
				t.Error(err)
			}
			if len(backends) != 1 || len(backends[0]) != 1 || backends[0]["asgId"] < 1 {
				t.Errorf("not an ASG selector: %s", body["backends"])
			}
			asg = backends[0]["asgId"]
			writes++
		case "GET /panel-main/api/loadbalancer/getLoadBalancer":
			if r.URL.Query().Get("loadBalancerId") != "42" {
				t.Error("wrong LB ID")
			}
		case "POST /panel-main/api/loadbalancer/deleteLoadbalancer/42":
			deleted = true
		default:
			t.Errorf("unexpected request %s", call)
			http.NotFound(w, r)
			return
		}
		status := "SUCCESS"
		if deleted {
			status = "DELETED"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"error": 0, "data": map[string]any{
			"id": 42, "name": "asg-lb", "description": "desc", "source": "FRONTEND", "userNet": 9, "protocol": "TCP", "isPublic": false,
			"status": map[string]any{"name": status}, "ports": []map[string]int{{"srcPort": 80, "dstPort": 8080}},
			// No materialized backends: the group is scaled to zero.
			"backends": []any{}, "backendGroups": []map[string]any{{"kind": "ASG", "asgId": asg}},
		}})
	}))
	defer server.Close()
	c, err := client.New(client.Config{APIBaseURL: server.URL, APIKeyID: "fake", APISecretKey: "fake", Region: "TEST", ProjectTag: "project-a"})
	if err != nil {
		t.Fatal(err)
	}
	r := &LbResource{c: c}
	plan, empty := asgLBPlan(t, 7)
	created := resource.CreateResponse{State: empty}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, &created)
	if created.Diagnostics.HasError() {
		t.Fatal(created.Diagnostics)
	}
	checkASGState(t, created.State, 7)
	read := resource.ReadResponse{State: created.State}
	r.Read(ctx, resource.ReadRequest{State: created.State}, &read)
	if read.Diagnostics.HasError() {
		t.Fatal(read.Diagnostics)
	}
	checkASGState(t, read.State, 7)
	updatePlan := tfsdk.Plan{Schema: read.State.Schema, Raw: read.State.Raw}
	if d := updatePlan.SetAttribute(ctx, path.Root("backend_group").AtName("asg_id"), int64(8)); d.HasError() {
		t.Fatal(d)
	}
	updated := resource.UpdateResponse{State: read.State}
	r.Update(ctx, resource.UpdateRequest{Plan: updatePlan, State: read.State}, &updated)
	if updated.Diagnostics.HasError() {
		t.Fatal(updated.Diagnostics)
	}
	checkASGState(t, updated.State, 8)
	imported := resource.ImportStateResponse{State: empty}
	r.ImportState(ctx, resource.ImportStateRequest{ID: "42"}, &imported)
	if imported.Diagnostics.HasError() {
		t.Fatal(imported.Diagnostics)
	}
	importRead := resource.ReadResponse{State: imported.State}
	r.Read(ctx, resource.ReadRequest{State: imported.State}, &importRead)
	if importRead.Diagnostics.HasError() {
		t.Fatal(importRead.Diagnostics)
	}
	checkASGState(t, importRead.State, 8)
	destroyed := resource.DeleteResponse{State: updated.State}
	r.Delete(ctx, resource.DeleteRequest{State: updated.State}, &destroyed)
	if destroyed.Diagnostics.HasError() {
		t.Fatal(destroyed.Diagnostics)
	}
	mu.Lock()
	defer mu.Unlock()
	if writes != 2 || !deleted {
		t.Errorf("writes=%d deleted=%v", writes, deleted)
	}
}

func TestLbASG_RequiresDurableAcknowledgement(t *testing.T) {
	want := &LbBackendGroupModel{VMIDs: types.SetNull(types.StringType), NodePoolID: types.Int64Null(), ASGID: types.Int64Value(7)}
	for _, lb := range []*client.LoadBalancer{
		{}, {BackendGroups: []client.LbBackendGroup{}}, {BackendGroups: []client.LbBackendGroup{{Kind: "ASG", ASGID: 8}}},
	} {
		if verifyAppliedASGBackend(lb, want) == nil {
			t.Errorf("accepted missing/wrong ASG declaration: %+v", lb)
		}
	}
	if err := verifyAppliedASGBackend(&client.LoadBalancer{BackendGroups: []client.LbBackendGroup{{Kind: "ASG", ASGID: 7}}}, want); err != nil {
		t.Fatal(err)
	}
}

func TestLbTerminalFailurePreservesPlatformReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"error": 0, "data": map[string]any{"id": 42, "status": map[string]any{"name": "FAIL"}, "errMessage": "No compute capacity in the selected region"}})
	}))
	defer server.Close()
	c, err := client.New(client.Config{APIBaseURL: server.URL, APIKeyID: "fake", APISecretKey: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	lb, err := (&LbResource{c: c}).waitForTerminalStatus(context.Background(), 42, nil, lbTerminalApply)
	if lb == nil || err == nil || !strings.Contains(err.Error(), "No compute capacity in the selected region") {
		t.Fatalf("lost platform failure reason: %+v / %v", lb, err)
	}
}

func TestLbASG_VMAndASGChangeIsInPlace(t *testing.T) {
	ctx := context.Background()
	plan, _ := asgLBPlan(t, 7)
	statePlan, _ := asgLBPlan(t, 7)
	if d := statePlan.SetAttribute(ctx, path.Root("backend_group").AtName("asg_id"), types.Int64Null()); d.HasError() {
		t.Fatal(d)
	}
	if d := statePlan.SetAttribute(ctx, path.Root("backend_group").AtName("vm_ids"), []string{"vm-1"}); d.HasError() {
		t.Fatal(d)
	}
	resp := resource.ModifyPlanResponse{Plan: plan}
	(&LbResource{}).ModifyPlan(ctx, resource.ModifyPlanRequest{Config: tfsdk.Config(plan), Plan: plan, State: tfsdk.State(statePlan)}, &resp)
	if resp.Diagnostics.HasError() || len(resp.RequiresReplace) != 0 {
		t.Fatalf("VM to ASG must update in place: %v / %v", resp.Diagnostics, resp.RequiresReplace)
	}
}

func asgLBPlan(t *testing.T, id int64) (tfsdk.Plan, tfsdk.State) {
	t.Helper()
	obj, sch := lbObjType(t)
	portType, ok := obj.AttributeTypes["port"].(tftypes.Set)
	if !ok {
		t.Fatal("port schema must be a set")
	}
	group := tftypes.NewValue(lbGroupType, map[string]tftypes.Value{"vm_ids": tftypes.NewValue(lbVMIDsType, nil), "node_pool_id": tftypes.NewValue(tftypes.Number, nil), "asg_id": nirNum(id)})
	raw := lbRaw(t, map[string]tftypes.Value{"id": nirNumUnknown(), "name": nirStr("asg-lb"), "description": nirStr("desc"), "region": nirStr("TEST"), "project_tag": nirStr("project-a"), "type": nirStr("internal"), "protocol": nirStr("TCP"), "network_id": nirNum(9), "backend_group": group,
		"port": tftypes.NewValue(portType, []tftypes.Value{tftypes.NewValue(portType.ElementType, map[string]tftypes.Value{"port": nirNum(80), "target_port": nirNum(8080)})})})
	return tfsdk.Plan{Schema: sch, Raw: raw}, tfsdk.State{Schema: sch, Raw: tftypes.NewValue(raw.Type(), nil)}
}
func checkASGState(t *testing.T, state tfsdk.State, id int64) {
	t.Helper()
	var m LbResourceModel
	if d := state.Get(context.Background(), &m); d.HasError() {
		t.Fatal(d)
	}
	if m.ID.ValueInt64() != 42 || m.BackendGroup == nil || m.BackendGroup.ASGID.ValueInt64() != id || !m.BackendGroup.VMIDs.IsNull() {
		t.Fatalf("lost ASG identity: %+v", m)
	}
}
