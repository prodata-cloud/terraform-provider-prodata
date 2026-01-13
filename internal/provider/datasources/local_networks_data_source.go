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
	_ datasource.DataSource              = &LocalNetworksDataSource{}
	_ datasource.DataSourceWithConfigure = &LocalNetworksDataSource{}
)

type LocalNetworksDataSource struct {
	client *client.Client
}

type LocalNetworksDataSourceModel struct {
	Region        types.String        `tfsdk:"region"`
	ProjectTag    types.String        `tfsdk:"project_tag"`
	LocalNetworks []LocalNetworkModel `tfsdk:"local_networks"`
}

type LocalNetworkModel struct {
	ID      types.Int64  `tfsdk:"id"`
	Name    types.String `tfsdk:"name"`
	CIDR    types.String `tfsdk:"cidr"`
	Gateway types.String `tfsdk:"gateway"`
	Linked  types.Bool   `tfsdk:"linked"`
}

func NewLocalNetworksDataSource() datasource.DataSource {
	return &LocalNetworksDataSource{}
}

func (d *LocalNetworksDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_local_networks"
}

func (d *LocalNetworksDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "List all available ProData local networks.",

		Attributes: map[string]schema.Attribute{
			"region": schema.StringAttribute{
				MarkdownDescription: "Region ID override. If not specified, uses the provider's default region.",
				Optional:            true,
			},
			"project_tag": schema.StringAttribute{
				MarkdownDescription: "Project Tag override. If not specified, uses the provider's default project tag.",
				Optional:            true,
			},
			"local_networks": schema.ListNestedAttribute{
				MarkdownDescription: "List of available local networks.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.Int64Attribute{
							MarkdownDescription: "The unique identifier of the local network.",
							Computed:            true,
						},
						"name": schema.StringAttribute{
							MarkdownDescription: "The name of the local network.",
							Computed:            true,
						},
						"cidr": schema.StringAttribute{
							MarkdownDescription: "The CIDR block of the local network.",
							Computed:            true,
						},
						"gateway": schema.StringAttribute{
							MarkdownDescription: "The gateway IP address of the local network.",
							Computed:            true,
						},
						"linked": schema.BoolAttribute{
							MarkdownDescription: "Whether the local network is linked to an instance.",
							Computed:            true,
						},
					},
				},
			},
		},
	}
}

func (d *LocalNetworksDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *LocalNetworksDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data LocalNetworksDataSourceModel

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

	tflog.Debug(ctx, "Listing local networks", map[string]any{
		"region":      opts.Region,
		"project_tag": opts.ProjectTag,
	})

	networks, err := d.client.GetLocalNetworks(ctx, opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to List Local Networks", err.Error())
		return
	}

	data.LocalNetworks = make([]LocalNetworkModel, len(networks))
	for i, net := range networks {
		data.LocalNetworks[i] = LocalNetworkModel{
			ID:      types.Int64Value(net.ID),
			Name:    types.StringValue(net.Name),
			CIDR:    types.StringValue(net.CIDR),
			Gateway: types.StringValue(net.Gateway),
			Linked:  types.BoolValue(net.Linked),
		}
	}

	tflog.Debug(ctx, "Successfully listed local networks", map[string]any{
		"count": len(networks),
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
