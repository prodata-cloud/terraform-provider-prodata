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
	_ datasource.DataSource              = &VolumesDataSource{}
	_ datasource.DataSourceWithConfigure = &VolumesDataSource{}
)

type VolumesDataSource struct {
	client *client.Client
}

type VolumesDataSourceModel struct {
	Region    types.String  `tfsdk:"region"`
	ProjectID types.Int64   `tfsdk:"project_id"`
	Volumes   []VolumeModel `tfsdk:"volumes"`
}

type VolumeModel struct {
	ID         types.Int64  `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	Type       types.String `tfsdk:"type"`
	Size       types.Int64  `tfsdk:"size"`
	InUse      types.Bool   `tfsdk:"in_use"`
	AttachedID types.Int64  `tfsdk:"attached_id"`
}

func NewVolumesDataSource() datasource.DataSource {
	return &VolumesDataSource{}
}

func (d *VolumesDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_volumes"
}

func (d *VolumesDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "List all available ProData volumes.",

		Attributes: map[string]schema.Attribute{
			"region": schema.StringAttribute{
				MarkdownDescription: "Region ID override. If not specified, uses the provider's default region.",
				Optional:            true,
			},
			"project_id": schema.Int64Attribute{
				MarkdownDescription: "Project ID override. If not specified, uses the provider's default project id.",
				Optional:            true,
			},
			"volumes": schema.ListNestedAttribute{
				MarkdownDescription: "List of available volumes.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.Int64Attribute{
							MarkdownDescription: "The unique identifier of the volume.",
							Computed:            true,
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
				},
			},
		},
	}
}

func (d *VolumesDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *VolumesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data VolumesDataSourceModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	opts := &client.RequestOpts{}
	if !data.Region.IsNull() && !data.Region.IsUnknown() {
		opts.Region = data.Region.ValueString()
	}
	if !data.ProjectID.IsNull() && !data.ProjectID.IsUnknown() {
		opts.ProjectID = data.ProjectID.ValueInt64()
	}

	tflog.Debug(ctx, "Listing volumes", map[string]any{
		"region":     opts.Region,
		"project_id": opts.ProjectID,
	})

	volumes, err := d.client.GetVolumes(ctx, opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to List Volumes", err.Error())
		return
	}

	data.Volumes = make([]VolumeModel, len(volumes))
	for i, vol := range volumes {
		data.Volumes[i] = VolumeModel{
			ID:    types.Int64Value(vol.ID),
			Name:  types.StringValue(vol.Name),
			Type:  types.StringValue(vol.Type),
			Size:  types.Int64Value(vol.Size),
			InUse: types.BoolValue(vol.InUse),
		}
		if vol.AttachedID != nil {
			data.Volumes[i].AttachedID = types.Int64Value(*vol.AttachedID)
		} else {
			data.Volumes[i].AttachedID = types.Int64Null()
		}
	}

	tflog.Debug(ctx, "Successfully listed volumes", map[string]any{
		"count": len(volumes),
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
