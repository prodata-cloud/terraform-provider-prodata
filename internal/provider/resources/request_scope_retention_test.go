package resources

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// An owned object queried with an unauthorized project/region still exists.
// The backend contract is 403/714; a refresh must retain all prior Terraform state.
func TestReadScopeConflictRetainsResourceIdentity(t *testing.T) {
	for _, kind := range []string{"cluster", "pool", "lb"} {
		for _, envelope := range []string{"v1", "v2"} {
			t.Run(kind+"/"+envelope, func(t *testing.T) {
				ctx := context.Background()
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					requests.Add(1)
					if req.Method != http.MethodGet {
						t.Errorf("scope failure must not create or mutate resources: %s", req.Method)
					}
					if req.Header.Get("X-Project-Tag") != "stored-project" || req.Header.Get("X-Region") != "TEST" {
						t.Errorf("refresh lost stored scope: project=%q region=%q", req.Header.Get("X-Project-Tag"), req.Header.Get("X-Region"))
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusForbidden)
					if envelope == "v1" {
						_, _ = fmt.Fprint(w, `{"error":714,"errMessage":"Permission denied","data":null}`)
					} else {
						_, _ = fmt.Fprint(w, `{"success":false,"data":null,"errors":[{"code":714,"message":"Permission denied"}]}`)
					}
				}))
				t.Cleanup(server.Close)
				c, err := client.New(client.Config{APIBaseURL: server.URL, APIKeyID: "fake-key", APISecretKey: "fake-secret", Region: "other-region", ProjectTag: "other-project"})
				if err != nil {
					t.Fatal(err)
				}
				values := map[string]tftypes.Value{
					"id": nirNum(90), "name": nirStr("managed-object"),
					"region": nirStr("TEST"), "project_tag": nirStr("stored-project"),
				}
				var r resource.Resource
				var prior tfsdk.State
				switch kind {
				case "cluster":
					r = &K8sClusterResource{c: c}
					_, sch := k8sObjType(t)
					prior = tfsdk.State{Schema: sch, Raw: k8sRaw(t, values)}
				case "pool":
					r = &K8sNodePoolResource{c: c}
					_, sch := poolObjType(t)
					values["cluster_id"] = nirNum(89)
					prior = tfsdk.State{Schema: sch, Raw: poolRaw(t, values)}
				default:
					r = &LbResource{c: c}
					_, sch := lbObjType(t)
					prior = tfsdk.State{Schema: sch, Raw: lbRaw(t, values)}
				}
				response := resource.ReadResponse{State: prior}
				r.Read(ctx, resource.ReadRequest{State: prior}, &response)
				if !response.Diagnostics.HasError() {
					t.Fatal("scope conflict must produce a diagnostic")
				}
				if !strings.Contains(fmt.Sprint(response.Diagnostics), "Permission denied") {
					t.Fatalf("lost platform scope failure: %v", response.Diagnostics)
				}
				if !response.State.Raw.Equal(prior.Raw) {
					t.Fatalf("scope conflict erased or changed Terraform state: %v", response.State.Raw)
				}
				if requests.Load() != 1 {
					t.Fatalf("refresh made %d requests, want one read without retry or mutation", requests.Load())
				}
			})
		}
	}
}
