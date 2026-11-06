package resources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// This is the provider's side of the project-scoped API contract. The fake panel
// deliberately stores the project from the incoming header; backend tests must
// separately prove that the real create endpoint honors that header.
func TestK8sClusterResource_ProjectScopeCreateAndRead(t *testing.T) {
	for _, tc := range []struct {
		name        string
		resourceTag string
		wantTag     string
	}{
		{name: "provider default", wantTag: "provider-project"},
		{name: "resource override", resourceTag: "resource-project", wantTag: "resource-project"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const base = "/panel-main/api/kubernetes"
			projectIDs := map[string]int64{"provider-project": 89, "resource-project": 144}
			var mu sync.Mutex
			var storedProject string
			calls := map[string]int{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				mu.Lock()
				defer mu.Unlock()

				call := req.Method + " " + req.URL.Path
				calls[call]++
				project := req.Header.Get("X-Project-Tag")
				if project != tc.wantTag {
					t.Errorf("%s: X-Project-Tag = %q, want %q", call, project, tc.wantTag)
				}
				if region := req.Header.Get("X-Region"); region != "TEST" {
					t.Errorf("%s: X-Region = %q, want TEST", call, region)
				}

				cluster := func(status string) map[string]any {
					body := credClusterBody(credClusterView{
						status: status, version: "v1.31.4", secret: credB64(credKubeconfigYAML()),
					})
					body["projectId"] = projectIDs[storedProject]
					return body
				}
				var data any
				switch call {
				case "GET /panel-main/api/v2/local-networks/7":
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{
						"success": true,
						"data": map[string]any{
							"id": 7, "name": "cluster-net", "cidr": "10.0.1.0/24", "gateway": "10.0.1.1",
						},
					})
					return
				case "GET " + base + "/getMasterNodeConfig/false":
					data = []map[string]any{{"id": 7, "cpu": 2, "ram": 4, "ssd": 20, "isHa": false}}
				case "GET " + base + "/getClusters":
					data = []any{}
					if storedProject != "" && storedProject == project {
						data = []any{cluster(client.ClusterStatusSuccess)}
					}
				case "POST " + base + "/createCluster":
					storedProject = project
					data = cluster("NEW")
				case "GET " + base + "/getCluster/313":
					if storedProject == "" || storedProject != project {
						http.NotFound(w, req)
						return
					}
					data = cluster(client.ClusterStatusSuccess)
				default:
					t.Errorf("unexpected request %s", call)
					http.NotFound(w, req)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"error": 0, "errMessage": nil, "data": data})
			}))
			t.Cleanup(server.Close)

			c, err := client.New(client.Config{
				APIBaseURL: server.URL, APIKeyID: "fake-key", APISecretKey: "fake-secret",
				Region: "TEST", ProjectTag: "provider-project",
			})
			if err != nil {
				t.Fatal(err)
			}
			_, sch := k8sObjType(t)
			values := map[string]tftypes.Value{
				"name": nirStr("tfc"), "kubernetes_version": nirStr("v1.31.4"),
				"high_availability": credBool(false), "network_id": nirNum(7),
				"pod_cidr": nirStr("10.244.0.0/16"), "master_flavor_id": nirNum(7),
				"ssh_access_enabled": credBool(false), "public_endpoint_enabled": credBool(false),
				"node_ip_range": nirStr("10.0.1.10-10.0.1.20"),
				"region":        nirStrUnknown(), "project_tag": nirStrUnknown(),
			}
			if tc.resourceTag != "" {
				values["project_tag"] = nirStr(tc.resourceTag)
			}
			plan := k8sRaw(t, values)
			created := resource.CreateResponse{State: tfsdk.State{Schema: sch, Raw: tftypes.NewValue(plan.Type(), nil)}}
			r := &K8sClusterResource{c: c}
			r.Create(context.Background(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: sch, Raw: plan}}, &created)
			if created.Diagnostics.HasError() {
				t.Fatalf("create: %v", created.Diagnostics)
			}
			model := credStateModel(t, created.State)
			if model.ID.ValueInt64() != 313 || model.ProjectTag.ValueString() != tc.wantTag {
				t.Fatalf("created id/project = %v/%v, want 313/%s", model.ID, model.ProjectTag, tc.wantTag)
			}

			clusters, err := c.ListClusters(context.Background(), &client.RequestOpts{ProjectTag: tc.wantTag})
			if err != nil {
				t.Fatalf("list created cluster: %v", err)
			}
			if len(clusters) != 1 || clusters[0].ID != 313 || clusters[0].ProjectID != projectIDs[tc.wantTag] {
				t.Fatalf("created cluster is missing from selected project %q: %+v", tc.wantTag, clusters)
			}

			// Refresh must retain the resource's stored scope even after the provider default changes.
			mu.Lock()
			readsBeforeRefresh := calls["GET "+base+"/getCluster/313"]
			mu.Unlock()
			c.ProjectTag = "different-provider-project"
			refreshed := resource.ReadResponse{State: created.State}
			r.Read(context.Background(), resource.ReadRequest{State: created.State}, &refreshed)
			if refreshed.Diagnostics.HasError() || refreshed.State.Raw.IsNull() {
				t.Fatalf("refresh lost the cluster: %v", refreshed.Diagnostics)
			}
			model = credStateModel(t, refreshed.State)
			if model.ProjectTag.ValueString() != tc.wantTag || model.ID.ValueInt64() != 313 {
				t.Errorf("refreshed id/project = %v/%v, want 313/%s", model.ID, model.ProjectTag, tc.wantTag)
			}

			mu.Lock()
			defer mu.Unlock()
			if storedProject != tc.wantTag {
				t.Errorf("fake panel stored project %q, want %q", storedProject, tc.wantTag)
			}
			for _, call := range []string{
				"GET /panel-main/api/v2/local-networks/7",
				"GET " + base + "/getMasterNodeConfig/false",
				"GET " + base + "/getClusters",
				"POST " + base + "/createCluster",
				"GET " + base + "/getCluster/313",
			} {
				if calls[call] == 0 {
					t.Errorf("expected request was never made: %s", call)
				}
			}
			if calls["GET "+base+"/getCluster/313"] <= readsBeforeRefresh {
				t.Error("refresh did not read the persisted cluster")
			}
		})
	}
}
