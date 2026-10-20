package resources

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource                = &LocalNetworkAttachmentResource{}
	_ resource.ResourceWithConfigure   = &LocalNetworkAttachmentResource{}
	_ resource.ResourceWithImportState = &LocalNetworkAttachmentResource{}
)

const (
	// localNetworkAttachmentDefaultCreateTime is a ceiling for the whole create — the attach call
	// and, with reboot_after_attach, the VM's stop and start — not a fixed wait: the provider
	// returns as soon as it is done. Overridable via the timeouts attribute.
	localNetworkAttachmentDefaultCreateTime = 15 * time.Minute

	// localNetworkAttachmentDefaultDeleteTime bounds how long a detach waits for a VM that is
	// neither RUNNING nor STOPPED to become one of them.
	localNetworkAttachmentDefaultDeleteTime = 10 * time.Minute
)

// Error codes of the panel that this resource tells apart.
const (
	panelCodeVMBusy        = 961 // another operation holds the VM (sent as HTTP 503)
	panelCodePrimary       = 958 // the interface is the VM's main one and cannot be detached
	panelCodeBadVMStatus   = 959 // the VM is neither RUNNING nor STOPPED
	panelCodeVMNotFound    = 633 // no such VM in this project
	panelCodeNoSuchNIC     = 703 // no such interface on this VM
	panelCodePlatformError = 627 // the platform refused, or the panel's catch-all
	panelCodeAlreadyHere   = 955 // the VM already has an interface in this network
)

const (
	// localNetworkAttachmentSettledRefusals is how many times in a row the panel may refuse, with 959,
	// a VM that the panel itself reports as RUNNING or STOPPED, before the provider stops asking.
	// Waiting does not cure it: the panel shows a paused or a hibernated VM as STOPPED and refuses it
	// all the same. A VM that is merely changing its status as the call arrives reads settled at the
	// next look and is accepted at the next call, so a few refusals in a row from a VM that reads
	// settled each time are something else.
	localNetworkAttachmentSettledRefusals = 3

	// localNetworkAttachmentLookupTimeout bounds the one read of the VM's interfaces that a failed
	// attach makes in order to name the interface to import.
	localNetworkAttachmentLookupTimeout = 20 * time.Second
)

type LocalNetworkAttachmentResource struct {
	client *client.Client

	// restartPoll is the interval between VM status reads while waiting for a VM, restarting it
	// included; zero means the default. Tests shorten it.
	restartPoll time.Duration

	// lookupTimeout bounds the read of the VM's interfaces that a failed attach makes; zero means
	// localNetworkAttachmentLookupTimeout. Tests shorten it.
	lookupTimeout time.Duration
}

type LocalNetworkAttachmentResourceModel struct {
	VmID              types.Int64    `tfsdk:"vm_id"`
	NetworkID         types.Int64    `tfsdk:"network_id"`
	IP                types.String   `tfsdk:"ip"`
	InterfaceID       types.Int64    `tfsdk:"interface_id"`
	MACAddress        types.String   `tfsdk:"mac_address"`
	Region            types.String   `tfsdk:"region"`
	ProjectTag        types.String   `tfsdk:"project_tag"`
	RebootAfterAttach types.Bool     `tfsdk:"reboot_after_attach"`
	Timeouts          timeouts.Value `tfsdk:"timeouts"`
}

func NewLocalNetworkAttachmentResource() resource.Resource {
	return &LocalNetworkAttachmentResource{}
}

func (r *LocalNetworkAttachmentResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_local_network_attachment"
}

func (r *LocalNetworkAttachmentResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Attaches a virtual machine to one more ProData local network, as an additional network interface. " +
			"Destroying this resource detaches the interface from the VM. " +
			"The VM's main interface, the one in the network the VM was created in, is not managed by this resource and cannot be detached.",

		Attributes: map[string]schema.Attribute{
			"vm_id": schema.Int64Attribute{
				MarkdownDescription: "The ID of the virtual machine to add the interface to.",
				Required:            true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"network_id": schema.Int64Attribute{
				MarkdownDescription: "The ID of the local network to attach the VM to. A VM can have only one interface in a network, " +
					"and cannot be attached to the network it was created in a second time.",
				Required: true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"ip": schema.StringAttribute{
				MarkdownDescription: "The IPv4 address of the interface in the network. If not specified, the platform assigns the next free address of the network, and it is recorded here. " +
					"Changing this forces a new resource. An address that is changed outside Terraform is reported as a warning when the state is refreshed and does not change the plan.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{IPv4Address()},
			},
			"interface_id": schema.Int64Attribute{
				MarkdownDescription: "The ID of the interface (not of the network). It identifies the interface for detaching and for import.",
				Computed:            true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"mac_address": schema.StringAttribute{
				MarkdownDescription: "The MAC address of the interface. Not set when the platform does not know it.",
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
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"project_tag": schema.StringAttribute{
				MarkdownDescription: "Project tag override. If not specified, uses the provider's default project_tag.",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"reboot_after_attach": schema.BoolAttribute{
				MarkdownDescription: "Restart the VM during `apply`, once the interface is attached, if the VM is running. " +
					"The platform does not restart the VM when an interface is added, and whether the new interface and its address work " +
					"without a restart depends on the VM's settings, so on a running VM they may work only after it has been stopped and started again. " +
					"With this set to `true` the attachment is not reported as created until the VM is `RUNNING` again. " +
					"The restart is the platform's stop followed by its start, and that stop is not a clean shutdown of the " +
					"guest operating system: it may power the VM off at once, leaving the guest no chance to flush its buffers " +
					"or stop its services. A VM that is not running is left alone. " +
					"Only used when the attachment is created; changing it afterwards does nothing, and the VM is " +
					"never restarted when the attachment is destroyed. " +
					"If unset, the VM is not restarted and a running VM produces a warning; set it to `false` to say that " +
					"you restart the VM yourself and silence the warning.",
				Optional: true,
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{
				Create: true,
				Delete: true,
			}),
		},
	}
}

func (r *LocalNetworkAttachmentResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *LocalNetworkAttachmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data LocalNetworkAttachmentResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	createTimeout, diags := data.Timeouts.Create(ctx, localNetworkAttachmentDefaultCreateTime)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()

	opts := r.buildOpts(&data)
	vmID := data.VmID.ValueInt64()
	attachReq := client.AttachLocalNetworkRequest{NetworkID: data.NetworkID.ValueInt64()}
	if !data.IP.IsNull() && !data.IP.IsUnknown() {
		attachReq.IP = data.IP.ValueString()
	}

	tflog.Debug(ctx, "Attaching local network to VM", map[string]any{
		"vm_id":      vmID,
		"network_id": attachReq.NetworkID,
		"ip":         attachReq.IP,
	})

	var (
		attached *client.AttachedLocalNetwork
		last     error // what the latest request of the attach was answered with
	)
	err := r.whileUnsettled(ctx, vmID, opts, func() (err error) {
		attached, err = r.client.AttachLocalNetwork(ctx, vmID, attachReq, opts)
		last = err
		return err
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to Attach Local Network", r.attachFailure(ctx, err, last, vmID, attachReq.NetworkID, opts))
		return
	}

	data.InterfaceID = types.Int64Value(attached.ID)
	data.MACAddress = nullableString(attached.MAC)
	if data.IP.IsNull() || data.IP.IsUnknown() {
		data.IP = nullableString(attached.IP)
	}

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

	tflog.Debug(ctx, "Attached local network to VM", map[string]any{
		"vm_id":        vmID,
		"interface_id": attached.ID,
	})

	// The interface exists on the platform from here on. Record it before anything that can
	// still fail: a failed restart must not leave an interface Terraform does not know of,
	// which a retry would then be refused ("already has an interface in this network").
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if attached.RequiresGuestConfiguration {
		resp.Diagnostics.AddWarning(
			"Local Network Attached, but the Address Must Be Set Inside the VM",
			fmt.Sprintf("VM %d has no cloud-init, so nothing configures the new interface inside the guest operating system. "+
				"Set the address%s on the interface with MAC address %s yourself.",
				vmID, forAddress(attached.IP), orUnknown(attached.MAC)),
		)
	}

	switch {
	case data.RebootAfterAttach.ValueBool():
		if !attached.RequiresRestart {
			// The VM was not running, so it reads its configuration when it next starts.
			break
		}
		restarted, err := restartRunningVM(ctx, r.client, vmID, opts, r.pollInterval())
		if err != nil {
			resp.Diagnostics.AddError(
				"Local Network Attached, but the VM Could Not Be Restarted",
				fmt.Sprintf("Interface %d is attached to VM %d, but restarting the VM so that it takes effect failed: %s\n\n"+
					"The attachment is kept in the state and marked as tainted, so the next apply detaches the interface and attaches it again. "+
					"Check the VM in the panel and start it if it is stopped: the provider does not start a VM that is not running, "+
					"the next apply included.",
					attached.ID, vmID, err),
			)
			return
		}
		tflog.Debug(ctx, "Local network attachment finished", map[string]any{
			"vm_id":     vmID,
			"restarted": restarted,
		})
		if !restarted {
			// It was running when the interface was attached and is not running now: somebody
			// stopped it in between. Starting a VM that is off is not the provider's call.
			resp.Diagnostics.AddWarning(
				"Local Network Attached, but the VM Was Not Restarted",
				fmt.Sprintf("VM %d was running when interface %d was attached, but it was no longer running when the provider came "+
					"to restart it, so it was left as it is. The interface takes effect when the VM starts; the provider does "+
					"not start a VM that is not running.",
					vmID, attached.ID),
			)
		}
	case data.RebootAfterAttach.IsNull() && attached.RequiresRestart:
		resp.Diagnostics.AddWarning(
			"Local Network Attached, but It May Work Only After the VM Restarts",
			fmt.Sprintf("VM %d was running when interface %d was attached. The platform does not restart the VM, and whether the "+
				"new interface and its address take effect without a restart depends on the VM's settings, so they may work only after "+
				"the VM has been stopped and started again, although the attachment is recorded as created.\n\n"+
				"Set reboot_after_attach = true to have the provider restart the VM during apply (the platform's stop is not "+
				"a clean shutdown of the guest operating system), or restart the VM yourself and set reboot_after_attach = "+
				"false to silence this warning.",
				vmID, attached.ID),
		)
	}
}

func (r *LocalNetworkAttachmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data LocalNetworkAttachmentResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	opts := r.buildOpts(&data)
	vmID := data.VmID.ValueInt64()
	interfaceID := data.InterfaceID.ValueInt64()

	tflog.Debug(ctx, "Reading local network attachment", map[string]any{
		"vm_id":        vmID,
		"interface_id": interfaceID,
	})

	nics, err := r.client.GetVmLocalNetworks(ctx, vmID, opts)
	if err != nil {
		// Only a VM that is gone takes its interfaces with it; any other error must not
		// silently drop the resource from state (it would be created again, and could end up
		// attached twice).
		if isLocalNICGone(err) {
			tflog.Warn(ctx, "VM not found, removing local network attachment from state", map[string]any{
				"vm_id": vmID,
			})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to Read Local Network Attachment", err.Error())
		return
	}

	// Being in the VM's list is itself the proof that the interface exists.
	var nic *client.VmLocalNetwork
	for i := range nics {
		if nics[i].ID == interfaceID {
			nic = &nics[i]
			break
		}
	}
	if nic == nil {
		tflog.Warn(ctx, "Interface no longer attached to the VM, removing from state", map[string]any{
			"vm_id":        vmID,
			"interface_id": interfaceID,
		})
		resp.State.RemoveResource(ctx)
		return
	}

	switch {
	case data.NetworkID.IsNull() || data.NetworkID.IsUnknown():
		// Right after an import the network is not known yet: it is whatever the interface is in.
		if nic.NetworkID == nil {
			resp.Diagnostics.AddError(
				"Unable to Read Local Network Attachment",
				fmt.Sprintf("Interface %d of VM %d is not tied to a local network in the panel, so it cannot be managed by this resource.", interfaceID, vmID),
			)
			return
		}
		data.NetworkID = types.Int64Value(*nic.NetworkID)
	case nic.NetworkID == nil || *nic.NetworkID != data.NetworkID.ValueInt64():
		tflog.Warn(ctx, "Interface now belongs to a different network, removing from state", map[string]any{
			"vm_id":        vmID,
			"interface_id": interfaceID,
			"network_id":   data.NetworkID.ValueInt64(),
		})
		resp.State.RemoveResource(ctx)
		return
	}

	data.MACAddress = nullableString(nic.MAC)

	switch {
	case data.IP.IsNull() || data.IP.IsUnknown():
		data.IP = nullableString(nic.IP)
	case nic.IP == nil || *nic.IP != data.IP.ValueString():
		// Read does not see the configuration. Writing the panel's address into the state would
		// give a diff against an explicit ip, and ip requires replacement: the interface would
		// be detached and attached again for a change somebody made on purpose. So the state
		// stays as it is, and the drift is reported.
		resp.Diagnostics.AddWarning(
			"Local Network Interface Address Changed Outside Terraform",
			fmt.Sprintf("Interface %d of VM %d has the address %s in the panel, but Terraform has %s for it. "+
				"The state is left as it is. %s",
				interfaceID, vmID, orUnknown(nic.IP), data.IP.ValueString(), driftAdvice(nic, vmID, interfaceID)),
		)
	}

	tflog.Debug(ctx, "Read local network attachment", map[string]any{
		"vm_id":        vmID,
		"interface_id": interfaceID,
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *LocalNetworkAttachmentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// vm_id, network_id, ip, region and project_tag all require replacement, so the only changes
	// that get here are to reboot_after_attach and timeouts — options that Create and Delete
	// alone read. There is nothing to tell the panel; the new values are just recorded.
	var data LocalNetworkAttachmentResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *LocalNetworkAttachmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data LocalNetworkAttachmentResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	deleteTimeout, diags := data.Timeouts.Delete(ctx, localNetworkAttachmentDefaultDeleteTime)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, deleteTimeout)
	defer cancel()

	opts := r.buildOpts(&data)
	vmID := data.VmID.ValueInt64()
	interfaceID := data.InterfaceID.ValueInt64()

	tflog.Debug(ctx, "Detaching local network from VM", map[string]any{
		"vm_id":        vmID,
		"interface_id": interfaceID,
	})

	// The VM is never stopped or restarted here: a detach that finds it neither RUNNING nor
	// STOPPED waits for it to settle, and gives up at the delete timeout.
	err := r.whileUnsettled(ctx, vmID, opts, func() error {
		return r.client.DetachLocalNetwork(ctx, vmID, interfaceID, opts)
	})
	switch {
	case err == nil:
	case isLocalNICGone(err):
		tflog.Info(ctx, "VM or interface not found, local network attachment already gone", map[string]any{
			"vm_id":        vmID,
			"interface_id": interfaceID,
		})
		return
	case client.IsAPIError(err, panelCodePrimary):
		resp.Diagnostics.AddError(
			"Unable to Detach Local Network",
			fmt.Sprintf("%s\n\nInterface %d of VM %d has become the VM's primary interface, for example because a default gateway was set on it, "+
				"or its address was edited, in the console, and the platform does not detach a primary interface. To detach it, "+
				moveGatewayAdvice+". "+consoleGatewayNote,
				err, interfaceID, vmID),
		)
		return
	case client.IsAPIError(err, panelCodePlatformError):
		resp.Diagnostics.AddError(
			"Unable to Detach Local Network",
			fmt.Sprintf("%s\n\nThe platform did not finish removing the interface, and does not say why. The likely reasons, in the order to try them:\n\n"+
				"- The platform could not reach Proxmox, or Proxmox failed for the moment: run terraform destroy again. Repeating it is harmless, "+
				"and it costs the VM no downtime.\n"+
				"- The VM does not hot-plug network devices (its hotplug setting has no network), or the guest did not release the device. "+
				"Proxmox has then queued the change, and it takes effect only when the VM has been stopped and started again: "+
				"a reboot from inside the guest is not enough. Stop the VM (shut the guest down from inside for a clean stop) and start it, "+
				"then run terraform destroy again; it finds the interface gone and finishes.\n"+
				"- If the destroy fails again after that, the interface may only have been waiting for a restart to appear: it was attached, "+
				"without reboot_after_attach, to a VM that does not hot-plug network devices, and the VM had not been restarted since. "+
				"The restart above then brought it up, and its removal is queued in turn: stop and start the VM once more, then run "+
				"terraform destroy again. If that does not describe the VM, go on to the next step.\n"+
				"- Only if the destroy still fails after that: the platform may have found another "+
				"interface than this one at the interface's index (for example after its tag or bridge was changed by hand). Contact support "+
				"to have the VM's configuration checked, and do not detach the interface in the console before they have confirmed which "+
				"interface it is — the console may detach another one.\n\n"+
				"Do not remove the attachment from the state (terraform state rm) in the meantime: a change that is still queued takes effect "+
				"when the VM next starts, and an interface that was only waiting to appear would then appear without Terraform knowing of it.",
				err),
		)
		return
	default:
		resp.Diagnostics.AddError("Unable to Detach Local Network", err.Error())
		return
	}

	tflog.Debug(ctx, "Detached local network from VM", map[string]any{
		"vm_id":        vmID,
		"interface_id": interfaceID,
	})
}

func (r *LocalNetworkAttachmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	const usage = "Usage: terraform import prodata_local_network_attachment.example <vm_id>:<interface_id>\n" +
		"Example: terraform import prodata_local_network_attachment.example 123:456"

	parts := strings.SplitN(req.ID, ":", 2)
	if len(parts) != 2 {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected format 'vm_id:interface_id', got: %s\n\n%s", req.ID, usage),
		)
		return
	}

	vmID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Could not parse vm_id as integer: %s\n\n%s", parts[0], usage),
		)
		return
	}

	interfaceID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Could not parse interface_id as integer: %s\n\n%s", parts[1], usage),
		)
		return
	}

	opts := &client.RequestOpts{
		Region:     r.client.Region,
		ProjectTag: r.client.ProjectTag,
	}

	nics, err := r.client.GetVmLocalNetworks(ctx, vmID, opts)
	if err != nil {
		detail := fmt.Sprintf("Could not list the local network interfaces of VM %d: %s", vmID, err)
		if isLocalNICGone(err) {
			detail += "\n\nThe VM must be in the region and project the provider is configured for."
		}
		resp.Diagnostics.AddError("Unable to List Local Network Interfaces", detail)
		return
	}

	var nic *client.VmLocalNetwork
	for i := range nics {
		if nics[i].ID == interfaceID {
			nic = &nics[i]
			break
		}
	}
	if nic == nil {
		resp.Diagnostics.AddError(
			"Interface Not Found",
			fmt.Sprintf("VM %d has no local network interface %d. Verify the IDs: interface_id is the ID of the interface, not of the network.", vmID, interfaceID),
		)
		return
	}
	if nic.Primary {
		detail := fmt.Sprintf("Interface %d is the primary interface of VM %d: the one in the network the VM was created in, or one with a default gateway. "+
			"It cannot be detached, so it is not managed by this resource.", interfaceID, vmID)
		if hasGateway(nic) {
			detail += " If it is primary only because it carries the gateway of the VM, " + moveGatewayAdvice + ". " + consoleGatewayNote + " Import it then."
		}
		resp.Diagnostics.AddError("Primary Interface Cannot Be Imported", detail)
		return
	}

	tflog.Info(ctx, "Importing local network attachment", map[string]any{
		"vm_id":        vmID,
		"interface_id": interfaceID,
	})

	// network_id and ip are filled in by Read, which runs right after the import.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("vm_id"), vmID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("interface_id"), interfaceID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("region"), r.client.Region)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_tag"), r.client.ProjectTag)...)
}

// whileUnsettled runs call, a request that changes the VM's interfaces, until the panel stops
// answering it with 961 or 959. 961 (another operation holds the VM, sent as HTTP 503) is retried
// with backoff. 959 means the VM is neither running nor stopped, typically because it is starting,
// stopping or rebooting: the VM is waited for, and the call repeated once it is one of the two.
// A VM that reads RUNNING or STOPPED and is refused all the same, localNetworkAttachmentSettledRefusals
// times in a row, is a paused or a hibernated one, which the panel shows as STOPPED: that ends the
// wait instead of asking until the deadline. The deadline is carried by ctx. When it ends the call, or
// the pause before a repeat of it, the error names what the panel last answered (959 or 961): the
// VM's status is blamed only if that was the last thing the panel said. The VM is never started or
// stopped here.
func (r *LocalNetworkAttachmentResource) whileUnsettled(ctx context.Context, vmID int64, opts *client.RequestOpts, call func() error) error {
	var (
		answered  error // what the panel last answered call with; a request the deadline cut short is no answer
		onSettled int   // the 959s in a row that came to a VM which read settled at the first look
	)
	ask := func() error {
		err := call()
		var apiErr *client.APIError
		if errors.As(err, &apiErr) {
			answered = err
		}
		return err
	}
	for {
		err := client.RetryVoidOnBusy(ctx, client.RetryTimeoutShort, ask)
		if !client.IsAPIError(err, panelCodeBadVMStatus) {
			if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
				switch {
				case client.IsAPIError(answered, panelCodeBadVMStatus):
					return fmt.Errorf("%s while the VM was neither running nor stopped: %w; %w", ended(ctx), answered, err)
				case client.IsAPIError(answered, panelCodeVMBusy):
					return fmt.Errorf("%s while another operation held the VM: %w; %w", ended(ctx), answered, err)
				}
			}
			return err
		}

		tflog.Info(ctx, "VM is neither running nor stopped, waiting for it to settle", map[string]any{"vm_id": vmID})
		status, waited, waitErr := r.waitUntilSettled(ctx, vmID, opts)
		if waitErr != nil {
			if isLocalNICGone(waitErr) {
				return waitErr
			}
			return fmt.Errorf("%w; %w", err, waitErr)
		}
		if waited {
			onSettled = 0
		} else {
			onSettled++
		}
		if onSettled >= localNetworkAttachmentSettledRefusals {
			return fmt.Errorf("%w; the panel reports VM %d as %s, yet it refused the request %d times in a row because the VM is neither "+
				"running nor stopped. A paused or a hibernated VM is shown as STOPPED and is refused like that: resume or stop it, "+
				"then apply again", err, vmID, status, onSettled)
		}

		// A VM that reads settled and is refused all the same is asked again only after a pause.
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s while the VM was neither running nor stopped: %w; %w", ended(ctx), err, ctx.Err())
		case <-time.After(r.pollInterval()):
		}
	}
}

