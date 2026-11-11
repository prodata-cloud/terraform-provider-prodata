package resources

import (
	"context"
	"fmt"
	uuid "github.com/hashicorp/go-uuid"
	"strconv"
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
	_ resource.Resource                = &VolumeResource{}
	_ resource.ResourceWithConfigure   = &VolumeResource{}
	_ resource.ResourceWithImportState = &VolumeResource{}
)

type VolumeResource struct {
	client *client.Client
	poll   time.Duration
}

type VolumeResourceModel struct {
	LegacySourceVolumeID types.String `tfsdk:"legacy_source_volume_id"`
	RequestUUID          types.String `tfsdk:"request_uuid"`
	Status               types.String `tfsdk:"status"`
	ID                   types.Int64  `tfsdk:"id"`
	Region               types.String `tfsdk:"region"`
	ProjectTag           types.String `tfsdk:"project_tag"`
	Name                 types.String `tfsdk:"name"`
	Type                 types.String `tfsdk:"type"`
	Size                 types.Int64  `tfsdk:"size"`
}

func NewVolumeResource() resource.Resource {
	return &VolumeResource{}
}

func (r *VolumeResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_volume"
}

func (r *VolumeResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a ProData volume.",

		Attributes: map[string]schema.Attribute{
			"legacy_source_volume_id": schema.StringAttribute{Optional: true, MarkdownDescription: "Explicit physical source volume ID for a legacy hub disk with no durable history. Apply this metadata before destroying the resource. It never overrides a known managed identity.", Validators: []validator.String{stringvalidator.LengthBetween(1, 2048)}},
			"request_uuid":            schema.StringAttribute{Computed: true, MarkdownDescription: "Durable creation request UUID. Retained even when a lost response has not yet confirmed the canonical volume ID.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"status":                  schema.StringAttribute{Computed: true, MarkdownDescription: "Verified creation or volume operation status.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"id": schema.Int64Attribute{
				MarkdownDescription: "The unique identifier of the volume.",
				Computed:            true,
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
				MarkdownDescription: "Project tag where the volume will be created. If not specified, uses the provider's default project_tag.",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The name of the volume.",
				Required:            true,
			},
			"type": schema.StringAttribute{
				MarkdownDescription: "The type of the volume (HDD or SSD).",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"size": schema.Int64Attribute{
				MarkdownDescription: "The size of the volume in GB. Changing this forces a new resource.",
				Required:            true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
		},
	}
}

func (r *VolumeResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *VolumeResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data VolumeResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	region, project := data.Region.ValueString(), data.ProjectTag.ValueString()
	if region == "" {
		region = r.client.Region
	}
	if project == "" {
		project = r.client.ProjectTag
	}
	requestUUID, err := uuid.GenerateUUID()
	if err != nil {
		resp.Diagnostics.AddError("Unable to Reserve Volume Identity", err.Error())
		return
	}
	data.RequestUUID = types.StringValue(requestUUID)
	data.ID = types.Int64Null()
	data.Status = types.StringValue("RESERVING")
	data.Region = types.StringValue(region)
	data.ProjectTag = types.StringValue(project)
	// This response checkpoint precedes the first HTTP call. Core persists it when this RPC returns;
	// server-side committed identity/fencing protects the physical operation during this RPC.
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	opts := &client.RequestOpts{Region: region, ProjectTag: project}
	if err := r.client.CheckVolumeCreationAPI(ctx, opts); err != nil {
		// Only read-only capability discovery ran, so there is no upstream reservation to retain.
		resp.State.RemoveResource(ctx)
		resp.Diagnostics.AddError("Durable Volume API Required", err.Error())
		return
	}
	create := client.CreateVolumeRequest{Region: region, ProjectTag: project, Name: data.Name.ValueString(), Type: data.Type.ValueString(), Size: data.Size.ValueInt64()}
	current, err := r.reserveVolume(ctx, requestUUID, create, opts)
	if current != nil {
		r.checkpointCreation(ctx, &data, current, resp)
	}
	if err != nil {
		resp.Diagnostics.AddError("Volume Reservation Unverified", err.Error())
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	if current.Status == "CANCELLED" || current.Status == "UNKNOWN" {
		resp.Diagnostics.AddError("Volume Reservation Not Active", "The durable request has not confirmed an active volume; its UUID was retained for safe resolution.")
		return
	}
	if current.Status != "READY" {
		current, err = submitKnownReservation(ctx, func(ctx context.Context) (*client.VolumeCreation, error) {
			return r.client.SubmitVolumeCreation(ctx, requestUUID, opts)
		}, func(ctx context.Context) (*client.VolumeCreation, error) {
			return r.client.GetVolumeCreation(ctx, requestUUID, opts)
		}, func(v *client.VolumeCreation) string { return v.Status }, r.creationPoll())
		if current != nil {
			r.checkpointCreation(ctx, &data, current, resp)
		}
		if err != nil {
			resp.Diagnostics.AddError("Volume Submission Unverified", err.Error())
			return
		}
	}
	current, err = r.waitForVolumeCreation(ctx, requestUUID, current, opts)
	if current != nil {
		r.checkpointCreation(ctx, &data, current, resp)
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to Complete Volume Creation", err.Error())
	}
}

func (r *VolumeResource) checkpointCreation(ctx context.Context, data *VolumeResourceModel, current *client.VolumeCreation, resp *resource.CreateResponse) {
	if current.VolumeID != nil && (!volumeCreationMatches(data, current) || data.ID.ValueInt64() > 0 && data.ID.ValueInt64() != *current.VolumeID) {
		resp.Diagnostics.AddError("Volume Creation Identity Changed", "The API returned another canonical identity or immutable specification; the original request checkpoint was retained.")
		return
	}
	if current.VolumeID != nil {
		data.ID = types.Int64Value(*current.VolumeID)
	}
	data.Status = types.StringValue(current.Status)
	resp.Diagnostics.Append(resp.State.Set(ctx, data)...)
}
func volumeCreationMatches(data *VolumeResourceModel, current *client.VolumeCreation) bool {
	return current.Name == data.Name.ValueString() && current.Type == data.Type.ValueString() && current.Size == data.Size.ValueInt64()
}

func (r *VolumeResource) reserveVolume(ctx context.Context, uuid string, request client.CreateVolumeRequest, opts *client.RequestOpts) (*client.VolumeCreation, error) {
	current, err := r.client.ReserveVolumeCreation(ctx, uuid, request)
	if err == nil {
		return current, nil
	}
	known, readErr := r.client.GetVolumeCreation(ctx, uuid, opts)
	if readErr != nil {
		return nil, fmt.Errorf("reservation outcome is unknown; request %s was retained: %w", uuid, err)
	}
	if known.Status != "UNKNOWN" {
		return known, nil
	}
	// No absence inference: only the SAME idempotent request is eligible for a bounded retry.
	return r.client.ReserveVolumeCreation(ctx, uuid, request)
}
func (r *VolumeResource) creationPoll() time.Duration {
	if r.poll > 0 {
		return r.poll
	}
	return 2 * time.Second
}
func (r *VolumeResource) waitForVolumeCreation(ctx context.Context, uuid string, current *client.VolumeCreation, opts *client.RequestOpts) (*client.VolumeCreation, error) {
	waitCtx, cancel := context.WithTimeout(ctx, client.RetryTimeoutShort)
	defer cancel()
	for {
		if current == nil {
			return nil, fmt.Errorf("no durable volume identity was confirmed")
		}
		if current.Status == "READY" {
			return current, nil
		}
		if current.Status == "UNVERIFIED" || current.Status == "CANCELLED" || current.Status == "UNKNOWN" {
			return current, fmt.Errorf("volume creation %s is %s; ownership is retained", uuid, current.Status)
		}
		timer := time.NewTimer(r.creationPoll())
		select {
		case <-waitCtx.Done():
			timer.Stop()
			return current, waitCtx.Err()
		case <-timer.C:
		}
		next, err := r.client.GetVolumeCreation(waitCtx, uuid, opts)
		if err != nil {
			return current, err
		}
		current = next
	}
}

func (r *VolumeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data VolumeResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	opts := &client.RequestOpts{}
	if !data.Region.IsNull() && !data.Region.IsUnknown() {
		opts.Region = data.Region.ValueString()
	}
	if !data.ProjectTag.IsNull() && !data.ProjectTag.IsUnknown() {
		opts.ProjectTag = data.ProjectTag.ValueString()
	}

	if !data.RequestUUID.IsNull() && !data.RequestUUID.IsUnknown() && data.RequestUUID.ValueString() != "" {
		creation, err := r.client.GetVolumeCreation(ctx, data.RequestUUID.ValueString(), opts)
		if err != nil {
			resp.Diagnostics.AddError("Unable to Read Volume Creation", err.Error())
			return
		}
		if data.ID.ValueInt64() <= 0 && creation.VolumeID != nil && !volumeCreationMatches(&data, creation) {
			resp.Diagnostics.AddError("Volume Creation Identity Changed", "The recovered request does not match the immutable creation specification; state was retained.")
			return
		}
		if data.ID.ValueInt64() > 0 && (creation.VolumeID == nil || *creation.VolumeID != data.ID.ValueInt64()) {
			resp.Diagnostics.AddError("Volume Creation Identity Changed", "The API did not confirm the previously owned canonical volume ID; state was retained.")
			return
		}
		data.Status = types.StringValue(creation.Status)
		if creation.Status == "CANCELLED" && creation.CanRelease != nil && *creation.CanRelease {
			resp.State.RemoveResource(ctx)
			return
		}
		if creation.VolumeID != nil {
			data.ID = types.Int64Value(*creation.VolumeID)
		}
		if creation.Status != "READY" {
			resp.Diagnostics.AddWarning("Volume Creation Pending", "The durable request remains owned; inspect or resolve its known UUID before retrying.")
			resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
			return
		}
	}
	if data.ID.IsNull() || data.ID.IsUnknown() || data.ID.ValueInt64() <= 0 {
		resp.Diagnostics.AddError("Volume Identity Unverified", "Neither a canonical volume ID nor a verified creation request is available; state was retained.")
		return
	}
	volumeID := data.ID.ValueInt64()

	tflog.Debug(ctx, "Reading volume", map[string]any{
		"id":          volumeID,
		"region":      opts.Region,
		"project_tag": opts.ProjectTag,
	})

	volume, err := r.client.GetManagedVolume(ctx, volumeID, opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Volume", err.Error())
		return
	}
	if volume.Status != "" && volume.Status != "AVAILABLE" && volume.Status != "SUCCESS" {
		resp.Diagnostics.AddWarning("Volume operation is pending", "The volume retains its canonical identity while its physical operation is reconciled. Inspect the known operation before retrying.")
	}

	if !*volume.Exists {
		tflog.Warn(ctx, "Stable API confirmed volume absence, removing from state", map[string]any{"id": volumeID})
		resp.State.RemoveResource(ctx)
		return
	}

	if volume.CreationRequestUUID != "" {
		data.RequestUUID = types.StringValue(volume.CreationRequestUUID)
	}
	data.Status = types.StringValue(volume.Status)
	if volume.Status == "" {
		data.Status = types.StringValue("AVAILABLE")
	}
	data.Name = types.StringValue(volume.Name)
	data.Type = types.StringValue(volume.Type)
	data.Size = types.Int64Value(volume.Size)

	tflog.Debug(ctx, "Read volume", map[string]any{
		"id":   volumeID,
		"name": volume.Name,
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *VolumeResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan VolumeResourceModel
	var state VolumeResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.RequestUUID = state.RequestUUID
	plan.Status = state.Status
	volumeID := state.ID.ValueInt64()
	if plan.Name.Equal(state.Name) {
		plan.ID = state.ID
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}
	updateReq := client.UpdateVolumeRequest{
		Name: plan.Name.ValueString(),
	}
	if !plan.Region.IsNull() && !plan.Region.IsUnknown() {
		updateReq.Region = plan.Region.ValueString()
	}
	if !plan.ProjectTag.IsNull() && !plan.ProjectTag.IsUnknown() {
		updateReq.ProjectTag = plan.ProjectTag.ValueString()
	}

	tflog.Debug(ctx, "Updating volume", map[string]any{
		"id":          volumeID,
		"name":        updateReq.Name,
		"region":      updateReq.Region,
		"project_tag": updateReq.ProjectTag,
	})

	volume, err := r.client.UpdateManagedVolume(ctx, volumeID, updateReq)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Update Volume", err.Error())
		return
	}

	plan.ID = state.ID
	plan.Name = types.StringValue(volume.Name)
	plan.Type = types.StringValue(volume.Type)
	plan.Size = types.Int64Value(volume.Size)

	tflog.Debug(ctx, "Updated volume", map[string]any{
		"id":   volumeID,
		"name": volume.Name,
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *VolumeResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data VolumeResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	opts := &client.RequestOpts{}
	if !data.Region.IsNull() && !data.Region.IsUnknown() {
		opts.Region = data.Region.ValueString()
	}
	if !data.ProjectTag.IsNull() && !data.ProjectTag.IsUnknown() {
		opts.ProjectTag = data.ProjectTag.ValueString()
	}

	if !data.RequestUUID.IsNull() && !data.RequestUUID.IsUnknown() && data.RequestUUID.ValueString() != "" {
		creation, err := r.client.CancelVolumeCreation(ctx, data.RequestUUID.ValueString(), opts)
		if err != nil {
			resp.Diagnostics.AddError("Unable to Resolve Volume Creation", err.Error())
			return
		}
		if data.ID.ValueInt64() <= 0 && creation.VolumeID != nil && !volumeCreationMatches(&data, creation) {
			resp.Diagnostics.AddError("Volume Creation Identity Changed", "The recovered request does not match the immutable creation specification; state was retained.")
			return
		}
		if data.ID.ValueInt64() > 0 && (creation.VolumeID == nil || *creation.VolumeID != data.ID.ValueInt64()) {
			resp.Diagnostics.AddError("Volume Creation Identity Changed", "The API did not confirm the canonical volume ID for deletion; state was retained.")
			return
		}
		if creation.Status == "CANCELLED" && creation.CanRelease != nil && *creation.CanRelease {
			return
		}
		if creation.VolumeID != nil {
			data.ID = types.Int64Value(*creation.VolumeID)
		}
		data.Status = types.StringValue(creation.Status)
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
		if resp.Diagnostics.HasError() {
			return
		}
		if creation.Status != "READY" {
			resp.Diagnostics.AddError("Volume Creation Still Unverified", "The platform has not positively confirmed cancellation or a completed owned volume; state was retained.")
			return
		}
	}
	if data.ID.IsNull() || data.ID.IsUnknown() || data.ID.ValueInt64() <= 0 {
		resp.Diagnostics.AddError("Volume Identity Unverified", "No verified canonical ID is available for deletion; request state was retained.")
		return
	}
	volumeID := data.ID.ValueInt64()

	tflog.Debug(ctx, "Deleting volume", map[string]any{
		"id":          volumeID,
		"region":      opts.Region,
		"project_tag": opts.ProjectTag,
	})

	err := r.client.DeleteManagedVolumeWithIdentity(ctx, volumeID, data.LegacySourceVolumeID.ValueString(), opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Delete Volume", err.Error())
		return
	}

	tflog.Debug(ctx, "Deleted volume", map[string]any{"id": volumeID})
}

func (r *VolumeResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id, err := strconv.ParseInt(req.ID, 10, 64)
	if err != nil || id <= 0 {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected integer volume ID, got: %s\n\n"+
				"Usage: terraform import prodata_volume.example <volume_id>\n"+
				"Example: terraform import prodata_volume.example 123", req.ID),
		)
		return
	}

	tflog.Info(ctx, "Importing volume", map[string]any{"id": id})
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("region"), r.client.Region)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_tag"), r.client.ProjectTag)...)
}
