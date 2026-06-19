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

// writeOnceInt64Modifier is the Int64 analogue of writeOnceStringModifier, for a
// create-time, immutable attribute the API does not echo back (e.g. network_id,
// node_subnet — neither is present on the cluster wire response). It forces
// replacement when changed on a Terraform-managed resource, but accepts the
// configured value without replacement after an import left it null in state.
type writeOnceInt64Modifier struct{}

func WriteOnceInt64() planmodifier.Int64 {
	return writeOnceInt64Modifier{}
}

func (m writeOnceInt64Modifier) Description(_ context.Context) string {
	return "Write-once attribute: required at creation, triggers replacement if changed, but accepts value without replacement after import."
}

func (m writeOnceInt64Modifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m writeOnceInt64Modifier) PlanModifyInt64(_ context.Context, req planmodifier.Int64Request, resp *planmodifier.Int64Response) {
	// Creating a new resource — no special behavior.
	if req.State.Raw.IsNull() {
		return
	}
	// Existing, Terraform-created resource (state has a value): replace on change.
	if !req.StateValue.IsNull() && !req.StateValue.IsUnknown() {
		if !req.PlanValue.Equal(req.StateValue) {
			resp.RequiresReplace = true
		}
		return
	}
	// Existing resource with null in state (imported): accept the configured value
	// without replacement; it flows into state via Update.
}

// writeOnceBoolModifier is the Bool analogue of writeOnceStringModifier, for a
// create-time, immutable flag the API does not echo back (e.g. authorize_ssh).
type writeOnceBoolModifier struct{}

func WriteOnceBool() planmodifier.Bool {
	return writeOnceBoolModifier{}
}

func (m writeOnceBoolModifier) Description(_ context.Context) string {
	return "Write-once attribute: required at creation, triggers replacement if changed, but accepts value without replacement after import."
}

func (m writeOnceBoolModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m writeOnceBoolModifier) PlanModifyBool(_ context.Context, req planmodifier.BoolRequest, resp *planmodifier.BoolResponse) {
	// Creating a new resource — no special behavior.
	if req.State.Raw.IsNull() {
		return
	}
	// Existing, Terraform-created resource (state has a value): replace on change.
	if !req.StateValue.IsNull() && !req.StateValue.IsUnknown() {
		if !req.PlanValue.Equal(req.StateValue) {
			resp.RequiresReplace = true
		}
		return
	}
	// Existing resource with null in state (imported): accept the configured value
	// without replacement; it flows into state via Update.
}
