package resources

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// UserDataPrefix returns a string validator that requires user_data to begin
// with either "#cloud-config" or a shebang ("#!"). Unlike a regex-based
// validator, it never echoes the raw user_data payload into the diagnostic,
// so cloud-init contents stay out of plan/validate output.
func UserDataPrefix() validator.String {
	return userDataPrefixValidator{}
}

type userDataPrefixValidator struct{}

func (v userDataPrefixValidator) Description(_ context.Context) string {
	return `user_data must begin with "#cloud-config" or a shebang ("#!").`
}

func (v userDataPrefixValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v userDataPrefixValidator) ValidateString(
	_ context.Context,
	req validator.StringRequest,
	resp *validator.StringResponse,
) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}

	value := req.ConfigValue.ValueString()
	if !strings.HasPrefix(value, "#cloud-config") && !strings.HasPrefix(value, "#!") {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Invalid user_data",
			`user_data must begin with "#cloud-config" or a shebang ("#!").`,
		)
	}
}

// IPv4Address returns a string validator that requires a plain IPv4 address
// (no spaces, no IPv6, no leading zeros). Null and unknown values are skipped.
func IPv4Address() validator.String {
	return ipv4AddressValidator{}
}

type ipv4AddressValidator struct{}

func (v ipv4AddressValidator) Description(_ context.Context) string {
	return "must be an IPv4 address, e.g. 192.168.0.10"
}

func (v ipv4AddressValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v ipv4AddressValidator) ValidateString(
	_ context.Context,
	req validator.StringRequest,
	resp *validator.StringResponse,
) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if _, err := parseIPv4(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid IPv4 address", fmt.Sprintf("%s: %s.", req.Path, err))
	}
}

// DNSNameservers accepts the same canonical, space-separated IPv4 list as the panel's update API.
func DNSNameservers() validator.String { return dnsNameserversValidator{} }

type dnsNameserversValidator struct{}

func (v dnsNameserversValidator) Description(context.Context) string {
	return "must be one to four IPv4 addresses separated by single spaces"
}
func (v dnsNameserversValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (v dnsNameserversValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	parts := strings.Split(req.ConfigValue.ValueString(), " ")
	if len(parts) == 0 || len(parts) > 4 {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid DNS nameservers", v.Description(ctx))
		return
	}
	for _, part := range parts {
		if _, err := parseIPv4(part); err != nil {
			resp.Diagnostics.AddAttributeError(req.Path, "Invalid DNS nameservers", v.Description(ctx))
			return
		}
	}
}
