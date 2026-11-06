package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func TestLocalNetworkResource_CreateDoesNotAdopt(t *testing.T) {
	for _, tc := range []struct {
		name, existingCIDR string
		code               int
		explicitScope      bool
	}{
		{name: "successful create"},
		{name: "matching existing network", code: 614, existingCIDR: "192.168.20.0/24"},
		{name: "different existing network", code: 614, existingCIDR: "192.168.30.0/24"},
		{name: "conflict in overridden scope", code: 614, existingCIDR: "192.168.20.0/24", explicitScope: true},
		{name: "other API error", code: 714},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			region, project := "TEST", "provider-project"
			if tc.explicitScope {
				region, project = "OTHER", "resource-project"
			}
			network := client.LocalNetwork{ID: 42, Name: "existing-net", CIDR: "192.168.20.0/24", Gateway: "192.168.20.1"}
			var mu sync.Mutex
			var calls []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				calls = append(calls, req.Method+" "+req.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				switch req.Method + " " + req.URL.Path {
				case "POST /panel-main/api/v2/local-networks":
					var body client.CreateLocalNetworkRequest
					if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if body.Region != region || body.ProjectTag != project || body.Name != network.Name || body.CIDR != network.CIDR || body.Gateway != network.Gateway {
						t.Errorf("unexpected create request: %+v", body)
					}
					if tc.code != 0 {
						w.WriteHeader(http.StatusConflict)
						_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []map[string]any{{"code": tc.code, "message": "create rejected"}}})
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": network})
				case "GET /panel-main/api/v2/local-networks":
					// A network with the same name is available to the old adoption path,
					// including an exact match. Neither is proof of Terraform ownership.
					existing := network
					existing.CIDR = tc.existingCIDR
					_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": []client.LocalNetwork{existing}})
				default:
					t.Errorf("unexpected request %s", calls[len(calls)-1])
					http.NotFound(w, req)
				}
			}))
			defer server.Close()
			r := &LocalNetworkResource{client: localNetworkTestClient(t, server)}
			plan, state := localNetworkTestPlan(t, r)
			if tc.explicitScope {
				if diags := plan.SetAttribute(ctx, path.Root("region"), region); diags.HasError() {
					t.Fatal(diags)
				}
				if diags := plan.SetAttribute(ctx, path.Root("project_tag"), project); diags.HasError() {
					t.Fatal(diags)
				}
			}
			resp := resource.CreateResponse{State: state}
			r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)
			if tc.code == 0 {
				if resp.Diagnostics.HasError() {
					t.Fatal(resp.Diagnostics)
				}
				var data LocalNetworkResourceModel
				if diags := resp.State.Get(ctx, &data); diags.HasError() {
					t.Fatal(diags)
				}
				if data.ID.ValueInt64() != network.ID || data.Region.ValueString() != region || data.ProjectTag.ValueString() != project {
					t.Errorf("unexpected created state: %+v", data)
				}
			} else {
				if !hasDiag(resp.Diagnostics, diag.SeverityError, fmt.Sprintf("[%d]", tc.code)) {
					t.Errorf("missing original API error: %v", resp.Diagnostics)
				}
				if !resp.State.Raw.IsNull() {
					t.Error("failed create took ownership of an existing network")
				}
				if tc.code == 614 && !hasDiag(resp.Diagnostics, diag.SeverityError, "terraform import") {
					t.Errorf("conflict must explain explicit import: %v", resp.Diagnostics)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if !reflect.DeepEqual(calls, []string{"POST /panel-main/api/v2/local-networks"}) {
				t.Errorf("create made extra requests: %v", calls)
			}
		})
	}
}

func TestLocalNetworkResource_ExplicitImportAndRefresh(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet || req.URL.Path != "/panel-main/api/v2/local-networks/42" {
			t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
			http.NotFound(w, req)
			return
		}
		if req.Header.Get("X-Region") != "TEST" || req.Header.Get("X-Project-Tag") != "provider-project" {
			t.Error("import refresh did not retain its scope")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": client.LocalNetwork{
			ID: 42, Name: "existing-net", CIDR: "192.168.20.0/24", Gateway: "192.168.20.1",
		}})
	}))
	defer server.Close()
	r := &LocalNetworkResource{client: localNetworkTestClient(t, server)}
	_, state := localNetworkTestPlan(t, r)
	imported := resource.ImportStateResponse{State: state}
	r.ImportState(ctx, resource.ImportStateRequest{ID: "42"}, &imported)
	if imported.Diagnostics.HasError() {
		t.Fatal(imported.Diagnostics)
	}
	refreshed := resource.ReadResponse{State: imported.State}
	r.Read(ctx, resource.ReadRequest{State: imported.State}, &refreshed)
	if refreshed.Diagnostics.HasError() {
		t.Fatal(refreshed.Diagnostics)
	}
	var data LocalNetworkResourceModel
	if diags := refreshed.State.Get(ctx, &data); diags.HasError() {
		t.Fatal(diags)
	}
	if data.ID.ValueInt64() != 42 || data.Name.ValueString() != "existing-net" || data.CIDR.ValueString() != "192.168.20.0/24" || data.Gateway.ValueString() != "192.168.20.1" {
		t.Errorf("import refresh did not retain the existing network: %+v", data)
	}
}

func localNetworkTestClient(t *testing.T, server *httptest.Server) *client.Client {
	t.Helper()
	c, err := client.New(client.Config{
		APIBaseURL: server.URL, APIKeyID: "fake-key", APISecretKey: "fake-secret",
		Region: "TEST", ProjectTag: "provider-project",
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func localNetworkTestPlan(t *testing.T, r *LocalNetworkResource) (tfsdk.Plan, tfsdk.State) {
	t.Helper()
	ctx := context.Background()
	var sch resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sch)
	typ := sch.Schema.Type().TerraformType(ctx)
	plan := tfsdk.Plan{Schema: sch.Schema, Raw: tftypes.NewValue(typ, nil)}
	model := LocalNetworkResourceModel{
		ID: types.Int64Unknown(), Region: types.StringUnknown(), ProjectTag: types.StringUnknown(),
		Name: types.StringValue("existing-net"), CIDR: types.StringValue("192.168.20.0/24"), Gateway: types.StringValue("192.168.20.1"),
	}
	if diags := plan.Set(ctx, &model); diags.HasError() {
		t.Fatal(diags)
	}
	return plan, tfsdk.State{Schema: sch.Schema, Raw: tftypes.NewValue(typ, nil)}
}
