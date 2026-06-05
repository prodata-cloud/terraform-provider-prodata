package resources

import (
	"context"
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
