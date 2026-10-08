package resources

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

var poolAutoscalingType = tftypes.Object{AttributeTypes: map[string]tftypes.Type{
	"min_nodes": tftypes.Number,
	"max_nodes": tftypes.Number,
}}

// poolAutoscaling is the autoscaling attribute as Terraform hands it over: absent (a nil
// pointer), a block with bounds, or unknown as a whole (autoscaling = var.<object>).
type poolAutoscaling struct {
	unknown            bool
	minNodes, maxNodes tftypes.Value
}

func asBlock(minNodes, maxNodes tftypes.Value) *poolAutoscaling {
	return &poolAutoscaling{minNodes: minNodes, maxNodes: maxNodes}
}

func asUnknownWhole() *poolAutoscaling { return &poolAutoscaling{unknown: true} }

func (a *poolAutoscaling) raw() tftypes.Value {
	switch {
	case a == nil:
		return tftypes.NewValue(poolAutoscalingType, nil)
	case a.unknown:
		return tftypes.NewValue(poolAutoscalingType, tftypes.UnknownValue)
	}
	return tftypes.NewValue(poolAutoscalingType, map[string]tftypes.Value{
		"min_nodes": a.minNodes,
		"max_nodes": a.maxNodes,
	})
}

func poolObjType(t *testing.T) (tftypes.Object, schema.Schema) {
	t.Helper()
	var resp resource.SchemaResponse
	NewK8sNodePoolResource().Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}
	obj, ok := resp.Schema.Type().TerraformType(context.Background()).(tftypes.Object)
	if !ok {
		t.Fatal("schema type is not an object")
	}
	return obj, resp.Schema
}

// poolRaw builds a pool object in which every attribute is null except those in set.
func poolRaw(t *testing.T, set map[string]tftypes.Value) tftypes.Value {
	t.Helper()
	obj, _ := poolObjType(t)
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

// runPoolValidateConfig runs K8sNodePoolResource.ValidateConfig over a configuration in which
// every attribute is null except node_count and autoscaling (nil = no autoscaling block).
func runPoolValidateConfig(t *testing.T, nodeCount tftypes.Value, as *poolAutoscaling) diag.Diagnostics {
	t.Helper()
	_, sch := poolObjType(t)
	req := resource.ValidateConfigRequest{
		Config: tfsdk.Config{Schema: sch, Raw: poolRaw(t, map[string]tftypes.Value{
			"node_count":  nodeCount,
			"autoscaling": as.raw(),
		})},
	}
	var resp resource.ValidateConfigResponse
	(&K8sNodePoolResource{}).ValidateConfig(context.Background(), req, &resp)
	return resp.Diagnostics
}

// Terraform validates a configuration before it plans, with variables and data sources still
// unknown. The pool's check treats an unknown node_count or autoscaling bound as "might be
// set", and an autoscaling block that is unknown as a whole (autoscaling = var.<object>, or a
// conditional on such a value) as "might be absent or present": none of the checks can apply
// to it yet, and the block must not fail to decode. These pin that next to the cluster's
// check, which used to treat unknown as unset.
func TestK8sNodePoolValidateConfig(t *testing.T) {
	nullNum := tftypes.NewValue(tftypes.Number, nil)

	nodeCountPath := path.Root("node_count")
	maxNodesPath := path.Root("autoscaling").AtName("max_nodes")

	tests := []struct {
		name      string
		nodeCount tftypes.Value
		as        *poolAutoscaling
		want      string // error summary substring; "" = no diagnostics
		wantPath  path.Path
	}{
		{"fixed size", nirNum(3), nil, "", path.Empty()},
		{"autoscaling only", nullNum, asBlock(nirNum(1), nirNum(5)), "", path.Empty()},
		{"node_count with autoscaling", nirNum(3), asBlock(nirNum(1), nirNum(5)),
			"node_count conflicts with autoscaling", nodeCountPath},
		{"neither node_count nor autoscaling", nullNum, nil,
			"node_count is required without autoscaling", nodeCountPath},

		{"node_count unknown, no autoscaling", nirNumUnknown(), nil, "", path.Empty()},
		{"node_count unknown next to autoscaling", nirNumUnknown(), asBlock(nirNum(1), nirNum(5)), "", path.Empty()},

		{"reversed bounds", nullNum, asBlock(nirNum(5), nirNum(1)), "Invalid autoscaling bounds", maxNodesPath},
		{"equal bounds", nullNum, asBlock(nirNum(3), nirNum(3)), "", path.Empty()},
		{"unknown min", nullNum, asBlock(nirNumUnknown(), nirNum(1)), "", path.Empty()},
		{"unknown max", nullNum, asBlock(nirNum(5), nirNumUnknown()), "", path.Empty()},

		// Whichever way the unknown block resolves, the other attribute is fine on its own, so
		// a conflict or a missing node_count cannot be told yet.
		{"autoscaling unknown as a whole, no node_count", nullNum, asUnknownWhole(), "", path.Empty()},
		{"autoscaling unknown as a whole, node_count set", nirNum(3), asUnknownWhole(), "", path.Empty()},
		{"autoscaling unknown as a whole, node_count unknown", nirNumUnknown(), asUnknownWhole(), "", path.Empty()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertValidateDiags(t, runPoolValidateConfig(t, tc.nodeCount, tc.as), tc.want, tc.wantPath)
		})
	}
}