// waitUntilSettled returns once the VM is RUNNING or STOPPED, the only statuses the panel changes
// its interfaces in: that status, and whether the VM was seen in another one first (a read that
// failed shows nothing about the VM, so it is no sign of a wait). It fails if the VM is gone or in a
// status it will not leave by itself, if its status cannot be read, or when ctx ends.
func (r *LocalNetworkAttachmentResource) waitUntilSettled(ctx context.Context, vmID int64, opts *client.RequestOpts) (string, bool, error) {
	failures := 0
	status := "unknown"
	unsettled := false
	for {
		vm, err := r.client.GetVmStatus(ctx, vmID, opts)
		switch {
		case err == nil:
			failures = 0
			status = vm.Status
			switch status {
			case "RUNNING", "STOPPED":
				return status, unsettled, nil
			case "ERROR", "DELETED":
				// The panel answers 601 for a VM that was deleted, not DELETED; the case stays for
				// the day it says so.
				return status, true, fmt.Errorf("VM %d is in status %s, which it does not leave by itself", vmID, status)
			}
			unsettled = true
		case ctx.Err() != nil:
			// The wait below reports it.
		default:
			failures++
			if failures >= vmRestartMaxPollErrs {
				return status, true, r.statusUnreadable(ctx, vmID, opts, err, failures)
			}
		}

		select {
		case <-ctx.Done():
			return status, true, fmt.Errorf("%s waiting for VM %d to become RUNNING or STOPPED (last status %s): %w", ended(ctx), vmID, status, ctx.Err())
		case <-time.After(r.pollInterval()):
		}
	}
}

