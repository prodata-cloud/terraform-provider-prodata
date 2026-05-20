package datasources

import (
	"context"
	"fmt"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ datasource.DataSource              = &LbDataSource{}
	_ datasource.DataSourceWithConfigure = &LbDataSource{}
)

type LbDataSource struct {
	c *client.Client
}

// LbDataSourceModel mirrors the resource shape, minus knobs that only matter for
// management (description on CCM is panel-driven; timeouts are resource-only).
// node_pool_id is NOT exposed because the panel does not return it on GET — see
// the M6 "known limitations" entry on the resource.
type LbDataSourceModel struct {
	ID           types.Int64           `tfsdk:"id"`
	Region       types.String          `tfsdk:"region"`
	ProjectTag   types.String          `tfsdk:"project_tag"`
	Name         types.String          `tfsdk:"name"`
	Description  types.String          `tfsdk:"description"`
	Type         types.String          `tfsdk:"type"`
	Protocol     types.String          `tfsdk:"protocol"`
	NetworkID    types.Int64           `tfsdk:"network_id"`
	Source       types.String          `tfsdk:"source"`
	Status       types.String          `tfsdk:"status"`
	PublicIP     types.String          `tfsdk:"public_ip"`
	PrivateIP    types.String          `tfsdk:"private_ip"`
	DateCreated  types.String          `tfsdk:"date_created"`
	Port         []LbDataSourcePort    `tfsdk:"port"`
	VMIDs        types.Set             `tfsdk:"vm_ids"`
}

type LbDataSourcePort struct {
	Port       types.Int64 `tfsdk:"port"`
	TargetPort types.Int64 `tfsdk:"target_port"`
}

func NewLbDataSource() datasource.DataSource {
	return &LbDataSource{}
}

func (d *LbDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_lb"
}

func (d *LbDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up a single ProData load balancer by ID. " +
			"Returns the same shape as the `prodata_lb` resource (minus the " +
			"`backend_group.node_pool_id`, which the panel does not surface on GET). " +
			"Errors clearly if the LB does not exist (server error 736).",
		Attributes: map[string]schema.Attribute{
			"id": schema.Int64Attribute{
				MarkdownDescription: "Load balancer ID to look up.",
				Required:            true,
			},
			"region": schema.StringAttribute{
				MarkdownDescription: "Region ID override. If omitted, uses the provider default.",
				Optional:            true,
			},
			"project_tag": schema.StringAttribute{
				MarkdownDescription: "Project tag override. If omitted, uses the provider default.",
				Optional:            true,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Load balancer name.",
				Computed:            true,
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Free-form description. For CCM-source LBs the panel " +
					"sets this to `\"CCM: <name>\"` at create time.",
				Computed: true,
			},
			"type": schema.StringAttribute{
				MarkdownDescription: "Load balancer type: `external` (public IP) or `internal`.",
				Computed:            true,
			},
			"protocol": schema.StringAttribute{
				MarkdownDescription: "L4 protocol: `TCP` or `UDP`.",
				Computed:            true,
			},
			"network_id": schema.Int64Attribute{
				MarkdownDescription: "Local network ID.",
				Computed:            true,
			},
			"source": schema.StringAttribute{
				MarkdownDescription: "Backend source: `FRONTEND` (VM backends) or `CCM` " +
					"(Kubernetes node pool backend).",
				Computed: true,
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "Lifecycle status: `NEW`, `PROCESSING`, `SUCCESS`, `DELETED`, or `FAIL`.",
				Computed:            true,
			},
			"public_ip": schema.StringAttribute{
				MarkdownDescription: "Public IP for external LBs. Null for internal LBs or " +
					"transiently while status is `NEW`.",
				Computed: true,
			},
			"private_ip": schema.StringAttribute{
				MarkdownDescription: "Private VIP inside `network_id`. May be transiently null while `NEW`.",
				Computed:            true,
			},
			"date_created": schema.StringAttribute{
				MarkdownDescription: "Server-reported creation timestamp.",
				Computed:            true,
			},
			"port": schema.ListNestedAttribute{
				MarkdownDescription: "Port mappings.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"port": schema.Int64Attribute{
							MarkdownDescription: "Port on the load balancer.",
							Computed:            true,
						},
						"target_port": schema.Int64Attribute{
							MarkdownDescription: "Port on each backend.",
							Computed:            true,
						},
					},
				},
			},
			"vm_ids": schema.SetAttribute{
				MarkdownDescription: "Set of VM guids backing the LB. Populated for `FRONTEND`-source " +
					"LBs; empty for `CCM`-source LBs (node-pool membership is not surfaced via GET).",
				ElementType: types.StringType,
				Computed:    true,
			},
		},
	}
}

func (d *LbDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *LbDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data LbDataSourceModel
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

	id := data.ID.ValueInt64()
	tflog.Debug(ctx, "Reading load balancer", map[string]any{
		"id":          id,
		"region":      opts.Region,
		"project_tag": opts.ProjectTag,
	})

	lb, err := d.c.GetLoadBalancer(ctx, id, opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read load balancer", err.Error())
		return
	}

	data.Name = types.StringValue(lb.Name)
	if lb.Description != "" {
		data.Description = types.StringValue(lb.Description)
	} else {
		data.Description = types.StringNull()
	}
	data.Type = types.StringValue(lb.Type)
	data.Protocol = types.StringValue(lb.Protocol)
	data.NetworkID = types.Int64Value(lb.NetworkID)
	data.Source = types.StringValue(lb.Source)
	data.Status = types.StringValue(lb.Status)
	if lb.PublicIP != "" {
		data.PublicIP = types.StringValue(lb.PublicIP)
	} else {
		data.PublicIP = types.StringNull()
	}
	if lb.PrivateIP != "" {
		data.PrivateIP = types.StringValue(lb.PrivateIP)
	} else {
		data.PrivateIP = types.StringNull()
	}
	if lb.DateCreated != "" {
		data.DateCreated = types.StringValue(lb.DateCreated)
	} else {
		data.DateCreated = types.StringNull()
	}

	data.Port = make([]LbDataSourcePort, 0, len(lb.Ports))
	for _, p := range lb.Ports {
		data.Port = append(data.Port, LbDataSourcePort{
			Port:       types.Int64Value(int64(p.Port)),
			TargetPort: types.Int64Value(int64(p.TargetPort)),
		})
	}

	values := make([]attr.Value, 0, len(lb.Backends))
	for _, b := range lb.Backends {
		values = append(values, types.StringValue(b.Guid))
	}
	data.VMIDs = types.SetValueMust(types.StringType, values)

	if data.Region.IsNull() || data.Region.IsUnknown() {
		data.Region = types.StringNull()
	}
	if data.ProjectTag.IsNull() || data.ProjectTag.IsUnknown() {
		data.ProjectTag = types.StringNull()
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
