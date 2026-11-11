package resources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// These endpoints inherit the parent's project even on older servers that ignore
// a conflicting request header. Reject a conflict before sending a create request.
func TestDependentResources_CreateProjectScope(t *testing.T) {
	for _, kind := range []string{"node_pool", "ccm_lb"} {
		for _, tc := range []struct {
			name, providerTag, resourceTag, parentTag, wantError string
			listFails, poolReadFails                             bool
		}{
			{name: "provider project matches parent", providerTag: "project-b", parentTag: "project-b"},
			{name: "resource override matches parent", providerTag: "project-a", resourceTag: "project-b", parentTag: "project-b"},
			{name: "parent outside selected project", providerTag: "project-a", resourceTag: "project-b", parentTag: "project-a", wantError: "outside the selected project"},
			{name: "cannot verify project", providerTag: "project-a", resourceTag: "project-b", parentTag: "project-b", listFails: true, wantError: "project lookup unavailable"},
			{name: "pool lookup permission denied", providerTag: "project-a", resourceTag: "project-b", parentTag: "project-b", poolReadFails: true, wantError: "Kubernetes read permissions"},
			{name: "unspecified project preserves inheritance", parentTag: "project-a"},
		} {
			if tc.poolReadFails && kind != "ccm_lb" {
				continue
			}
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				const base = "/panel-main/api/kubernetes"
				selectedTag := tc.resourceTag
				if selectedTag == "" {
					selectedTag = tc.providerTag
				}
				var mu sync.Mutex
				writes, lists := 0, 0
				reserves := 0
				poolCreated := false
				pool := map[string]any{
					"id": 7, "poolName": "workers", "clusterId": 313,
					"nodeCount": 1, "nodeSubnet": 24, "cpu": 2, "ram": 4, "ssd": 20,
					"status": map[string]any{"name": "SUCCESS"}, "autoscaleEnabled": false,
				}
				lb := map[string]any{
					"id": 300, "name": "workers-lb", "source": "CCM", "protocol": "TCP",
					"userNet": 9, "isPublic": false, "projectTag": tc.parentTag,
					"status": map[string]any{"name": "SUCCESS"},
					"ports":  []map[string]any{{"srcPort": 80, "dstPort": 8080}},
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					mu.Lock()
					defer mu.Unlock()
					call := req.Method + " " + req.URL.Path
					if kind == "node_pool" && strings.HasPrefix(req.URL.Path, "/panel-main/api/v2/kubernetes/clusters/313/node-pool-operations/") {
						if tag := req.Header.Get("X-Project-Tag"); tag != selectedTag {
							t.Errorf("durable pool scope %q, want %q", tag, selectedTag)
						}
						var data any
						if strings.HasSuffix(req.URL.Path, "/capabilities") {
							data = poolLifecycleCapabilities()
						} else {
							suffix := strings.TrimPrefix(req.URL.Path, "/panel-main/api/v2/kubernetes/clusters/313/node-pool-operations/")
							requestUUID := strings.TrimSuffix(strings.TrimSuffix(suffix, "/reservations"), "/submit")
							state := "RESERVED"
							if req.Method == http.MethodPost && strings.HasSuffix(suffix, "/reservations") {
								reserves++
							}
							if req.Method == http.MethodPost && strings.HasSuffix(suffix, "/submit") {
								writes++
								poolCreated = true
								state = "SUCCESS"
							}
							if poolCreated {
								state = "SUCCESS"
							}
							data = poolLifecycleView(requestUUID, state, "CREATE", 7)
						}
						w.Header().Set("Content-Type", "application/json")
						_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": data})
						return
					}

					if tag := req.Header.Get("X-Project-Tag"); tag != selectedTag {
						t.Errorf("%s: project header %q, want %q", call, tag, selectedTag)
					}
					w.Header().Set("Content-Type", "application/json")
					var data any
					switch call {
					case "GET " + base + "/getClusters":
						lists++
						if tc.listFails {
							w.WriteHeader(http.StatusInternalServerError)
							_ = json.NewEncoder(w).Encode(map[string]any{"error": 500, "errMessage": "project lookup unavailable"})
							return
						}
						data = []any{}
						if tc.parentTag == req.Header.Get("X-Project-Tag") {
							data = []map[string]any{{"id": 313, "name": "parent", "status": map[string]any{"name": "SUCCESS"}}}
						}
					case "GET " + base + "/getCluster/313":
						data = map[string]any{"id": 313, "status": map[string]any{"name": "SUCCESS"}, "blocked": false}
					case "GET " + base + "/getNodePoolsByClusters/313":
						data = []any{}
						if poolCreated {
							data = []any{pool}
						}
					case "GET " + base + "/getK8SNodePool/7":
						if tc.poolReadFails {
							w.WriteHeader(http.StatusForbidden)
							_ = json.NewEncoder(w).Encode(map[string]any{"error": 403, "errMessage": "Access denied"})
							return
						}
						data = pool
					case "POST " + base + "/createNewNodePool":
						writes++
						poolCreated = true
					case "POST /panel-main/api/ccm/loadbalancer/create":
						writes++
						data = lb
					case "GET /panel-main/api/loadbalancer/getLoadBalancer":
						data = lb
					default:
						t.Errorf("unexpected request %s", call)
						http.NotFound(w, req)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"error": 0, "errMessage": nil, "data": data})
				}))
				t.Cleanup(server.Close)
				c, err := client.New(client.Config{
					APIBaseURL: server.URL, APIKeyID: "fake-key", APISecretKey: "fake-secret",
					Region: "TEST", ProjectTag: tc.providerTag,
				})
				if err != nil {
					t.Fatal(err)
				}
				var r resource.Resource
				if kind == "node_pool" {
					r = &K8sNodePoolResource{c: c}
				} else {
					r = &LbResource{c: c}
				}
				plan, state := dependentProjectPlan(t, r, kind, tc.resourceTag)
				resp := resource.CreateResponse{State: state}
				r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)
				if tc.wantError != "" {
					if !hasDiag(resp.Diagnostics, diag.SeverityError, tc.wantError) {
						t.Errorf("expected project error containing %q, got %v", tc.wantError, resp.Diagnostics)
					}
					if tc.listFails && !hasDiag(resp.Diagnostics, diag.SeverityError, "Kubernetes read permissions") {
						t.Errorf("unavailable project lookup must explain required permissions: %v", resp.Diagnostics)
					}
					if !resp.State.Raw.IsNull() {
						t.Error("rejected create wrote resource state")
					}
				} else {
					if resp.Diagnostics.HasError() {
						t.Fatalf("create: %v", resp.Diagnostics)
					}
					var tag types.String
					if d := resp.State.GetAttribute(context.Background(), path.Root("project_tag"), &tag); d.HasError() {
						t.Fatal(d)
					}
					if tag.ValueString() != selectedTag {
						t.Errorf("state project %q, want %q", tag.ValueString(), selectedTag)
					}
				}
				mu.Lock()
				defer mu.Unlock()
				if tc.wantError != "" && (writes != 0 || reserves != 0) {
					t.Errorf("rejected project sent %d mutation requests", writes)
				}
				if tc.wantError == "" && writes == 0 {
					t.Error("matching parent project was not created")
				}
				if selectedTag == "" && lists != 0 {
					t.Error("unspecified project unexpectedly required a scoped cluster list")
				}
			})
		}
	}
}

