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
	_ datasource.DataSource              = &LocalNetworkDataSource{}
	_ datasource.DataSourceWithConfigure = &LocalNetworkDataSource{}
)

type LocalNetworkDataSource struct {
	client *client.Client
}

type LocalNetworkDataSourceModel struct {
	ID         types.Int64  `tfsdk:"id"`
	Region     types.String `tfsdk:"region"`
	ProjectTag types.String `tfsdk:"project_tag"`
	Name       types.String `tfsdk:"name"`
	CIDR       types.String `tfsdk:"cidr"`
	Gateway    types.String `tfsdk:"gateway"`
	Linked     types.Bool   `tfsdk:"linked"`
}

func NewLocalNetworkDataSource() datasource.DataSource {
	return &LocalNetworkDataSource{}
}

func (d *LocalNetworkDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_local_network"
}

func (d *LocalNetworkDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lookup a ProData local network by ID.",

		Attributes: map[string]schema.Attribute{
			"id": schema.Int64Attribute{
				MarkdownDescription: "The unique identifier of the local network.",
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
	}
}

func (d *LocalNetworkDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *LocalNetworkDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data LocalNetworkDataSourceModel

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

	networkID := data.ID.ValueInt64()

	tflog.Debug(ctx, "Reading local network", map[string]any{
		"id":          networkID,
		"region":      opts.Region,
		"project_tag": opts.ProjectTag,
	})

	network, err := d.client.GetLocalNetwork(ctx, networkID, opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Local Network", err.Error())
		return
	}

	data.Name = types.StringValue(network.Name)
	data.CIDR = types.StringValue(network.CIDR)
	data.Gateway = types.StringValue(network.Gateway)
	data.Linked = types.BoolValue(network.Linked)

	tflog.Debug(ctx, "Successfully read local network", map[string]any{
		"id":   networkID,
		"name": network.Name,
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
