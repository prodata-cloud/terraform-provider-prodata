package resources

import (
	"context"
	"strings"
	"testing"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Unit tests for the prodata_lb resource's plan-time validation logic.
//
// These exercise the extracted pure helpers and the framework validator shims
// that drive the actual Schema/ConfigValidators. Full create/read/update/destroy
// behavior is covered by the acceptance suite (the TF_ACC=1 path).

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

	// Pure helper covers the same surface for callers that bypass the framework.
	for _, bad := range []string{"EXTERNAL", "Internal", ""} {
		if msg := validateLbType(bad); msg == "" {
			t.Errorf("pure validateLbType: expected %q to fail, but it passed", bad)
		}
	}
}

// 2 — invalid `protocol` enum (case-sensitive).
func TestLb_ProtocolOneOfRejectsBadValue(t *testing.T) {
	v := stringvalidator.OneOf("TCP", "UDP")
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

	for _, bad := range []string{"tcp", "Http", ""} {
		if msg := validateLbProtocol(bad); msg == "" {
			t.Errorf("pure validateLbProtocol: expected %q to fail, but it passed", bad)
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

	if msg := validatePortCount(0); msg == "" {
		t.Errorf("pure validatePortCount(0): expected failure")
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

	if msg := validatePortCount(11); msg == "" {
		t.Errorf("pure validatePortCount(11): expected failure")
	}
	if msg := validatePortCount(10); msg != "" {
		t.Errorf("pure validatePortCount(10): expected pass, got %q", msg)
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

// 6 — backend_group with BOTH vm_ids and node_pool_id set fails ExactlyOneOf.
func TestLb_BackendGroupBothModesRejected(t *testing.T) {
	if msg := validateBackendGroupExactlyOne(true, true); msg == "" {
		t.Errorf("expected both-modes-set to fail, but it passed")
	} else if !strings.Contains(msg, "exactly one") {
		t.Errorf("expected message to mention 'exactly one', got: %s", msg)
	}
}

// 7 — backend_group with NEITHER vm_ids nor node_pool_id set fails ExactlyOneOf.
func TestLb_BackendGroupNeitherModeRejected(t *testing.T) {
	if msg := validateBackendGroupExactlyOne(false, false); msg == "" {
		t.Errorf("expected neither-mode-set to fail, but it passed")
	}
}

// 9 — CCM-create rejects user-supplied description (server hard-codes "CCM: <name>").
func TestLb_CCMDescriptionNotConfigurable(t *testing.T) {
	cases := []struct {
		name           string
		isCreate       bool
		hasPool        bool
		descriptionSet bool
		wantReject     bool
	}{
		{"create + CCM + user description -> rejected", true, true, true, true},
		{"create + CCM + no description -> OK", true, true, false, false},
		{"create + Frontend + user description -> OK", true, false, true, false},
		{"update + CCM + user description -> OK (configure honors it)", false, true, true, false},
		{"update + CCM + no description -> OK", false, true, false, false},
		{"create + Frontend + no description -> OK", true, false, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := validateCCMDescriptionNotConfigurable(c.isCreate, c.hasPool, c.descriptionSet)
			if c.wantReject && got == "" {
				t.Errorf("expected rejection, got OK")
			}
			if !c.wantReject && got != "" {
				t.Errorf("expected OK, got rejection: %s", got)
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
