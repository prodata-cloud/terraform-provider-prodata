package resources

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"terraform-provider-prodata/internal/client"
)

func r05LBState(t *testing.T, source string) (tfsdk.Plan, tfsdk.State) {
	t.Helper()
	plan, _ := asgLBPlan(t, 7)
	state := tfsdk.State(plan)
	ctx := context.Background()
	for name, value := range map[string]any{"id": int64(42), "source": source, "status": "SUCCESS"} {
		if d := state.SetAttribute(ctx, path.Root(name), value); d.HasError() {
			t.Fatal(d)
		}
	}
	return plan, state
}

func r05LBReply(w http.ResponseWriter, source string, groups, backends []map[string]any) {
	_ = json.NewEncoder(w).Encode(map[string]any{"error": 0, "data": map[string]any{"id": 42, "name": "asg-lb", "description": "CCM: asg-lb", "source": source, "status": map[string]any{"name": "SUCCESS"}, "protocol": "TCP", "userNet": 9, "ports": []map[string]any{{"srcPort": 80, "dstPort": 8080}}, "backendGroups": groups, "backends": backends, "backendSelectorContract": "replace-v1"}})
}

func TestR05MixedSelectorReadWarnsAndPlansAnInPlaceRepair(t *testing.T) {
	for _, groups := range [][]map[string]any{
		{{"kind": "ASG", "asgId": 7}},
		{{"kind": "ASG", "asgId": 7}, {"kind": "ASG", "asgId": 8}},
	} {
		c, closeServer := r05Client(t, func(w http.ResponseWriter, req *http.Request) {
			r05LBReply(w, client.LbSourceFrontend, groups, []map[string]any{{"userVM": map[string]any{"guid": "member"}, "managedByGroup": true}, {"userVM": map[string]any{"guid": "manual"}, "managedByGroup": false}})
		})
		plan, state := r05LBState(t, client.LbSourceFrontend)
		r := &LbResource{c: c}
		ctx := context.Background()
		read := resource.ReadResponse{State: state}
		r.Read(ctx, resource.ReadRequest{State: state}, &read)
		if read.Diagnostics.HasError() || len(read.Diagnostics) == 0 || read.State.Raw.IsNull() {
			t.Fatalf("mixed read blocks repair/destroy: %v", read.Diagnostics)
		}
		var drift types.Bool
		d := read.State.GetAttribute(ctx, path.Root("backend_selector_drift"), &drift)
		if d.HasError() || !drift.ValueBool() {
			t.Fatalf("mixed selector was not represented as drift: %v %v", drift, d)
		}
		changed := resource.ModifyPlanResponse{Plan: plan}
		r.ModifyPlan(ctx, resource.ModifyPlanRequest{State: read.State, Plan: plan, Config: tfsdk.Config(plan)}, &changed)
		if changed.Diagnostics.HasError() || len(changed.RequiresReplace) != 0 {
			t.Fatalf("repair replaces LB: %v %v", changed.Diagnostics, changed.RequiresReplace)
		}
		d = changed.Plan.GetAttribute(ctx, path.Root("backend_selector_drift"), &drift)
		if d.HasError() || drift.IsUnknown() || drift.ValueBool() {
			t.Fatalf("repair was not planned: %v %v", drift, d)
		}
		closeServer()
	}
}

func TestR05CCMImportReadsOnlyPositivePoolAssociation(t *testing.T) {
	c, closeServer := r05Client(t, func(w http.ResponseWriter, req *http.Request) {
		r05LBReply(w, client.LbSourceCCM, []map[string]any{}, []map[string]any{{"userVM": map[string]any{"guid": "worker"}, "nodePoolId": 5}})
	})
	defer closeServer()
	_, state := r05LBState(t, client.LbSourceCCM)
	ctx := context.Background()
	_ = state.SetAttribute(ctx, path.Root("backend_group"), &LbBackendGroupModel{VMIDs: types.SetNull(types.StringType), NodePoolID: types.Int64Null(), ASGID: types.Int64Null()})
	r := &LbResource{c: c}
	read := resource.ReadResponse{State: state}
	r.Read(ctx, resource.ReadRequest{State: state}, &read)
	var pool types.Int64
	d := read.State.GetAttribute(ctx, path.Root("backend_group").AtName("node_pool_id"), &pool)
	if read.Diagnostics.HasError() || d.HasError() || pool.ValueInt64() != 5 {
		t.Fatalf("positive CCM association lost: %v %v %v", pool, d, read.Diagnostics)
	}
}

func TestR05CCMImportPoolRequiresPositiveProofBeforeReplacement(t *testing.T) {
	for _, known := range []bool{true, false} {
		c, closeServer := r05Client(t, func(w http.ResponseWriter, req *http.Request) {
			rows := []map[string]any{}
			if known {
				rows = append(rows, map[string]any{"userVM": map[string]any{"guid": "worker"}, "nodePoolId": 5})
			}
			r05LBReply(w, client.LbSourceCCM, []map[string]any{}, rows)
		})
		_, state := r05LBState(t, client.LbSourceCCM)
		ctx := context.Background()
		_ = state.SetAttribute(ctx, path.Root("description"), types.StringNull())
		_ = state.SetAttribute(ctx, path.Root("backend_group"), &LbBackendGroupModel{VMIDs: types.SetNull(types.StringType), NodePoolID: types.Int64Null(), ASGID: types.Int64Null()})
		plan := tfsdk.Plan(state)
		_ = plan.SetAttribute(ctx, path.Root("backend_group").AtName("node_pool_id"), int64(5))
		r := &LbResource{c: c}
		modified := resource.ModifyPlanResponse{Plan: plan}
		r.ModifyPlan(ctx, resource.ModifyPlanRequest{State: state, Plan: plan, Config: tfsdk.Config(plan)}, &modified)
		if modified.Diagnostics.HasError() == known {
			t.Fatalf("known=%v diagnostic=%v", known, modified.Diagnostics)
		}
		if len(modified.RequiresReplace) != 0 {
			t.Fatalf("unverified import caused replacement: %v", modified.RequiresReplace)
		}
		if known {
			fullSchema, ok := plan.Schema.(schema.Schema)
			if !ok {
				t.Fatal("unexpected plan schema type")
			}
			nested, ok := fullSchema.Attributes["backend_group"].(schema.SingleNestedAttribute)
			if !ok {
				t.Fatal("unexpected backend_group schema type")
			}
			pool, ok := nested.Attributes["node_pool_id"].(schema.Int64Attribute)
			if !ok {
				t.Fatal("unexpected node_pool_id schema type")
			}
			for _, modifier := range pool.PlanModifiers {
				var result planmodifier.Int64Response
				modifier.PlanModifyInt64(ctx, planmodifier.Int64Request{State: state, Plan: plan, StateValue: types.Int64Null(), PlanValue: types.Int64Value(5), ConfigValue: types.Int64Value(5)}, &result)
				if result.RequiresReplace {
					t.Fatal("attribute modifier replaces verified imported CCM")
				}
			}
		}
		closeServer()
	}
}
