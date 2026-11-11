package resources

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
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
	_ resource.Resource                = &VolumeAttachmentResource{}
	_ resource.ResourceWithConfigure   = &VolumeAttachmentResource{}
	_ resource.ResourceWithImportState = &VolumeAttachmentResource{}
)

type VolumeAttachmentResource struct {
	client *client.Client
	poll   time.Duration
}

type VolumeAttachmentResourceModel struct {
	LegacySourceVolumeID   types.String `tfsdk:"legacy_source_volume_id"`
	LegacyAttachedVolumeID types.String `tfsdk:"legacy_attached_volume_id"`
	Status                 types.String `tfsdk:"status"`
	VmID                   types.Int64  `tfsdk:"vm_id"`
	VolumeID               types.Int64  `tfsdk:"volume_id"`
	AttachedVolumeID       types.Int64  `tfsdk:"attached_volume_id"`
	Region                 types.String `tfsdk:"region"`
	ProjectTag             types.String `tfsdk:"project_tag"`
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
			"legacy_source_volume_id":   schema.StringAttribute{Optional: true, MarkdownDescription: "Explicit physical volume ID for a legacy hub disk without a recorded identity history. Never inferred from a slot or name. Managed volumes do not need this assertion.", Validators: []validator.String{stringvalidator.LengthBetween(1, 2048)}},
			"legacy_attached_volume_id": schema.StringAttribute{Optional: true, MarkdownDescription: "Explicit physical volume ID for detaching an imported legacy binding without a recorded identity history. Apply this metadata assertion before destroying the attachment. It cannot override a known managed identity.", Validators: []validator.String{stringvalidator.LengthBetween(1, 2048)}},

			"status": schema.StringAttribute{Computed: true, MarkdownDescription: "Verified attachment operation status. Pending or unverified moves retain their identity for recovery.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
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
	vmID, volumeID := data.VmID.ValueInt64(), data.VolumeID.ValueInt64()
	if err := r.client.CheckVolumeAttachmentAPI(ctx, vmID, opts); err != nil {
		resp.Diagnostics.AddError("Recoverable Volume API Required", err.Error())
		return
	}
	reserved, err := r.client.ReserveVolumeAttachmentWithIdentity(ctx, vmID, volumeID, data.LegacySourceVolumeID.ValueString(), opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Reserve Volume Attachment", err.Error())
		return
	}
	data.AttachedVolumeID = types.Int64Value(reserved.ID)
	if data.Region.ValueString() == "" {
		data.Region = types.StringValue(r.client.Region)
	}
	if data.ProjectTag.ValueString() == "" {
		data.ProjectTag = types.StringValue(r.client.ProjectTag)
	}
	data.Status = types.StringValue(reserved.Status)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !*reserved.Exists {
		resp.Diagnostics.AddError("Volume Reservation Cancelled", "The reservation was positively cancelled before submission.")
		return
	}
	if reserved.Status != "RESERVED" {
		resp.Diagnostics.AddError("Unexpected Volume Reservation", "The API did not return an inactive disk reservation; its identity was retained.")
		return
	}
	current, err := submitKnownReservation(ctx, func(ctx context.Context) (*client.VolumeAttachmentOperation, error) {
		return r.client.SubmitVolumeAttachment(ctx, vmID, volumeID, reserved.ID, opts)
	}, func(ctx context.Context) (*client.VolumeAttachmentOperation, error) {
		return r.client.GetVolumeAttachment(ctx, vmID, volumeID, reserved.ID, opts)
	}, func(v *client.VolumeAttachmentOperation) string { return v.Status }, r.movePoll())
	if current != nil {
		data.Status = types.StringValue(current.Status)
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	}
	if err != nil {
		resp.Diagnostics.AddError("Volume Attachment Outcome Unverified", err.Error())
		return
	}
	current, err = r.waitForVolumeMove(ctx, vmID, volumeID, reserved.ID, current, opts, false)
	if current != nil {
		data.Status = types.StringValue(current.Status)
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to Complete Volume Attachment", err.Error())
	}
}

func (r *VolumeAttachmentResource) movePoll() time.Duration {
	if r.poll > 0 {
		return r.poll
	}
	return detachPollInterval
}
func (r *VolumeAttachmentResource) waitForVolumeMove(ctx context.Context, vmID, volumeID, id int64, current *client.VolumeAttachmentOperation, opts *client.RequestOpts, deleting bool) (*client.VolumeAttachmentOperation, error) {
	waitCtx, cancel := context.WithTimeout(ctx, client.RetryTimeoutLong)
	defer cancel()
	for {
		if current == nil {
			return nil, fmt.Errorf("the API did not return a verified volume move")
		}
		if !*current.Exists {
			if deleting {
				return current, nil
			}
			return current, fmt.Errorf("the attachment was cancelled; its reserved identity was retained")
		}
		switch current.Status {
		case "SUCCESS":
			if !deleting && current.Direction == "ATTACH" {
				return current, nil
			}
			return current, fmt.Errorf("the API did not confirm detachment")
		case "ERROR", "UNVERIFIED", "NOT_SUBMITTED":
			return current, fmt.Errorf("volume move %d is %s; retain its identity and reconcile the known operation before retrying", id, current.Status)
		}
		timer := time.NewTimer(r.movePoll())
		select {
		case <-waitCtx.Done():
			timer.Stop()
			return current, waitCtx.Err()
		case <-timer.C:
		}
		next, err := r.client.GetVolumeAttachment(waitCtx, vmID, volumeID, id, opts)
		if err != nil {
			return current, err
		}
		current = next
	}
}

func (r *VolumeAttachmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data VolumeAttachmentResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	opts := r.buildOpts(&data)
	vmID, volumeID, id := data.VmID.ValueInt64(), data.VolumeID.ValueInt64(), data.AttachedVolumeID.ValueInt64()
	if id <= 0 {
		r.readLegacy(ctx, req, resp)
		return
	}
	current, err := r.client.GetVolumeAttachment(ctx, vmID, volumeID, id, opts)
	if err != nil {
		if client.IsNotFound(err) && data.Status.IsNull() {
			r.readLegacy(ctx, req, resp)
			return
		}
		resp.Diagnostics.AddError("Unable to Verify Volume Attachment", err.Error())
		return
	}
	if !*current.Exists {
		resp.State.RemoveResource(ctx)
		return
	}
	data.Status = types.StringValue(current.Status)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	if current.Status != "SUCCESS" || current.Direction != "ATTACH" {
		resp.Diagnostics.AddWarning("Volume Move Pending", fmt.Sprintf("Attachment %d is %s (%s); its identity is retained. Inspect the known operation before retrying.", id, current.Status, current.Direction))
	}
}

func (r *VolumeAttachmentResource) readLegacy(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data VolumeAttachmentResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	opts := r.buildOpts(&data)
	attachedVolumeID := data.AttachedVolumeID.ValueInt64() // VmDisk id (the disk as attached to the VM)
	volumeID := data.VolumeID.ValueInt64()                 // underlying volume (UserDisk) id
	vmID := data.VmID.ValueInt64()

	tflog.Debug(ctx, "Reading volume attachment", map[string]any{
		"vm_id":              vmID,
		"attached_volume_id": attachedVolumeID,
	})

	// attached_volume_id is a VmDisk id, NOT a volume/UserDisk id, so it must be
	// resolved through the VM's disk list rather than GetVolume (which keys on
	// volume ids and would query the wrong object). Membership in the VM's disk
	// list is itself the proof that the attachment still exists on this VM.
	disks, err := r.client.GetVmVolumes(ctx, vmID, opts)
	if err != nil {
		// Only a genuinely-gone VM means the attachment is gone; a transient error
		// must not silently drop the resource from state (it would be recreated and
		// could double-attach on the next apply).
		if client.IsAPIError(err, 601) {
			tflog.Warn(ctx, "VM not found, removing volume attachment from state", map[string]any{
				"vm_id": vmID,
			})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to Read Volume Attachment", err.Error())
		return
	}

	// Resolve the attached disk in the VM's disk list. Normally we match on the stored
	// VmDisk id (attached_volume_id). Right after import that computed id is absent (0),
	// so fall back to matching the underlying volume id (UserDiskID) and repopulate it.
	var attached *client.VmDisk
	for i := range disks {
		d := &disks[i]
		if attachedVolumeID != 0 {
			if d.ID == attachedVolumeID {
				attached = d
				break
			}
			continue
		}
		if d.UserDiskID != nil && *d.UserDiskID == volumeID {
			attached = d
			break
		}
	}
	if attached == nil {
		resp.Diagnostics.AddError("Legacy Volume Attachment Is Unverified", "The legacy list does not contain this binding, but cannot prove that no delayed managed operation exists. Its identity is retained; use the matching versioned API to verify cancellation or deletion.")
		return
	}

	// Defense in depth: the attached disk should map back to the configured volume.
	if attached.UserDiskID != nil && *attached.UserDiskID != volumeID {
		tflog.Warn(ctx, "Attached disk maps to a different volume than recorded; retaining state", map[string]any{
			"vm_id":              vmID,
			"attached_volume_id": attached.ID,
			"expected_volume_id": volumeID,
			"actual_volume_id":   *attached.UserDiskID,
		})
		resp.Diagnostics.AddError("Volume Attachment Identity Conflict", "The attachment belongs to another volume; existing state was retained.")
		return
	}

	// Refresh/repopulate the computed VmDisk id (covers the post-import case).
	data.AttachedVolumeID = types.Int64Value(attached.ID)

	tflog.Debug(ctx, "Read volume attachment", map[string]any{
		"vm_id":              vmID,
		"attached_volume_id": attachedVolumeID,
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *VolumeAttachmentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var previous, planned VolumeAttachmentResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &previous)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &planned)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !previous.VmID.Equal(planned.VmID) || !previous.VolumeID.Equal(planned.VolumeID) || !previous.Region.Equal(planned.Region) || !previous.ProjectTag.Equal(planned.ProjectTag) {
		resp.Diagnostics.AddError("Replacement Required", "Changing attachment identity requires resource replacement.")
		return
	}
	// Assertions record explicit user input; the core checks them before any physical operation.
	previous.LegacySourceVolumeID = planned.LegacySourceVolumeID
	previous.LegacyAttachedVolumeID = planned.LegacyAttachedVolumeID
	resp.Diagnostics.Append(resp.State.Set(ctx, &previous)...)
}

func (r *VolumeAttachmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data VolumeAttachmentResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	opts := r.buildOpts(&data)
	vmID, volumeID, id := data.VmID.ValueInt64(), data.VolumeID.ValueInt64(), data.AttachedVolumeID.ValueInt64()
	current, err := r.client.GetVolumeAttachment(ctx, vmID, volumeID, id, opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Verify Volume Attachment", err.Error())
		return
	}
	if !*current.Exists {
		return
	}
	if err := r.client.CheckVolumeAttachmentAPI(ctx, vmID, opts); err != nil {
		if client.IsAPIError(err, 601) {
			return
		}
		resp.Diagnostics.AddError("Recoverable Volume API Required", err.Error())
		return
	}
	// Cancelling an inactive reservation needs no VM shutdown. A running detach is already fenced.
	if current.Direction == "ATTACH" && (current.Status == "SUCCESS" || data.LegacyAttachedVolumeID.ValueString() != "") {
		stopCtx, cancel := context.WithTimeout(ctx, client.RetryTimeoutLong)
		defer cancel()
		stopRequested := false
		for {
			vm, e := r.client.GetVmStatus(stopCtx, vmID, opts)
			if e != nil {
				if client.IsAPIError(e, 601) {
					return
				}
				resp.Diagnostics.AddError("Unable to Read VM Status", e.Error())
				return
			}
			if vm.Status == "STOPPED" {
				break
			}
			if vm.Status == "RUNNING" && !stopRequested {
				if e = r.client.StopVm(stopCtx, vmID, opts); e != nil {
					resp.Diagnostics.AddError("Unable to Stop VM for Volume Detach", e.Error())
					return
				}
				stopRequested = true
			}
			if vm.Status != "RUNNING" && vm.Status != "STARTING" && vm.Status != "STOPPING" {
				resp.Diagnostics.AddError("VM Not Ready for Volume Detach", fmt.Sprintf("VM %d is %s", vmID, vm.Status))
				return
			}
			timer := time.NewTimer(r.movePoll())
			select {
			case <-stopCtx.Done():
				timer.Stop()
				resp.Diagnostics.AddError("Volume Detach Timed Out", stopCtx.Err().Error())
				return
			case <-timer.C:
			}
		}
	}
	current, err = submitKnownReservation(ctx, func(ctx context.Context) (*client.VolumeAttachmentOperation, error) {
		return r.client.DeleteVolumeAttachmentWithIdentity(ctx, vmID, volumeID, id, data.LegacyAttachedVolumeID.ValueString(), opts)
	}, func(ctx context.Context) (*client.VolumeAttachmentOperation, error) {
		return r.client.GetVolumeAttachment(ctx, vmID, volumeID, id, opts)
	}, func(v *client.VolumeAttachmentOperation) string { return v.Status }, r.movePoll())
	if err == nil {
		_, err = r.waitForVolumeMove(ctx, vmID, volumeID, id, current, opts, true)
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to Confirm Volume Detachment", err.Error())
	}
}

// detachPollInterval is how long the detach loop waits between VM-status polls.
const detachPollInterval = 5 * time.Second

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
