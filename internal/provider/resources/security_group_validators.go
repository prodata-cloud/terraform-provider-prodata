package resources

import (
	"context"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type securityGroupAddressValidator struct{}

func (securityGroupAddressValidator) Description(context.Context) string {
	return "Must be an IP address, CIDR, or empty."
}
func (v securityGroupAddressValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (securityGroupAddressValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() || req.ConfigValue.ValueString() == "" {
		return
	}
	value := req.ConfigValue.ValueString()
	if strings.Contains(value, "/") {
		if _, err := netip.ParsePrefix(value); err == nil {
			return
		}
	} else if ip, err := netip.ParseAddr(value); err == nil && ip.Zone() == "" {
		return
	}
	resp.Diagnostics.AddAttributeError(req.Path, "Invalid Firewall Address", "Use a literal IPv4/IPv6 address or CIDR; an empty string matches any address.")
}

type securityGroupPortValidator struct{}

func (securityGroupPortValidator) Description(context.Context) string {
	return "Ports 1–65535, colon-separated ranges, or comma-separated lists; empty matches any port."
}
func (v securityGroupPortValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (securityGroupPortValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() || req.ConfigValue.ValueString() == "" {
		return
	}
	if err := validateSecurityGroupPorts(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid Firewall Port", err.Error())
	}
}

func validateSecurityGroupPorts(value string) error {
	for _, part := range strings.Split(value, ",") {
		rangeParts := strings.Split(part, ":")
		start, err := strconv.Atoi(rangeParts[0])
		if err != nil || start < 1 || start > 65535 || len(rangeParts) > 2 {
			return fmt.Errorf("use ports 1–65535, ascending colon-separated ranges, or comma-separated lists")
		}
		if len(rangeParts) == 2 {
			end, err := strconv.Atoi(rangeParts[1])
			if err != nil || end < start || end > 65535 {
				return fmt.Errorf("port ranges must be ascending and remain within 1–65535")
			}
		}
	}
	return nil
}

func (r *SecurityGroupResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var rules types.List
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("rules"), &rules)...)
	if resp.Diagnostics.HasError() || rules.IsNull() || rules.IsUnknown() {
		return
	}
	for i, element := range rules.Elements() {
		if element.IsNull() || element.IsUnknown() {
			continue
		}
		object, ok := element.(types.Object)
		if !ok {
			continue
		}
		attributes := object.Attributes()
		protocol, ok := attributes["protocol"].(types.String)
		if !ok || protocol.IsUnknown() {
			continue
		}
		if protocol.ValueString() == "tcp" || protocol.ValueString() == "udp" {
			continue
		}
		for _, name := range []string{"source_port", "destination_port"} {
			port, ok := attributes[name].(types.String)
			if ok && !port.IsUnknown() && port.ValueString() != "" {
				resp.Diagnostics.AddAttributeError(path.Root("rules").AtListIndex(i).AtName(name), "Firewall Port Requires TCP or UDP", "Set protocol to tcp or udp when matching a port.")
			}
		}
	}
}
