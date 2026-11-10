package resources

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/go-uuid"
	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = &VmGatewayResource{}
	_ resource.ResourceWithConfigure   = &VmGatewayResource{}
	_ resource.ResourceWithModifyPlan  = &VmGatewayResource{}
	_ resource.ResourceWithImportState = &VmGatewayResource{}
)

const vmGatewayTimeout = 15 * time.Minute

type VmGatewayResource struct {
	client *client.Client
	poll   time.Duration // shortened in tests
}

type VmGatewayResourceModel struct {
	PlatformOperationUUID     types.String   `tfsdk:"platform_operation_uuid"`
	PlatformOperationStatus   types.String   `tfsdk:"platform_operation_status"`
	GuestOwnedGateway         types.String   `tfsdk:"guest_owned_gateway"`
	GuestOperationUUID        types.String   `tfsdk:"guest_operation_uuid"`
	GuestOperationStatus      types.String   `tfsdk:"guest_operation_status"`
	VmID                      types.Int64    `tfsdk:"vm_id"`
	NetworkID                 types.Int64    `tfsdk:"network_id"`
	InterfaceID               types.Int64    `tfsdk:"interface_id"`
	Gateway                   types.String   `tfsdk:"gateway"`
	OwnedGateway              types.String   `tfsdk:"owned_gateway"`
	RestartPending            types.Bool     `tfsdk:"restart_pending"`
	ConfigureGuest            types.Bool     `tfsdk:"configure_guest"`
	GuestConfigurationApplied types.Bool     `tfsdk:"guest_configuration_applied"`
	Region                    types.String   `tfsdk:"region"`
	ProjectTag                types.String   `tfsdk:"project_tag"`
	Timeouts                  timeouts.Value `tfsdk:"timeouts"`
}

func NewVmGatewayResource() resource.Resource { return &VmGatewayResource{} }
func (r *VmGatewayResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm_gateway"
}
func (r *VmGatewayResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages the VM's IPv4 default gateway on a selected local network interface. A VM has one default route: use only one prodata_vm_gateway per VM. Setting it can move the route from another interface, including a public interface.",
		Attributes: map[string]schema.Attribute{
			"platform_operation_uuid":     schema.StringAttribute{Computed: true, MarkdownDescription: "Caller-known durable gateway intent, retained before reservation and every physical write.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"platform_operation_status":   schema.StringAttribute{Computed: true, MarkdownDescription: "Last verified status of the gateway intent. Pending or unavailable operations retain their ownership.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"guest_owned_gateway":         schema.StringAttribute{Computed: true, MarkdownDescription: "Last gateway verified inside the guest. Retained independently during an incomplete platform route change.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"guest_operation_status":      schema.StringAttribute{Computed: true, MarkdownDescription: "Last observed status of the durable guest operation.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"guest_operation_uuid":        schema.StringAttribute{Computed: true, MarkdownDescription: "Durable identity of the guest operation associated with this gateway change.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"vm_id":                       schema.Int64Attribute{Required: true, MarkdownDescription: "ID of the virtual machine.", Validators: []validator.Int64{int64validator.AtLeast(1)}, PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"network_id":                  schema.Int64Attribute{Required: true, MarkdownDescription: "ID of a local network already attached to the VM. It can be the primary network or an additional attachment.", Validators: []validator.Int64{int64validator.AtLeast(1)}, PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"interface_id":                schema.Int64Attribute{Computed: true, MarkdownDescription: "ID of the verified network interface. Changes or deletion are refused if the interface has been replaced.", PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}},
			"gateway":                     schema.StringAttribute{Required: true, MarkdownDescription: "IPv4 address of the default gateway, in the selected interface's subnet.", Validators: []validator.String{IPv4Address()}},
			"owned_gateway":               schema.StringAttribute{Computed: true, MarkdownDescription: "Gateway last applied or imported by this resource; used to avoid deleting a different route.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"restart_pending":             schema.BoolAttribute{Computed: true, MarkdownDescription: "A previous gateway change stopped a running VM but could not confirm its restart.", PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}},
			"configure_guest":             schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), MarkdownDescription: "Opt in to persistent guest IPv4 default-route configuration after the platform gateway change. Requires a running Linux QEMU guest agent with supported static Netplan configuration; preflight fails before changing the gateway on unsupported guests."},
			"guest_configuration_applied": schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the requested guest address and default route were verified. Drift is reconciled in place when configure_guest is enabled.", PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}},
			"region":                      schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Region override; defaults to the provider region.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()}},
			"project_tag":                 schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Project override; defaults to the provider project.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()}},
			"timeouts":                    timeouts.Attributes(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}
