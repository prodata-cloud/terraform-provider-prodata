package resources

import (
	"context"
	"encoding/json"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"net/http"
	"net/http/httptest"
	"terraform-provider-prodata/internal/client"
	"testing"
)

func TestControlPlaneReadVerifiesRolloutWithoutMutatingCluster(t *testing.T) {
	for _, tc := range []struct {
		name, clusterStatus string
		ready               bool
		code                int
		malformed           bool
		wantStatus          string
		warn                bool
	}{
		{"rollout not ready", "SUCCESS", false, 200, false, "PROCESSING", true},
		{"failed rollout retains failure", "FAIL", false, 200, false, "FAIL", true},
		{"deleting remains deletable", "DELETING", false, 200, false, "DELETING", true},
		{"verified convergence", "SUCCESS", true, 200, false, "SUCCESS", false},
		{"missing old route", "SUCCESS", false, 404, false, "SUCCESS", true},
		{"temporary failure", "FAIL", false, 503, false, "FAIL", true},
		{"incomplete reply", "SUCCESS", true, 200, true, "SUCCESS", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutations, calls := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				if q.Method != "GET" {
					mutations++
				}
				w.Header().Set("Content-Type", "application/json")
				switch q.URL.Path {
				case "/panel-main/api/kubernetes/getCluster/313":
					_ = json.NewEncoder(w).Encode(map[string]any{"error": 0, "data": map[string]any{"id": 313, "name": "tfc", "kuberVersion": "v1.31.4", "status": map[string]any{"name": tc.clusterStatus}, "masterNodeConfig": map[string]any{"id": 2, "cpu": 4, "ram": 8, "ssd": 40}}})
				case "/panel-main/api/v2/kubernetes/clusters/313/hardware":
					calls++
					w.WriteHeader(tc.code)
					data := map[string]any{"protocolVersion": 1, "masterFlavorId": 2, "vcpu": 4, "ram": 8, "diskSize": 40, "ready": tc.ready}
					if tc.malformed {
						delete(data, "ready")
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": data})
				default:
					t.Errorf("unexpected %s", q.URL.Path)
					http.NotFound(w, q)
				}
			}))
			defer srv.Close()
			c, e := client.New(client.Config{APIBaseURL: srv.URL, APIKeyID: "fake", APISecretKey: "fake", Region: "TEST", ProjectTag: "proj"})
			if e != nil {
				t.Fatal(e)
			}
			state, _ := credStateAndPlan(t, "true", "true", false)
			if ds := state.SetAttribute(context.Background(), path.Root("master_flavor_id"), int64(1)); ds.HasError() {
				t.Fatal(ds)
			}
			out := resource.ReadResponse{State: state}
			(&K8sClusterResource{c: c}).Read(context.Background(), resource.ReadRequest{State: state}, &out)
			if out.Diagnostics.HasError() || out.State.Raw.IsNull() || calls != 1 || mutations != 0 {
				t.Fatalf("calls=%d mutations=%d diagnostics=%v", calls, mutations, out.Diagnostics)
			}
			var got K8sClusterModel
			if ds := out.State.Get(context.Background(), &got); ds.HasError() {
				t.Fatal(ds)
			}
			if got.ID.ValueInt64() != 313 || got.Status.ValueString() != tc.wantStatus || (out.Diagnostics.WarningsCount() > 0) != tc.warn {
				t.Fatalf("status=%s diagnostics=%v", got.Status, out.Diagnostics)
			}
			if tc.code == 200 && !tc.malformed && !tc.ready && !got.Blocked.ValueBool() {
				t.Fatal("incomplete rollout was published as unblocked")
			}
		})
	}
}
