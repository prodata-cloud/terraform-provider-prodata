package resources

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource                = &PublicIPAttachmentResource{}
	_ resource.ResourceWithConfigure   = &PublicIPAttachmentResource{}
	_ resource.ResourceWithImportState = &PublicIPAttachmentResource{}
)

// publicIPAttachmentDefaultCreateTime is a ceiling for the whole create — the attach call and,
// with reboot_after_attach, the VM's stop and start — not a fixed wait: the provider returns as
// soon as the VM is back. Overridable via the timeouts attribute.
const publicIPAttachmentDefaultCreateTime = 15 * time.Minute

type PublicIPAttachmentResource struct {
	client *client.Client

	// restartPoll is the interval between VM status reads while restarting; zero means the
	// default. Tests shorten it.
	restartPoll time.Duration
}

type PublicIPAttachmentResourceModel struct {
	VmID              types.Int64    `tfsdk:"vm_id"`
	PublicIPID        types.Int64    `tfsdk:"public_ip_id"`
	PublicIP          types.String   `tfsdk:"public_ip"`
	Region            types.String   `tfsdk:"region"`
	ProjectTag        types.String   `tfsdk:"project_tag"`
	RebootAfterAttach types.Bool     `tfsdk:"reboot_after_attach"`
	Timeouts          timeouts.Value `tfsdk:"timeouts"`
}

func NewPublicIPAttachmentResource() resource.Resource {
	return &PublicIPAttachmentResource{}
}

func (r *PublicIPAttachmentResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_public_ip_attachment"
}

