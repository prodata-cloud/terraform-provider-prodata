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
	"net/http"
	"net/http/httptest"
	"sync"
	"terraform-provider-prodata/internal/client"
	"testing"
	"time"
)

func TestPoolHardwarePlanUpdatesAndRejectsShrink(t *testing.T) {
	ctx := context.Background()
	r := &K8sNodePoolResource{}
	var schemaResponse resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResponse)
	initial, _ := dependentProjectPlan(t, r, "node_pool", "project-a")
	if d := initial.SetAttribute(ctx, path.Root("id"), int64(7)); d.HasError() {
		t.Fatal(d)
	}
	state := tfsdk.State(initial)
	plan := initial
	for name, want := range map[string]int64{"vcpu": 4, "ram": 8, "disk_size": 40} {
		if d := plan.SetAttribute(ctx, path.Root(name), want); d.HasError() {
			t.Fatal(d)
		}
		attribute, ok := schemaResponse.Schema.Attributes[name].(schema.Int64Attribute)
		if !ok {
			t.Fatalf("%s must be numeric", name)
		}
		for _, modifier := range attribute.PlanModifiers {
			response := planmodifier.Int64Response{PlanValue: types.Int64Value(want)}
			modifier.PlanModifyInt64(ctx, planmodifier.Int64Request{State: state, Plan: plan, StateValue: types.Int64Value(2), PlanValue: types.Int64Value(want)}, &response)
			if response.RequiresReplace {
				t.Errorf("%s would replace the whole pool", name)
			}
		}
	}
	response := resource.ModifyPlanResponse{Plan: plan}
	r.ModifyPlan(ctx, resource.ModifyPlanRequest{Config: tfsdk.Config(plan), Plan: plan, State: state}, &response)
	if response.Diagnostics.HasError() || len(response.RequiresReplace) != 0 {
		t.Fatalf("hardware grow plan: %v / %v", response.Diagnostics, response.RequiresReplace)
	}
	if len(response.Diagnostics.Warnings()) == 0 {
		t.Fatal("worker replacement must be explained in the plan")
	}
	if d := plan.SetAttribute(ctx, path.Root("disk_size"), int64(10)); d.HasError() {
		t.Fatal(d)
	}
	response = resource.ModifyPlanResponse{Plan: plan}
	r.ModifyPlan(ctx, resource.ModifyPlanRequest{Config: tfsdk.Config(plan), Plan: plan, State: state}, &response)
	if !response.Diagnostics.HasError() || len(response.RequiresReplace) != 0 {
		t.Fatal("shrink must fail instead of replacing the pool")
	}
}

func TestPoolHardwareUpdateKeepsPoolIDAndDoesNotRecreate(t *testing.T) {
	for _, tc := range []struct {
		name      string
		disk      int64
		badAck    bool
		wantError bool
	}{
		{"grow", 40, false, false}, {"shrink", 10, false, true}, {"incomplete acknowledgement", 40, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var mu sync.Mutex
			puts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if req.Header.Get("X-Project-Tag") != "project-a" || req.Header.Get("X-Region") != "TEST" {
					t.Error("scope lost")
				}
				switch req.Method + " " + req.URL.Path {
				case "GET /panel-main/api/kubernetes/getCluster/313":
					_ = json.NewEncoder(w).Encode(map[string]any{"error": 0, "data": map[string]any{"id": 313, "blocked": false, "status": map[string]any{"name": "SUCCESS"}}})
				case "PUT /panel-main/api/v2/kubernetes/node-pools/7/hardware":
					puts++
					var body client.NodePoolHardwareRequest
					if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if body.VCPU != 4 || body.RAM != 8 || body.DiskSize != 40 {
						t.Errorf("wrong sizing: %+v", body)
					}
					data := map[string]any{"protocolVersion": 1, "vcpu": 4, "ram": 8, "diskSize": 40, "ready": true}
					if tc.badAck {
						delete(data, "ready")
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": data})
				case "GET /panel-main/api/kubernetes/getK8SNodePool/7":
					_ = json.NewEncoder(w).Encode(map[string]any{"error": 0, "data": map[string]any{"id": 7, "clusterId": 313, "poolName": "workers", "cpu": 4, "ram": 8, "ssd": 40, "nodeCount": 1, "nodeSubnet": 24, "status": map[string]any{"name": "SUCCESS"}}})
				default:
					t.Errorf("unexpected request (must not recreate/scale pool): %s %s", req.Method, req.URL.Path)
					http.NotFound(w, req)
				}
			}))
			defer server.Close()
			c, err := client.New(client.Config{APIBaseURL: server.URL, APIKeyID: "fake", APISecretKey: "fake", Region: "TEST", ProjectTag: "project-a"})
			if err != nil {
				t.Fatal(err)
			}
			r := &K8sNodePoolResource{c: c}
			initial, _ := dependentProjectPlan(t, r, "node_pool", "project-a")
			for key, value := range map[string]any{"id": int64(7), "region": "TEST", "status": "SUCCESS", "node_subnet": int64(24)} {
				if d := initial.SetAttribute(ctx, path.Root(key), value); d.HasError() {
					t.Fatal(d)
				}
			}
			state := tfsdk.State(initial)
			plan := tfsdk.Plan{Schema: initial.Schema, Raw: initial.Raw}
			for key, value := range map[string]int64{"vcpu": 4, "ram": 8, "disk_size": tc.disk} {
				if d := plan.SetAttribute(ctx, path.Root(key), value); d.HasError() {
					t.Fatal(d)
				}
			}
			resp := resource.UpdateResponse{State: state}
			r.Update(ctx, resource.UpdateRequest{State: state, Plan: plan}, &resp)
			if resp.Diagnostics.HasError() != tc.wantError {
				t.Fatalf("diagnostics: %v", resp.Diagnostics)
			}
			var result K8sNodePoolModel
			if d := resp.State.Get(ctx, &result); d.HasError() {
				t.Fatal(d)
			}
			if result.ID.ValueInt64() != 7 {
				t.Fatal("pool identity changed")
			}
			if tc.wantError {
				if result.DiskSize.ValueInt64() != 20 {
					t.Fatal("failed operation overwrote prior state")
				}
			} else if result.DiskSize.ValueInt64() != 40 {
				t.Fatal("new sizing was not recorded")
			}
			mu.Lock()
			defer mu.Unlock()
			if tc.disk < 20 && puts != 0 {
				t.Fatal("shrink issued a mutation")
			}
		})
	}
}
