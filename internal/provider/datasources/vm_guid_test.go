package datasources

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
)

// The VM data sources expose `guid` so it can be fed into a load balancer backend
// (prodata_lb.backend_group.vm_ids). These guard the attribute against the same
// regression covered for the resource: it was absent through v0.17.1.

func TestVmDataSource_GuidAttributeExposed(t *testing.T) {
	var resp datasource.SchemaResponse
	NewVmDataSource().Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}

	attr, ok := resp.Schema.Attributes["guid"]
	if !ok {
		t.Fatal("data.prodata_vm must expose a `guid` attribute")
	}
	if !attr.IsComputed() {
		t.Error("data.prodata_vm `guid` must be Computed")
	}
}

func TestVmsDataSource_GuidAttributeExposed(t *testing.T) {
	var resp datasource.SchemaResponse
	NewVmsDataSource().Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}

	vms, ok := resp.Schema.Attributes["vms"].(schema.ListNestedAttribute)
	if !ok {
		t.Fatal("data.prodata_vms must expose a `vms` list-nested attribute")
	}
	attr, ok := vms.NestedObject.Attributes["guid"]
	if !ok {
		t.Fatal("each element of data.prodata_vms.vms must expose a `guid` attribute")
	}
	if !attr.IsComputed() {
		t.Error("data.prodata_vms.vms[].guid must be Computed")
	}
}