// statusUnreadable is the error of a VM whose status could not be read failures times in a row.
// The status endpoint answers 601 for a VM that is not there, and 601 is also what a blocked user is
// told, so it proves nothing about the VM. The list of the VM's interfaces answers 633 for a VM that
// is not there, and that is asked once before the read error is given up with.
func (r *LocalNetworkAttachmentResource) statusUnreadable(ctx context.Context, vmID int64, opts *client.RequestOpts, readErr error, failures int) error {
	if _, err := r.client.GetVmLocalNetworks(ctx, vmID, opts); isLocalNICGone(err) {
		return err
	}
	return fmt.Errorf("reading the status of VM %d: %w (after %d consecutive failures)", vmID, readErr, failures)
}

// ended says how ctx ended: a deadline is a timeout, anything else, Terraform being interrupted.
func ended(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timed out"
	}
	return "interrupted"
}

// pollInterval is the interval between VM status reads while waiting for a VM.
func (r *LocalNetworkAttachmentResource) pollInterval() time.Duration {
	if r.restartPoll > 0 {
		return r.restartPoll
	}
	return vmRestartPollInterval
}

func (r *LocalNetworkAttachmentResource) buildOpts(data *LocalNetworkAttachmentResourceModel) *client.RequestOpts {
	opts := &client.RequestOpts{}
	if !data.Region.IsNull() && !data.Region.IsUnknown() {
		opts.Region = data.Region.ValueString()
	}
	if !data.ProjectTag.IsNull() && !data.ProjectTag.IsUnknown() {
		opts.ProjectTag = data.ProjectTag.ValueString()
	}
	return opts
}

