package resources

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Unit tests for the prodata_lb resource's plan-time logic. These exercise the
// pure helpers and the name pattern we wrote — not the framework validators wired
// into the schema (OneOf / Size*), whose behavior is asserted through the real
// validation path in the TF_ACC acceptance suite. Full create/read/update/destroy
// behavior is covered by that same acceptance suite in the provider package.

// lbNameRegex is our own (hostname-shaped) pattern, so it is worth locking down;
// the length bounds are enforced by the framework's LengthBetween and are not
// re-tested here.
func TestLb_NameRegexPattern(t *testing.T) {
	for _, bad := range []string{"", "-lead", "trail-", "has space", "has_underscore", "has.dot"} {
		if lbNameRegex.MatchString(bad) {
			t.Errorf("expected name %q to be rejected by lbNameRegex, but it matched", bad)
		}
	}
	for _, good := range []string{"abc", "web-lb", "metrics-collector", "lb-1", "a1"} {
		if !lbNameRegex.MatchString(good) {
			t.Errorf("expected name %q to match lbNameRegex, but it did not", good)
		}
	}
}

// CCM load balancers reject a user-supplied description on both create and update
// (the panel owns it as "CCM: <name>").
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

// Import ID parsing: bare integer and composite {region}/{id}@{project}.
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

// Mode-switch detection (state vm_ids -> plan node_pool_id, and vice versa) plus
// the backendMode helper that feeds it.
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

	t.Run("backendMode reads pointer + null + unknown correctly", func(t *testing.T) {
		hasVMs, hasPool := backendMode(nil)
		if hasVMs || hasPool {
			t.Errorf("backendMode(nil) = (%v,%v), want (false,false)", hasVMs, hasPool)
		}

		bg := &LbBackendGroupModel{
			VMIDs:      types.SetNull(types.StringType),
			NodePoolID: types.Int64Null(),
		}
		hasVMs, hasPool = backendMode(bg)
		if hasVMs || hasPool {
			t.Errorf("backendMode(empty) = (%v,%v), want (false,false)", hasVMs, hasPool)
		}

		bg.VMIDs = types.SetValueMust(types.StringType, []attr.Value{types.StringValue("VM-GUID-1")})
		hasVMs, hasPool = backendMode(bg)
		if !hasVMs || hasPool {
			t.Errorf("backendMode(vm_ids set) = (%v,%v), want (true,false)", hasVMs, hasPool)
		}

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