// poolShape is the part of a pool that ModifyPlan looks at.
type poolShape struct {
	nodeCount tftypes.Value
	as        *poolAutoscaling
	status    tftypes.Value
}

func (s *poolShape) raw(t *testing.T) tftypes.Value {
	t.Helper()
	if s == nil {
		obj, _ := poolObjType(t)
		return tftypes.NewValue(obj, nil)
	}
	return poolRaw(t, map[string]tftypes.Value{
		"node_count":  s.nodeCount,
		"autoscaling": s.as.raw(),
		"status":      s.status,
	})
}

// rawAttr returns one attribute of an object value.
func rawAttr(t *testing.T, obj tftypes.Value, name string) tftypes.Value {
	t.Helper()
	var m map[string]tftypes.Value
	if err := obj.As(&m); err != nil {
		t.Fatalf("object value: %v", err)
	}
	v, ok := m[name]
	if !ok {
		t.Fatalf("no attribute %q", name)
	}
	return v
}

// runPoolModifyPlan runs K8sNodePoolResource.ModifyPlan the way the framework does: the
// response starts out as a copy of the plan. A nil state is a create plan, a nil plan a
// destroy plan; cfgNodeCount is node_count as written in the configuration.
func runPoolModifyPlan(t *testing.T, state, plan *poolShape, cfgNodeCount tftypes.Value) resource.ModifyPlanResponse {
	t.Helper()
	_, sch := poolObjType(t)
	cfg := map[string]tftypes.Value{"node_count": cfgNodeCount}
	if plan != nil {
		cfg["autoscaling"] = plan.as.raw()
	}
	req := resource.ModifyPlanRequest{
		Config: tfsdk.Config{Schema: sch, Raw: poolRaw(t, cfg)},
		Plan:   tfsdk.Plan{Schema: sch, Raw: plan.raw(t)},
		State:  tfsdk.State{Schema: sch, Raw: state.raw(t)},
	}
	resp := resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: sch, Raw: plan.raw(t)}}
	(&K8sNodePoolResource{}).ModifyPlan(context.Background(), req, &resp)
	return resp
}