// isLocalNICGone reports whether the panel's answer says that the VM, or the interface, is not
// there: 633 (no such VM in this project) or 703 (no such interface, which is also what a detach that
// has already happened is answered with). The code decides, not the HTTP status: 703 comes with
// another status on another stand, and a bare 404 says nothing about the VM. It is what a panel that
// does not have these routes yet answers, and taking it for "gone" would drop a live interface from
// the state and report a destroy that detached nothing. It is narrower than client.IsNotFound for the
// same reason: a code that only means "missing" somewhere else, such as 601 (a blocked user), must not
// drop an attachment from the state or have a detach reported as done.
func isLocalNICGone(err error) bool {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.HasCode(panelCodeVMNotFound) || apiErr.HasCode(panelCodeNoSuchNIC)
}

// attachFailure is the text of a failed attach: the panel's own words, and what they leave out.
// For its catch-all that is the one thing the user should not do. Where the interface may exist all
// the same, it is how to take it over: Terraform has no record of it, and the next apply would be
// refused with 955. That is the case when the panel says the VM already has an interface in the
// network, and when the latest request got no answer, because the connection broke, a gateway gave
// up on the panel or the deadline cut the request short; last is the error of that request.
func (r *LocalNetworkAttachmentResource) attachFailure(ctx context.Context, err, last error, vmID, networkID int64, opts *client.RequestOpts) string {
	switch {
	case client.IsAPIError(err, panelCodePlatformError):
		return err.Error() + "\n\nThe platform may have created the interface without recording it in the panel, in which case it is not " +
			"listed and a retry creates a second one. If this keeps happening, contact support instead of retrying."
	case client.IsAPIError(err, panelCodeAlreadyHere):
		advice, own := r.takeOver(ctx, vmID, networkID, opts)
		if own {
			return err.Error() + "\n\n" + advice
		}
		return err.Error() + "\n\nIf an earlier apply created this interface and was cut short before Terraform recorded it, Terraform " +
			"does not know of it. " + advice + " Otherwise choose another network."
	case attachOutcomeUnknown(last):
		advice, own := r.takeOver(ctx, vmID, networkID, opts)
		if own {
			// The attach cannot have worked: the panel refuses the VM's own network.
			return err.Error() + "\n\n" + advice
		}
		return err.Error() + "\n\nNo answer to the request reached the provider, so the platform may have attached the interface all the same. " +
			advice
	}
	return err.Error()
}