func dependentProjectPlan(t *testing.T, r resource.Resource, kind, resourceTag string) (tfsdk.Plan, tfsdk.State) {
	t.Helper()
	var sch resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &sch)
	obj, ok := sch.Schema.Type().TerraformType(context.Background()).(tftypes.Object)
	if !ok {
		t.Fatal("resource schema must be an object")
	}
	values := make(map[string]tftypes.Value, len(obj.AttributeTypes))
	for name, typ := range obj.AttributeTypes {
		values[name] = tftypes.NewValue(typ, nil)
	}
	if resourceTag != "" {
		values["project_tag"] = nirStr(resourceTag)
	}
	if kind == "node_pool" {
		values["cluster_id"], values["name"] = nirNum(313), nirStr("workers")
		values["vcpu"], values["ram"], values["disk_size"], values["node_count"] = nirNum(2), nirNum(4), nirNum(20), nirNum(1)
	} else {
		values["name"], values["type"], values["protocol"], values["network_id"] = nirStr("workers-lb"), nirStr("internal"), nirStr("TCP"), nirNum(9)
		group, ok := obj.AttributeTypes["backend_group"].(tftypes.Object)
		if !ok {
			t.Fatal("backend_group must be an object")
		}
		values["backend_group"] = tftypes.NewValue(group, map[string]tftypes.Value{
			"node_pool_id": nirNum(7), "vm_ids": tftypes.NewValue(group.AttributeTypes["vm_ids"], nil),
			"asg_id": tftypes.NewValue(tftypes.Number, nil),
		})
		ports, ok := obj.AttributeTypes["port"].(tftypes.Set)
		if !ok {
			t.Fatal("port must be a set")
		}
		values["port"] = tftypes.NewValue(ports, []tftypes.Value{tftypes.NewValue(ports.ElementType, map[string]tftypes.Value{
			"port": nirNum(80), "target_port": nirNum(8080),
		})})
	}
	return tfsdk.Plan{Schema: sch.Schema, Raw: tftypes.NewValue(obj, values)},
		tfsdk.State{Schema: sch.Schema, Raw: tftypes.NewValue(obj, nil)}
}
