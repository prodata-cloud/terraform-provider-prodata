package resources

import (
	"context"
	"testing"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Unit tests for the prodata_lb resource's plan-time validation logic. These
// exercise the framework validators wired into Schema/ConfigValidators
// directly; full create/read/update/destroy behavior is covered by the live-API
// gated test in the client package and (eventually) a TF_ACC=1 acceptance suite.

// 1 — invalid `type` enum.
func TestLb_TypeOneOfRejectsBadValue(t *testing.T) {
	v := stringvalidator.OneOf(client.LbTypeExternal, client.LbTypeInternal)
	for _, bad := range []string{"EXTERNAL", "Internal", "public", "", "private"} {
		req := validator.StringRequest{
			Path:        path.Root("type"),
			ConfigValue: types.StringValue(bad),
		}
		resp := &validator.StringResponse{}
		v.ValidateString(context.Background(), req, resp)
		if !resp.Diagnostics.HasError() {
			t.Errorf("expected type=%q to fail OneOf validation, but it passed", bad)
		}
	}

	// Sanity: the canonical values must pass.
	for _, good := range []string{client.LbTypeExternal, client.LbTypeInternal} {
		req := validator.StringRequest{
			Path:        path.Root("type"),
			ConfigValue: types.StringValue(good),
		}
		resp := &validator.StringResponse{}
		v.ValidateString(context.Background(), req, resp)
		if resp.Diagnostics.HasError() {
			t.Errorf("expected type=%q to pass, got: %s", good, resp.Diagnostics)
		}
	}
}

// 2 — invalid `protocol` enum (case-sensitive).
func TestLb_ProtocolOneOfRejectsBadValue(t *testing.T) {
	v := stringvalidator.OneOf(client.LbProtocolTCP, client.LbProtocolUDP)
	// Includes lowercase variants because the server silently downgrades unknown
	// values to TCP — the plan-time validator is the only line of defence.
	for _, bad := range []string{"tcp", "udp", "Tcp", "HTTP", "https", "", "ICMP"} {
		req := validator.StringRequest{
			Path:        path.Root("protocol"),
			ConfigValue: types.StringValue(bad),
		}
		resp := &validator.StringResponse{}
		v.ValidateString(context.Background(), req, resp)
		if !resp.Diagnostics.HasError() {
			t.Errorf("expected protocol=%q to fail OneOf validation, but it passed", bad)
		}
	}
}

// 3 — `port` set must be between 1 and 10 entries (empty rejected).
func TestLb_PortSetSizeRejectsEmpty(t *testing.T) {
	v := setvalidator.SizeBetween(lbMinPorts, lbMaxPorts)
	empty, diags := types.SetValue(types.StringType, nil)
	if diags.HasError() {
		t.Fatalf("constructing empty Set: %s", diags)
	}
	req := validator.SetRequest{
		Path:        path.Root("port"),
		ConfigValue: empty,
	}
	resp := &validator.SetResponse{}
	v.ValidateSet(context.Background(), req, resp)
	if !resp.Diagnostics.HasError() {
		t.Errorf("expected empty port set to fail SizeBetween, but it passed")
	}
}

// 4 — `port` set must be between 1 and 10 entries (eleven rejected).
func TestLb_PortSetSizeRejectsOverflow(t *testing.T) {
	v := setvalidator.SizeBetween(lbMinPorts, lbMaxPorts)
	elems := make([]attr.Value, 0, 11)
	for i := 0; i < 11; i++ {
		elems = append(elems, types.StringValue(string(rune('a'+i))))
	}
	eleven, diags := types.SetValue(types.StringType, elems)
	if diags.HasError() {
		t.Fatalf("constructing 11-entry Set: %s", diags)
	}
	req := validator.SetRequest{
		Path:        path.Root("port"),
		ConfigValue: eleven,
	}
	resp := &validator.SetResponse{}
	v.ValidateSet(context.Background(), req, resp)
	if !resp.Diagnostics.HasError() {
		t.Errorf("expected 11-entry port set to fail SizeBetween, but it passed")
	}
}

// 5 — `vm_ids` set must contain at least one entry when set.
func TestLb_VmIdsSizeAtLeastOne(t *testing.T) {
	v := setvalidator.SizeAtLeast(1)
	empty, diags := types.SetValue(types.StringType, nil)
	if diags.HasError() {
		t.Fatalf("constructing empty Set: %s", diags)
	}
	req := validator.SetRequest{
		Path:        path.Root("backend_group").AtName("vm_ids"),
		ConfigValue: empty,
	}
	resp := &validator.SetResponse{}
	v.ValidateSet(context.Background(), req, resp)
	if !resp.Diagnostics.HasError() {
		t.Errorf("expected empty vm_ids to fail SizeAtLeast(1), but it passed")
	}

	one, diags := types.SetValue(types.StringType, []attr.Value{types.StringValue("VM-GUID-1")})
	if diags.HasError() {
		t.Fatalf("constructing single-entry Set: %s", diags)
	}
	req.ConfigValue = one
	resp = &validator.SetResponse{}
	v.ValidateSet(context.Background(), req, resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected single-entry vm_ids to pass: %s", resp.Diagnostics)
	}
}

// 6 — `name` length + charset validation.
func TestLb_NameValidator(t *testing.T) {
	lengthV := stringvalidator.LengthBetween(lbMinNameLen, lbMaxNameLen)
	regexV := stringvalidator.RegexMatches(lbNameRegex, "")

	for _, bad := range []string{"", "ab", "-lead", "trail-", "has space", "has_underscore", "has.dot"} {
		req := validator.StringRequest{Path: path.Root("name"), ConfigValue: types.StringValue(bad)}
		lResp, rResp := &validator.StringResponse{}, &validator.StringResponse{}
		lengthV.ValidateString(context.Background(), req, lResp)
		regexV.ValidateString(context.Background(), req, rResp)
		if !lResp.Diagnostics.HasError() && !rResp.Diagnostics.HasError() {
			t.Errorf("expected name=%q to fail length or regex, both passed", bad)
		}
	}

	for _, good := range []string{"abc", "web-lb", "metrics-collector", "lb-1"} {
		req := validator.StringRequest{Path: path.Root("name"), ConfigValue: types.StringValue(good)}
		lResp, rResp := &validator.StringResponse{}, &validator.StringResponse{}
		lengthV.ValidateString(context.Background(), req, lResp)
		regexV.ValidateString(context.Background(), req, rResp)
		if lResp.Diagnostics.HasError() || rResp.Diagnostics.HasError() {
			t.Errorf("expected name=%q to pass, got length=%s regex=%s", good, lResp.Diagnostics, rResp.Diagnostics)
		}
	}
}

// 7 — CCM load balancers reject a user-supplied description on both create and
// update (the panel owns it as "CCM: <name>").
func TestLb_CCMDescriptionNotConfigurable(t *testing.T) {
	cases := []struct {
		name           string
		hasPool        bool
		descriptionSet bool
		wantReject     bool
	}{
		{"CCM + user description -> rejected", true, true, true},
		{"CCM + no description -> OK", true, false, false},
		{"Frontend + user description -> OK", false, true, false},
		{"Frontend + no description -> OK", false, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := validateCCMDescriptionNotConfigurable(c.hasPool, c.descriptionSet)
			if c.wantReject && got == "" {
				t.Errorf("expected rejection, got OK")
			}
			if !c.wantReject && got != "" {
				t.Errorf("expected OK, got rejection: %s", got)
			}
		})
	}
}

