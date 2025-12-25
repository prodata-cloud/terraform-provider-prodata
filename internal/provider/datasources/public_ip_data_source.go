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
	_ datasource.DataSource              = &PublicIPDataSource{}
	_ datasource.DataSourceWithConfigure = &PublicIPDataSource{}
)

type PublicIPDataSource struct {
	client *client.Client
}

type PublicIPDataSourceModel struct {
	ID        types.Int64  `tfsdk:"id"`
	Region    types.String `tfsdk:"region"`
	ProjectID types.Int64  `tfsdk:"project_id"`
	Name      types.String `tfsdk:"name"`
	IP        types.String `tfsdk:"ip"`
	Mask      types.String `tfsdk:"mask"`
	Gateway   types.String `tfsdk:"gateway"`
}

func NewPublicIPDataSource() datasource.DataSource {
	return &PublicIPDataSource{}
}

func (d *PublicIPDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_public_ip"
}

func (d *PublicIPDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lookup a ProData public IP by ID.",

		Attributes: map[string]schema.Attribute{
			"id": schema.Int64Attribute{
				MarkdownDescription: "The unique identifier of the public IP.",
				Required:            true,
			},
			"region": schema.StringAttribute{
				MarkdownDescription: "Region ID override. If not specified, uses the provider's default region.",
				Optional:            true,
			},
			"project_id": schema.Int64Attribute{
				MarkdownDescription: "Project ID override. If not specified, uses the provider's default project id.",
				Optional:            true,
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
	}
}

func (d *PublicIPDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *PublicIPDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data PublicIPDataSourceModel

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

	ipID := data.ID.ValueInt64()

	tflog.Debug(ctx, "Reading public IP", map[string]any{
		"id":         ipID,
		"region":     opts.Region,
		"project_id": opts.ProjectID,
	})

	ip, err := d.client.GetPublicIP(ctx, ipID, opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Public IP", err.Error())
		return
	}

	data.Name = types.StringValue(ip.Name)
	data.IP = types.StringValue(ip.IP)
	data.Mask = types.StringValue(ip.Mask)
	data.Gateway = types.StringValue(ip.Gateway)

	tflog.Debug(ctx, "Successfully read public IP", map[string]any{
		"id":   ipID,
		"name": ip.Name,
		"ip":   ip.IP,
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
