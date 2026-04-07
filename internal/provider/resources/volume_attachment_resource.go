package resources

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"terraform-provider-prodata/internal/client"

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
	_ resource.Resource                = &VolumeAttachmentResource{}
	_ resource.ResourceWithConfigure   = &VolumeAttachmentResource{}
	_ resource.ResourceWithImportState = &VolumeAttachmentResource{}
)

type VolumeAttachmentResource struct {
	client *client.Client
}

type VolumeAttachmentResourceModel struct {
	VmID             types.Int64  `tfsdk:"vm_id"`
	VolumeID         types.Int64  `tfsdk:"volume_id"`
	AttachedVolumeID types.Int64  `tfsdk:"attached_volume_id"`
	Region           types.String `tfsdk:"region"`
	ProjectTag       types.String `tfsdk:"project_tag"`
}

func NewVolumeAttachmentResource() resource.Resource {
	return &VolumeAttachmentResource{}
}

func (r *VolumeAttachmentResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_volume_attachment"
}

func (r *VolumeAttachmentResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Attaches a ProData volume to a virtual machine. " +
			"Destroying this resource detaches the volume from the VM.",

		Attributes: map[string]schema.Attribute{
			"vm_id": schema.Int64Attribute{
				MarkdownDescription: "The ID of the virtual machine to attach the volume to.",
				Required:            true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"volume_id": schema.Int64Attribute{
				MarkdownDescription: "The ID of the volume (UserDisks) to attach.",
				Required:            true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"attached_volume_id": schema.Int64Attribute{
				MarkdownDescription: "The ID of the attached volume (VmDisk) after attachment. " +
					"This is computed by the server upon successful attach.",
				Computed: true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
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
		},
	}
}

func (r *VolumeAttachmentResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *VolumeAttachmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data VolumeAttachmentResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	opts := r.buildOpts(&data)
	vmID := data.VmID.ValueInt64()
	attachReq := client.AttachVolumeRequest{
		VolumeID: data.VolumeID.ValueInt64(),
	}

	tflog.Debug(ctx, "Attaching volume to VM", map[string]any{
		"vm_id":     vmID,
		"volume_id": attachReq.VolumeID,
	})

	volume, err := client.RetryOnBusy(ctx, client.RetryTimeoutLong, func() (*client.Volume, error) {
		return r.client.AttachVolume(ctx, vmID, attachReq, opts)
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to Attach Volume", err.Error())
		return
	}

	data.AttachedVolumeID = types.Int64Value(volume.ID)

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

	tflog.Debug(ctx, "Attached volume to VM", map[string]any{
		"vm_id":              vmID,
		"attached_volume_id": volume.ID,
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *VolumeAttachmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data VolumeAttachmentResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	opts := r.buildOpts(&data)
	attachedVolumeID := data.AttachedVolumeID.ValueInt64()
	vmID := data.VmID.ValueInt64()

	tflog.Debug(ctx, "Reading volume attachment", map[string]any{
		"vm_id":              vmID,
		"attached_volume_id": attachedVolumeID,
	})

	volume, err := r.client.GetVolume(ctx, attachedVolumeID, opts)
	if err != nil {
		tflog.Warn(ctx, "Volume not found, removing volume attachment from state", map[string]any{
			"attached_volume_id": attachedVolumeID,
			"error":              err.Error(),
		})
		resp.State.RemoveResource(ctx)
		return
	}

	// Verify the volume is still attached to the expected VM
	if !volume.InUse || volume.AttachedID == nil || *volume.AttachedID != vmID {
		tflog.Warn(ctx, "Volume is not attached to the expected VM, removing from state", map[string]any{
			"vm_id":              vmID,
			"attached_volume_id": attachedVolumeID,
			"in_use":             volume.InUse,
		})
		resp.State.RemoveResource(ctx)
		return
	}

	tflog.Debug(ctx, "Read volume attachment", map[string]any{
		"vm_id":              vmID,
		"attached_volume_id": attachedVolumeID,
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *VolumeAttachmentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError(
		"Update Not Supported",
		"All attributes of prodata_volume_attachment require replacement. This is a bug in the provider.",
	)
}

func (r *VolumeAttachmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data VolumeAttachmentResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	opts := r.buildOpts(&data)
	vmID := data.VmID.ValueInt64()
	attachedVolumeID := data.AttachedVolumeID.ValueInt64()

	tflog.Debug(ctx, "Detaching volume from VM", map[string]any{
		"vm_id":              vmID,
		"attached_volume_id": attachedVolumeID,
	})

	// Poll VM status until it's stopped, then detach. No restart after detach:
	// when Terraform destroys multiple volume_attachment resources in parallel,
	// the first detach stops the VM and leaves it stopped so subsequent detaches
	// find it already stopped and complete immediately without stop/start cycles.
	const (
		pollInterval   = 5 * time.Second
		overallTimeout = 10 * time.Minute
	)
	deadline := time.Now().Add(overallTimeout)

	for {
		if time.Now().After(deadline) {
			resp.Diagnostics.AddError(
				"Volume detach timed out",
				fmt.Sprintf("timed out waiting to detach volume from VM %d — VM could not be stopped or detach was blocked by concurrent operations", vmID),
			)
			return
		}

		vm, err := r.client.GetVmStatus(ctx, vmID, opts)
		if err != nil {
			if client.IsNotFound(err) {
				tflog.Info(ctx, "VM not found, volume attachment already gone", map[string]any{"vm_id": vmID})
				return
			}
			resp.Diagnostics.AddError("Unable to Read VM status", err.Error())
			return
		}

		switch vm.Status {
		case "STOPPED":
			// VM is stopped — attempt detach
			if err := r.client.DetachVolume(ctx, vmID, attachedVolumeID, opts); err != nil {
				if client.IsNotFound(err) {
					tflog.Info(ctx, "Volume already detached", map[string]any{"vm_id": vmID, "attached_volume_id": attachedVolumeID})
					goto detached
				}
				// 627 = VM locked by another operation (concurrent detach), retry
				// 711 = VM became running between our check and the API call, retry
				if client.IsAPIError(err, 627) || client.IsAPIError(err, 711) {
					tflog.Info(ctx, "Detach blocked by concurrent operation, retrying", map[string]any{
						"vm_id": vmID, "error": err.Error(),
					})
					sleepWithContext(ctx, pollInterval)
					continue
				}
				resp.Diagnostics.AddError("Unable to Detach Volume", err.Error())
				return
			}
			goto detached

		case "RUNNING":
			tflog.Info(ctx, "VM is running, stopping before volume detach", map[string]any{"vm_id": vmID})
			if err := r.client.StopVm(ctx, vmID, opts); err != nil {
				// 627 = VM already being stopped/operated on by concurrent detach
				if client.IsAPIError(err, 627) {
					tflog.Info(ctx, "VM is busy (stop rejected), will re-check status", map[string]any{"vm_id": vmID})
					sleepWithContext(ctx, pollInterval)
					continue
				}
				resp.Diagnostics.AddError("Unable to Stop VM for volume detach", err.Error())
				return
			}
			sleepWithContext(ctx, pollInterval)

		case "STOPPING":
			tflog.Debug(ctx, "VM is stopping, waiting", map[string]any{"vm_id": vmID})
			sleepWithContext(ctx, pollInterval)

		case "STARTING":
			tflog.Debug(ctx, "VM is starting, waiting for it to finish", map[string]any{"vm_id": vmID})
			sleepWithContext(ctx, pollInterval)

		default:
			tflog.Warn(ctx, "VM in unexpected status, attempting detach", map[string]any{"vm_id": vmID, "status": vm.Status})
			if err := r.client.DetachVolume(ctx, vmID, attachedVolumeID, opts); err != nil {
				if client.IsNotFound(err) {
					goto detached
				}
				resp.Diagnostics.AddError("Unable to Detach Volume", err.Error())
				return
			}
			goto detached
		}
	}

detached:
	tflog.Debug(ctx, "Detached volume from VM", map[string]any{
		"vm_id":              vmID,
		"attached_volume_id": attachedVolumeID,
	})
}

// sleepWithContext sleeps for the given duration, but returns early if ctx is cancelled.
func sleepWithContext(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func (r *VolumeAttachmentResource) buildOpts(data *VolumeAttachmentResourceModel) *client.RequestOpts {
	opts := &client.RequestOpts{}
	if !data.Region.IsNull() && !data.Region.IsUnknown() {
		opts.Region = data.Region.ValueString()
	}
	if !data.ProjectTag.IsNull() && !data.ProjectTag.IsUnknown() {
		opts.ProjectTag = data.ProjectTag.ValueString()
	}
	return opts
}

func (r *VolumeAttachmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, ":", 2)
	if len(parts) != 2 {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected format 'vm_id:volume_id', got: %s\n\n"+
				"Usage: terraform import prodata_volume_attachment.example <vm_id>:<volume_id>\n"+
				"Example: terraform import prodata_volume_attachment.example 123:456", req.ID),
		)
		return
	}

	vmID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Could not parse vm_id as integer: %s\n\n"+
				"Usage: terraform import prodata_volume_attachment.example <vm_id>:<volume_id>\n"+
				"Example: terraform import prodata_volume_attachment.example 123:456", parts[0]),
		)
		return
	}

	volumeID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Could not parse volume_id as integer: %s\n\n"+
				"Usage: terraform import prodata_volume_attachment.example <vm_id>:<volume_id>\n"+
				"Example: terraform import prodata_volume_attachment.example 123:456", parts[1]),
		)
		return
	}

	// Resolve attached_volume_id (VmDisk ID) via API
	opts := &client.RequestOpts{
		Region:     r.client.Region,
		ProjectTag: r.client.ProjectTag,
	}

	vmDisks, err := r.client.GetVmVolumes(ctx, vmID, opts)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to List VM Volumes",
			fmt.Sprintf("Could not list volumes for VM %d: %s", vmID, err.Error()),
		)
		return
	}

	var attachedVolumeID int64
	found := false
	for _, disk := range vmDisks {
		if disk.UserDiskID != nil && *disk.UserDiskID == volumeID {
			attachedVolumeID = disk.ID
			found = true
			break
		}
	}

	if !found {
		resp.Diagnostics.AddError(
			"Volume Not Attached",
			fmt.Sprintf("Volume %d is not attached to VM %d. "+
				"Verify the volume is attached and the IDs are correct.", volumeID, vmID),
		)
		return
	}

	tflog.Info(ctx, "Importing volume attachment", map[string]any{
		"vm_id":              vmID,
		"volume_id":          volumeID,
		"attached_volume_id": attachedVolumeID,
	})

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("vm_id"), vmID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("volume_id"), volumeID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("attached_volume_id"), attachedVolumeID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("region"), r.client.Region)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_tag"), r.client.ProjectTag)...)
}