// attachOutcomeUnknown reports whether a request was never answered by the panel: it failed on its
// way or was cut short by the deadline (not an answer at all), or a gateway in front of the panel
// replied for it with a 5xx page that carries none of the panel's error codes. Whether the panel
// carried the request out is not known then. A 5xx with a code is the panel's own word: 627 has its
// advice, and 961 says the request was not taken up.
func attachOutcomeUnknown(last error) bool {
	if last == nil {
		return false
	}
	var apiErr *client.APIError
	if !errors.As(last, &apiErr) {
		return true
	}
	return apiErr.StatusCode >= http.StatusInternalServerError && len(apiErr.Codes) == 0
}

// moveGatewayAdvice says what leaving an interface without the default gateway of a VM takes: the gateway goes to the
// interface that is to carry the VM's default route. It continues a sentence.
const moveGatewayAdvice = "move the gateway to the interface that is to carry the VM's default route, and leave this one without it"

// consoleGatewayNote says what to know about doing that in the console, which is what makes it easy to get wrong: the
// Gateway settings are offered only for a VM without a public IP, and No gateway clears the gateway of every interface
// of the VM, whichever one it is chosen on, and leaves the VM without a default gateway. The steps are in the
// documentation, not here: they quote labels of the console, which the provider does not own.
const consoleGatewayNote = "The console can do that only for a VM without a public IP, and its No gateway option clears the gateway of every interface of the VM, " +
	"not only of the one it is chosen on. The steps are in the documentation of this resource, in the section \"Destroying the attachment\"."