func (r *VmGatewayResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	var ok bool
	r.client, ok = req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Resource Configure Type", fmt.Sprintf("Expected *client.Client, got %T.", req.ProviderData))
	}
}
func (r *VmGatewayResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		if !req.State.Raw.IsNull() {
			resp.Diagnostics.AddWarning("Removing a VM default gateway",
				"Removing the gateway may leave the VM without a default route and may power off and restart a running VM. Depending on the image and cloud-init configuration, the changed data may be treated as a new instance, regenerating SSH host keys and rerunning once-per-instance scripts.")
		}
		return
	}
	var plan, state VmGatewayResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	changed := req.State.Raw.IsNull()
	if !changed {
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
		changed = !state.Gateway.Equal(plan.Gateway)
		if plan.ConfigureGuest.IsUnknown() || !plan.ConfigureGuest.Equal(state.ConfigureGuest) ||
			plan.ConfigureGuest.ValueBool() && (!state.GuestConfigurationApplied.ValueBool() || changed) {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("guest_configuration_applied"), types.BoolUnknown())...)
		}
		if state.RestartPending.ValueBool() {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("restart_pending"), types.BoolUnknown())...)
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("owned_gateway"), types.StringUnknown())...)
		}
	}
	if changed || !req.State.Raw.IsNull() && (state.RestartPending.ValueBool() || gatewayOperationPending(&state)) {
		for _, name := range []string{"platform_operation_uuid", "platform_operation_status"} {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root(name), types.StringUnknown())...)
		}
	}
	if changed {
		if !req.State.Raw.IsNull() {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("owned_gateway"), types.StringUnknown())...)
		}
		resp.Diagnostics.AddWarning("Changing a VM default gateway",
			"This may move the VM's default route from another interface. A running VM is powered off and started again; the stop is not a clean guest shutdown. A guest without cloud-init must be configured separately. Depending on the image and cloud-init configuration, the changed data may be treated as a new instance, regenerating SSH host keys and rerunning once-per-instance scripts.")
	}
	if !changed && !req.State.Raw.IsNull() && !state.RestartPending.ValueBool() && !gatewayOperationPending(&state) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("platform_operation_uuid"), state.PlatformOperationUUID)...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("platform_operation_status"), state.PlatformOperationStatus)...)
	}
	if !req.State.Raw.IsNull() {
		guestChange := plan.ConfigureGuest.IsUnknown() || !plan.ConfigureGuest.Equal(state.ConfigureGuest) || plan.ConfigureGuest.ValueBool() && (!state.GuestConfigurationApplied.ValueBool() || !plan.Gateway.Equal(state.Gateway))
		if guestChange {
			for _, name := range []string{"guest_operation_uuid", "guest_operation_status", "guest_owned_gateway"} {
				resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root(name), types.StringUnknown())...)
			}
		} else {
			for name, value := range map[string]types.String{"guest_operation_uuid": state.GuestOperationUUID, "guest_operation_status": state.GuestOperationStatus, "guest_owned_gateway": state.GuestOwnedGateway} {
				resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root(name), value)...)
			}
		}

	}
}

func (r *VmGatewayResource) opts(data *VmGatewayResourceModel) *client.RequestOpts {
	operation := ""
	if gatewayOperationPending(data) {
		operation = data.PlatformOperationUUID.ValueString()
	}
	return &client.RequestOpts{Region: data.Region.ValueString(), ProjectTag: data.ProjectTag.ValueString(), GuestOperationUUID: data.GuestOperationUUID.ValueString(), GatewayOperationUUID: operation, GatewayNetworkID: data.NetworkID.ValueInt64()}
}
func (r *VmGatewayResource) pollInterval() time.Duration {
	if r.poll > 0 {
		return r.poll
	}
	return vmRestartPollInterval
}

