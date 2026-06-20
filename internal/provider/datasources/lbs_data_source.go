package datasources

import (
	"context"
	"fmt"

	"terraform-provider-prodata/internal/client"
	"terraform-provider-prodata/internal/tfutil"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ datasource.DataSource              = &LbsDataSource{}
	_ datasource.DataSourceWithConfigure = &LbsDataSource{}
)

type LbsDataSource struct {
	c *client.Client
}

type LbsDataSourceModel struct {
	Region        types.String `tfsdk:"region"`
	ProjectTag    types.String `tfsdk:"project_tag"`
	LoadBalancers []LbSummary  `tfsdk:"load_balancers"`
}

// LbSummary is the trimmed shape returned by the list endpoint. Port mappings
// and backend membership are intentionally omitted — fetch the full LB by id via
// the `prodata_lb` data source if you need them.
type LbSummary struct {
	ID          types.Int64  `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Type        types.String `tfsdk:"type"`
	Status      types.String `tfsdk:"status"`
	Source      types.String `tfsdk:"source"`
	PublicIP    types.String `tfsdk:"public_ip"`
	PrivateIP   types.String `tfsdk:"private_ip"`
	DateCreated types.String `tfsdk:"date_created"`
}

func NewLbsDataSource() datasource.DataSource {
	return &LbsDataSource{}
}

func (d *LbsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_lbs"
}

func (d *LbsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "List load balancers visible to the current project. " +
			"Returns a summary view — port mappings and backend membership are not " +
			"included to keep list responses small. Use the `prodata_lb` data source " +
			"for the full shape of a single LB.",
		Attributes: map[string]schema.Attribute{
			"region": schema.StringAttribute{
				MarkdownDescription: "Region ID override. If omitted, uses the provider default.",
				Optional:            true,
			},
			"project_tag": schema.StringAttribute{
				MarkdownDescription: "Project tag override. If omitted, uses the provider default.",
				Optional:            true,
			},
			"load_balancers": schema.ListNestedAttribute{
				MarkdownDescription: "Load balancers, in the order returned by the server.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.Int64Attribute{
							MarkdownDescription: "Load balancer ID.",
							Computed:            true,
						},
						"name": schema.StringAttribute{
							MarkdownDescription: "Load balancer name.",
							Computed:            true,
						},
						"type": schema.StringAttribute{
							MarkdownDescription: "Load balancer type: `external` or `internal`.",
							Computed:            true,
						},
						"status": schema.StringAttribute{
							MarkdownDescription: "Lifecycle status: `NEW`, `PROCESSING`, `SUCCESS`, `DELETED`, or `FAIL`.",
							Computed:            true,
						},
						"source": schema.StringAttribute{
							MarkdownDescription: "Backend source: `FRONTEND` or `CCM`.",
							Computed:            true,
						},
						"public_ip": schema.StringAttribute{
							MarkdownDescription: "Public IP (external LBs only).",
							Computed:            true,
						},
						"private_ip": schema.StringAttribute{
							MarkdownDescription: "Private VIP inside the LB's local network.",
							Computed:            true,
						},
						"date_created": schema.StringAttribute{
							MarkdownDescription: "Server-reported creation timestamp.",
							Computed:            true,
						},
					},
				},
			},
		},
	}
}

func (d *LbsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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
	d.c = c
}

func (d *LbsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data LbsDataSourceModel
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

	tflog.Debug(ctx, "Listing load balancers", map[string]any{
		"region":      opts.Region,
		"project_tag": opts.ProjectTag,
	})

	lbs, err := d.c.ListLoadBalancers(ctx, opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to list load balancers", client.LBErrorDetail(err))
		return
	}

	data.LoadBalancers = make([]LbSummary, 0, len(lbs))
	for _, lb := range lbs {
		// Skip soft-deleted load balancers so the list matches the live set.
		if lb.Status == client.LbStatusDeleted {
			continue
		}
		data.LoadBalancers = append(data.LoadBalancers, LbSummary{
			ID:          types.Int64Value(lb.ID),
			Name:        types.StringValue(lb.Name),
			Type:        types.StringValue(lb.Type),
			Status:      types.StringValue(lb.Status),
			Source:      types.StringValue(lb.Source),
			PublicIP:    tfutil.StringOrNull(lb.PublicIP),
			PrivateIP:   tfutil.StringOrNull(lb.PrivateIP),
			DateCreated: tfutil.StringOrNull(lb.DateCreated),
		})
	}

	tflog.Debug(ctx, "Listed load balancers", map[string]any{"count": len(lbs)})
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
