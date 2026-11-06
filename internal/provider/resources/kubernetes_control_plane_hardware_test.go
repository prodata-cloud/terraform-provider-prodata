package resources

import (
	"context"
	"encoding/json"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"net/http"
	"net/http/httptest"
	"sync"
	"terraform-provider-prodata/internal/client"
	"testing"
	"time"
)

func TestControlPlaneHardwareResolvesFlavorAndPreservesCluster(t *testing.T) {
	for _, tc := range []struct {
		name      string
		alias     bool
		badAck    bool
		wantError bool
	}{
		{"explicit flavor", false, false, false}, {"size alias", true, false, false}, {"unsupported acknowledgement", false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var mu sync.Mutex
			puts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if r.Header.Get("X-Region") != "TEST" || r.Header.Get("X-Project-Tag") != "project-a" {
					t.Error("scope lost")
				}
				switch r.Method + " " + r.URL.Path {
				case "GET /panel-main/api/kubernetes/getMasterNodeConfig/false":
					_ = json.NewEncoder(w).Encode(map[string]any{"error": 0, "data": []map[string]any{{"id": 1, "cpu": 2, "ram": 4, "ssd": 20, "isHa": false}, {"id": 2, "cpu": 4, "ram": 8, "ssd": 40, "isHa": false}, {"id": 3, "cpu": 8, "ram": 16, "ssd": 80, "isHa": false}}})
				case "PUT /panel-main/api/v2/kubernetes/clusters/313/hardware":
					puts++
					var order map[string]int64
					if err := json.NewDecoder(r.Body).Decode(&order); err != nil {
						t.Error(err)
					}
					if len(order) != 1 || order["masterFlavorId"] != 2 {
						t.Errorf("bad order: %v", order)
					}
					data := map[string]any{"protocolVersion": 1, "masterFlavorId": 2, "vcpu": 4, "ram": 8, "diskSize": 40, "ready": true}
					if tc.badAck {
						delete(data, "ready")
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": data})
				default:
					t.Errorf("unexpected request, cluster must not be recreated: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			c, err := client.New(client.Config{APIBaseURL: server.URL, APIKeyID: "fake", APISecretKey: "fake", Region: "TEST", ProjectTag: "project-a"})
			if err != nil {
				t.Fatal(err)
			}
			r := &K8sClusterResource{c: c}
			state := K8sClusterModel{ID: types.Int64Value(313), MasterFlavorID: types.Int64Value(1)}
			plan := K8sClusterModel{ID: state.ID, MasterFlavorID: types.Int64Value(2), HighAvailability: types.BoolValue(false), ControlPlaneSize: types.StringNull()}
			if tc.alias {
				plan.ControlPlaneSize = types.StringValue("medium")
				plan.MasterFlavorID = types.Int64Unknown()
			}
			err = r.resizeControlPlane(ctx, 313, &state, &plan, &client.RequestOpts{Region: "TEST", ProjectTag: "project-a"})
			if (err != nil) != tc.wantError {
				t.Fatalf("error: %v", err)
			}
			if state.ID.ValueInt64() != 313 || state.MasterFlavorID.ValueInt64() != 1 {
				t.Fatal("operation overwrote prior state")
			}
			if !tc.wantError && plan.MasterFlavorID.ValueInt64() != 2 {
				t.Fatal("flavor was not resolved")
			}
			mu.Lock()
			defer mu.Unlock()
			if puts != 1 {
				t.Errorf("mutations=%d", puts)
			}
		})
	}
}

func TestControlPlaneHardwarePlanDoesNotReplaceCluster(t *testing.T) {
	ctx := context.Background()
	r := &K8sClusterResource{}
	var sch resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sch)
	old := k8sRaw(t, map[string]tftypes.Value{"id": nirNum(313), "master_flavor_id": nirNum(1), "control_plane_size": nirStr("small"), "kubernetes_version": nirStr("v1.31.4"), "high_availability": credBool(false)})
	state := tfsdk.State{Schema: sch.Schema, Raw: old}
	plan := tfsdk.Plan{Schema: sch.Schema, Raw: old}
	if d := plan.SetAttribute(ctx, path.Root("control_plane_size"), "medium"); d.HasError() {
		t.Fatal(d)
	}
	cfg := plan
	if d := cfg.SetAttribute(ctx, path.Root("master_flavor_id"), types.Int64Null()); d.HasError() {
		t.Fatal(d)
	}
	resp := resource.ModifyPlanResponse{Plan: plan}
	r.ModifyPlan(ctx, resource.ModifyPlanRequest{State: state, Plan: plan, Config: tfsdk.Config(cfg)}, &resp)
	if resp.Diagnostics.HasError() || len(resp.RequiresReplace) > 0 {
		t.Fatalf("alias upgrade plan: %v / %v", resp.Diagnostics, resp.RequiresReplace)
	}
	var flavor types.Int64
	if d := resp.Plan.GetAttribute(ctx, path.Root("master_flavor_id"), &flavor); d.HasError() {
		t.Fatal(d)
	}
	if !flavor.IsUnknown() {
		t.Fatal("size alias retained stale master flavor in plan")
	}
	attribute, ok := sch.Schema.Attributes["master_flavor_id"].(schema.Int64Attribute)
	if !ok {
		t.Fatal("master flavor must be numeric")
	}
	for _, modifier := range attribute.PlanModifiers {
		result := planmodifier.Int64Response{PlanValue: types.Int64Value(2)}
		modifier.PlanModifyInt64(ctx, planmodifier.Int64Request{State: state, Plan: plan, StateValue: types.Int64Value(1), PlanValue: types.Int64Value(2)}, &result)
		if result.RequiresReplace {
			t.Fatal("flavor change would replace cluster")
		}
	}
}
