package resources

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// What an attribute looks like to ValidateConfig: unset, a literal, or a value Terraform
// does not know yet (a variable, a data source, another resource's attribute).
const (
	cfgNull = iota
	cfgKnown
	cfgUnknown
)

func flavorCfg(kind int) tftypes.Value {
	switch kind {
	case cfgKnown:
		return nirNum(5)
	case cfgUnknown:
		return nirNumUnknown()
	}
	return tftypes.NewValue(tftypes.Number, nil)
}

func sizeCfg(kind int) tftypes.Value {
	switch kind {
	case cfgKnown:
		return nirStr("medium")
	case cfgUnknown:
		return nirStrUnknown()
	}
	return nirStrNull()
}

// runClusterValidateConfig runs K8sClusterResource.ValidateConfig over a configuration in
// which every attribute is null except those in set.
func runClusterValidateConfig(t *testing.T, set map[string]tftypes.Value) diag.Diagnostics {
	t.Helper()
	_, sch := k8sObjType(t)
	req := resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: sch, Raw: k8sRaw(t, set)}}
	var resp resource.ValidateConfigResponse
	(&K8sClusterResource{}).ValidateConfig(context.Background(), req, &resp)
	return resp.Diagnostics
}

// assertValidateDiags checks that diags is empty (want == "") or holds exactly one error
// whose summary contains want and which is attached to wantPath.
func assertValidateDiags(t *testing.T, diags diag.Diagnostics, want string, wantPath path.Path) {
	t.Helper()
	if want == "" {
		if len(diags) != 0 {
			t.Fatalf("want no diagnostics, got %v", diags)
		}
		return
	}
	if len(diags) != 1 || !diags.HasError() {
		t.Fatalf("want exactly one error containing %q, got %v", want, diags)
	}
	d := diags[0]
	if !strings.Contains(d.Summary(), want) {
		t.Errorf("summary = %q, want it to contain %q", d.Summary(), want)
	}
	dp, ok := d.(diag.DiagnosticWithPath)
	if !ok || !dp.Path().Equal(wantPath) {
		t.Errorf("error is not attached to %s: %#v", wantPath, d)
	}
}

// otherAttributesUnknown is the rest of the configuration as it looks in the documented
// explicit-flavor form: network, version and so on come from data sources or other resources,
// so they are unknown at validate time as well. The verdict must not depend on them.
func otherAttributesUnknown() map[string]tftypes.Value {
	return map[string]tftypes.Value{
		"name":               nirStrUnknown(),
		"kubernetes_version": nirStrUnknown(),
		"network_id":         nirNumUnknown(),
		"node_ip_range":      nirStrUnknown(),
	}
}

// Terraform validates the configuration with variables and data sources unknown before it
// plans, so "unknown" must never be read as "unset" — that rejected every control_plane_size /
// master_flavor_id that was not a literal. It validates again with the real values during
// plan and apply, which is where "both" and "neither" are caught for them.
func TestK8sClusterValidateConfig(t *testing.T) {
	const (
		required  = "a control-plane size is required"
		exclusive = "mutually exclusive"
	)
	tests := []struct {
		name   string
		flavor int
		size   int
		want   string // error summary substring; "" = no diagnostics
	}{
		{"neither is set", cfgNull, cfgNull, required},
		{"flavor only", cfgKnown, cfgNull, ""},
		{"size only", cfgNull, cfgKnown, ""},
		{"both are set", cfgKnown, cfgKnown, exclusive},

		{"flavor unknown, size unset", cfgUnknown, cfgNull, ""},
		{"flavor unset, size unknown", cfgNull, cfgUnknown, ""},
		{"both unknown", cfgUnknown, cfgUnknown, ""},
		{"flavor unknown, size set", cfgUnknown, cfgKnown, ""},
		{"flavor set, size unknown", cfgKnown, cfgUnknown, ""},
	}
	// Every row runs twice: with the other attributes null and with them unknown. The second run
	// also guards the conversion of the configuration into the model, and keeps an unknown
	// elsewhere from switching the check off ("neither" must still be reported early).
	variants := []struct {
		name   string
		others map[string]tftypes.Value
	}{
		{"", nil},
		{", other attributes unknown", otherAttributesUnknown()},
	}
	for _, tc := range tests {
		for _, v := range variants {
			t.Run(tc.name+v.name, func(t *testing.T) {
				set := map[string]tftypes.Value{
					"master_flavor_id":   flavorCfg(tc.flavor),
					"control_plane_size": sizeCfg(tc.size),
				}
				for k, val := range v.others {
					set[k] = val
				}
				assertValidateDiags(t, runClusterValidateConfig(t, set), tc.want, path.Root("control_plane_size"))
			})
		}
	}
}
