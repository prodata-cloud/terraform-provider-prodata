package resources

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestIPv4Address(t *testing.T) {
	tests := []struct {
		name    string
		in      types.String
		wantErr bool
	}{
		{"a plain address", types.StringValue("192.168.20.15"), false},
		{"the lowest", types.StringValue("0.0.0.0"), false},
		{"the highest", types.StringValue("255.255.255.255"), false},
		{"null is skipped", types.StringNull(), false},
		{"unknown is skipped", types.StringUnknown(), false},
		{"empty", types.StringValue(""), true},
		{"a name", types.StringValue("backend"), true},
		{"a short address", types.StringValue("192.168.20"), true},
		{"a long address", types.StringValue("192.168.20.15.1"), true},
		{"an octet out of range", types.StringValue("192.168.20.256"), true},
		{"a leading zero", types.StringValue("192.168.020.15"), true},
		{"a leading space", types.StringValue(" 192.168.20.15"), true},
		{"a trailing space", types.StringValue("192.168.20.15 "), true},
		{"a prefix length", types.StringValue("192.168.20.15/24"), true},
		{"a port", types.StringValue("192.168.20.15:22"), true},
		{"an IPv6 address", types.StringValue("fd00::15"), true},
		{"an IPv6 address holding an IPv4 one", types.StringValue("::ffff:192.168.20.15"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resp validator.StringResponse
			IPv4Address().ValidateString(context.Background(), validator.StringRequest{Path: path.Root("ip"), ConfigValue: tt.in}, &resp)
			if got := resp.Diagnostics.HasError(); got != tt.wantErr {
				t.Errorf("HasError = %v, want %v (%v)", got, tt.wantErr, resp.Diagnostics)
			}
		})
	}
}

func TestIPv4Address_NamesTheAttributeAndTheValue(t *testing.T) {
	var resp validator.StringResponse
	IPv4Address().ValidateString(context.Background(), validator.StringRequest{Path: path.Root("ip"), ConfigValue: types.StringValue("nope")}, &resp)

	if len(resp.Diagnostics) != 1 {
		t.Fatalf("got %d diagnostics, want 1: %v", len(resp.Diagnostics), resp.Diagnostics)
	}
	d := resp.Diagnostics[0]
	if d.Summary() != "Invalid IPv4 address" {
		t.Errorf("summary = %q", d.Summary())
	}
	wantInText(t, d.Detail(), "ip", `"nope"`)
}

// The ip attribute must actually carry the validator — a unit test of the validator alone would
// stay green if someone dropped it from the schema.
func TestLocalNetworkAttachmentSchema_IPUsesIPv4Address(t *testing.T) {
	var sresp resource.SchemaResponse
	NewLocalNetworkAttachmentResource().Schema(context.Background(), resource.SchemaRequest{}, &sresp)
	attr, ok := sresp.Schema.Attributes["ip"]
	if !ok {
		t.Fatal("ip missing from schema")
	}
	sa, ok := attr.(interface {
		StringValidators() []validator.String
	})
	if !ok {
		t.Fatalf("ip is %T, want a string attribute", attr)
	}
	var vresp validator.StringResponse
	for _, v := range sa.StringValidators() {
		v.ValidateString(context.Background(), validator.StringRequest{Path: path.Root("ip"), ConfigValue: types.StringValue("not-an-address")}, &vresp)
	}
	if !vresp.Diagnostics.HasError() {
		t.Error("a malformed ip must be rejected by the schema validators")
	}
}
