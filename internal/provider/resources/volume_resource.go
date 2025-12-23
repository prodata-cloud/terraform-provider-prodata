package resources

import (
	"context"
	"fmt"

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
	_ resource.Resource              = &VolumeResource{}
	_ resource.ResourceWithConfigure = &VolumeResource{}
)

type VolumeResource struct {
	client *client.Client
}

type VolumeResourceModel struct {
	ID        types.Int64  `tfsdk:"id"`
	Region    types.String `tfsdk:"region"`
	ProjectID types.Int64  `tfsdk:"project_id"`
	Name      types.String `tfsdk:"name"`
	Type      types.String `tfsdk:"type"`
	Size      types.Int64  `tfsdk:"size"`
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
				MarkdownDescription: "Region where the volume will be created (e.g., UZ5). If not specified, uses the provider's default region.",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"project_id": schema.Int64Attribute{
				MarkdownDescription: "Project ID where the volume will be created. If not specified, uses the provider's default project_id.",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
					int64planmodifier.UseStateForUnknown(),
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
				MarkdownDescription: "The size of the volume in GB.",
				Required:            true,
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
	projectID := data.ProjectID.ValueInt64()
	if projectID == 0 {
		projectID = r.client.ProjectID
	}

	createReq := client.CreateVolumeRequest{
		Region:    region,
		ProjectID: projectID,
		Name:      data.Name.ValueString(),
		Type:      data.Type.ValueString(),
		Size:      data.Size.ValueInt64(),
	}

	tflog.Debug(ctx, "Creating volume", map[string]any{
		"name":       createReq.Name,
		"region":     createReq.Region,
		"project_id": createReq.ProjectID,
		"type":       createReq.Type,
		"size":       createReq.Size,
	})

	volume, err := r.client.CreateVolume(ctx, createReq)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Create Volume", err.Error())
		return
	}

	data.ID = types.Int64Value(volume.ID)
	data.Region = types.StringValue(region)
	data.ProjectID = types.Int64Value(projectID)
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

	// Use provider defaults if not set in state
	region := data.Region.ValueString()
	if region == "" {
		region = r.client.Region
	}
	projectID := data.ProjectID.ValueInt64()
	if projectID == 0 {
		projectID = r.client.ProjectID
	}

	opts := &client.RequestOpts{
		Region:    region,
		ProjectID: projectID,
	}

	volumeID := data.ID.ValueInt64()

	tflog.Debug(ctx, "Reading volume", map[string]any{
		"id":         volumeID,
		"region":     opts.Region,
		"project_id": opts.ProjectID,
	})

	volume, err := r.client.GetVolume(ctx, volumeID, opts)
	if err != nil {
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
	var data VolumeResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Volume update is not supported by the API yet.
	// For now, just refresh the state.
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *VolumeResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data VolumeResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Volume delete is not supported by the API yet.
	// For now, just refresh the state.
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