func TestK8sNodePoolModifyPlan(t *testing.T) {
	running, unknownStatus := nirStr("RUNNING"), nirStrUnknown()
	fixed := func(count int64) *poolShape { return &poolShape{nodeCount: nirNum(count), status: running} }
	scaled := func(count, minNodes, maxNodes int64) *poolShape {
		return &poolShape{nodeCount: nirNum(count), as: asBlock(nirNum(minNodes), nirNum(maxNodes)), status: running}
	}
	// the plan of a pool whose autoscaling block is not known yet; node_count is what the
	// framework planned for it (the prior value, via UseStateForUnknown, unless written)
	whole := func(count tftypes.Value) *poolShape {
		return &poolShape{nodeCount: count, as: asUnknownWhole(), status: running}
	}
	nullNum := tftypes.NewValue(tftypes.Number, nil)

	tests := []struct {
		name         string
		state, plan  *poolShape
		cfgNodeCount tftypes.Value
		wantStatus   tftypes.Value
		wantCount    tftypes.Value
	}{
		// ---- autoscaling unknown as a whole: whether the autoscaler will own the count is open
		{"autoscaling unknown, node_count left to the provider",
			fixed(3), whole(nirNum(3)), nullNum, unknownStatus, nirNumUnknown()},
		{"autoscaling unknown, node_count written in the configuration",
			fixed(3), whole(nirNum(3)), nirNum(3), unknownStatus, nirNum(3)},
		{"autoscaling unknown, node_count unknown too",
			fixed(3), whole(nirNumUnknown()), nirNumUnknown(), unknownStatus, nirNumUnknown()},
		{"autoscaling unknown over an autoscaling pool",
			scaled(2, 1, 3), whole(nirNum(2)), nullNum, unknownStatus, nirNumUnknown()},

		// ---- autoscaling known: what the plan modifier did before
		{"scale a fixed pool", fixed(3), fixed(5), nirNum(5), unknownStatus, nirNum(5)},
		{"turn autoscaling on", fixed(3), scaled(3, 1, 5), nullNum, unknownStatus, nirNumUnknown()},
		{"autoscaling bounds change", scaled(2, 1, 3), scaled(2, 2, 4), nullNum, unknownStatus, nirNumUnknown()},
		{"turn autoscaling off", scaled(2, 1, 3), fixed(4), nirNum(4), unknownStatus, nirNum(4)},
		{"fixed pool unchanged", fixed(3), fixed(3), nirNum(3), running, nirNum(3)},
		{"autoscaling pool unchanged", scaled(2, 1, 3), scaled(2, 1, 3), nullNum, running, nirNum(2)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := runPoolModifyPlan(t, tc.state, tc.plan, tc.cfgNodeCount)
			if resp.Diagnostics.HasError() {
				t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
			}
			if got := rawAttr(t, resp.Plan.Raw, "status"); !got.Equal(tc.wantStatus) {
				t.Errorf("planned status = %v, want %v", got, tc.wantStatus)
			}
			if got := rawAttr(t, resp.Plan.Raw, "node_count"); !got.Equal(tc.wantCount) {
				t.Errorf("planned node_count = %v, want %v", got, tc.wantCount)
			}
		})
	}
}

// A create plan has no prior state to diff and a destroy plan nothing to change: ModifyPlan
// leaves both alone — including a create whose autoscaling block is still unknown.
func TestK8sNodePoolModifyPlan_createAndDestroyUntouched(t *testing.T) {
	unknownPool := &poolShape{nodeCount: nirNumUnknown(), as: asUnknownWhole(), status: nirStrUnknown()}
	existing := &poolShape{nodeCount: nirNum(3), status: nirStr("RUNNING")}

	tests := []struct {
		name        string
		state, plan *poolShape
	}{
		{"create, autoscaling unknown as a whole", nil, unknownPool},
		{"create, fixed size", nil, &poolShape{nodeCount: nirNum(3), status: nirStrUnknown()}},
		{"destroy", existing, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := runPoolModifyPlan(t, tc.state, tc.plan, tc.plan.cfgNodeCount())
			if resp.Diagnostics.HasError() {
				t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
			}
			if want := tc.plan.raw(t); !resp.Plan.Raw.Equal(want) {
				t.Errorf("plan was changed:\n got %v\nwant %v", resp.Plan.Raw, want)
			}
		})
	}
}

// cfgNodeCount is node_count as a configuration would carry it for this plan: null for a
// destroy and for a node_count the provider fills in.
func (s *poolShape) cfgNodeCount() tftypes.Value {
	if s == nil || !s.nodeCount.IsKnown() {
		return tftypes.NewValue(tftypes.Number, nil)
	}
	return s.nodeCount
}
