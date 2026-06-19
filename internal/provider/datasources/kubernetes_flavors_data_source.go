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
	_ datasource.DataSource              = &K8sFlavorsDataSource{}
	_ datasource.DataSourceWithConfigure = &K8sFlavorsDataSource{}
)

type K8sFlavorsDataSource struct {
	c *client.Client
}

// K8sFlavorsDataSourceModel lists master-node configurations (flavors). The backend
// endpoint is parameterized by HA, so when is_ha is omitted both sets are fetched
// and merged; when set, only that set is returned.
type K8sFlavorsDataSourceModel struct {
	Region           types.String       `tfsdk:"region"`
	ProjectTag       types.String       `tfsdk:"project_tag"`
	HighAvailability types.Bool         `tfsdk:"high_availability"`
	Flavors          []K8sFlavorSummary `tfsdk:"flavors"`
}

type K8sFlavorSummary struct {
	ID               types.Int64 `tfsdk:"id"`
	VCPU             types.Int64 `tfsdk:"vcpu"`
	RAM              types.Int64 `tfsdk:"ram"`
	DiskSize         types.Int64 `tfsdk:"disk_size"`
	HighAvailability types.Bool  `tfsdk:"high_availability"`
	RegionID         types.Int64 `tfsdk:"region_id"`
}

func NewK8sFlavorsDataSource() datasource.DataSource {
	return &K8sFlavorsDataSource{}
}

func (d *K8sFlavorsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_flavors"
}

func (d *K8sFlavorsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "List the master-node configurations (flavors) available for the resolved region, " +
			"used for the `master_flavor_id` of a `prodata_kubernetes_cluster`. When `high_availability` is omitted, " +
			"both HA and non-HA flavors are returned.",
		Attributes: map[string]schema.Attribute{
			"region": schema.StringAttribute{
				MarkdownDescription: "Region ID override. If omitted, uses the provider default.",
				Optional:            true,
			},
			"project_tag": schema.StringAttribute{
				MarkdownDescription: "Project tag override. If omitted, uses the provider default.",
				Optional:            true,
			},
			"high_availability": schema.BoolAttribute{
				MarkdownDescription: "Restrict the result to highly-available (`true`) or single-master (`false`) " +
					"flavors. If omitted, both are returned.",
				Optional: true,
			},
			"flavors": schema.ListNestedAttribute{
				MarkdownDescription: "Master-node flavors.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.Int64Attribute{
							MarkdownDescription: "Flavor ID — use as `master_flavor_id`.",
							Computed:            true,
						},
						"vcpu": schema.Int64Attribute{
							MarkdownDescription: "vCPUs per master node.",
							Computed:            true,
						},
						"ram": schema.Int64Attribute{
							MarkdownDescription: "RAM per master node, in GB.",
							Computed:            true,
						},
						"disk_size": schema.Int64Attribute{
							MarkdownDescription: "Disk size per master node, in GB.",
							Computed:            true,
						},
						"high_availability": schema.BoolAttribute{
							MarkdownDescription: "Whether this flavor provisions a highly-available control plane.",
							Computed:            true,
						},
						"region_id": schema.Int64Attribute{
							MarkdownDescription: "Region this flavor belongs to.",
							Computed:            true,
						},
					},
				},
			},
		},
	}
}

func (d *K8sFlavorsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T. Please report this issue to the provider developers.", req.ProviderData))
		return
	}
	d.c = c
}

func (d *K8sFlavorsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data K8sFlavorsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	opts := scopeOpts(data.Region, data.ProjectTag)

	var haValues []bool
	if !data.HighAvailability.IsNull() && !data.HighAvailability.IsUnknown() {
		haValues = []bool{data.HighAvailability.ValueBool()}
	} else {
		// Endpoint is parameterized by HA; fetch both and merge.
		haValues = []bool{false, true}
	}

	tflog.Debug(ctx, "Listing Kubernetes flavors", map[string]any{"ha_values": haValues})

	var configs []client.MasterNodeConfig
	for _, ha := range haValues {
		got, err := d.c.GetMasterNodeConfigs(ctx, ha, opts)
		if err != nil {
			resp.Diagnostics.AddError("Unable to list Kubernetes flavors", client.KuberErrorDetail(err))
			return
		}
		configs = append(configs, got...)
	}

	data.Flavors = make([]K8sFlavorSummary, 0, len(configs))
	for _, c := range configs {
		data.Flavors = append(data.Flavors, K8sFlavorSummary{
			ID:               types.Int64Value(c.ID),
			VCPU:             types.Int64Value(int64(c.CPU)),
			RAM:              types.Int64Value(int64(c.RAM)),
			DiskSize:         types.Int64Value(int64(c.SSD)),
			HighAvailability: types.BoolValue(c.IsHA),
			RegionID:         types.Int64Value(c.RegionID),
		})
	}

	tflog.Debug(ctx, "Listed Kubernetes flavors", map[string]any{"count": len(data.Flavors)})
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