// takeOver says how to bring the VM's interface in the network under Terraform, and names the
// interface when the panel lists it. own reports that the interface is a primary one, the VM's own
// or one that carries a gateway, which this resource does not manage: there is nothing to take over
// then, and the advice says so.
func (r *LocalNetworkAttachmentResource) takeOver(ctx context.Context, vmID, networkID int64, opts *client.RequestOpts) (advice string, own bool) {
	const importCommand = "terraform import prodata_local_network_attachment.<name>"
	nic, listed := r.interfaceInNetwork(ctx, vmID, networkID, opts)
	switch {
	case nic != nil && nic.Primary && hasGateway(nic):
		// The platform treats as primary the interface of the network the VM was created in, and any
		// interface that carries a gateway, and the list does not say which of the two this is. Nothing
		// here may tell the user to remove the gateway of the VM's own interface.
		return fmt.Sprintf("VM %d has interface %d in network %d, and the platform treats it as primary: network %d is the one the VM was created in, "+
			"or the interface carries a default gateway. This resource does not manage a primary interface and the platform does not detach it. "+
			"If network %d is the VM's own network, choose another network. If it is not, and the interface does not need to carry the gateway, "+
			moveGatewayAdvice+". "+consoleGatewayNote+
			" Then run `%s %d:%d` and apply again.",
			vmID, nic.ID, networkID, networkID, networkID, importCommand, vmID, nic.ID), true
	case nic != nil && nic.Primary:
		return fmt.Sprintf("Network %d is the VM's own network: interface %d is its primary interface, which this resource does not manage "+
			"and the platform does not detach. Choose another network.", networkID, nic.ID), true
	case nic != nil:
		return fmt.Sprintf("VM %d has interface %d in network %d (address %s, MAC address %s): to bring it under Terraform, run `%s %d:%d` and apply again.",
			vmID, nic.ID, networkID, orUnknown(nic.IP), orUnknown(nic.MAC), importCommand, vmID, nic.ID), false
	case listed:
		return fmt.Sprintf("No interface in network %d is listed for VM %d (yet), and the platform may still be finishing the request. Before applying again, "+
			"check GET /api/v2/vms/%d/local-networks of the ProData API, and if an interface of that network appears, run `%s %d:<interface_id>` "+
			"with its id and apply again.",
			networkID, vmID, vmID, importCommand, vmID), false
	default:
		return fmt.Sprintf("The interfaces of the VM and their IDs are listed by GET /api/v2/vms/%d/local-networks of the ProData API: to bring the one in "+
			"network %d under Terraform, run `%s %d:<interface_id>` and apply again.",
			vmID, networkID, importCommand, vmID), false
	}
}