func (r *VmGatewayResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data VmGatewayResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, diags := data.Timeouts.Create(ctx, vmGatewayTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if data.Region.IsNull() || data.Region.IsUnknown() {
		data.Region = types.StringValue(r.client.Region)
	}
	if data.ProjectTag.IsNull() || data.ProjectTag.IsUnknown() {
		data.ProjectTag = types.StringValue(r.client.ProjectTag)
	}
	gateway := data.Gateway.ValueString()
	data.PlatformOperationUUID = types.StringNull()
	data.PlatformOperationStatus = types.StringNull()
	data.InterfaceID = types.Int64Null()
	data.GuestOwnedGateway = types.StringNull()
	data.GuestConfigurationApplied = types.BoolValue(false)
	if err := r.client.RequireGatewayOperations(ctx, data.VmID.ValueInt64(), r.opts(&data)); err != nil {
		resp.Diagnostics.AddError("Recoverable Gateway Operations Unavailable", err.Error())
		return
	}
	if data.ConfigureGuest.ValueBool() {
		if err := requireGuestNetworkSupport(ctx, r.client, data.VmID.ValueInt64(), r.opts(&data)); err != nil {
			resp.Diagnostics.AddError("Guest Network Automation Unavailable", err.Error())
			return
		}
	}
	data.RestartPending = types.BoolValue(false)
	data.GuestOperationUUID = types.StringNull()
	data.GuestOperationStatus = types.StringNull()
	data.OwnedGateway = types.StringNull()
	change := client.ChangeVmGatewayRequest{NetworkID: data.NetworkID.ValueInt64(), Gateway: &gateway}
	if data.ConfigureGuest.ValueBool() {
		if err := r.prepareGuestGateway(ctx, &data, change); err != nil {
			resp.Diagnostics.AddError("Unable to Reserve Guest Gateway", err.Error())
			return
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	result, changes := r.apply(ctx, &data, change, func() { resp.Diagnostics.Append(resp.State.Set(ctx, &data)...) })
	resp.Diagnostics.Append(changes...)
	if resp.Diagnostics.HasError() {
		if data.GuestOperationUUID.ValueString() != "" {
			if actual, readErr := r.client.GetVmGateway(ctx, data.VmID.ValueInt64(), data.NetworkID.ValueInt64(), r.opts(&data)); readErr == nil && verifiedGuestGatewayChild(&data, actual) {
				data.OwnedGateway = types.StringValue(*actual.Gateway)
			}
		}
		if data.RestartPending.ValueBool() {
			if result != nil {
				data.InterfaceID = types.Int64Value(result.InterfaceID)
				data.OwnedGateway = data.Gateway
			} else if data.OwnedGateway.IsUnknown() {
				data.OwnedGateway = types.StringNull()
			}
			resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
		} else if data.GuestOperationUUID.ValueString() != "" || data.PlatformOperationUUID.ValueString() != "" {
			resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
		}
		return
	}
	data.InterfaceID = types.Int64Value(result.InterfaceID)
	data.OwnedGateway = data.Gateway
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	if data.ConfigureGuest.ValueBool() {
		if err := r.configureGuest(ctx, &data, &gateway, nil); err != nil {
			resp.Diagnostics.AddError("Gateway Changed but Guest Configuration Failed", err.Error()+" The platform gateway remains recorded in state.")
			return
		}
		data.GuestConfigurationApplied = types.BoolValue(true)
		data.GuestOperationStatus = types.StringValue("SUCCESS")
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	}
}

func (r *VmGatewayResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data, state VmGatewayResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.GuestOwnedGateway.IsNull() && state.ConfigureGuest.ValueBool() && state.GuestConfigurationApplied.ValueBool() {
		state.GuestOwnedGateway = state.OwnedGateway
	}
	data.GuestOwnedGateway = state.GuestOwnedGateway
	data.GuestOperationUUID = state.GuestOperationUUID
	data.GuestOperationStatus = state.GuestOperationStatus
	data.PlatformOperationUUID = state.PlatformOperationUUID
	data.PlatformOperationStatus = state.PlatformOperationStatus
	data.InterfaceID = state.InterfaceID

	timeout, diags := data.Timeouts.Update(ctx, vmGatewayTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if gatewayOperationPending(&state) {
		if err := r.settlePlatformOperation(ctx, &state, true); err != nil {
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			resp.Diagnostics.AddError("Gateway Operation Is Unverified", err.Error())
			return
		}
		data.PlatformOperationUUID = state.PlatformOperationUUID
		data.PlatformOperationStatus = state.PlatformOperationStatus
		data.OwnedGateway = state.OwnedGateway
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	}
	if state.RestartPending.ValueBool() {
		if err := resumePendingVM(ctx, r.client, state.VmID.ValueInt64(), r.opts(&state), r.pollInterval()); err != nil {
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			resp.Diagnostics.AddError("Unable to Restore VM After Gateway Change", err.Error())
			return
		}
		// A prior failed delete or lost write answer can have changed the route even if
		// Terraform's saved gateway equals the plan. Verify it even with -refresh=false.
		actual, err := r.client.GetVmGateway(ctx, state.VmID.ValueInt64(), state.NetworkID.ValueInt64(), r.opts(&state))
		if err != nil || actual.Pending || actual.InterfaceID != state.InterfaceID.ValueInt64() {
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			resp.Diagnostics.AddError("Unable to Confirm VM Gateway After Restart", fmt.Sprintf("Verify the selected interface and pending configuration before retrying (error: %v).", err))
			return
		}
		if actual.Gateway == nil || actual.ReconcileRequired || state.OwnedGateway.IsNull() ||
			state.OwnedGateway.IsUnknown() || *actual.Gateway != state.OwnedGateway.ValueString() {
			state.Gateway = types.StringNull()
		} else {
			state.Gateway = types.StringValue(*actual.Gateway)
		}
		state.RestartPending = types.BoolValue(false)
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		data.RestartPending = types.BoolValue(false)
		data.OwnedGateway = state.OwnedGateway
	}
	if state.ConfigureGuest.ValueBool() && !data.ConfigureGuest.ValueBool() {
		request, err := gatewayGuestRequest(ctx, r.client, state.VmID.ValueInt64(), state.InterfaceID.ValueInt64(), state.NetworkID.ValueInt64(), nil, nil, r.opts(&state))
		if err == nil {
			request.ConfigureDefaultRoute = false
			err = releaseGuestNetwork(ctx, r.client, state.VmID.ValueInt64(), state.InterfaceID.ValueInt64(), request, r.opts(&state), r.pollInterval())
		}
		if err != nil {
			resp.Diagnostics.AddError("Unable to Release Guest Network Ownership", err.Error())
			return
		}
		data.GuestOperationUUID = types.StringNull()
		data.GuestOperationStatus = types.StringNull()
		data.GuestOwnedGateway = types.StringNull()
		data.GuestConfigurationApplied = types.BoolValue(false)
		state.ConfigureGuest = data.ConfigureGuest
		state.GuestConfigurationApplied = data.GuestConfigurationApplied
		state.GuestOperationUUID = data.GuestOperationUUID
		state.GuestOperationStatus = data.GuestOperationStatus
		state.GuestOwnedGateway = data.GuestOwnedGateway
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	if data.ConfigureGuest.ValueBool() {
		if err := requireGuestNetworkSupport(ctx, r.client, data.VmID.ValueInt64(), r.opts(&data)); err != nil {
			resp.Diagnostics.AddError("Guest Network Automation Unavailable", err.Error())
			return
		}
	}
	if data.Gateway.Equal(state.Gateway) {
		if data.ConfigureGuest.ValueBool() && (!state.ConfigureGuest.ValueBool() || !state.GuestConfigurationApplied.ValueBool()) {
			gateway := data.Gateway.ValueString()
			if err := r.configureGuest(ctx, &data, &gateway, nil); err != nil {
				resp.Diagnostics.AddError("Unable to Configure Guest Gateway", err.Error())
				return
			}
			data.GuestConfigurationApplied = types.BoolValue(true)
			data.GuestOperationStatus = types.StringValue("SUCCESS")
		} else if !data.ConfigureGuest.ValueBool() {
			data.GuestConfigurationApplied = types.BoolValue(false)
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
		return
	}
	gateway := data.Gateway.ValueString()
	interfaceID := state.InterfaceID.ValueInt64()
	change := client.ChangeVmGatewayRequest{NetworkID: data.NetworkID.ValueInt64(), Gateway: &gateway, ExpectedInterfaceID: &interfaceID}
	// The planned verification result is unknown until both platform and guest work
	// finish. Every partial-change checkpoint must record a concrete unverified value.
	data.GuestConfigurationApplied = types.BoolValue(false)
	data.RestartPending = state.RestartPending
	checkpoint := state
	if data.ConfigureGuest.ValueBool() {
		if err := r.prepareGuestGateway(ctx, &data, change); err != nil {
			resp.Diagnostics.AddError("Unable to Reserve Guest Gateway", err.Error())
			return
		}
		checkpoint.GuestOperationUUID = data.GuestOperationUUID
		checkpoint.GuestOperationStatus = data.GuestOperationStatus
		checkpoint.InterfaceID = data.InterfaceID
		checkpoint.GuestConfigurationApplied = types.BoolValue(false)
		resp.Diagnostics.Append(resp.State.Set(ctx, &checkpoint)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	result, changes := r.apply(ctx, &data, change, func() {
		checkpoint.PlatformOperationUUID = data.PlatformOperationUUID
		checkpoint.PlatformOperationStatus = data.PlatformOperationStatus
		checkpoint.InterfaceID = data.InterfaceID
		resp.Diagnostics.Append(resp.State.Set(ctx, &checkpoint)...)
	})
	resp.Diagnostics.Append(changes...)
	if resp.Diagnostics.HasError() {
		checkpoint.RestartPending = data.RestartPending
		checkpoint.PlatformOperationUUID = data.PlatformOperationUUID
		checkpoint.PlatformOperationStatus = data.PlatformOperationStatus
		checkpoint.GuestConfigurationApplied = types.BoolValue(false)
		if result != nil {
			data.InterfaceID = types.Int64Value(result.InterfaceID)
			data.OwnedGateway = data.Gateway
			resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
		} else if data.GuestOperationUUID.ValueString() != "" {
			if actual, err := r.client.GetVmGateway(ctx, data.VmID.ValueInt64(), data.NetworkID.ValueInt64(), r.opts(&data)); err == nil && verifiedGuestGatewayChild(&data, actual) {
				checkpoint.OwnedGateway = types.StringValue(*actual.Gateway)
				checkpoint.Gateway = checkpoint.OwnedGateway
			}
			resp.Diagnostics.Append(resp.State.Set(ctx, &checkpoint)...)
		} else if data.RestartPending.ValueBool() || data.PlatformOperationUUID.ValueString() != "" {
			resp.Diagnostics.Append(resp.State.Set(ctx, &checkpoint)...)
		}
		return
	}

	data.InterfaceID = types.Int64Value(result.InterfaceID)
	data.OwnedGateway = data.Gateway
	data.GuestConfigurationApplied = types.BoolValue(false)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	if data.ConfigureGuest.ValueBool() {
		if err := r.configureGuest(ctx, &data, &gateway, nil); err != nil {
			resp.Diagnostics.AddError("Gateway Changed but Guest Configuration Failed", err.Error())
			return
		}
		data.GuestConfigurationApplied = types.BoolValue(true)
		data.GuestOperationStatus = types.StringValue("SUCCESS")
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	}
}

func (r *VmGatewayResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data VmGatewayResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if data.GuestOwnedGateway.IsNull() && data.ConfigureGuest.ValueBool() && data.GuestConfigurationApplied.ValueBool() {
		data.GuestOwnedGateway = data.OwnedGateway
	}

	if data.ConfigureGuest.IsNull() {
		data.ConfigureGuest = types.BoolValue(false)
	}
	if gatewayOperationPending(&data) {
		if err := r.settlePlatformOperation(ctx, &data, false); err != nil {
			resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
			resp.Diagnostics.AddWarning("Gateway Operation Is Unverified", err.Error())
			return
		}
	}
	actual, err := r.client.GetVmGateway(ctx, data.VmID.ValueInt64(), data.NetworkID.ValueInt64(), r.opts(&data))
	if err != nil {
		if data.PlatformOperationUUID.ValueString() != "" {
			proof, proofErr := r.client.ReadGatewayOperation(ctx, data.VmID.ValueInt64(), data.NetworkID.ValueInt64(), data.PlatformOperationUUID.ValueString(), r.opts(&data))
			if proofErr == nil && proof.Status == "DELETED" {
				resp.State.RemoveResource(ctx)
				return
			}
		}
		resp.Diagnostics.AddError("Unable to Read VM Gateway", err.Error())
		return
	}
	if !data.InterfaceID.IsNull() && !data.InterfaceID.IsUnknown() && data.InterfaceID.ValueInt64() != actual.InterfaceID {
		resp.Diagnostics.AddError("VM network interface was replaced", "The selected network now has a different interface ID. Review the VM and remove or re-import this resource before changing the gateway.")
		return
	}
	if actual.Pending {
		resp.Diagnostics.AddError("VM gateway has a pending change", "Resolve the pending hypervisor configuration before applying again.")
		return
	}
	data.InterfaceID = types.Int64Value(actual.InterfaceID)
	if verifiedGuestGatewayChild(&data, actual) {
		data.OwnedGateway = types.StringValue(*actual.Gateway)
		data.Gateway = data.OwnedGateway
	}
	if actual.Gateway == nil || actual.ReconcileRequired || data.OwnedGateway.IsNull() ||
		data.OwnedGateway.IsUnknown() || *actual.Gateway != data.OwnedGateway.ValueString() {
		data.Gateway = types.StringNull()
	} else {
		data.Gateway = types.StringValue(*actual.Gateway)
	}
	if data.ConfigureGuest.ValueBool() && (actual.Gateway == nil || data.OwnedGateway.IsNull() || *actual.Gateway != data.OwnedGateway.ValueString()) {
		resp.Diagnostics.AddWarning("Guest Gateway Configuration Is Unverified", "The platform route differs from the owned gateway. The last guest verification is preserved without probing a different route.")
	} else if data.ConfigureGuest.ValueBool() {
		request, err := gatewayGuestRequest(ctx, r.client, data.VmID.ValueInt64(), actual.InterfaceID, data.NetworkID.ValueInt64(), actual.Gateway, nil, r.opts(&data))
		if err != nil {
			resp.Diagnostics.AddWarning("Guest Gateway Configuration Is Unverified", err.Error()+" The last verified result is preserved.")
			resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
			return
		}
		guest, err := r.client.GetVmGuestNetworkState(ctx, data.VmID.ValueInt64(), actual.InterfaceID, request, r.opts(&data))
		if err != nil {
			resp.Diagnostics.AddWarning("Guest Gateway Configuration Is Unverified", err.Error())
		} else if confirmedGuestNetwork(guest) {
			if guest.OperationUUID == data.GuestOperationUUID.ValueString() && guest.Status != "" {
				data.GuestOperationStatus = types.StringValue(guest.Status)
			}
			data.GuestConfigurationApplied = types.BoolValue(*guest.Configured)
			if *guest.Configured {
				data.GuestOwnedGateway = data.OwnedGateway
			}
		} else {
			resp.Diagnostics.AddWarning("Guest Gateway Configuration Is Unverified", guest.Reason+" The last verified result is preserved.")
		}
	} else {
		data.GuestConfigurationApplied = types.BoolValue(false)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *VmGatewayResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data VmGatewayResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if data.GuestOwnedGateway.IsNull() && data.ConfigureGuest.ValueBool() && data.GuestConfigurationApplied.ValueBool() {
		data.GuestOwnedGateway = data.OwnedGateway
	}

	if data.PlatformOperationUUID.ValueString() != "" {
		if err := r.settlePlatformOperation(ctx, &data, true); err != nil {
			resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
			resp.Diagnostics.AddError("Gateway Operation Is Unverified", err.Error())
			return
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	}

	if data.PlatformOperationStatus.ValueString() == "DELETED" {
		return
	}

	if data.RestartPending.ValueBool() {
		resp.Diagnostics.AddError("VM restart needs recovery", "The gateway operation stopped a running VM and could not confirm its restart. Restore the previous resource configuration if it was removed or changed to require replacement; if Terraform marked the resource tainted, run terraform untaint for it. Apply again to recover the VM before deleting or replacing the resource.")
		return
	}
	if (data.OwnedGateway.IsNull() || data.OwnedGateway.IsUnknown()) && data.GuestOperationUUID.ValueString() != "" {
		actual, err := r.client.GetVmGateway(ctx, data.VmID.ValueInt64(), data.NetworkID.ValueInt64(), r.opts(&data))
		if err != nil {
			resp.Diagnostics.AddError("Unable to Verify Guest Gateway Ownership", err.Error())
			return
		}
		if verifiedGuestGatewayChild(&data, actual) {
			data.OwnedGateway = types.StringValue(*actual.Gateway)
		}
	}
	if data.OwnedGateway.IsNull() || data.OwnedGateway.IsUnknown() {
		if data.GuestOperationUUID.ValueString() != "" {
			if err := r.client.CancelVmGuestNetworkReservation(ctx, data.VmID.ValueInt64(), data.InterfaceID.ValueInt64(), data.GuestOperationUUID.ValueString(), r.opts(&data)); err != nil {
				resp.Diagnostics.AddError("Unable to Cancel Guest Reservation", err.Error())
			}
		}
		return
	}
	timeout, diags := data.Timeouts.Delete(ctx, vmGatewayTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	actual, readErr := r.client.GetVmGateway(ctx, data.VmID.ValueInt64(), data.NetworkID.ValueInt64(), r.opts(&data))
	if readErr != nil {
		resp.Diagnostics.AddError("Unable to Verify Gateway Before Delete", readErr.Error())
		return
	}
	if actual.Pending {
		resp.Diagnostics.AddError("Gateway Operation Is Unverified", "The pending platform operation retains its ownership.")
		return
	}
	if verifiedGuestGatewayChild(&data, actual) {
		data.OwnedGateway = types.StringValue(*actual.Gateway)
		data.Gateway = data.OwnedGateway
	}
	cancelUnsubmittedGuest := false
	if data.GuestOperationUUID.ValueString() != "" {
		request, err := gatewayGuestRequest(ctx, r.client, data.VmID.ValueInt64(), data.InterfaceID.ValueInt64(), data.NetworkID.ValueInt64(), data.OwnedGateway.ValueStringPointer(), nil, r.opts(&data))
		if err != nil {
			resp.Diagnostics.AddError("Unable to Verify Guest Reservation", err.Error())
			return
		}
		state, err := r.client.GetVmGuestNetworkState(ctx, data.VmID.ValueInt64(), data.InterfaceID.ValueInt64(), request, r.opts(&data))
		if err != nil {
			resp.Diagnostics.AddError("Unable to Verify Guest Reservation", err.Error())
			return
		}
		cancelUnsubmittedGuest = state.OperationUUID == data.GuestOperationUUID.ValueString() && (state.Status == "RESERVED" || state.Status == "NOT_SUBMITTED")
		if state.OperationUUID == data.GuestOperationUUID.ValueString() {
			data.GuestOperationStatus = types.StringValue(state.Status)
		}
		if cancelUnsubmittedGuest {
			cancelErr := r.client.CancelVmGuestNetworkReservation(ctx, data.VmID.ValueInt64(), data.InterfaceID.ValueInt64(), data.GuestOperationUUID.ValueString(), r.opts(&data))
			if cancelErr == nil {
				data.GuestOperationUUID = types.StringNull()
				data.GuestOperationStatus = types.StringValue("NOT_SUBMITTED")
				cancelUnsubmittedGuest = false
			} else if !client.IsAPIError(cancelErr, 370970) {
				resp.Diagnostics.AddError("Unable to Cancel Guest Reservation", cancelErr.Error())
				return
			}
		}
	}
	if data.ConfigureGuest.ValueBool() && !cancelUnsubmittedGuest && actual.Gateway != nil && *actual.Gateway != data.OwnedGateway.ValueString() {
		resp.Diagnostics.AddWarning("Gateway Was Replaced Outside Terraform", "The other gateway and guest route are preserved; no new guest operation was reserved.")
		return
	}
	if data.ConfigureGuest.ValueBool() && !cancelUnsubmittedGuest {
		if err := requireGuestNetworkDrainSupport(ctx, r.client, data.VmID.ValueInt64(), r.opts(&data)); err != nil {
			resp.Diagnostics.AddError("Guest Network Automation Unavailable", err.Error())
			return
		}
	}
	expected, interfaceID := data.OwnedGateway.ValueString(), data.InterfaceID.ValueInt64()
	change := client.ChangeVmGatewayRequest{NetworkID: data.NetworkID.ValueInt64(), ExpectedGateway: &expected, ExpectedInterfaceID: &interfaceID, Clear: true}
	if data.ConfigureGuest.ValueBool() && !cancelUnsubmittedGuest {
		if err := r.prepareGuestGateway(ctx, &data, change); err != nil {
			resp.Diagnostics.AddError("Unable to Reserve Guest Gateway", err.Error())
			return
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	result, changes := r.apply(ctx, &data, change, func() { resp.Diagnostics.Append(resp.State.Set(ctx, &data)...) })
	resp.Diagnostics.Append(changes...)
	if resp.Diagnostics.HasError() && (data.RestartPending.ValueBool() || data.PlatformOperationUUID.ValueString() != "") {
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	}
	if !resp.Diagnostics.HasError() && cancelUnsubmittedGuest {
		if err := r.client.CancelVmGuestNetworkReservation(ctx, data.VmID.ValueInt64(), data.InterfaceID.ValueInt64(), data.GuestOperationUUID.ValueString(), r.opts(&data)); err != nil {
			resp.Diagnostics.AddError("Unable to Cancel Guest Reservation", err.Error())
		}
		if !resp.Diagnostics.HasError() && data.GuestOwnedGateway.ValueString() != "" {
			expectedGuest := data.GuestOwnedGateway.ValueString()
			if err := r.configureGuest(ctx, &data, nil, &expectedGuest); err != nil {
				resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
				resp.Diagnostics.AddError("Unable to Clear Prior Guest Gateway", err.Error())
			}
		}
		return
	}
	if !resp.Diagnostics.HasError() && data.ConfigureGuest.ValueBool() && result != nil && result.Gateway == nil {
		data.GuestConfigurationApplied = types.BoolValue(false)
		if err := r.configureGuest(ctx, &data, nil, guestCleanupExpected(&data, &expected)); err != nil {
			resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
			resp.Diagnostics.AddError("Platform Gateway Cleared but Guest Cleanup Failed", err.Error())
		}
	}
}

func (r *VmGatewayResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, ":")
	if len(parts) != 2 && len(parts) != 3 {
		resp.Diagnostics.AddError("Invalid Import ID", "Use <vm_id>:<network_id> or <vm_id>:<network_id>:<operation_uuid> for an explicit known operation.")
		return
	}
	vmID, err1 := strconv.ParseInt(parts[0], 10, 64)
	networkID, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err1 != nil || err2 != nil || vmID <= 0 || networkID <= 0 {
		resp.Diagnostics.AddError("Invalid Import ID", "Use two positive integers: <vm_id>:<network_id>.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("vm_id"), vmID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("network_id"), networkID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("region"), r.client.Region)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_tag"), r.client.ProjectTag)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("restart_pending"), false)...)
	if len(parts) == 3 {
		operation, readErr := r.client.ReadGatewayOperation(ctx, vmID, networkID, parts[2], &client.RequestOpts{Region: r.client.Region, ProjectTag: r.client.ProjectTag})
		if readErr != nil {
			resp.Diagnostics.AddError("Unable to Import Known Gateway Operation", readErr.Error())
			return
		}
		if operation.Operation != "SET_GATEWAY" || operation.Status == "NOT_SUBMITTED" || operation.Status == "DELETED" || operation.Result == nil || operation.Result.Gateway == nil {
			resp.Diagnostics.AddError("Gateway Intent Cannot Be Imported", "Only a retained SET intent can be imported as a gateway resource. Resolve a conditional CLEAR by its exact UUID through the API before importing a current route.")
			return
		}
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("platform_operation_uuid"), parts[2])...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("platform_operation_status"), operation.Status)...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("interface_id"), *operation.InterfaceID)...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("gateway"), *operation.Result.Gateway)...)
		if operation.Status == "SUCCESS" {
			resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("owned_gateway"), *operation.Result.Gateway)...)
		}
		return
	}
	// Import explicitly adopts the route present now. Ordinary refresh must never adopt a
	// route after an unconfirmed create: it could belong to another actor.
	actual, err := r.client.GetVmGateway(ctx, vmID, networkID, &client.RequestOpts{Region: r.client.Region, ProjectTag: r.client.ProjectTag})
	if err != nil {
		resp.Diagnostics.AddError("Unable to Import VM Gateway", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("interface_id"), actual.InterfaceID)...)
	if actual.Gateway != nil {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("owned_gateway"), *actual.Gateway)...)
	}
}

// apply checks first, then changes the configuration while stopped. The panel's durable guard
// prevents its start endpoints from starting the VM when the outcome of the write is unresolved.
func (r *VmGatewayResource) apply(ctx context.Context, data *VmGatewayResourceModel, change client.ChangeVmGatewayRequest, checkpoints ...func()) (*client.ChangedVmGateway, diag.Diagnostics) {
	var diags diag.Diagnostics
	vmID := data.VmID.ValueInt64()
	if err := r.client.RequireGatewayOperations(ctx, vmID, r.opts(data)); err != nil {
		diags.AddError("Recoverable Gateway Operations Unavailable", err.Error())
		return nil, diags
	}
	if gatewayOperationPending(data) {
		diags.AddError("Gateway Operation Is Unverified", "Resolve or cancel the existing known operation before allocating another intent.")
		return nil, diags
	}
	id, err := uuid.GenerateUUID()
	if err != nil {
		diags.AddError("Unable to Retain Gateway Intent", err.Error())
		return nil, diags
	}
	data.PlatformOperationUUID = types.StringValue(id)
	data.PlatformOperationStatus = types.StringValue("RESERVED")
	for _, checkpoint := range checkpoints {
		checkpoint()
	}
	opts := r.opts(data)
	operation, err := r.client.ReserveGatewayOperation(ctx, vmID, id, change, opts)
	if err != nil {
		diags.AddError("Unable to Reserve VM Gateway", err.Error()+" The caller-known UUID is retained for exact cancellation or recovery.")
		return nil, diags
	}
	if operation.Status == "NOT_SUBMITTED" {
		data.PlatformOperationStatus = types.StringValue(operation.Status)
		diags.AddError("Gateway Reservation Was Cancelled", "The exact UUID cannot be submitted. Apply again to allocate a fresh intent.")
		return nil, diags
	}
	forecast := operation.Result
	data.InterfaceID = types.Int64Value(*operation.InterfaceID)
	for _, checkpoint := range checkpoints {
		checkpoint()
	}
	if forecast.PreviousGatewayOnAnotherInterface {
		diags.AddWarning("The VM default route will move", "The current default gateway is on another interface. Setting this gateway moves the route to the selected local interface.")
	}
	if forecast.RequiresGuestConfiguration && !data.ConfigureGuest.ValueBool() {
		diags.AddWarning("Guest network needs configuration", "The VM has no cloud-init; configure its default gateway inside the guest as well.")
	}
	if !forecast.Changed {
		result, writeErr := r.submitPlatformOperation(ctx, data)
		if writeErr != nil {
			diags.AddError("Unable to Verify VM Gateway", writeErr.Error())
		}
		return result, diags
	}
	var applied *client.ChangedVmGateway
	write := func(writeCtx context.Context) error {
		var writeErr error
		applied, writeErr = r.submitPlatformOperation(writeCtx, data)
		if writeErr == nil {
			opts.GatewayOperationUUID = ""
		}
		return writeErr
	}
	cycle, cycleErr := restartRunningVMWith(ctx, r.client, vmID, opts, r.pollInterval(), write)
	if cycle.hookRan && cycle.hookErr != nil {
		detail := cycle.hookErr.Error()
		if cycleErr != nil {
			data.RestartPending = types.BoolValue(true)
			detail += "; starting the VM again: " + cycleErr.Error() +
				". Resolve any pending platform operation, untaint this resource if Terraform tainted it, and apply again to recover the VM."
		} else {
			detail += "; the VM was started again"
		}
		diags.AddError("Unable to Change VM Gateway", detail)
		return nil, diags
	}
	if cycle.hookSkipped || errors.Is(cycleErr, errHookTimeShort) {
		detail := "The gateway was not changed. Increase the operation timeout and apply again."
		if cycle.hookSkipped && cycleErr == nil {
			detail += " The VM was started again."
		} else if cycleErr != nil {
			if cycle.hookSkipped {
				data.RestartPending = types.BoolValue(true)
				detail += " Restore the VM and apply again; untaint the resource first if needed."
			}
			detail += " " + cycleErr.Error()
		}
		diags.AddError("Not enough time to change VM gateway", detail)
		return nil, diags
	}
	if cycle.hookRan {
		if cycleErr != nil {
			data.RestartPending = types.BoolValue(true)
			diags.AddError("VM gateway changed but the VM did not restart", cycleErr.Error()+". Restore the VM and apply again; for a tainted resource, untaint it before applying.")
		}
		return applied, diags
	}
	if cycleErr != nil {
		diags.AddError("Unable to Stop VM", cycleErr.Error())
		return nil, diags
	}
	if cycle.cycled {
		diags.AddError("VM was started during the change", "The VM was started before the gateway could be changed; apply again.")
		return nil, diags
	}
	if err := write(ctx); err != nil {
		diags.AddError("Unable to Change VM Gateway", err.Error())
		return nil, diags
	}
	return applied, diags
}

// The local guest reservation exists before STOP/GW/START. Only its exact UUID can
// authorize those children; CORE guest execution starts after their verified completion.
func (r *VmGatewayResource) prepareGuestGateway(ctx context.Context, data *VmGatewayResourceModel, change client.ChangeVmGatewayRequest) error {
	actual, err := r.client.GetVmGateway(ctx, data.VmID.ValueInt64(), data.NetworkID.ValueInt64(), r.opts(data))
	if err != nil {
		return err
	}
	if actual.Pending {
		return fmt.Errorf("the selected interface has a pending platform change")
	}
	if !data.InterfaceID.IsNull() && !data.InterfaceID.IsUnknown() && data.InterfaceID.ValueInt64() != actual.InterfaceID {
		return fmt.Errorf("the selected interface was replaced")
	}
	request, err := gatewayGuestRequest(ctx, r.client, data.VmID.ValueInt64(), actual.InterfaceID, data.NetworkID.ValueInt64(), change.Gateway, change.ExpectedGateway, r.opts(data))
	if err != nil {
		return err
	}
	if _, err = r.client.DryRunVmGuestNetwork(ctx, data.VmID.ValueInt64(), actual.InterfaceID, request, r.opts(data)); err != nil {
		return err
	}
	reserved, err := r.client.ReserveVmGuestNetwork(ctx, data.VmID.ValueInt64(), actual.InterfaceID, request, r.opts(data))
	if err != nil {
		observed, readErr := r.client.GetVmGuestNetworkState(ctx, data.VmID.ValueInt64(), actual.InterfaceID, request, r.opts(data))
		if readErr != nil || observed.Status != "RESERVED" || observed.OperationUUID == "" {
			return err
		}
		reserved = observed
	}
	if reserved.Status == "SUCCESS" {
		matches := change.Clear && actual.Gateway == nil || !change.Clear && actual.Gateway != nil && change.Gateway != nil && *actual.Gateway == *change.Gateway
		if !matches || actual.ReconcileRequired {
			return fmt.Errorf("a fresh guest reservation is required before repairing the platform gateway")
		}
	}
	data.InterfaceID = types.Int64Value(actual.InterfaceID)
	data.GuestOperationUUID = types.StringValue(reserved.OperationUUID)
	data.GuestOperationStatus = types.StringValue(reserved.Status)
	return nil
}

func verifiedGuestGatewayChild(data *VmGatewayResourceModel, actual *client.VmGateway) bool {
	return actual != nil && data.GuestOperationUUID.ValueString() != "" && actual.GuestOperationUUID == data.GuestOperationUUID.ValueString() && actual.GuestChildVerified && !actual.Pending && !actual.ReconcileRequired && actual.InterfaceID == data.InterfaceID.ValueInt64() && actual.Gateway != nil
}

func guestCleanupExpected(data *VmGatewayResourceModel, fallback *string) *string {
	if data.GuestOwnedGateway.ValueString() != "" {
		v := data.GuestOwnedGateway.ValueString()
		return &v
	}
	return fallback
}

func gatewayOperationPending(data *VmGatewayResourceModel) bool {
	return data.PlatformOperationUUID.ValueString() != "" && data.PlatformOperationStatus.ValueString() != "SUCCESS" && data.PlatformOperationStatus.ValueString() != "NOT_SUBMITTED" && data.PlatformOperationStatus.ValueString() != "DELETED"
}
func (r *VmGatewayResource) settlePlatformOperation(ctx context.Context, data *VmGatewayResourceModel, cancel bool) error {
	if data.PlatformOperationUUID.ValueString() == "" {
		return nil
	}
	read := func() (*client.GatewayOperation, error) {
		return r.client.ReadGatewayOperation(ctx, data.VmID.ValueInt64(), data.NetworkID.ValueInt64(), data.PlatformOperationUUID.ValueString(), r.opts(data))
	}
	out, err := read()
	if cancel && (err != nil || out.Status == "RESERVED" || out.Status == "SUBMITTING") {
		out, err = r.client.CancelGatewayOperation(ctx, data.VmID.ValueInt64(), data.NetworkID.ValueInt64(), data.PlatformOperationUUID.ValueString(), r.opts(data))
	}
	if err != nil {
		return err
	}
	data.PlatformOperationStatus = types.StringValue(out.Status)
	if out.Status == "NOT_SUBMITTED" || out.Status == "DELETED" {
		if out.Status == "NOT_SUBMITTED" {
			data.Gateway = types.StringNull()
		}
		return nil
	}
	if out.Status != "SUCCESS" {
		return fmt.Errorf("gateway operation %s is %s; ownership and the exact UUID are retained", out.RequestUUID, out.Status)
	}
	if out.InterfaceID != nil {
		data.InterfaceID = types.Int64Value(*out.InterfaceID)
	}
	if out.Operation == "SET_GATEWAY" && out.Result != nil && out.Result.Gateway != nil {
		data.OwnedGateway = types.StringValue(*out.Result.Gateway)
		data.Gateway = data.OwnedGateway
	}
	return nil
}
func (r *VmGatewayResource) submitPlatformOperation(ctx context.Context, data *VmGatewayResourceModel) (*client.ChangedVmGateway, error) {
	data.PlatformOperationStatus = types.StringValue("SUBMITTING")
	out, err := submitKnownReservation(ctx, func(callCtx context.Context) (*client.GatewayOperation, error) {
		return r.client.SubmitGatewayOperation(callCtx, data.VmID.ValueInt64(), data.NetworkID.ValueInt64(), data.PlatformOperationUUID.ValueString(), r.opts(data))
	}, func(callCtx context.Context) (*client.GatewayOperation, error) {
		return r.client.ReadGatewayOperation(callCtx, data.VmID.ValueInt64(), data.NetworkID.ValueInt64(), data.PlatformOperationUUID.ValueString(), r.opts(data))
	}, func(out *client.GatewayOperation) string { return out.Status }, r.pollInterval())
	if err != nil {
		return nil, err
	}
	polls := 0
	for out.Status != "SUCCESS" {
		data.PlatformOperationStatus = types.StringValue(out.Status)
		if out.Status == "NOT_SUBMITTED" {
			return nil, fmt.Errorf("the exact gateway operation was not submitted")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(r.pollInterval()):
		}
		out, err = r.client.ReadGatewayOperation(ctx, data.VmID.ValueInt64(), data.NetworkID.ValueInt64(), data.PlatformOperationUUID.ValueString(), r.opts(data))
		if err != nil {
			return nil, err
		}

		polls++
		if out.Status == "SUBMITTING" && polls%3 == 0 {
			// Main claims locally before Core reserve. A preclaim busy/lost request can leave
			// no remote task at all. Only this explicit mutation waiter re-drives the SAME
			// public UUID; the backend epoch prevents any repeated physical submission.
			out, err = submitKnownReservation(ctx, func(callCtx context.Context) (*client.GatewayOperation, error) {
				return r.client.SubmitGatewayOperation(callCtx, data.VmID.ValueInt64(), data.NetworkID.ValueInt64(), data.PlatformOperationUUID.ValueString(), r.opts(data))
			}, func(callCtx context.Context) (*client.GatewayOperation, error) {
				return r.client.ReadGatewayOperation(callCtx, data.VmID.ValueInt64(), data.NetworkID.ValueInt64(), data.PlatformOperationUUID.ValueString(), r.opts(data))
			}, func(current *client.GatewayOperation) string { return current.Status }, r.pollInterval())
			if err != nil {
				return nil, err
			}
		}
	}
	data.PlatformOperationStatus = types.StringValue("SUCCESS")
	return out.Result, nil
}
