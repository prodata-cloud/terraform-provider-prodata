package resources

import (
	"context"
	"fmt"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"net/http"
	"net/http/httptest"
	"terraform-provider-prodata/internal/client"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
)

// TestVm_GuidAttributeExposed guards a regression: load balancer backends are
// referenced by VM guid (prodata_lb.backend_group.vm_ids), and the docs/examples
// wire them as vm_ids = [prodata_vm.x.guid]. That attribute was missing from the
// VM resource through v0.17.1, which made the canonical VM-backed LB config fail
// at plan time. Lock the attribute's presence and that it is computed so it cannot
// silently disappear again.
func TestVm_GuidAttributeExposed(t *testing.T) {
	var resp resource.SchemaResponse
	NewVmResource().Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}

	attr, ok := resp.Schema.Attributes["guid"]
	if !ok {
		t.Fatal("prodata_vm must expose a `guid` attribute (load balancer backends reference it)")
	}
	if !attr.IsComputed() {
		t.Error("prodata_vm `guid` must be Computed (assigned by the panel)")
	}
}

func TestVmCreateNameConflictNeverRenamesUnrelatedVm(t *testing.T) {
	creates, renames, lists := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch q.Method {
		case "POST":
			creates++
			w.WriteHeader(409)
			fmt.Fprint(w, `{"success":false,"errors":[{"code":666,"message":"name conflict"}]}`)
		case "GET":
			lists++
			fmt.Fprint(w, `{"success":true,"data":[{"id":99,"name":"web-1"}]}`)
		default:
			renames++
			fmt.Fprint(w, `{"success":true,"data":null}`)
		}
	}))
	defer server.Close()
	c, err := client.New(client.Config{APIBaseURL: server.URL, APIKeyID: "fake", APISecretKey: "fake", Region: "TEST", ProjectTag: "proj"})
	if err != nil {
		t.Fatal(err)
	}
	planner := newVMPlanner(t)
	s := vmSchema(t)
	values := vmState().with("password", vmStr("local-test-placeholder"))
	raw := planner.object(values)
	r := &VmResource{client: c}
	out := resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: planner.object(nil)}}
	r.Create(context.Background(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: raw}, Config: tfsdk.Config{Schema: s, Raw: raw}}, &out)
	if !out.Diagnostics.HasError() || creates != 1 || renames != 0 || lists != 0 {
		t.Fatalf("creates=%d renames=%d lists=%d diag=%v", creates, renames, lists, out.Diagnostics)
	}
}
