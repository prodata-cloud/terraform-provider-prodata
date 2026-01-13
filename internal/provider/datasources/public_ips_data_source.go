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
	_ datasource.DataSource              = &PublicIPsDataSource{}
	_ datasource.DataSourceWithConfigure = &PublicIPsDataSource{}
)

type PublicIPsDataSource struct {
	client *client.Client
}

type PublicIPsDataSourceModel struct {
	Region     types.String    `tfsdk:"region"`
	ProjectTag types.String    `tfsdk:"project_tag"`
	PublicIPs  []PublicIPModel `tfsdk:"public_ips"`
}

type PublicIPModel struct {
	ID      types.Int64  `tfsdk:"id"`
	Name    types.String `tfsdk:"name"`
	IP      types.String `tfsdk:"ip"`
	Mask    types.String `tfsdk:"mask"`
	Gateway types.String `tfsdk:"gateway"`
}

func NewPublicIPsDataSource() datasource.DataSource {
	return &PublicIPsDataSource{}
}

func (d *PublicIPsDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_public_ips"
}

func (d *PublicIPsDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "List all available ProData public IPs.",

		Attributes: map[string]schema.Attribute{
			"region": schema.StringAttribute{
				MarkdownDescription: "Region ID override. If not specified, uses the provider's default region.",
				Optional:            true,
			},
			"project_tag": schema.StringAttribute{
				MarkdownDescription: "Project ID override. If not specified, uses the provider's default project id.",
				Optional:            true,
			},
			"public_ips": schema.ListNestedAttribute{
				MarkdownDescription: "List of available public IPs.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.Int64Attribute{
							MarkdownDescription: "The unique identifier of the public IP.",
							Computed:            true,
						},
						"name": schema.StringAttribute{
							MarkdownDescription: "The name of the public IP.",
							Computed:            true,
						},
						"ip": schema.StringAttribute{
							MarkdownDescription: "The allocated public IP address.",
							Computed:            true,
						},
						"mask": schema.StringAttribute{
							MarkdownDescription: "The subnet mask of the public IP.",
							Computed:            true,
						},
						"gateway": schema.StringAttribute{
							MarkdownDescription: "The gateway IP address.",
							Computed:            true,
						},
					},
				},
			},
		},
	}
}

func (d *PublicIPsDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *PublicIPsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data PublicIPsDataSourceModel

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

	tflog.Debug(ctx, "Listing public IPs", map[string]any{
		"region":      opts.Region,
		"project_tag": opts.ProjectTag,
	})

	ips, err := d.client.GetPublicIPs(ctx, opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to List Public IPs", err.Error())
		return
	}

	data.PublicIPs = make([]PublicIPModel, len(ips))
	for i, ip := range ips {
		data.PublicIPs[i] = PublicIPModel{
			ID:      types.Int64Value(ip.ID),
			Name:    types.StringValue(ip.Name),
			IP:      types.StringValue(ip.IP),
			Mask:    types.StringValue(ip.Mask),
			Gateway: types.StringValue(ip.Gateway),
		}
	}

	tflog.Debug(ctx, "Successfully listed public IPs", map[string]any{
		"count": len(ips),
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
