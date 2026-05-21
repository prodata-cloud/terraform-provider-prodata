package resources

import (
	"context"
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