func (r *PublicIPAttachmentResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Attaches a ProData public IP to a virtual machine. " +
			"Destroying this resource detaches the public IP from the VM.",

		Attributes: map[string]schema.Attribute{
			"vm_id": schema.Int64Attribute{
				MarkdownDescription: "The ID of the virtual machine to attach the public IP to.",
				Required:            true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"public_ip_id": schema.Int64Attribute{
				MarkdownDescription: "The ID of the public IP to attach.",
				Required:            true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"public_ip": schema.StringAttribute{
				MarkdownDescription: "The public IP address string assigned to the VM.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"region": schema.StringAttribute{
				MarkdownDescription: "Region ID override. If not specified, uses the provider's default region.",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"project_tag": schema.StringAttribute{
				MarkdownDescription: "Project tag override. If not specified, uses the provider's default project_tag.",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"reboot_after_attach": schema.BoolAttribute{
				MarkdownDescription: "Restart the VM during `apply`, once the public IP is attached, so that the address works " +
					"when `apply` finishes. The platform applies the network configuration of a public IP only when the VM " +
					"starts, so on a running VM the address does not work until the VM has been stopped and started again; " +
					"with this set to `true` the attachment is not reported as created until the VM is `RUNNING` again. " +
					"The restart is the platform's stop followed by its start, and that stop is not a clean shutdown of the " +
					"guest operating system: it may power the VM off at once, leaving the guest no chance to flush its buffers " +
					"or stop its services. A VM that is not running is left alone: the configuration is applied the next time " +
					"it starts. Only used when the attachment is created; changing it afterwards does nothing, and the VM is " +
					"never restarted when the attachment is destroyed. " +
					"If unset, the VM is not restarted and a running VM produces a warning; set it to `false` to say that " +
					"you restart the VM yourself and silence the warning.",
				Optional: true,
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{
				Create: true,
			}),
		},
	}
}

func (r *PublicIPAttachmentResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	r.client = c
}

func (r *PublicIPAttachmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data PublicIPAttachmentResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	createTimeout, diags := data.Timeouts.Create(ctx, publicIPAttachmentDefaultCreateTime)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()

	opts := r.buildOpts(&data)
	vmID := data.VmID.ValueInt64()
	attachReq := client.AttachPublicIPRequest{
		PublicIPID: data.PublicIPID.ValueInt64(),
	}

	tflog.Debug(ctx, "Attaching public IP to VM", map[string]any{
		"vm_id":        vmID,
		"public_ip_id": attachReq.PublicIPID,
	})

	vm, err := client.RetryOnBusy(ctx, client.RetryTimeoutShort, func() (*client.Vm, error) {
		return r.client.AttachPublicIP(ctx, vmID, attachReq, opts)
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to Attach Public IP", err.Error())
		return
	}

	data.PublicIP = types.StringValue(vm.PublicIP)

	region := data.Region.ValueString()
	if region == "" {
		region = r.client.Region
	}
	projectTag := data.ProjectTag.ValueString()
	if projectTag == "" {
		projectTag = r.client.ProjectTag
	}
	data.Region = types.StringValue(region)
	data.ProjectTag = types.StringValue(projectTag)

	tflog.Debug(ctx, "Attached public IP to VM", map[string]any{
		"vm_id":     vmID,
		"public_ip": vm.PublicIP,
	})

	// The attachment exists on the panel from here on. Record it before anything that can
	// still fail: a failed restart must not leave an attachment Terraform does not know of,
	// which a retry would then be refused ("VM already has a public IP").
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	switch {
	case data.RebootAfterAttach.ValueBool():
		restarted, err := restartRunningVM(ctx, r.client, vmID, opts, r.pollInterval())
		if err != nil {
			resp.Diagnostics.AddError(
				"Public IP Attached, but the VM Could Not Be Restarted",
				fmt.Sprintf("Public IP %d is attached to VM %d, but restarting the VM so that the address takes effect failed: %s\n\n"+
					"The attachment is kept in the state and marked as tainted, so the next apply detaches the public IP and attaches it again. "+
					"Check the VM in the panel and start it if it is stopped: the provider does not start a VM that is not running, "+
					"the next apply included.",
					attachReq.PublicIPID, vmID, err),
			)
			return
		}
		tflog.Debug(ctx, "Public IP attachment finished", map[string]any{
			"vm_id":     vmID,
			"restarted": restarted,
		})
		if !restarted && vmIsUp(vm.Status) {
			// It was up when the IP was attached and is not up now: somebody stopped it in
			// between. Starting a VM that is off is not the provider's call.
			resp.Diagnostics.AddWarning(
				"Public IP Attached, but the VM Was Not Restarted",
				fmt.Sprintf("VM %d was %s when public IP %d was attached, but it was no longer running when the provider came "+
					"to restart it, so it was left as it is. The platform applies the network configuration of a public IP when "+
					"the VM starts, so the address works once the VM has been started; the provider does not start a VM that "+
					"is not running.",
					vmID, vm.Status, attachReq.PublicIPID),
			)
		}
	case data.RebootAfterAttach.IsNull() && vmIsUp(vm.Status):
		resp.Diagnostics.AddWarning(
			"Public IP Attached, but It Works Only After the VM Restarts",
			fmt.Sprintf("VM %d was %s when public IP %d was attached. The platform applies the network configuration of a "+
				"public IP only when the VM starts, so the address does not work until the VM has been stopped and started again, "+
				"although the attachment is recorded as created.\n\n"+
				"Set reboot_after_attach = true to have the provider restart the VM during apply (the platform's stop is not "+
				"a clean shutdown of the guest operating system), or restart the VM yourself and set reboot_after_attach = "+
				"false to silence this warning.",
				vmID, vm.Status, attachReq.PublicIPID),
		)
	}
}

// pollInterval is the interval between VM status reads while the VM restarts.
func (r *PublicIPAttachmentResource) pollInterval() time.Duration {
	if r.restartPoll > 0 {
		return r.restartPoll
	}
	return vmRestartPollInterval
}

// vmIsUp reports whether a VM in this status is running or on its way to running, so that a
// public IP attached now is not part of the network configuration it started with.
func vmIsUp(status string) bool {
	switch status {
	case "RUNNING", "STARTING", "REBOOTING":
		return true
	}
	return false
}

func (r *PublicIPAttachmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data PublicIPAttachmentResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	opts := r.buildOpts(&data)
	vmID := data.VmID.ValueInt64()
	expectedPublicIPID := data.PublicIPID.ValueInt64()

	tflog.Debug(ctx, "Reading public IP attachment", map[string]any{
		"vm_id":        vmID,
		"public_ip_id": expectedPublicIPID,
	})

	vm, err := r.client.GetVm(ctx, vmID, opts)
	if err != nil {
		if client.IsNotFound(err) {
			tflog.Warn(ctx, "VM not found, removing public IP attachment from state", map[string]any{
				"vm_id": vmID,
			})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to Read VM for Public IP Attachment", err.Error())
		return
	}

	// Verify the SPECIFIC public IP is still attached (not just any IP)
	if vm.PublicIPID == 0 || vm.PublicIPID != expectedPublicIPID {
		tflog.Warn(ctx, "Public IP attachment drifted — expected public IP not attached", map[string]any{
			"vm_id":                 vmID,
			"expected_public_ip_id": expectedPublicIPID,
			"actual_public_ip_id":   vm.PublicIPID,
		})
		resp.State.RemoveResource(ctx)
		return
	}

	data.PublicIP = types.StringValue(vm.PublicIP)

	tflog.Debug(ctx, "Read public IP attachment", map[string]any{
		"vm_id":     vmID,
		"public_ip": vm.PublicIP,
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *PublicIPAttachmentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// vm_id, public_ip_id, region and project_tag all require replacement, so the only changes
	// that get here are to reboot_after_attach and timeouts — options that Create alone reads.
	// There is nothing to tell the panel; the new values are just recorded.
	var data PublicIPAttachmentResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *PublicIPAttachmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data PublicIPAttachmentResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	opts := r.buildOpts(&data)
	vmID := data.VmID.ValueInt64()
	expectedPublicIPID := data.PublicIPID.ValueInt64()

	// The detach endpoint is VM-scoped — it removes whatever public IP is currently on
	// the VM. Confirm the VM still carries the IP we manage before detaching, so under
	// stale state (e.g. -refresh=false) we never strip a different IP that was attached
	// out-of-band. No-op if the VM is gone or now carries a different / no IP.
	vm, err := r.client.GetVm(ctx, vmID, opts)
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Unable to Read VM for Public IP detach", err.Error())
		return
	}
	if vm.PublicIPID != expectedPublicIPID {
		tflog.Warn(ctx, "VM no longer carries the managed public IP; skipping detach", map[string]any{
			"vm_id":                 vmID,
			"expected_public_ip_id": expectedPublicIPID,
			"actual_public_ip_id":   vm.PublicIPID,
		})
		return
	}

	tflog.Debug(ctx, "Detaching public IP from VM", map[string]any{
		"vm_id": vmID,
	})

	err = r.client.DetachPublicIP(ctx, vmID, opts)
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Unable to Detach Public IP", err.Error())
		return
	}

	tflog.Debug(ctx, "Detached public IP from VM", map[string]any{
		"vm_id": vmID,
	})
}

func (r *PublicIPAttachmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, ":", 2)
	if len(parts) != 2 {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected format 'vm_id:public_ip_id', got: %s\n\n"+
				"Usage: terraform import prodata_public_ip_attachment.example <vm_id>:<public_ip_id>\n"+
				"Example: terraform import prodata_public_ip_attachment.example 123:456", req.ID),
		)
		return
	}

	vmID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Could not parse vm_id as integer: %s\n\n"+
				"Usage: terraform import prodata_public_ip_attachment.example <vm_id>:<public_ip_id>\n"+
				"Example: terraform import prodata_public_ip_attachment.example 123:456", parts[0]),
		)
		return
	}

	publicIPID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Could not parse public_ip_id as integer: %s\n\n"+
				"Usage: terraform import prodata_public_ip_attachment.example <vm_id>:<public_ip_id>\n"+
				"Example: terraform import prodata_public_ip_attachment.example 123:456", parts[1]),
		)
		return
	}

	tflog.Info(ctx, "Importing public IP attachment", map[string]any{
		"vm_id":        vmID,
		"public_ip_id": publicIPID,
	})

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("vm_id"), vmID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("public_ip_id"), publicIPID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("region"), r.client.Region)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_tag"), r.client.ProjectTag)...)
}

func (r *PublicIPAttachmentResource) buildOpts(data *PublicIPAttachmentResourceModel) *client.RequestOpts {
	opts := &client.RequestOpts{}
	if !data.Region.IsNull() && !data.Region.IsUnknown() {
		opts.Region = data.Region.ValueString()
	}
	if !data.ProjectTag.IsNull() && !data.ProjectTag.IsUnknown() {
		opts.ProjectTag = data.ProjectTag.ValueString()
	}
	return opts
}
