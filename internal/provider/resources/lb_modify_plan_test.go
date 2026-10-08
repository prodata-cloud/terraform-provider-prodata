package resources

import (
	"context"
	"maps"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

var (
	lbVMIDsType = tftypes.Set{ElementType: tftypes.String}

	lbGroupType = tftypes.Object{AttributeTypes: map[string]tftypes.Type{
		"vm_ids":       lbVMIDsType,
		"node_pool_id": tftypes.Number,
	}}
)

// lbGroup is backend_group as Terraform hands it over: whole says whether the block is absent
// (cfgNull), present (cfgKnown, then vms and pool describe its two attributes) or unknown as a
// whole (cfgUnknown) — a conditional on a value known only after apply.
type lbGroup struct {
	whole, vms, pool int
}

func lbBlock(vms, pool int) lbGroup { return lbGroup{whole: cfgKnown, vms: vms, pool: pool} }

var lbUnknownBlock = lbGroup{whole: cfgUnknown}

func (g lbGroup) raw() tftypes.Value {
	switch g.whole {
	case cfgNull:
		return tftypes.NewValue(lbGroupType, nil)
	case cfgUnknown:
		return tftypes.NewValue(lbGroupType, tftypes.UnknownValue)
	}
	vms := tftypes.NewValue(lbVMIDsType, nil)
	switch g.vms {
	case cfgKnown:
		vms = tftypes.NewValue(lbVMIDsType, []tftypes.Value{nirStr("vm-1")})
	case cfgUnknown:
		vms = tftypes.NewValue(lbVMIDsType, tftypes.UnknownValue)
	}
	return tftypes.NewValue(lbGroupType, map[string]tftypes.Value{
		"vm_ids":       vms,
		"node_pool_id": poolIDCfg(g.pool),
	})
}

func poolIDCfg(kind int) tftypes.Value {
	switch kind {
	case cfgKnown:
		return nirNum(5)
	case cfgUnknown:
		return nirNumUnknown()
	}
	return tftypes.NewValue(tftypes.Number, nil)
}

func descriptionCfg(kind int) tftypes.Value {
	switch kind {
	case cfgKnown:
		return nirStr("my own description")
	case cfgUnknown:
		return nirStrUnknown()
	}
	return nirStrNull()
}

func lbObjType(t *testing.T) (tftypes.Object, schema.Schema) {
	t.Helper()
	var resp resource.SchemaResponse
	NewLbResource().Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}
	obj, ok := resp.Schema.Type().TerraformType(context.Background()).(tftypes.Object)
	if !ok {
		t.Fatal("schema type is not an object")
	}
	return obj, resp.Schema
}

// lbRaw builds a load balancer object in which every attribute is null except those in set.
func lbRaw(t *testing.T, set map[string]tftypes.Value) tftypes.Value {
	t.Helper()
	obj, _ := lbObjType(t)
	vals := make(map[string]tftypes.Value, len(obj.AttributeTypes))
	for k, typ := range obj.AttributeTypes {
		vals[k] = tftypes.NewValue(typ, nil)
	}
	for k, v := range set {
		if _, ok := vals[k]; !ok {
			t.Fatalf("unknown attribute %q", k)
		}
		vals[k] = v
	}
	return tftypes.NewValue(obj, vals)
}

// lbPlan is what the plan and the configuration carry for the attributes ModifyPlan reads.
type lbPlan struct {
	group       lbGroup
	description int  // the configuration: cfgNull / cfgKnown / cfgUnknown
	portUnknown bool // port is unknown as a whole

	// carriedDescription is a description the plan has but the configuration does not: on an
	// update plan the provider carries the panel's value over from state (UseStateForUnknown).
	carriedDescription string
}

// runLbModifyPlan runs LbResource.ModifyPlan with the plan equal to the configuration, except
// for carriedDescription. A nil state is a create plan.
func runLbModifyPlan(t *testing.T, state *lbGroup, plan lbPlan) resource.ModifyPlanResponse {
	t.Helper()
	obj, sch := lbObjType(t)
	configured := map[string]tftypes.Value{
		"description":   descriptionCfg(plan.description),
		"backend_group": plan.group.raw(),
	}
	if plan.portUnknown {
		configured["port"] = tftypes.NewValue(obj.AttributeTypes["port"], tftypes.UnknownValue)
	}
	configRaw := lbRaw(t, configured)

	planned := maps.Clone(configured)
	if plan.carriedDescription != "" {
		planned["description"] = nirStr(plan.carriedDescription)
	}
	planRaw := lbRaw(t, planned)

	stateRaw := tftypes.NewValue(obj, nil)
	if state != nil {
		stateRaw = lbRaw(t, map[string]tftypes.Value{"backend_group": state.raw()})
	}
	req := resource.ModifyPlanRequest{
		Config: tfsdk.Config{Schema: sch, Raw: configRaw},
		Plan:   tfsdk.Plan{Schema: sch, Raw: planRaw},
		State:  tfsdk.State{Schema: sch, Raw: stateRaw},
	}
	resp := resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: sch, Raw: planRaw}}
	(&LbResource{}).ModifyPlan(context.Background(), req, &resp)
	return resp
}

