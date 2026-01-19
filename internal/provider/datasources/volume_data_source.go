package datasources

import (
	"context"
	"fmt"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ datasource.DataSource              = &VolumeDataSource{}
	_ datasource.DataSourceWithConfigure = &VolumeDataSource{}
)

type VolumeDataSource struct {
	client *client.Client
}

type VolumeDataSourceModel struct {
	ID         types.Int64  `tfsdk:"id"`
	Region     types.String `tfsdk:"region"`
	ProjectTag types.String `tfsdk:"project_tag"`
	Name       types.String `tfsdk:"name"`
	Type       types.String `tfsdk:"type"`
	Size       types.Int64  `tfsdk:"size"`
	InUse      types.Bool   `tfsdk:"in_use"`
	AttachedID types.Int64  `tfsdk:"attached_id"`
}

func NewVolumeDataSource() datasource.DataSource {
	return &VolumeDataSource{}
}

func (d *VolumeDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_volume"
}

func (d *VolumeDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lookup a ProData volume by ID.",

		Attributes: map[string]schema.Attribute{
			"id": schema.Int64Attribute{
				MarkdownDescription: "The unique identifier of the volume.",
				Required:            true,
			},
			"region": schema.StringAttribute{
				MarkdownDescription: "Region ID override. If not specified, uses the provider's default region.",
				Optional:            true,
			},
			"project_tag": schema.StringAttribute{
				MarkdownDescription: "Project Tag override. If not specified, uses the provider's default project tag.",
				Optional:            true,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The name of the volume.",
				Computed:            true,
			},
			"type": schema.StringAttribute{
				MarkdownDescription: "The type of the volume (e.g., HDD, SSD).",
				Computed:            true,
			},
			"size": schema.Int64Attribute{
				MarkdownDescription: "The size of the volume in GB.",
				Computed:            true,
			},
			"in_use": schema.BoolAttribute{
				MarkdownDescription: "Whether the volume is currently attached to an instance.",
				Computed:            true,
			},
			"attached_id": schema.Int64Attribute{
				MarkdownDescription: "The ID of the instance the volume is attached to (if any).",
				Computed:            true,
			},
		},
	}
}

func (d *VolumeDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	d.client = c
}

func (d *VolumeDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data VolumeDataSourceModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
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

	volumeID := data.ID.ValueInt64()

	tflog.Debug(ctx, "Reading volume", map[string]any{
		"id":          volumeID,
		"region":      opts.Region,
		"project_tag": opts.ProjectTag,
	})

	volume, err := d.client.GetVolume(ctx, volumeID, opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Volume", err.Error())
		return
	}

	data.Name = types.StringValue(volume.Name)
	data.Type = types.StringValue(volume.Type)
	data.Size = types.Int64Value(volume.Size)
	data.InUse = types.BoolValue(volume.InUse)

	if volume.AttachedID != nil {
		data.AttachedID = types.Int64Value(*volume.AttachedID)
	} else {
		data.AttachedID = types.Int64Null()
	}

	tflog.Debug(ctx, "Successfully read volume", map[string]any{
		"id":   volumeID,
		"name": volume.Name,
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