// interfaceInNetwork looks for the interface that the VM has in the network, and says whether the
// VM's interfaces could be listed. An interface that can be taken over is preferred to the primary
// one, should the VM have both in the network, and of two primary ones the one that carries a gateway, which is
// the one the advice on the gateway is about; the other is the VM's own. It asks on a context of its own, because the one of the attach may
// have run out, which is one of the reasons for asking; Terraform being interrupted is not the moment
// for one more request. That context neither times out with the attach nor is cancelled with it, so
// its own limit is all that keeps a panel which does not answer from holding the apply for good.
func (r *LocalNetworkAttachmentResource) interfaceInNetwork(ctx context.Context, vmID, networkID int64, opts *client.RequestOpts) (*client.VmLocalNetwork, bool) {
	if errors.Is(ctx.Err(), context.Canceled) {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.lookupLimit())
	defer cancel()

	nics, err := r.client.GetVmLocalNetworks(ctx, vmID, opts)
	if err != nil {
		tflog.Warn(ctx, "Could not list the interfaces of the VM after a failed attach", map[string]any{"vm_id": vmID, "error": err.Error()})
		return nil, false
	}
	var primary *client.VmLocalNetwork
	for i := range nics {
		if nics[i].NetworkID == nil || *nics[i].NetworkID != networkID {
			continue
		}
		if !nics[i].Primary {
			return &nics[i], true
		}
		if primary == nil || (!hasGateway(primary) && hasGateway(&nics[i])) {
			primary = &nics[i]
		}
	}
	return primary, true
}

// lookupLimit is how long the read of the VM's interfaces after a failed attach may take.
func (r *LocalNetworkAttachmentResource) lookupLimit() time.Duration {
	if r.lookupTimeout > 0 {
		return r.lookupTimeout
	}
	return localNetworkAttachmentLookupTimeout
}

// driftAdvice is what to do about an interface whose address in the panel is not the one in the
// state. Setting ip to the address of the panel is no way out by itself: a changed ip forces a new
// resource, so the plan would detach the interface and attach a new one. A primary interface leaves
// neither way open: the platform does not detach it, and an import refuses it. Editing the address of an
// interface in the console gives it the gateway of the VM, which is what makes it primary, so that is
// said first. An interface that is primary without a gateway (the VM's own, or one that shares the slot of
// a primary one) gets no advice about a gateway, as in ImportState: moving one changes nothing for it, and
// only the platform can say why it treats the interface as primary.
func driftAdvice(nic *client.VmLocalNetwork, vmID, interfaceID int64) string {
	const replace = "To get the configured address back, replace the attachment: terraform apply -replace=<address of this resource>."
	if nic.Primary && !hasGateway(nic) {
		return "The interface is a primary one now, although the panel lists no gateway on it, so moving a gateway would not change that. " +
			"The platform does not detach a primary interface and this resource does not import one, so neither taking the interface over again " +
			"nor replacing the attachment works: ask support why the platform treats it as primary."
	}
	noAddress := nic.IP == nil || *nic.IP == ""
	var primary string
	if nic.Primary {
		ways := "neither of the ways below works"
		if noAddress {
			ways = "the way below does not work"
		}
		primary = "The interface is a primary one now, for example because a default gateway was set on it, or its address was edited, in the console. " +
			"The platform does not detach a primary interface and this resource does not import one, so " + ways + " until you " +
			moveGatewayAdvice + ". " + consoleGatewayNote + " "
	}
	if noAddress {
		return primary + replace
	}
	return fmt.Sprintf("%sTo keep the address of the panel, set ip to it in the configuration, then remove the attachment from the state "+
		"(terraform state rm <address of this resource>) and import it again (terraform import <address of this resource> %d:%d). "+
		"Setting ip alone would make Terraform replace the interface, because a changed ip forces a new resource. %s",
		primary, vmID, interfaceID, replace)
}

// hasGateway reports that the panel lists a gateway for the interface.
func hasGateway(nic *client.VmLocalNetwork) bool {
	return nic.Gateway != nil && *nic.Gateway != ""
}

func nullableString(s *string) types.String {
	if s == nil {
		return types.StringNull()
	}
	return types.StringValue(*s)
}

func orUnknown(s *string) string {
	if s == nil || *s == "" {
		return "(unknown)"
	}
	return *s
}

// forAddress is " <address>" for the sentence "Set the address<…> on the interface", or nothing.
func forAddress(ip *string) string {
	if ip == nil || *ip == "" {
		return ""
	}
	return " " + *ip
}