// A balancer in node-pool (CCM) mode must not carry a description. The pool of a balancer
// created together with its pool is not known at plan time, but that does not make the
// balancer any less a CCM one — unless the other mode is in use, or nothing can be told yet.
func TestLbModifyPlan_CCMDescription(t *testing.T) {
	const wantErr = "description not configurable for CCM load balancers"
	descPath := path.Root("description")

	tests := []struct {
		name string
		plan lbPlan
		want string // error summary; "" = plan accepted
	}{
		{"vm_ids with description", lbPlan{group: lbBlock(cfgKnown, cfgNull), description: cfgKnown}, ""},
		{"known pool with description", lbPlan{group: lbBlock(cfgNull, cfgKnown), description: cfgKnown}, wantErr},
		{"known pool without description", lbPlan{group: lbBlock(cfgNull, cfgKnown)}, ""},

		// node_pool_id known only after apply
		{"pool unknown, vm_ids absent, description set",
			lbPlan{group: lbBlock(cfgNull, cfgUnknown), description: cfgKnown}, wantErr},
		{"pool unknown, vm_ids absent, no description", lbPlan{group: lbBlock(cfgNull, cfgUnknown)}, ""},
		{"pool unknown, vm_ids absent, description unknown",
			lbPlan{group: lbBlock(cfgNull, cfgUnknown), description: cfgUnknown}, ""},
		{"pool unknown, vm_ids set, description set",
			lbPlan{group: lbBlock(cfgKnown, cfgUnknown), description: cfgKnown}, ""},

		// vm_ids known only after apply
		{"vm_ids unknown, pool absent, description set",
			lbPlan{group: lbBlock(cfgUnknown, cfgNull), description: cfgKnown}, ""},
		{"vm_ids unknown, pool known, description set",
			lbPlan{group: lbBlock(cfgUnknown, cfgKnown), description: cfgKnown}, wantErr},

		// nothing can be told
		{"backend_group absent, description set", lbPlan{group: lbGroup{}, description: cfgKnown}, ""},
		{"both modes unknown, description set",
			lbPlan{group: lbBlock(cfgUnknown, cfgUnknown), description: cfgKnown}, ""},
		{"backend_group unknown as a whole, description set",
			lbPlan{group: lbUnknownBlock, description: cfgKnown}, ""},
		{"backend_group unknown as a whole, port unknown as a whole",
			lbPlan{group: lbUnknownBlock, description: cfgKnown, portUnknown: true}, ""},
		{"port unknown as a whole next to a CCM balancer",
			lbPlan{group: lbBlock(cfgNull, cfgKnown), description: cfgKnown, portUnknown: true}, wantErr},
		{"port unknown as a whole next to a vm_ids balancer",
			lbPlan{group: lbBlock(cfgKnown, cfgNull), portUnknown: true}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := runLbModifyPlan(t, nil, tc.plan)
			assertValidateDiags(t, resp.Diagnostics, tc.want, descPath)
		})
	}
}

// The rule is about what the user wrote. On an update plan of a CCM balancer the plan carries
// the panel's "CCM: <name>" over from state although the configuration has no description;
// reading it from the plan would reject every plan of such a balancer.
func TestLbModifyPlan_CCMDescriptionIsReadFromConfig(t *testing.T) {
	const wantErr = "description not configurable for CCM load balancers"
	poolMode := lbBlock(cfgNull, cfgKnown)

	tests := []struct {
		name string
		plan lbPlan
		want string // error summary; "" = plan accepted
	}{
		{"carried over from state, none configured",
			lbPlan{group: poolMode, carriedDescription: "CCM: lb-1"}, ""},
		{"carried over from state, pool known after apply",
			lbPlan{group: lbBlock(cfgNull, cfgUnknown), carriedDescription: "CCM: lb-1"}, ""},
		{"configured on an existing CCM balancer", lbPlan{group: poolMode, description: cfgKnown}, wantErr},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := runLbModifyPlan(t, &poolMode, tc.plan)
			assertValidateDiags(t, resp.Diagnostics, tc.want, path.Root("description"))
		})
	}
}

