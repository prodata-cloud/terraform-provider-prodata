package resources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"terraform-provider-prodata/internal/client"
)

func TestPoolHardwareUnavailableReadRetainsSizingAndAllowsDestroy(t *testing.T) {
	for _, tc := range []struct {
		status string
		code   int
		body   string
	}{
		{"DELETING", 503, `{"success":false,"errors":[{"code":503,"message":"CAPI unavailable"}]}`},
		{"FAIL", 503, `{"success":false,"errors":[{"code":503,"message":"missing deployment"}]}`},
		{"SUCCESS", 404, `{}`},
		{"SUCCESS", 403, `{"success":false,"errors":[{"code":633,"message":"scope unavailable"}]}`},
	} {
		t.Run(tc.status+http.StatusText(tc.code), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			deleted := false
			hardwareCalls := 0
			deletionUUID := ""
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				prefix := "/panel-main/api/v2/kubernetes/clusters/313/node-pool-operations/"
				if strings.HasPrefix(r.URL.Path, prefix) {
					suffix := strings.TrimPrefix(r.URL.Path, prefix)
					var data any
					if suffix == "capabilities" {
						data = poolLifecycleCapabilities()
					} else {
						key := strings.TrimSuffix(strings.TrimSuffix(suffix, "/reservations"), "/submit")
						if deletionUUID != "" && deletionUUID != key {
							t.Error("destroy changed its exact request UUID")
						}
						deletionUUID = key
						if r.Method == http.MethodPost && strings.HasSuffix(suffix, "/submit") {
							deleted = true
						}
						status := "RESERVED"
						if deleted {
							status = "SUCCESS"
						}
						data = poolLifecycleView(key, status, "DELETE", 7)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": data})
					return
				}

				switch r.Method + " " + r.URL.Path {
				case "GET /panel-main/api/kubernetes/getK8SNodePool/7":
					status := tc.status
					if deleted {
						status = "DELETED"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"error": 0, "data": map[string]any{"id": 7, "clusterId": 313, "poolName": "workers", "cpu": 8, "ram": 16, "ssd": 80, "nodeCount": 1, "nodeSubnet": 24, "status": map[string]any{"name": status}}})
				case "GET /panel-main/api/v2/kubernetes/node-pools/7/hardware":
					hardwareCalls++
					w.WriteHeader(tc.code)
					_, _ = w.Write([]byte(tc.body))
				case "GET /panel-main/api/kubernetes/getCluster/313":
					_, _ = w.Write([]byte(`{"error":0,"data":{"id":313,"blocked":false,"status":{"name":"FAIL"}}}`))
				case "POST /panel-main/api/kubernetes/deleteNodePool/7":
					t.Error("destroy used a legacy unpinned deletion route")
					w.WriteHeader(500)
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			c, err := client.New(client.Config{APIBaseURL: srv.URL, APIKeyID: "fake", APISecretKey: "fake", Region: "TEST", ProjectTag: "project-a"})
			if err != nil {
				t.Fatal(err)
			}
			r := &K8sNodePoolResource{c: c}
			plan, _ := dependentProjectPlan(t, r, "node_pool", "project-a")
			for key, value := range map[string]any{"id": int64(7), "region": "TEST", "status": "SUCCESS", "node_subnet": int64(24)} {
				if d := plan.SetAttribute(ctx, path.Root(key), value); d.HasError() {
					t.Fatal(d)
				}
			}
			state := tfsdk.State(plan)
			var previous K8sNodePoolModel
			if d := state.Get(ctx, &previous); d.HasError() {
				t.Fatal(d)
			}
			resp := resource.ReadResponse{State: state}
			r.Read(ctx, resource.ReadRequest{State: state}, &resp)
			if resp.Diagnostics.HasError() || resp.State.Raw.IsNull() {
				t.Fatalf("read blocked recovery: %v", resp.Diagnostics)
			}
			if len(resp.Diagnostics.Warnings()) == 0 {
				t.Fatal("unverified hardware needs an explicit warning")
			}
			var got K8sNodePoolModel
			if d := resp.State.Get(ctx, &got); d.HasError() {
				t.Fatal(d)
			}
			if !got.VCPU.Equal(previous.VCPU) || !got.RAM.Equal(previous.RAM) || !got.DiskSize.Equal(previous.DiskSize) || got.Status.ValueString() != tc.status {
				t.Fatalf("unverified sizing published: %+v", got)
			}
			removed := resource.DeleteResponse{State: resp.State}
			r.Delete(ctx, resource.DeleteRequest{State: resp.State}, &removed)
			if removed.Diagnostics.HasError() || !deleted || hardwareCalls != 1 {
				t.Fatalf("destroy blocked: %v calls=%d deleted=%v", removed.Diagnostics, hardwareCalls, deleted)
			}
		})
	}
}