// 7b — import ID parsing: bare integer and composite {region}/{id}@{project}.
func TestLb_ParseImportID(t *testing.T) {
	t.Run("bare id", func(t *testing.T) {
		id, region, project, err := parseLBImportID("42")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if id != 42 || region != "" || project != "" {
			t.Errorf("got id=%d region=%q project=%q, want 42/\"\"/\"\"", id, region, project)
		}
	})
	t.Run("composite", func(t *testing.T) {
		id, region, project, err := parseLBImportID("UZ-5/42@my-project")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if id != 42 || region != "UZ-5" || project != "my-project" {
			t.Errorf("got id=%d region=%q project=%q, want 42/UZ-5/my-project", id, region, project)
		}
	})
	for _, bad := range []string{"", "abc", "/42@p", "UZ-5/@p", "UZ-5/42@", "UZ-5/notint@p", "UZ-5/42"} {
		t.Run("invalid "+bad, func(t *testing.T) {
			if _, _, _, err := parseLBImportID(bad); err == nil {
				t.Errorf("expected error for %q, got nil", bad)
			}
		})
	}
}

// 8 — mode-switch detection (state vm_ids -> plan node_pool_id, and vice versa).
func TestLb_DetectModeSwitch(t *testing.T) {
	cases := []struct {
		name                                   string
		stateVMs, statePool, planVMs, planPool bool
		wantSwitch                             bool
	}{
		{"vm_ids -> node_pool_id", true, false, false, true, true},
		{"node_pool_id -> vm_ids", false, true, true, false, true},
		{"vm_ids -> vm_ids (content change)", true, false, true, false, false},
		{"node_pool_id -> node_pool_id (value change)", false, true, false, true, false},
		{"none -> vm_ids (initial create — not a switch)", false, false, true, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := detectModeSwitch(c.stateVMs, c.statePool, c.planVMs, c.planPool)
			if got != c.wantSwitch {
				t.Errorf("detectModeSwitch(stateVMs=%v,statePool=%v,planVMs=%v,planPool=%v) = %v, want %v",
					c.stateVMs, c.statePool, c.planVMs, c.planPool, got, c.wantSwitch)
			}
		})
	}

	// Sanity check the backendMode helper that feeds detectModeSwitch.
	t.Run("backendMode reads pointer + null + unknown correctly", func(t *testing.T) {
		// nil backend_group → both false.
		hasVMs, hasPool := backendMode(nil)
		if hasVMs || hasPool {
			t.Errorf("backendMode(nil) = (%v,%v), want (false,false)", hasVMs, hasPool)
		}

		// Empty (null fields) → both false.
		bg := &LbBackendGroupModel{
			VMIDs:      types.SetNull(types.StringType),
			NodePoolID: types.Int64Null(),
		}
		hasVMs, hasPool = backendMode(bg)
		if hasVMs || hasPool {
			t.Errorf("backendMode(empty) = (%v,%v), want (false,false)", hasVMs, hasPool)
		}

		// Set vm_ids → hasVMs=true.
		bg.VMIDs = types.SetValueMust(types.StringType, []attr.Value{types.StringValue("VM-GUID-1")})
		hasVMs, hasPool = backendMode(bg)
		if !hasVMs || hasPool {
			t.Errorf("backendMode(vm_ids set) = (%v,%v), want (true,false)", hasVMs, hasPool)
		}

		// Set node_pool_id only → hasPool=true.
		bg = &LbBackendGroupModel{
			VMIDs:      types.SetNull(types.StringType),
			NodePoolID: types.Int64Value(42),
		}
		hasVMs, hasPool = backendMode(bg)
		if hasVMs || !hasPool {
			t.Errorf("backendMode(pool set) = (%v,%v), want (false,true)", hasVMs, hasPool)
		}
	})
}
