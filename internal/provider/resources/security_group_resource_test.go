package resources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func securityGroupState(t *testing.T, r resource.Resource, model any) tfsdk.State {
	t.Helper()
	var sch resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &sch)
	state := tfsdk.State{Schema: sch.Schema}
	if d := state.Set(context.Background(), model); d.HasError() {
		t.Fatal(d)
	}
	return state
}

func securityGroupClient(t *testing.T, handler http.HandlerFunc) *client.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := client.New(client.Config{APIBaseURL: server.URL, APIKeyID: "fake", APISecretKey: "fake", Region: "DEFAULT", ProjectTag: "default"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSecurityGroupReadPreservesStateUnlessApplicationConfirmsAbsence(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status, code int
		absent       bool
	}{
		{"missing API route", 404, 0, false}, {"forbidden", 403, 714, false},
		{"server error", 500, 627, false}, {"scoped resource gone", 404, 703, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := securityGroupClient(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Method != http.MethodGet || req.URL.Path != "/panel-main/api/v2/security-groups/7" {
					t.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
				}
				if req.Header.Get("X-Region") != "OVERRIDE" || req.Header.Get("X-Project-Tag") != "chosen" {
					t.Error("lost resource scope override")
				}
				w.WriteHeader(tc.status)
				if tc.code != 0 {
					_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []any{map[string]any{"code": tc.code, "message": "failure"}}})
				}
			})
			r := &SecurityGroupResource{c: c}
			m := SecurityGroupModel{ID: types.Int64Value(7), Name: types.StringValue("web"), Region: types.StringValue("OVERRIDE"), ProjectTag: types.StringValue("chosen"), Rules: types.ListNull(types.ObjectType{AttrTypes: securityGroupRuleTypes})}
			state := securityGroupState(t, r, &m)
			resp := resource.ReadResponse{State: state}
			r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
			if tc.absent {
				if !resp.State.Raw.IsNull() || resp.Diagnostics.HasError() {
					t.Fatalf("confirmed deletion: state=%v diagnostics=%v", resp.State.Raw, resp.Diagnostics)
				}
			} else if resp.State.Raw.IsNull() || !resp.Diagnostics.HasError() {
				t.Fatalf("failed read must preserve state and report an error: %v", resp.Diagnostics)
			}
		})
	}
}

func TestSecurityGroupAttachmentDeleteRejectsOtherGroupsBinding(t *testing.T) {
	c := securityGroupClient(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodDelete || req.URL.Path != "/panel-main/api/v2/security-groups/7/attachments/8/9" {
			t.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
		}
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []any{map[string]any{"code": 631, "message": "interface is attached to a different group"}}})
	})
	r := &SecurityGroupAttachmentResource{c: c}
	m := SecurityGroupAttachmentModel{ID: types.StringValue("7/8/9"), GroupID: types.Int64Value(7), VmID: types.Int64Value(8), InterfaceID: types.Int64Value(9)}
	state := securityGroupState(t, r, &m)
	resp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
	if !resp.Diagnostics.HasError() || resp.State.Raw.IsNull() {
		t.Fatal("conflicting detach must fail and keep state")
	}
}

func TestSecurityGroupImportRejectsMalformedIdentity(t *testing.T) {
	for _, value := range []string{"", "0", "-1", "1/2", "7/8/0", "7//9", "7/8/9/10"} {
		for _, kind := range []string{"group", "attachment"} {
			t.Run(kind+":"+value, func(t *testing.T) {
				var r resource.ResourceWithImportState = &SecurityGroupResource{}
				if kind == "attachment" {
					r = &SecurityGroupAttachmentResource{}
				}
				var sch resource.SchemaResponse
				r.Schema(context.Background(), resource.SchemaRequest{}, &sch)
				resp := resource.ImportStateResponse{State: tfsdk.State{Schema: sch.Schema}}
				r.ImportState(context.Background(), resource.ImportStateRequest{ID: value}, &resp)
				if !resp.Diagnostics.HasError() {
					t.Fatal("invalid import identity accepted")
				}
			})
		}
	}
}

func TestSecurityGroupAddressValidation(t *testing.T) {
	for _, tc := range []struct {
		value string
		valid bool
	}{
		{"", true}, {"192.0.2.1", true}, {"192.0.2.0/24", true}, {"2001:db8::/32", true},
		{"example.com", false}, {"192.0.2.1/33", false}, {"2001:db8::1/129", false}, {"fe80::1%en0", false},
	} {
		var resp validator.StringResponse
		securityGroupAddressValidator{}.ValidateString(context.Background(), validator.StringRequest{Path: path.Root("source"), ConfigValue: types.StringValue(tc.value)}, &resp)
		if resp.Diagnostics.HasError() == tc.valid {
			t.Errorf("%q: valid=%v diagnostics=%v", tc.value, tc.valid, resp.Diagnostics)
		}
	}
}

func TestSecurityGroupPortValidation(t *testing.T) {
	for _, tc := range []struct {
		value string
		valid bool
	}{
		{"", true}, {"443", true}, {"1:65535", true}, {"22,80:90,443", true},
		{"0", false}, {"65536", false}, {"443:80", false}, {"22,", false}, {"http", false}, {"1:2:3", false},
	} {
		var resp validator.StringResponse
		securityGroupPortValidator{}.ValidateString(context.Background(), validator.StringRequest{Path: path.Root("source_port"), ConfigValue: types.StringValue(tc.value)}, &resp)
		if resp.Diagnostics.HasError() == tc.valid {
			t.Errorf("%q: valid=%v diagnostics=%v", tc.value, tc.valid, resp.Diagnostics)
		}
	}
}

func TestSecurityGroupValidateConfigWaitsForUnknownProtocolAndRules(t *testing.T) {
	typeRule := types.ObjectType{AttrTypes: securityGroupRuleTypes}
	for _, tc := range []struct {
		name                                string
		protocol                            types.String
		unknownRule, unknownList, wantError bool
	}{
		{name: "TCP port is valid", protocol: types.StringValue("tcp")},
		{name: "port without protocol fails", protocol: types.StringNull(), wantError: true},
		{name: "ICMP port fails", protocol: types.StringValue("icmp"), wantError: true},
		{name: "unknown protocol is deferred", protocol: types.StringUnknown()},
		{name: "unknown rule is deferred", unknownRule: true},
		{name: "unknown list is deferred", unknownList: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attrs := map[string]attr.Value{}
			for name := range securityGroupRuleTypes {
				attrs[name] = types.StringNull()
			}
			attrs["protocol"], attrs["destination_port"] = tc.protocol, types.StringValue("443")
			rule := types.ObjectValueMust(securityGroupRuleTypes, attrs)
			if tc.unknownRule {
				rule = types.ObjectUnknown(securityGroupRuleTypes)
			}
			rules := types.ListValueMust(typeRule, []attr.Value{rule})
			if tc.unknownList {
				rules = types.ListUnknown(typeRule)
			}
			r := &SecurityGroupResource{}
			m := SecurityGroupModel{Rules: rules}
			state := securityGroupState(t, r, &m)
			var resp resource.ValidateConfigResponse
			r.ValidateConfig(context.Background(), resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: state.Schema, Raw: state.Raw}}, &resp)
			if resp.Diagnostics.HasError() != tc.wantError {
				t.Fatalf("diagnostics: %v", resp.Diagnostics)
			}
		})
	}
}
