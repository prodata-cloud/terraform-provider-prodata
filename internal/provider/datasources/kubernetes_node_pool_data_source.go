package datasources

import (
	"context"
	"fmt"
	"strings"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ datasource.DataSource                     = &K8sNodePoolDataSource{}
	_ datasource.DataSourceWithConfigure        = &K8sNodePoolDataSource{}
	_ datasource.DataSourceWithConfigValidators = &K8sNodePoolDataSource{}
)

type K8sNodePoolDataSource struct {
	c *client.Client
}

// K8sNodePoolDataSourceModel looks a pool up within a cluster by id XOR name. The
// backend has no get-pool-by-name endpoint, so the name lookup lists the cluster's
// pools and filters; more than one match (possible only for legacy duplicate names)
// is an explicit error asking the caller to use id.
type K8sNodePoolDataSourceModel struct {
	// Lookup criteria.
	ClusterID  types.Int64  `tfsdk:"cluster_id"`
	ID         types.Int64  `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	Region     types.String `tfsdk:"region"`
	ProjectTag types.String `tfsdk:"project_tag"`

	// Computed output.
	VCPU             types.Int64  `tfsdk:"vcpu"`
	RAM              types.Int64  `tfsdk:"ram"`
	DiskSize         types.Int64  `tfsdk:"disk_size"`
	NodeCount        types.Int64  `tfsdk:"node_count"`
	NodeSubnet       types.Int64  `tfsdk:"node_subnet"`
	Status           types.String `tfsdk:"status"`
	AutoscaleEnabled types.Bool   `tfsdk:"autoscale_enabled"`
	MinNodes         types.Int64  `tfsdk:"min_nodes"`
	MaxNodes         types.Int64  `tfsdk:"max_nodes"`
}

func NewK8sNodePoolDataSource() datasource.DataSource {
	return &K8sNodePoolDataSource{}
}

func (d *K8sNodePoolDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_node_pool"
}

func (d *K8sNodePoolDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up a single node pool within a ProData Managed Kubernetes cluster by `id` " +
			"or `name` (exactly one is required, in addition to `cluster_id`). Unlike the `prodata_kubernetes_node_pool` " +
			"resource's nested `autoscaling` block, autoscaling is exposed here as the flat computed attributes " +
			"`autoscale_enabled` / `min_nodes` / `max_nodes` (`min_nodes`/`max_nodes` are `0` when autoscaling is off).",
		Attributes: map[string]schema.Attribute{
			"cluster_id": schema.Int64Attribute{
				MarkdownDescription: "ID of the cluster the pool belongs to.",
				Required:            true,
			},
			"id": schema.Int64Attribute{
				MarkdownDescription: "Node pool ID. Conflicts with `name`.",
				Optional:            true,
				Computed:            true,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Node pool name. Conflicts with `id`.",
				Optional:            true,
				Computed:            true,
			},
			"region": schema.StringAttribute{
				MarkdownDescription: "Region ID override. If omitted, uses the provider default.",
				Optional:            true,
			},
			"project_tag": schema.StringAttribute{
				MarkdownDescription: "Project tag override. If omitted, uses the provider default.",
				Optional:            true,
			},
			"vcpu": schema.Int64Attribute{
				MarkdownDescription: "vCPUs per worker node.",
				Computed:            true,
			},
			"ram": schema.Int64Attribute{
				MarkdownDescription: "RAM per worker node, in GB.",
				Computed:            true,
			},
			"disk_size": schema.Int64Attribute{
				MarkdownDescription: "Disk size per worker node, in GB.",
				Computed:            true,
			},
			"node_count": schema.Int64Attribute{
				MarkdownDescription: "Current number of worker nodes.",
				Computed:            true,
			},
			"node_subnet": schema.Int64Attribute{
				MarkdownDescription: "Node subnet prefix length assigned to the pool.",
				Computed:            true,
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "Lifecycle status: `PROCESSING` or `SUCCESS`.",
				Computed:            true,
			},
			"autoscale_enabled": schema.BoolAttribute{
				MarkdownDescription: "Whether the cluster-autoscaler manages this pool.",
				Computed:            true,
			},
			"min_nodes": schema.Int64Attribute{
				MarkdownDescription: "Autoscaling minimum node count (0 when autoscaling is off).",
				Computed:            true,
			},
			"max_nodes": schema.Int64Attribute{
				MarkdownDescription: "Autoscaling maximum node count (0 when autoscaling is off).",
				Computed:            true,
			},
		},
	}
}

func (d *K8sNodePoolDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(
			path.MatchRoot("id"),
			path.MatchRoot("name"),
		),
	}
}

func (d *K8sNodePoolDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *K8sNodePoolDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data K8sNodePoolDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	opts := scopeOpts(data.Region, data.ProjectTag)
	clusterID := data.ClusterID.ValueInt64()

	var pool *client.NodePool
	if !data.ID.IsNull() && !data.ID.IsUnknown() {
		id := data.ID.ValueInt64()
		tflog.Debug(ctx, "Reading node pool by id", map[string]any{"id": id, "cluster_id": clusterID})
		found, err := d.c.GetNodePool(ctx, id, opts)
		if err != nil {
			resp.Diagnostics.AddError("Unable to read node pool", client.KuberErrorDetail(err))
			return
		}
		if found.ClusterID != clusterID {
			resp.Diagnostics.AddError("Node pool is not in the given cluster",
				fmt.Sprintf("Node pool %d belongs to cluster %d, not %d.", id, found.ClusterID, clusterID))
			return
		}
		pool = found
	} else {
		name := data.Name.ValueString()
		tflog.Debug(ctx, "Reading node pool by name", map[string]any{"name": name, "cluster_id": clusterID})
		pools, err := d.c.ListNodePools(ctx, clusterID, opts)
		if err != nil {
			resp.Diagnostics.AddError("Unable to list node pools", client.KuberErrorDetail(err))
			return
		}
		want := strings.ToLower(name)
		var matches []*client.NodePool
		for i := range pools {
			if strings.ToLower(pools[i].Name) == want {
				matches = append(matches, &pools[i])
			}
		}
		switch len(matches) {
		case 0:
			resp.Diagnostics.AddError("Node pool not found",
				fmt.Sprintf("No node pool named %q was found in cluster %d.", name, clusterID))
			return
		case 1:
			pool = matches[0]
		default:
			resp.Diagnostics.AddError("Ambiguous node pool name",
				fmt.Sprintf("%d node pools named %q exist in cluster %d; look the pool up by id instead.",
					len(matches), name, clusterID))
			return
		}
	}

	data.ID = types.Int64Value(pool.ID)
	data.Name = types.StringValue(pool.Name)
	data.VCPU = types.Int64Value(int64(pool.CPU))
	data.RAM = types.Int64Value(int64(pool.RAM))
	data.DiskSize = types.Int64Value(int64(pool.SSD))
	data.NodeCount = types.Int64Value(int64(pool.NodeCount))
	data.NodeSubnet = types.Int64Value(int64(pool.NodeSubnet))
	data.Status = types.StringValue(pool.Status)
	data.AutoscaleEnabled = types.BoolValue(pool.AutoscaleEnabled)
	data.MinNodes = types.Int64Value(int64(pool.MinNodes))
	data.MaxNodes = types.Int64Value(int64(pool.MaxNodes))

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
