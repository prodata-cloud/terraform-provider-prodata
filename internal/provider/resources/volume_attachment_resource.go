package resources

import (
	"context"
	"fmt"
	"time"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource              = &VolumeAttachmentResource{}
	_ resource.ResourceWithConfigure = &VolumeAttachmentResource{}
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

	volume, err := client.RetryOnBusy(ctx, client.RetryTimeoutShort, func() (*client.Volume, error) {
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

	// Check VM status — if running, stop it first (SCSI hot-unplug not supported)
	vm, err := r.client.GetVm(ctx, vmID, opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read VM before detach", err.Error())
		return
	}

	needsRestart := false
	if vm.Status == "RUNNING" {
		tflog.Info(ctx, "VM is running, stopping before volume detach", map[string]any{"vm_id": vmID})

		if err := r.client.StopVm(ctx, vmID, opts); err != nil {
			resp.Diagnostics.AddError("Unable to Stop VM for volume detach", err.Error())
			return
		}
		needsRestart = true

		if err := r.client.WaitForVmStatus(ctx, vmID, "STOPPED", 5*time.Minute, opts); err != nil {
			resp.Diagnostics.AddError("VM did not reach STOPPED state", err.Error())
			return
		}
	}

	err = r.client.DetachVolume(ctx, vmID, attachedVolumeID, opts)
	if err != nil {
		if needsRestart {
			tflog.Warn(ctx, "Detach failed, attempting to restart VM", map[string]any{"vm_id": vmID})
			_ = r.client.StartVm(ctx, vmID, opts)
		}
		resp.Diagnostics.AddError("Unable to Detach Volume", err.Error())
		return
	}

	tflog.Debug(ctx, "Detached volume from VM", map[string]any{
		"vm_id":              vmID,
		"attached_volume_id": attachedVolumeID,
	})

	if needsRestart {
		tflog.Info(ctx, "Restarting VM after volume detach", map[string]any{"vm_id": vmID})
		if err := r.client.StartVm(ctx, vmID, opts); err != nil {
			resp.Diagnostics.AddWarning(
				"Volume detached but VM could not be restarted",
				fmt.Sprintf("The volume was detached successfully, but the VM failed to start: %s. Please start it manually.", err.Error()),
			)
		}
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
