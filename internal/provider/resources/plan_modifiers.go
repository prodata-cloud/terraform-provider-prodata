package resources

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
)

// writeOnceStringModifier is a plan modifier for write-only, create-time attributes
// like password and ssh_public_key.
//
// Behavior:
//   - Creating (state is null): no special behavior, value flows from config
//   - Existing resource, state has value (Terraform-created): RequiresReplace if changed
//   - Existing resource, state is null (imported): accept config value without replace
type writeOnceStringModifier struct{}

func WriteOnceString() planmodifier.String {
	return writeOnceStringModifier{}
}

func (m writeOnceStringModifier) Description(_ context.Context) string {
	return "Write-once attribute: required at creation, triggers replacement if changed, but accepts value without replacement after import."
}

func (m writeOnceStringModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m writeOnceStringModifier) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	// Creating a new resource — no special behavior
	if req.State.Raw.IsNull() {
		return
	}

	// Existing resource with value in state (Terraform-created):
	// if config changed, require replacement (preserves original behavior)
	if !req.StateValue.IsNull() && !req.StateValue.IsUnknown() {
		if !req.PlanValue.Equal(req.StateValue) {
			resp.RequiresReplace = true
		}
		return
	}

	// Existing resource with null in state (imported):
	// accept config value without triggering replacement.
	// The value will flow into state via Update.
}