// A switch between the two modes replaces the balancer; a change within one mode is an
// ordinary update. A mode that is not known yet counts only when the other is known absent.
func TestLbModifyPlan_ModeSwitch(t *testing.T) {
	vmMode := lbBlock(cfgKnown, cfgNull)
	poolMode := lbBlock(cfgNull, cfgKnown)

	tests := []struct {
		name        string
		state       lbGroup
		plan        lbGroup
		wantReplace bool
	}{
		{"vm_ids to pool", vmMode, poolMode, true},
		{"pool to vm_ids", poolMode, vmMode, true},
		{"vm_ids to vm_ids", vmMode, vmMode, false},
		{"pool to pool", poolMode, poolMode, false},

		{"vm_ids to a pool known after apply", vmMode, lbBlock(cfgNull, cfgUnknown), true},
		{"pool to vm_ids known after apply", poolMode, lbBlock(cfgUnknown, cfgNull), true},
		{"pool to a pool known after apply", poolMode, lbBlock(cfgNull, cfgUnknown), false},
		{"vm_ids to vm_ids known after apply", vmMode, lbBlock(cfgUnknown, cfgNull), false},
		{"vm_ids to vm_ids next to a pool known after apply", vmMode, lbBlock(cfgKnown, cfgUnknown), false},

		{"vm_ids to both unknown", vmMode, lbBlock(cfgUnknown, cfgUnknown), false},
		{"no backend_group in state to a pool", lbGroup{}, poolMode, false},
		{"pool to no backend_group", poolMode, lbGroup{}, false},
		{"pool to backend_group unknown as a whole", poolMode, lbUnknownBlock, false},
		{"vm_ids to backend_group unknown as a whole", vmMode, lbUnknownBlock, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := runLbModifyPlan(t, &tc.state, lbPlan{group: tc.plan})
			if resp.Diagnostics.HasError() {
				t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
			}
			replace := false
			for _, p := range resp.RequiresReplace {
				if p.Equal(path.Root("backend_group")) {
					replace = true
				}
			}
			if replace != tc.wantReplace {
				t.Errorf("RequiresReplace = %v, want backend_group replace = %v", resp.RequiresReplace, tc.wantReplace)
			}
		})
	}
}

// A destroy plan has nothing to check.
func TestLbModifyPlan_destroyUntouched(t *testing.T) {
	obj, sch := lbObjType(t)
	state := lbRaw(t, map[string]tftypes.Value{"backend_group": lbBlock(cfgNull, cfgKnown).raw()})
	null := tftypes.NewValue(obj, nil)
	req := resource.ModifyPlanRequest{
		Config: tfsdk.Config{Schema: sch, Raw: null},
		Plan:   tfsdk.Plan{Schema: sch, Raw: null},
		State:  tfsdk.State{Schema: sch, Raw: state},
	}
	resp := resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: sch, Raw: null}}
	(&LbResource{}).ModifyPlan(context.Background(), req, &resp)
	if len(resp.Diagnostics) != 0 || len(resp.RequiresReplace) != 0 {
		t.Errorf("destroy plan: diagnostics %v, RequiresReplace %v", resp.Diagnostics, resp.RequiresReplace)
	}
}

// backendMode over every combination of vm_ids and node_pool_id being unset, set or not known yet.
func TestLbBackendMode_unknownValues(t *testing.T) {
	vms := func(kind int) types.Set {
		switch kind {
		case cfgKnown:
			return types.SetValueMust(types.StringType, []attr.Value{types.StringValue("vm-1")})
		case cfgUnknown:
			return types.SetUnknown(types.StringType)
		}
		return types.SetNull(types.StringType)
	}
	pool := func(kind int) types.Int64 {
		switch kind {
		case cfgKnown:
			return types.Int64Value(5)
		case cfgUnknown:
			return types.Int64Unknown()
		}
		return types.Int64Null()
	}

	tests := []struct {
		name              string
		vms, pool         int
		wantVMs, wantPool bool
	}{
		{"neither", cfgNull, cfgNull, false, false},
		{"vm_ids", cfgKnown, cfgNull, true, false},
		{"pool", cfgNull, cfgKnown, false, true},
		{"both set", cfgKnown, cfgKnown, true, true},

		{"pool unknown, vm_ids absent", cfgNull, cfgUnknown, false, true},
		{"pool unknown, vm_ids set", cfgKnown, cfgUnknown, true, false},
		{"vm_ids unknown, pool absent", cfgUnknown, cfgNull, true, false},
		{"vm_ids unknown, pool set", cfgUnknown, cfgKnown, false, true},
		{"both unknown", cfgUnknown, cfgUnknown, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotVMs, gotPool := backendMode(&LbBackendGroupModel{VMIDs: vms(tc.vms), NodePoolID: pool(tc.pool)})
			if gotVMs != tc.wantVMs || gotPool != tc.wantPool {
				t.Errorf("backendMode = (%v,%v), want (%v,%v)", gotVMs, gotPool, tc.wantVMs, tc.wantPool)
			}
		})
	}
}
