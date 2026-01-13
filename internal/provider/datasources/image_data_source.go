package datasources

import (
	"context"
	"fmt"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ datasource.DataSource                     = &ImageDataSource{}
	_ datasource.DataSourceWithConfigure        = &ImageDataSource{}
	_ datasource.DataSourceWithConfigValidators = &ImageDataSource{}
)

type ImageDataSource struct {
	client *client.Client
}

type ImageDataSourceModel struct {
	// Input - one of these required
	Name       types.String `tfsdk:"name"`
	Slug       types.String `tfsdk:"slug"`
	Region     types.String `tfsdk:"region"`
	ProjectTag types.String `tfsdk:"project_tag"`

	// Computed output
	ID       types.Int64 `tfsdk:"id"`
	IsCustom types.Bool  `tfsdk:"is_custom"`
}

func NewImageDataSource() datasource.DataSource {
	return &ImageDataSource{}
}

func (d *ImageDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_image"
}

func (d *ImageDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lookup a ProData image (OS template or custom image) by name or slug.",

		Attributes: map[string]schema.Attribute{
			// Lookup criteria
			"name": schema.StringAttribute{
				MarkdownDescription: "The name of the image. Used for custom images lookup. Conflicts with `slug`.",
				Optional:            true,
			},
			"slug": schema.StringAttribute{
				MarkdownDescription: "The slug of the image. Used for OS template lookup (e.g., `ubuntu-22.04`, `debian-11`). Conflicts with `name`.",
				Optional:            true,
			},
			"region": schema.StringAttribute{
				MarkdownDescription: "Region ID override. If not specified, uses the provider's default region.",
				Optional:            true,
			},
			"project_tag": schema.StringAttribute{
				MarkdownDescription: "Project Tag override. If not specified, uses the provider's default project tag.",
				Optional:            true,
			},

			// Computed attributes
			"id": schema.Int64Attribute{
				MarkdownDescription: "The unique identifier of the image.",
				Computed:            true,
			},
			"is_custom": schema.BoolAttribute{
				MarkdownDescription: "Whether this is a custom image (`true`) or OS template (`false`).",
				Computed:            true,
			},
		},
	}
}

func (d *ImageDataSource) ConfigValidators(ctx context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(
			path.MatchRoot("name"),
			path.MatchRoot("slug"),
		),
	}
}

func (d *ImageDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *ImageDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data ImageDataSourceModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Build query with optional overrides.
	query := client.ImageQuery{}
	query.Slug = data.Slug.ValueString()
	query.Name = data.Name.ValueString()

	if !data.Region.IsNull() && !data.Region.IsUnknown() {
		query.Region = data.Region.ValueString()
	}
	if !data.ProjectTag.IsNull() && !data.ProjectTag.IsUnknown() {
		query.ProjectTag = data.ProjectTag.ValueString()
	}

	tflog.Debug(ctx, "Looking up image", map[string]any{
		"slug":        query.Slug,
		"name":        query.Name,
		"region":      query.Region,
		"project_tag": query.ProjectTag,
	})

	image, err := d.client.GetImage(ctx, query)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Image", err.Error())
		return
	}

	data.ID = types.Int64Value(image.ID)
	data.IsCustom = types.BoolValue(image.IsCustom)

	tflog.Debug(ctx, "Successfully read image", map[string]any{
		"id":        data.ID.ValueInt64(),
		"is_custom": data.IsCustom.ValueBool(),
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
