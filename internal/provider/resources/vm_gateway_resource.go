package resources

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
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
	VmID           types.Int64    `tfsdk:"vm_id"`
	NetworkID      types.Int64    `tfsdk:"network_id"`
	InterfaceID    types.Int64    `tfsdk:"interface_id"`
	Gateway        types.String   `tfsdk:"gateway"`
	OwnedGateway   types.String   `tfsdk:"owned_gateway"`
	RestartPending types.Bool     `tfsdk:"restart_pending"`
	Region         types.String   `tfsdk:"region"`
	ProjectTag     types.String   `tfsdk:"project_tag"`
	Timeouts       timeouts.Value `tfsdk:"timeouts"`
}

func NewVmGatewayResource() resource.Resource { return &VmGatewayResource{} }
func (r *VmGatewayResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm_gateway"
}
func (r *VmGatewayResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages the VM's IPv4 default gateway on a selected local network interface. A VM has one default route: use only one prodata_vm_gateway per VM. Setting it can move the route from another interface, including a public interface.",
		Attributes: map[string]schema.Attribute{
			"vm_id":           schema.Int64Attribute{Required: true, MarkdownDescription: "ID of the virtual machine.", Validators: []validator.Int64{int64validator.AtLeast(1)}, PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"network_id":      schema.Int64Attribute{Required: true, MarkdownDescription: "ID of a local network already attached to the VM. It can be the primary network or an additional attachment.", Validators: []validator.Int64{int64validator.AtLeast(1)}, PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"interface_id":    schema.Int64Attribute{Computed: true, MarkdownDescription: "ID of the verified network interface. Changes or deletion are refused if the interface has been replaced.", PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}},
			"gateway":         schema.StringAttribute{Required: true, MarkdownDescription: "IPv4 address of the default gateway, in the selected interface's subnet.", Validators: []validator.String{IPv4Address()}},
			"owned_gateway":   schema.StringAttribute{Computed: true, MarkdownDescription: "Gateway last applied or imported by this resource; used to avoid deleting a different route.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"restart_pending": schema.BoolAttribute{Computed: true, MarkdownDescription: "A previous gateway change stopped a running VM but could not confirm its restart.", PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}},
			"region":          schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Region override; defaults to the provider region.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()}},
			"project_tag":     schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Project override; defaults to the provider project.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()}},
			"timeouts":        timeouts.Attributes(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
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
	var plan VmGatewayResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	changed := req.State.Raw.IsNull()
	if !changed {
		var state VmGatewayResourceModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
		changed = !state.Gateway.Equal(plan.Gateway)
		if state.RestartPending.ValueBool() {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("restart_pending"), types.BoolUnknown())...)
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("owned_gateway"), types.StringUnknown())...)
		}
	}
	if changed {
		if !req.State.Raw.IsNull() {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("owned_gateway"), types.StringUnknown())...)
		}
		resp.Diagnostics.AddWarning("Changing a VM default gateway",
			"This may move the VM's default route from another interface. A running VM is powered off and started again; the stop is not a clean guest shutdown. A guest without cloud-init must be configured separately. Depending on the image and cloud-init configuration, the changed data may be treated as a new instance, regenerating SSH host keys and rerunning once-per-instance scripts.")
	}
}

