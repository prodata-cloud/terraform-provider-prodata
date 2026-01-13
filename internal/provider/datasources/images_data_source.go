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
	_ datasource.DataSource              = &ImagesDataSource{}
	_ datasource.DataSourceWithConfigure = &ImagesDataSource{}
)

type ImagesDataSource struct {
	client *client.Client
}

type ImagesDataSourceModel struct {
	Region     types.String `tfsdk:"region"`
	ProjectTag types.String `tfsdk:"project_tag"`
	Images     []ImageModel `tfsdk:"images"`
}

type ImageModel struct {
	ID       types.Int64  `tfsdk:"id"`
	Name     types.String `tfsdk:"name"`
	Slug     types.String `tfsdk:"slug"`
	IsCustom types.Bool   `tfsdk:"is_custom"`
}

func NewImagesDataSource() datasource.DataSource {
	return &ImagesDataSource{}
}

func (d *ImagesDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_images"
}

func (d *ImagesDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "List all available ProData images (OS templates and custom images).",

		Attributes: map[string]schema.Attribute{
			"region": schema.StringAttribute{
				MarkdownDescription: "Region ID override. If not specified, uses the provider's default region.",
				Optional:            true,
			},
			"project_tag": schema.StringAttribute{
				MarkdownDescription: "Project Tag override. If not specified, uses the provider's default project tag.",
				Optional:            true,
			},
			"images": schema.ListNestedAttribute{
				MarkdownDescription: "List of available images.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.Int64Attribute{
							MarkdownDescription: "The unique identifier of the image.",
							Computed:            true,
						},
						"name": schema.StringAttribute{
							MarkdownDescription: "The name of the image.",
							Computed:            true,
						},
						"slug": schema.StringAttribute{
							MarkdownDescription: "The slug of the image.",
							Computed:            true,
						},
						"is_custom": schema.BoolAttribute{
							MarkdownDescription: "Whether this is a custom image (`true`) or OS template (`false`).",
							Computed:            true,
						},
					},
				},
			},
		},
	}
}

func (d *ImagesDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *ImagesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data ImagesDataSourceModel

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

	tflog.Debug(ctx, "Listing images", map[string]any{
		"region":      opts.Region,
		"project_tag": opts.ProjectTag,
	})

	images, err := d.client.GetImages(ctx, opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to List Images", err.Error())
		return
	}

	data.Images = make([]ImageModel, len(images))
	for i, img := range images {
		data.Images[i] = ImageModel{
			ID:       types.Int64Value(img.ID),
			Name:     types.StringValue(img.Name),
			Slug:     types.StringValue(img.Slug),
			IsCustom: types.BoolValue(img.IsCustom),
		}
	}

	tflog.Debug(ctx, "Successfully listed images", map[string]any{
		"count": len(images),
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
