package resources

import (
	"context"
	"fmt"
	"strings"
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

const (
	volumeRetryInterval = 5 * time.Second
	volumeRetryTimeout  = 2 * time.Minute
)

var (
	_ resource.Resource              = &VolumeResource{}
	_ resource.ResourceWithConfigure = &VolumeResource{}
)

type VolumeResource struct {
	client *client.Client
}

type VolumeResourceModel struct {
	ID         types.Int64  `tfsdk:"id"`
	Region     types.String `tfsdk:"region"`
	ProjectTag types.String `tfsdk:"project_tag"`
	Name       types.String `tfsdk:"name"`
	Type       types.String `tfsdk:"type"`
	Size       types.Int64  `tfsdk:"size"`
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

	// Use provider defaults if not specified in resource
	region := data.Region.ValueString()
	if region == "" {
		region = r.client.Region
	}
	projectTag := data.ProjectTag.ValueString()
	if projectTag == "" {
		projectTag = r.client.ProjectTag
	}

	createReq := client.CreateVolumeRequest{
		Region:     region,
		ProjectTag: projectTag,
		Name:       data.Name.ValueString(),
		Type:       data.Type.ValueString(),
		Size:       data.Size.ValueInt64(),
	}

	tflog.Debug(ctx, "Creating volume", map[string]any{
		"name":        createReq.Name,
		"region":      createReq.Region,
		"project_tag": createReq.ProjectTag,
		"type":        createReq.Type,
		"size":        createReq.Size,
	})

	volume, err := r.createVolumeWithRetry(ctx, createReq)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Create Volume", err.Error())
		return
	}

	data.ID = types.Int64Value(volume.ID)
	data.Region = types.StringValue(region)
	data.ProjectTag = types.StringValue(projectTag)
	data.Name = types.StringValue(volume.Name)
	data.Type = types.StringValue(volume.Type)
	data.Size = types.Int64Value(volume.Size)

	tflog.Debug(ctx, "Created volume", map[string]any{
		"id":   volume.ID,
		"name": volume.Name,
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *VolumeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data VolumeResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Only set opts if explicitly provided in resource (overrides provider defaults)
	opts := &client.RequestOpts{}
	if !data.Region.IsNull() && !data.Region.IsUnknown() {
		opts.Region = data.Region.ValueString()
	}
	if !data.ProjectTag.IsNull() && !data.ProjectTag.IsUnknown() {
		opts.ProjectTag = data.ProjectTag.ValueString()
	}

	volumeID := data.ID.ValueInt64()

	tflog.Debug(ctx, "Reading volume", map[string]any{
		"id":          volumeID,
		"region":      opts.Region,
		"project_tag": opts.ProjectTag,
	})

	volume, err := r.client.GetVolume(ctx, volumeID, opts)
	if err != nil {
		if strings.Contains(err.Error(), "703") || strings.Contains(err.Error(), "404") {
			tflog.Warn(ctx, "Volume not found, removing from state", map[string]any{
				"id": volumeID,
			})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to Read Volume", err.Error())
		return
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

	volumeID := state.ID.ValueInt64()

	// Only name can be updated via API, region and projectTag in request body
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

	volume, err := r.client.UpdateVolume(ctx, volumeID, updateReq)
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

	// Only set opts if explicitly provided in resource (overrides provider defaults)
	opts := &client.RequestOpts{}
	if !data.Region.IsNull() && !data.Region.IsUnknown() {
		opts.Region = data.Region.ValueString()
	}
	if !data.ProjectTag.IsNull() && !data.ProjectTag.IsUnknown() {
		opts.ProjectTag = data.ProjectTag.ValueString()
	}

	volumeID := data.ID.ValueInt64()

	tflog.Debug(ctx, "Deleting volume", map[string]any{
		"id":          volumeID,
		"region":      opts.Region,
		"project_tag": opts.ProjectTag,
	})

	err := r.client.DeleteVolume(ctx, volumeID, opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Delete Volume", err.Error())
		return
	}

	tflog.Debug(ctx, "Deleted volume", map[string]any{
		"id": volumeID,
	})
}

// createVolumeWithRetry retries volume creation when the server returns error 627.
func (r *VolumeResource) createVolumeWithRetry(ctx context.Context, req client.CreateVolumeRequest) (*client.Volume, error) {
	deadline := time.Now().Add(volumeRetryTimeout)

	for {
		volume, err := r.client.CreateVolume(ctx, req)
		if err == nil {
			return volume, nil
		}

		if !strings.Contains(err.Error(), "627") {
			return nil, err
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting to create volume %q (error 627): %w", req.Name, err)
		}

		tflog.Info(ctx, "Volume creation failed with error 627, retrying", map[string]any{
			"name":     req.Name,
			"retry_in": volumeRetryInterval.String(),
		})

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(volumeRetryInterval):
		}
	}
}
