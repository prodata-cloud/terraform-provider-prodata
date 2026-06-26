package datasources

import (
	"context"
	"fmt"
	"strings"

	"terraform-provider-prodata/internal/client"
	"terraform-provider-prodata/internal/tfutil"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ datasource.DataSource                     = &K8sClusterDataSource{}
	_ datasource.DataSourceWithConfigure        = &K8sClusterDataSource{}
	_ datasource.DataSourceWithConfigValidators = &K8sClusterDataSource{}
)

type K8sClusterDataSource struct {
	c *client.Client
}

// K8sClusterDataSourceModel looks a cluster up by id XOR name. The name lookup
// goes through the list endpoint (which omits soft-deleted clusters), so it never
// returns a DELETED cluster; the id lookup can, and is rejected explicitly.
type K8sClusterDataSourceModel struct {
	// Lookup criteria — exactly one of id / name.
	ID         types.Int64  `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	Region     types.String `tfsdk:"region"`
	ProjectTag types.String `tfsdk:"project_tag"`

	// Computed output.
	KubernetesVersion     types.String `tfsdk:"kubernetes_version"`
	HighAvailability      types.Bool   `tfsdk:"high_availability"`
	PublicEndpointEnabled types.Bool   `tfsdk:"public_endpoint_enabled"`
	PodCIDR               types.String `tfsdk:"pod_cidr"`
	NodeIPRange           types.String `tfsdk:"node_ip_range"`
	MasterFlavorID        types.Int64  `tfsdk:"master_flavor_id"`
	APIEndpoint           types.String `tfsdk:"api_endpoint"`
	KubeConfig            types.Object `tfsdk:"kube_config"`
	SSHKeyEncoded         types.String `tfsdk:"ssh_key_encoded"`
	PrivateKeyEncoded     types.String `tfsdk:"private_key_encoded"`
	Status                types.String `tfsdk:"status"`
	Blocked               types.Bool   `tfsdk:"blocked"`
	NodePoolCount         types.Int64  `tfsdk:"node_pool_count"`
	WorkerNodeCount       types.Int64  `tfsdk:"worker_node_count"`
	MasterNodeCount       types.Int64  `tfsdk:"master_node_count"`
	IPAddressesCount      types.Int64  `tfsdk:"ip_addresses_count"`
	DateCreated           types.String `tfsdk:"date_created"`
}

func NewK8sClusterDataSource() datasource.DataSource {
	return &K8sClusterDataSource{}
}

func (d *K8sClusterDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_cluster"
}

func (d *K8sClusterDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up a single ProData Managed Kubernetes cluster by `id` or `name` " +
			"(exactly one is required). Returns the cluster's configuration plus its kubeconfig and API " +
			"endpoint. Looking up by `id` can return a soft-deleted cluster, which is rejected; looking " +
			"up by `name` only sees live clusters.",
		Attributes: map[string]schema.Attribute{
			"id": schema.Int64Attribute{
				MarkdownDescription: "Cluster ID. Conflicts with `name`.",
				Optional:            true,
				Computed:            true,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Cluster name. Conflicts with `id`.",
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
			"kubernetes_version": schema.StringAttribute{
				MarkdownDescription: "Kubernetes version (e.g. `v1.31.4`).",
				Computed:            true,
			},
			"high_availability": schema.BoolAttribute{
				MarkdownDescription: "Whether the control plane is highly available.",
				Computed:            true,
			},
			"public_endpoint_enabled": schema.BoolAttribute{
				MarkdownDescription: "Whether the cluster API endpoint has a public IP.",
				Computed:            true,
			},
			"pod_cidr": schema.StringAttribute{
				MarkdownDescription: "Pod network CIDR.",
				Computed:            true,
			},
			"node_ip_range": schema.StringAttribute{
				MarkdownDescription: "Control-plane IP range within the local network, as `start-end`. " +
					"Either supplied at creation or auto-allocated by the platform.",
				Computed: true,
			},
			"master_flavor_id": schema.Int64Attribute{
				MarkdownDescription: "Master node configuration (flavor) ID.",
				Computed:            true,
			},
			"api_endpoint": schema.StringAttribute{
				MarkdownDescription: "Kubernetes API server endpoint. Null until the cluster reaches `SUCCESS`.",
				Computed:            true,
			},
			"kube_config": schema.SingleNestedAttribute{
				MarkdownDescription: "Structured cluster credentials parsed from the kubeconfig, for wiring the " +
					"`kubernetes` and `helm` providers directly. Sensitive. Null until the cluster reaches `SUCCESS` " +
					"— gate any downstream consumer on `status`. The certificate fields are base64-encoded exactly as " +
					"they appear in the kubeconfig; wrap them in `base64decode()` when passing them to the kubernetes provider.",
				Computed:  true,
				Sensitive: true,
				Attributes: map[string]schema.Attribute{
					"host": schema.StringAttribute{
						MarkdownDescription: "Kubernetes API server URL.",
						Computed:            true,
					},
					"cluster_ca_certificate": schema.StringAttribute{
						MarkdownDescription: "Base64-encoded cluster CA certificate.",
						Computed:            true,
					},
					"client_certificate": schema.StringAttribute{
						MarkdownDescription: "Base64-encoded client certificate for cluster-admin access.",
						Computed:            true,
					},
					"client_key": schema.StringAttribute{
						MarkdownDescription: "Base64-encoded client key for cluster-admin access.",
						Computed:            true,
					},
					"token": schema.StringAttribute{
						MarkdownDescription: "Bearer token, when the cluster uses token auth (empty otherwise).",
						Computed:            true,
					},
					"raw_config": schema.StringAttribute{
						MarkdownDescription: "The full kubeconfig as plain YAML.",
						Computed:            true,
					},
				},
			},
			"ssh_key_encoded": schema.StringAttribute{
				MarkdownDescription: "Base64-encoded SSH public key registered on the nodes.",
				Computed:            true,
			},
			"private_key_encoded": schema.StringAttribute{
				MarkdownDescription: "Base64-encoded SSH private key for the nodes. Sensitive.",
				Computed:            true,
				Sensitive:           true,
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "Lifecycle status: `NEW`, `PROCESSING`, `SUCCESS`, or `FAIL`. A `DELETED` " +
					"cluster is never returned — the lookup errors instead.",
				Computed: true,
			},
			"blocked": schema.BoolAttribute{
				MarkdownDescription: "True while a mutating operation is in flight on the cluster.",
				Computed:            true,
			},
			"node_pool_count": schema.Int64Attribute{
				MarkdownDescription: "Number of node pools (including the default and master pools).",
				Computed:            true,
			},
			"worker_node_count": schema.Int64Attribute{
				MarkdownDescription: "Total worker node count across pools.",
				Computed:            true,
			},
			"master_node_count": schema.Int64Attribute{
				MarkdownDescription: "Master node count.",
				Computed:            true,
			},
			"ip_addresses_count": schema.Int64Attribute{
				MarkdownDescription: "Number of IP addresses allocated to the cluster.",
				Computed:            true,
			},
			"date_created": schema.StringAttribute{
				MarkdownDescription: "Server-reported creation timestamp.",
				Computed:            true,
			},
		},
	}
}

func (d *K8sClusterDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(
			path.MatchRoot("id"),
			path.MatchRoot("name"),
		),
	}
}

func (d *K8sClusterDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *K8sClusterDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data K8sClusterDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	opts := scopeOpts(data.Region, data.ProjectTag)

	var cl *client.Cluster
	if !data.ID.IsNull() && !data.ID.IsUnknown() {
		id := data.ID.ValueInt64()
		tflog.Debug(ctx, "Reading Kubernetes cluster by id", map[string]any{"id": id})
		found, err := d.c.GetCluster(ctx, id, opts)
		if err != nil {
			resp.Diagnostics.AddError("Unable to read Kubernetes cluster", client.KuberErrorDetail(err))
			return
		}
		if found.Status == client.ClusterStatusDeleted {
			resp.Diagnostics.AddError("Kubernetes cluster is deleted",
				fmt.Sprintf("Cluster %d has been deleted.", id))
			return
		}
		cl = found
	} else {
		name := data.Name.ValueString()
		tflog.Debug(ctx, "Reading Kubernetes cluster by name", map[string]any{"name": name})
		clusters, err := d.c.ListClusters(ctx, opts)
		if err != nil {
			resp.Diagnostics.AddError("Unable to list Kubernetes clusters", client.KuberErrorDetail(err))
			return
		}
		want := strings.ToLower(name)
		var matches []*client.Cluster
		for i := range clusters {
			if strings.ToLower(clusters[i].Name) == want && clusters[i].Status != client.ClusterStatusDeleted {
				matches = append(matches, &clusters[i])
			}
		}
		switch len(matches) {
		case 0:
			resp.Diagnostics.AddError("Kubernetes cluster not found",
				fmt.Sprintf("No cluster named %q was found in this region/project.", name))
			return
		case 1:
			cl = matches[0]
		default:
			resp.Diagnostics.AddError("Ambiguous cluster name",
				fmt.Sprintf("%d clusters named %q exist in this region/project; look the cluster up by id instead.",
					len(matches), name))
			return
		}
	}

	data.ID = types.Int64Value(cl.ID)
	data.Name = types.StringValue(cl.Name)
	data.KubernetesVersion = types.StringValue(cl.KubeVersion)
	data.HighAvailability = types.BoolValue(cl.IsHA)
	data.PublicEndpointEnabled = types.BoolValue(cl.IsPublic)
	data.PodCIDR = tfutil.StringOrNull(cl.PodSubnet)
	data.NodeIPRange = tfutil.StringOrNull(cl.NodeIPRange)
	if cl.MasterNodeConfig != nil {
		data.MasterFlavorID = types.Int64Value(cl.MasterNodeConfig.ID)
	} else {
		data.MasterFlavorID = types.Int64Null()
	}
	data.APIEndpoint = tfutil.StringOrNull(cl.APIEndpoint)
	data.KubeConfig = kubeConfigObject(ctx, cl.Kubeconfig)
	data.SSHKeyEncoded = tfutil.StringOrNull(cl.SSHKeyEncoded)
	data.PrivateKeyEncoded = tfutil.StringOrNull(cl.PrivateKeyEncoded)
	data.Status = types.StringValue(cl.Status)
	data.Blocked = types.BoolValue(cl.Blocked)
	data.NodePoolCount = types.Int64Value(int64(cl.NodePoolCount))
	data.WorkerNodeCount = types.Int64Value(int64(cl.WorkerNodeCount))
	data.MasterNodeCount = types.Int64Value(int64(cl.MasterNodeCount))
	data.IPAddressesCount = types.Int64Value(cl.IPAddressesCount)
	data.DateCreated = tfutil.StringOrNull(cl.DateCreated)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