func (r *VmGatewayResource) opts(data *VmGatewayResourceModel) *client.RequestOpts {
	return &client.RequestOpts{Region: data.Region.ValueString(), ProjectTag: data.ProjectTag.ValueString()}
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
	data.RestartPending = types.BoolValue(false)
	change := client.ChangeVmGatewayRequest{NetworkID: data.NetworkID.ValueInt64(), Gateway: &gateway}
	result, changes := r.apply(ctx, &data, change)
	resp.Diagnostics.Append(changes...)
	if resp.Diagnostics.HasError() {
		if data.RestartPending.ValueBool() {
			if result != nil {
				data.InterfaceID = types.Int64Value(result.InterfaceID)
				data.OwnedGateway = data.Gateway
			} else {
				// A lost write response cannot establish ownership of any route.
				data.OwnedGateway = types.StringNull()
			}
			resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
		}
		return
	}
	data.InterfaceID = types.Int64Value(result.InterfaceID)
	data.OwnedGateway = data.Gateway
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *VmGatewayResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data, state VmGatewayResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, diags := data.Timeouts.Update(ctx, vmGatewayTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
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
	if data.Gateway.Equal(state.Gateway) {
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
		return
	}
	gateway := data.Gateway.ValueString()
	interfaceID := state.InterfaceID.ValueInt64()
	change := client.ChangeVmGatewayRequest{NetworkID: data.NetworkID.ValueInt64(), Gateway: &gateway, ExpectedInterfaceID: &interfaceID}
	result, changes := r.apply(ctx, &data, change)
	resp.Diagnostics.Append(changes...)
	if resp.Diagnostics.HasError() {
		if data.RestartPending.ValueBool() {
			if result != nil {
				data.InterfaceID = types.Int64Value(result.InterfaceID)
				data.OwnedGateway = data.Gateway
				resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
			} else {
				state.RestartPending = types.BoolValue(true)
				resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			}
		}
		return
	}
	data.InterfaceID = types.Int64Value(result.InterfaceID)
	data.OwnedGateway = data.Gateway
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *VmGatewayResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data VmGatewayResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	actual, err := r.client.GetVmGateway(ctx, data.VmID.ValueInt64(), data.NetworkID.ValueInt64(), r.opts(&data))
	if err != nil {
		if client.IsAPIError(err, 633) || client.IsAPIError(err, 703) {
			if data.RestartPending.ValueBool() {
				resp.Diagnostics.AddError("VM recovery requires verification", "The VM or its network interface could not be found while restart recovery is pending. Verify their actual state before removing the gateway from Terraform state or creating a replacement.")
				return
			}
			resp.State.RemoveResource(ctx)
			return
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
	if actual.Gateway == nil || actual.ReconcileRequired || data.OwnedGateway.IsNull() ||
		data.OwnedGateway.IsUnknown() || *actual.Gateway != data.OwnedGateway.ValueString() {
		data.Gateway = types.StringNull()
	} else {
		data.Gateway = types.StringValue(*actual.Gateway)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *VmGatewayResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data VmGatewayResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if data.RestartPending.ValueBool() {
		resp.Diagnostics.AddError("VM restart needs recovery", "The gateway operation stopped a running VM and could not confirm its restart. Restore the previous resource configuration if it was removed or changed to require replacement; if Terraform marked the resource tainted, run terraform untaint for it. Apply again to recover the VM before deleting or replacing the resource.")
		return
	}
	if data.OwnedGateway.IsNull() || data.OwnedGateway.IsUnknown() {
		return
	}
	timeout, diags := data.Timeouts.Delete(ctx, vmGatewayTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	expected, interfaceID := data.OwnedGateway.ValueString(), data.InterfaceID.ValueInt64()
	change := client.ChangeVmGatewayRequest{NetworkID: data.NetworkID.ValueInt64(), ExpectedGateway: &expected, ExpectedInterfaceID: &interfaceID, Clear: true}
	_, changes := r.apply(ctx, &data, change)
	resp.Diagnostics.Append(changes...)
	if resp.Diagnostics.HasError() && data.RestartPending.ValueBool() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	}
}

func (r *VmGatewayResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, ":")
	if len(parts) != 2 {
		resp.Diagnostics.AddError("Invalid Import ID", "Use <vm_id>:<network_id>.")
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
func (r *VmGatewayResource) apply(ctx context.Context, data *VmGatewayResourceModel, change client.ChangeVmGatewayRequest) (*client.ChangedVmGateway, diag.Diagnostics) {
	var diags diag.Diagnostics
	vmID, opts := data.VmID.ValueInt64(), r.opts(data)
	forecast, err := r.client.ChangeVmGateway(ctx, vmID, change, true, opts)
	if err != nil {
		diags.AddError("Unable to Check VM Gateway", err.Error())
		return nil, diags
	}
	if data.InterfaceID.IsNull() || data.InterfaceID.IsUnknown() {
		data.InterfaceID = types.Int64Value(forecast.InterfaceID)
	}
	if change.ExpectedInterfaceID == nil {
		change.ExpectedInterfaceID = &forecast.InterfaceID
	}
	if forecast.PreviousGatewayOnAnotherInterface {
		diags.AddWarning("The VM default route will move", "The current default gateway is on another interface, which may be a public interface. Setting this gateway moves the default route to the selected local interface.")
	}
	if forecast.RequiresGuestConfiguration {
		diags.AddWarning("Guest network needs configuration", "The VM has no cloud-init; configure its default gateway inside the guest as well.")
	}
	if !forecast.Changed {
		actual, readErr := r.client.GetVmGateway(ctx, vmID, change.NetworkID, opts)
		if readErr != nil {
			diags.AddError("Unable to Read VM Gateway", readErr.Error())
			return nil, diags
		}
		if actual.InterfaceID != forecast.InterfaceID || actual.Pending ||
			(!change.Clear && (actual.Gateway == nil || *actual.Gateway != *change.Gateway)) ||
			(change.Clear && actual.Gateway != nil && change.ExpectedGateway != nil && *actual.Gateway == *change.ExpectedGateway) {
			diags.AddError("VM gateway changed during the check", "The selected interface or its gateway changed during preflight. Run apply again.")
			return nil, diags
		}
		if !actual.ReconcileRequired {
			return forecast, diags
		}
	}
	var applied *client.ChangedVmGateway
	write := func(writeCtx context.Context) error {
		var writeErr error
		applied, writeErr = r.client.ChangeVmGateway(writeCtx, vmID, change, false, opts)
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
