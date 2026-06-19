package resources

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"terraform-provider-prodata/internal/client"
	"terraform-provider-prodata/internal/tfutil"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource                   = &K8sClusterResource{}
	_ resource.ResourceWithConfigure      = &K8sClusterResource{}
	_ resource.ResourceWithModifyPlan     = &K8sClusterResource{}
	_ resource.ResourceWithImportState    = &K8sClusterResource{}
	_ resource.ResourceWithValidateConfig = &K8sClusterResource{}
)

// K8sClusterResource implements the prodata_kubernetes_cluster resource. The
// resource owns the cluster and its inline default worker node pool; additional
// pools are managed by the separate prodata_kubernetes_node_pool resource.
type K8sClusterResource struct {
	c *client.Client
}

// K8sClusterModel mirrors the prodata_kubernetes_cluster schema. DefaultNodePool
// is a pointer so the framework keeps the block addressable for nested plan
// modifiers.
type K8sClusterModel struct {
	ID                    types.Int64          `tfsdk:"id"`
	Region                types.String         `tfsdk:"region"`
	ProjectTag            types.String         `tfsdk:"project_tag"`
	Name                  types.String         `tfsdk:"name"`
	KubernetesVersion     types.String         `tfsdk:"kubernetes_version"`
	HighAvailability      types.Bool           `tfsdk:"high_availability"`
	NetworkID             types.Int64          `tfsdk:"network_id"`
	PodCIDR               types.String         `tfsdk:"pod_cidr"`
	NodeSubnet            types.Int64          `tfsdk:"node_subnet"`
	NodeIPRange           types.String         `tfsdk:"node_ip_range"`
	PublicKey             types.String         `tfsdk:"public_key"`
	SSHAccessEnabled      types.Bool           `tfsdk:"ssh_access_enabled"`
	PublicEndpointEnabled types.Bool           `tfsdk:"public_endpoint_enabled"`
	MasterFlavorID        types.Int64          `tfsdk:"master_flavor_id"`
	DefaultNodePool       *K8sDefaultPoolModel `tfsdk:"default_node_pool"`

	// Computed, server-owned.
	APIEndpoint       types.String   `tfsdk:"api_endpoint"`
	KubeConfig        types.Object   `tfsdk:"kube_config"`
	SSHKeyEncoded     types.String   `tfsdk:"ssh_key_encoded"`
	PrivateKeyEncoded types.String   `tfsdk:"private_key_encoded"`
	Status            types.String   `tfsdk:"status"`
	Blocked           types.Bool     `tfsdk:"blocked"`
	NodePoolCount     types.Int64    `tfsdk:"node_pool_count"`
	WorkerNodeCount   types.Int64    `tfsdk:"worker_node_count"`
	MasterNodeCount   types.Int64    `tfsdk:"master_node_count"`
	IPAddressesCount  types.Int64    `tfsdk:"ip_addresses_count"`
	DateCreated       types.String   `tfsdk:"date_created"`
	Timeouts          timeouts.Value `tfsdk:"timeouts"`
}

// K8sKubeConfigModel is the structured kube_config block: the connection fields
// parsed from the cluster's kubeconfig so the kubernetes/helm providers can be
// wired directly. It is the typed source for the computed kube_config object (the
// model field itself is a types.Object so it can hold the unknown value Terraform
// plans for it before apply). The certificate fields are base64 as they appear in
// the kubeconfig (wrap in base64decode()).
type K8sKubeConfigModel struct {
	Host                 types.String `tfsdk:"host"`
	ClusterCACertificate types.String `tfsdk:"cluster_ca_certificate"`
	ClientCertificate    types.String `tfsdk:"client_certificate"`
	ClientKey            types.String `tfsdk:"client_key"`
	Token                types.String `tfsdk:"token"`
	RawConfig            types.String `tfsdk:"raw_config"`
}

// kubeConfigAttrTypes is the object type of the kube_config block, used to build
// the value and to set it unknown in ModifyPlan when a version/master change may
// rotate the credentials. It must stay in lockstep with K8sKubeConfigModel's tags
// and the schema (asserted by the schema-consistency tests).
func kubeConfigAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"host":                   types.StringType,
		"cluster_ca_certificate": types.StringType,
		"client_certificate":     types.StringType,
		"client_key":             types.StringType,
		"token":                  types.StringType,
		"raw_config":             types.StringType,
	}
}

// kubeConfigObject parses the base64 kubeconfig secret into the computed
// kube_config object, or a null object when the cluster has no kubeconfig yet
// (NEW/PROCESSING). A construction error can only mean a static drift between the
// struct and kubeConfigAttrTypes (guarded by a unit test), so it fails safe to null.
func kubeConfigObject(ctx context.Context, secret string) types.Object {
	kc := client.ParseKubeConfig(secret)
	if kc == nil {
		return types.ObjectNull(kubeConfigAttrTypes())
	}
	obj, diags := types.ObjectValueFrom(ctx, kubeConfigAttrTypes(), K8sKubeConfigModel{
		Host:                 tfutil.StringOrNull(kc.Host),
		ClusterCACertificate: tfutil.StringOrNull(kc.ClusterCACertificate),
		ClientCertificate:    tfutil.StringOrNull(kc.ClientCertificate),
		ClientKey:            tfutil.StringOrNull(kc.ClientKey),
		Token:                tfutil.StringOrNull(kc.Token),
		RawConfig:            tfutil.StringOrNull(kc.Raw),
	})
	if diags.HasError() {
		return types.ObjectNull(kubeConfigAttrTypes())
	}
	return obj
}

// K8sDefaultPoolModel is the inline default_node_pool block. vcpu/ram/disk_size
// and name are RequiresReplace; node_count is updated in place (when autoscaling
// is off); autoscaling presence toggles the autoscaler.
type K8sDefaultPoolModel struct {
	ID          types.Int64          `tfsdk:"id"`
	Name        types.String         `tfsdk:"name"`
	VCPU        types.Int64          `tfsdk:"vcpu"`
	RAM         types.Int64          `tfsdk:"ram"`
	DiskSize    types.Int64          `tfsdk:"disk_size"`
	NodeCount   types.Int64          `tfsdk:"node_count"`
	Autoscaling *K8sAutoscalingModel `tfsdk:"autoscaling"`
}

// K8sAutoscalingModel is the optional autoscaling sub-block. Its mere presence
// means "autoscaling enabled"; absence means a fixed-size pool (node_count).
type K8sAutoscalingModel struct {
	MinNodes types.Int64 `tfsdk:"min_nodes"`
	MaxNodes types.Int64 `tfsdk:"max_nodes"`
}

const (
	k8sPollInterval       = 30 * time.Second
	k8sDefaultCreateTime  = 90 * time.Minute
	k8sDefaultUpdateTime  = 60 * time.Minute
	k8sDefaultDeleteTime  = 5 * time.Minute
	k8sMaxConsecutiveErrs = 3 // ADR-K5: tolerate up to 3 consecutive transient errors
	// k8sKubeconfigGrace bounds how long create polling waits for the lazily
	// fetched kubeconfig after the cluster is already SUCCESS, so a usable cluster
	// is not tainted by the full create timeout when the kubeconfig lags (G5).
	k8sKubeconfigGrace = 3 * time.Minute

	k8sMinNameLen = 3
	k8sMaxNameLen = 24
)

// k8sNameRegex: lowercase letters, digits and hyphens; no leading/trailing
// hyphen. The backend silently lowercases names, so we require lowercase up front
// to keep state stable.
var k8sNameRegex = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)

// clusterLocks serializes mutating operations per cluster within this process
// (ADR-K7). The backend does not enforce its `blocked` flag, so concurrent
// applies in one run that touch the same cluster (e.g. cluster + node pool) are
// kept in order here. Cross-run/CI races are out of scope (deferred G8b).
var clusterLocks sync.Map // map[int64]*sync.Mutex

// lockCluster acquires the per-cluster mutex and returns the unlock function.
func lockCluster(id int64) func() {
	m, _ := clusterLocks.LoadOrStore(id, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// ensureMutable verifies a cluster can be mutated (ADR-K7): it refuses to mutate
// a FAILed cluster and waits out an in-flight (blocked) operation until the
// cluster is unblocked or the context deadline hits. Returns an error suitable
// for a diagnostic.
func (r *K8sClusterResource) ensureMutable(ctx context.Context, id int64, opts *client.RequestOpts) error {
	for {
		cl, err := r.c.GetCluster(ctx, id, opts)
		if err != nil {
			return err
		}
		if cl.Status == client.ClusterStatusFail {
			return fmt.Errorf("cluster %d is in FAIL state and cannot be modified; inspect it in the panel and recreate", id)
		}
		if !cl.Blocked {
			return nil
		}
		tflog.Debug(ctx, "Cluster is blocked by an in-flight operation, waiting", map[string]any{"id": id})
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for cluster %d to become unblocked: %w", id, ctx.Err())
		case <-time.After(k8sPollInterval):
		}
	}
}

func NewK8sClusterResource() resource.Resource {
	return &K8sClusterResource{}
}

func (r *K8sClusterResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_cluster"
}

func (r *K8sClusterResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a ProData Managed Kubernetes cluster and its inline default worker " +
			"node pool. Cluster creation is asynchronous; `terraform apply` blocks until the cluster " +
			"reaches a usable state (SUCCESS with a kubeconfig) or the create timeout elapses. " +
			"Additional worker pools are managed with `prodata_kubernetes_node_pool`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.Int64Attribute{
				MarkdownDescription: "Cluster ID, assigned by the panel.",
				Computed:            true,
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"region": schema.StringAttribute{
				MarkdownDescription: "Region ID. If omitted, uses the provider default. The create endpoint " +
					"places the cluster in the caller's current region; changing this forces a new resource.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"project_tag": schema.StringAttribute{
				MarkdownDescription: "Project tag the cluster belongs to. If omitted, uses the provider default. " +
					"Changing this forces a new resource.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Cluster name. 3-24 characters, lowercase letters / digits / hyphens, must " +
					"not start or end with a hyphen. Must be unique across your whole account (the backend " +
					"enforces uniqueness per parent user, not per region/project). Changing it forces a new resource.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.LengthBetween(k8sMinNameLen, k8sMaxNameLen),
					stringvalidator.RegexMatches(k8sNameRegex,
						"must be lowercase letters, digits and hyphens, and must not start or end with a hyphen"),
				},
			},
			"kubernetes_version": schema.StringAttribute{
				MarkdownDescription: "Kubernetes version (e.g. `v1.31.4`). Must be a version offered by the " +
					"`prodata_kubernetes_versions` data source in this region. Upgrading is applied in place " +
					"(asynchronous rollout).",
				Required: true,
			},
			"high_availability": schema.BoolAttribute{
				MarkdownDescription: "Highly-available control plane (multiple master nodes). Defaults to false. " +
					"Changing it forces a new resource.",
				Optional:      true,
				Computed:      true,
				Default:       booldefault.StaticBool(false),
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			},
			"network_id": schema.Int64Attribute{
				MarkdownDescription: "Local network ID the cluster's nodes attach to. Changing it forces a new " +
					"resource. The API does not return this value, so it is write-once: preserved across reads " +
					"and accepted from configuration without replacement after `terraform import`.",
				Required:      true,
				PlanModifiers: []planmodifier.Int64{WriteOnceInt64()},
				Validators:    []validator.Int64{int64validator.AtLeast(1)},
			},
			"pod_cidr": schema.StringAttribute{
				MarkdownDescription: "Pod network CIDR. Must be a `/16` (e.g. `10.244.0.0/16`). " +
					"Changing it forces a new resource.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{
					stringvalidator.RegexMatches(regexp.MustCompile(`^(\d{1,3}\.){3}\d{1,3}/16$`),
						"must be a CIDR with a /16 prefix, e.g. 10.244.0.0/16"),
				},
			},
			"node_subnet": schema.Int64Attribute{
				MarkdownDescription: "Node subnet prefix length used to carve node addressing out of the local " +
					"network. Changing it forces a new resource.",
				Required:      true,
				PlanModifiers: []planmodifier.Int64{WriteOnceInt64()},
			},
			"node_ip_range": schema.StringAttribute{
				MarkdownDescription: "Control-plane IP range within the local network, as `start-end` " +
					"(e.g. `10.0.0.10-10.0.0.20`). Write-once: required at creation, set once, and not read " +
					"back from the API. Changing it forces a new resource.",
				Required:      true,
				PlanModifiers: []planmodifier.String{WriteOnceString()},
			},
			"public_key": schema.StringAttribute{
				MarkdownDescription: "SSH public key authorized on the nodes (used when `ssh_access_enabled` is true). " +
					"Write-once: not read back from the API. Changing it forces a new resource.",
				Optional:      true,
				PlanModifiers: []planmodifier.String{WriteOnceString()},
			},
			"ssh_access_enabled": schema.BoolAttribute{
				MarkdownDescription: "Authorize the `public_key` for SSH access to the nodes. Defaults to false. " +
					"Changing it forces a new resource.",
				Optional:      true,
				Computed:      true,
				Default:       booldefault.StaticBool(false),
				PlanModifiers: []planmodifier.Bool{WriteOnceBool()},
			},
			"public_endpoint_enabled": schema.BoolAttribute{
				MarkdownDescription: "Provision a public IP for the cluster API endpoint. Defaults to false. " +
					"Changing it forces a new resource.",
				Optional:      true,
				Computed:      true,
				Default:       booldefault.StaticBool(false),
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			},
			"master_flavor_id": schema.Int64Attribute{
				MarkdownDescription: "Master node configuration (flavor) ID, from the " +
					"`prodata_kubernetes_flavors` data source. Updated in place: changing it triggers a rolling " +
					"replacement of the control-plane nodes (the cluster goes `PROCESSING` until it converges).",
				Required:   true,
				Validators: []validator.Int64{int64validator.AtLeast(1)},
			},
			"default_node_pool": schema.SingleNestedAttribute{
				MarkdownDescription: "The cluster's default worker node pool, created with the cluster. " +
					"Sizing (`vcpu`, `ram`, `disk_size`) and `name` are immutable (force replacement); " +
					"`node_count` and `autoscaling` are updated in place.",
				Required: true,
				Attributes: map[string]schema.Attribute{
					"id": schema.Int64Attribute{
						MarkdownDescription: "Default pool ID, discovered after creation.",
						Computed:            true,
						PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
					},
					"name": schema.StringAttribute{
						MarkdownDescription: "Default pool name (lowercase). Changing it forces a new resource.",
						Required:            true,
						PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
						Validators: []validator.String{
							stringvalidator.LengthBetween(k8sMinNameLen, k8sMaxNameLen),
							stringvalidator.RegexMatches(k8sNameRegex,
								"must be lowercase letters, digits and hyphens, and must not start or end with a hyphen"),
						},
					},
					"vcpu": schema.Int64Attribute{
						MarkdownDescription: "vCPUs per worker node. Changing it forces a new resource.",
						Required:            true,
						PlanModifiers:       []planmodifier.Int64{int64planmodifier.RequiresReplace()},
						Validators:          []validator.Int64{int64validator.AtLeast(1)},
					},
					"ram": schema.Int64Attribute{
						MarkdownDescription: "RAM per worker node, in GB. Changing it forces a new resource.",
						Required:            true,
						PlanModifiers:       []planmodifier.Int64{int64planmodifier.RequiresReplace()},
						Validators:          []validator.Int64{int64validator.AtLeast(1)},
					},
					"disk_size": schema.Int64Attribute{
						MarkdownDescription: "Disk size per worker node, in GB. Changing it forces a new resource.",
						Required:            true,
						PlanModifiers:       []planmodifier.Int64{int64planmodifier.RequiresReplace()},
						Validators:          []validator.Int64{int64validator.AtLeast(1)},
					},
					"node_count": schema.Int64Attribute{
						MarkdownDescription: "Number of worker nodes. Updated in place. Must be omitted when " +
							"`autoscaling` is set (the autoscaler owns the count); the live value is then computed.",
						Optional:      true,
						Computed:      true,
						PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
						Validators:    []validator.Int64{int64validator.AtLeast(1)},
					},
					"autoscaling": schema.SingleNestedAttribute{
						MarkdownDescription: "Enable cluster-autoscaler for this pool. Presence enables it; " +
							"omit the block for a fixed-size pool. Mutually exclusive with `node_count`.",
						Optional: true,
						Attributes: map[string]schema.Attribute{
							"min_nodes": schema.Int64Attribute{
								MarkdownDescription: "Minimum node count.",
								Required:            true,
								Validators:          []validator.Int64{int64validator.AtLeast(1)},
							},
							"max_nodes": schema.Int64Attribute{
								MarkdownDescription: "Maximum node count (>= min_nodes).",
								Required:            true,
								Validators:          []validator.Int64{int64validator.AtLeast(1)},
							},
						},
					},
				},
			},

			// ---- computed, server-owned ----
			"api_endpoint": schema.StringAttribute{
				MarkdownDescription: "Kubernetes API server endpoint.",
				Computed:            true,
			},
			"kube_config": schema.SingleNestedAttribute{
				MarkdownDescription: "Structured cluster credentials parsed from the kubeconfig, for wiring the " +
					"`kubernetes` and `helm` providers directly. Sensitive. Null until the cluster reaches `SUCCESS`. " +
					"The certificate fields are base64-encoded exactly as they appear in the kubeconfig — wrap them in " +
					"`base64decode()` when passing them to the kubernetes provider.",
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
				MarkdownDescription: "Lifecycle status: `NEW`, `PROCESSING`, `SUCCESS`, `FAIL`, or `DELETED`.",
				Computed:            true,
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
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

func (r *K8sClusterResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T", req.ProviderData))
		return
	}
	r.c = c
}

// ValidateConfig enforces the ADR-K4 mutual exclusion: node_count and autoscaling
// cannot both be set (the autoscaler owns the count). Validators are no-ops on
// unknown values.
func (r *K8sClusterResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg K8sClusterModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || cfg.DefaultNodePool == nil {
		return
	}
	p := cfg.DefaultNodePool
	nodeCountSet := !p.NodeCount.IsNull() && !p.NodeCount.IsUnknown()
	if p.Autoscaling != nil && nodeCountSet {
		resp.Diagnostics.AddAttributeError(
			path.Root("default_node_pool").AtName("node_count"),
			"node_count conflicts with autoscaling",
			"When default_node_pool.autoscaling is set, the autoscaler owns the node count. "+
				"Remove node_count (its live value is exported as a computed attribute).",
		)
	}
	if p.Autoscaling == nil && p.NodeCount.IsNull() {
		resp.Diagnostics.AddAttributeError(
			path.Root("default_node_pool").AtName("node_count"),
			"node_count is required without autoscaling",
			"Set default_node_pool.node_count for a fixed-size pool, or add an autoscaling block.",
		)
	}
	if p.Autoscaling != nil {
		min, max := p.Autoscaling.MinNodes, p.Autoscaling.MaxNodes
		if !min.IsUnknown() && !max.IsUnknown() && min.ValueInt64() > max.ValueInt64() {
			resp.Diagnostics.AddAttributeError(
				path.Root("default_node_pool").AtName("autoscaling").AtName("max_nodes"),
				"Invalid autoscaling bounds",
				"max_nodes must be greater than or equal to min_nodes.",
			)
		}
	}
}

// ModifyPlan: when the kubernetes_version changes in place, the backend rewrites
// the kubeconfig / api_endpoint and the cluster transits PROCESSING, so those
// computed values must be unknown in the plan (ADR-K3) — otherwise Terraform's
// "computed output must be consistent" check fails after apply. Also handles
// default-pool out-of-band deletion drift (ADR-K8).
func (r *K8sClusterResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	// Destroy plan — nothing to do.
	if req.Plan.Raw.IsNull() {
		return
	}
	// Create plan — no prior state to diff.
	if req.State.Raw.IsNull() {
		return
	}

	var state, plan K8sClusterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// ADR-K8: default pool was deleted out-of-band (Read nulled the block) but the
	// config still declares it — force replacement of the whole cluster.
	if state.DefaultNodePool == nil && plan.DefaultNodePool != nil {
		resp.RequiresReplace = append(resp.RequiresReplace, path.Root("default_node_pool"))
		resp.Diagnostics.AddAttributeWarning(
			path.Root("default_node_pool"),
			"Default node pool was deleted out-of-band",
			"The cluster's default node pool is no longer present on the server; Terraform will "+
				"replace the cluster to restore it.",
		)
	}

	versionChanged := !plan.KubernetesVersion.Equal(state.KubernetesVersion)
	masterChanged := !plan.MasterFlavorID.Equal(state.MasterFlavorID)
	poolChanged := defaultPoolChanged(state.DefaultNodePool, plan.DefaultNodePool)

	// ADR-K3: a version upgrade or a master-flavor change rolls the control plane and
	// can rewrite the kubeconfig / api_endpoint, so those (and the credentials) become
	// unknown in the plan. The kube_config is a nested object, so it is blanked as a
	// whole ObjectUnknown.
	if versionChanged || masterChanged {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("kube_config"), types.ObjectUnknown(kubeConfigAttrTypes()))...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("api_endpoint"), types.StringUnknown())...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("ssh_key_encoded"), types.StringUnknown())...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("private_key_encoded"), types.StringUnknown())...)
	}

	// Any in-place mutation transits the cluster through PROCESSING / blocked and
	// changes the pool/node counts, so the volatile status fields must be unknown
	// to avoid a "provider produced inconsistent result after apply" error.
	if versionChanged || poolChanged || masterChanged {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("status"), types.StringUnknown())...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("blocked"), types.BoolUnknown())...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("node_pool_count"), types.Int64Unknown())...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("worker_node_count"), types.Int64Unknown())...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("master_node_count"), types.Int64Unknown())...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("ip_addresses_count"), types.Int64Unknown())...)
	}

	// When the default pool is autoscaling after this change, the autoscaler owns
	// the live node_count — it is server-chosen, so it must be unknown in the plan
	// (UseStateForUnknown would otherwise pin the stale prior value and trip the
	// inconsistent-result check when the autoscaler rebalances). ADR-K4.
	if poolChanged && plan.DefaultNodePool != nil && plan.DefaultNodePool.Autoscaling != nil {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx,
			path.Root("default_node_pool").AtName("node_count"), types.Int64Unknown())...)
	}
}

// defaultPoolChanged reports whether the in-place-updatable fields of the default
// pool (node_count, autoscaling presence/bounds) differ between state and plan.
func defaultPoolChanged(state, plan *K8sDefaultPoolModel) bool {
	if state == nil || plan == nil {
		return state != plan
	}
	if !plan.NodeCount.Equal(state.NodeCount) {
		return true
	}
	stateAuto, planAuto := state.Autoscaling != nil, plan.Autoscaling != nil
	if stateAuto != planAuto {
		return true
	}
	if stateAuto && planAuto {
		if !plan.Autoscaling.MinNodes.Equal(state.Autoscaling.MinNodes) ||
			!plan.Autoscaling.MaxNodes.Equal(state.Autoscaling.MaxNodes) {
			return true
		}
	}
	return false
}

// ---- Create ----

func (r *K8sClusterResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan K8sClusterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	region, projectTag := r.resolveScope(plan.Region, plan.ProjectTag)
	opts := &client.RequestOpts{Region: region, ProjectTag: projectTag}

	createTimeout, diags := plan.Timeouts.Create(ctx, k8sDefaultCreateTime)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()

	pool := plan.DefaultNodePool
	autoscale := pool.Autoscaling != nil

	wire := client.CreateClusterRequest{
		ClusterName:        plan.Name.ValueString(),
		WorkerDiskSize:     int(pool.DiskSize.ValueInt64()),
		WorkerCPU:          int(pool.VCPU.ValueInt64()),
		WorkerRAM:          int(pool.RAM.ValueInt64()),
		Addresses:          []string{plan.NodeIPRange.ValueString()},
		KuberVersion:       plan.KubernetesVersion.ValueString(),
		NodePoolName:       pool.Name.ValueString(),
		NeedPublicIP:       plan.PublicEndpointEnabled.ValueBool(),
		PublicKey:          plan.PublicKey.ValueString(),
		AuthorizeSSH:       plan.SSHAccessEnabled.ValueBool(),
		PodSubnet:          plan.PodCIDR.ValueString(),
		NodeSubnet:         int(plan.NodeSubnet.ValueInt64()),
		LocalNetID:         plan.NetworkID.ValueInt64(),
		IsHA:               plan.HighAvailability.ValueBool(),
		MasterNodeConfigID: plan.MasterFlavorID.ValueInt64(),
		AutoScaleEnabled:   autoscale,
	}
	if autoscale {
		wire.MinNodes = int(pool.Autoscaling.MinNodes.ValueInt64())
		wire.MaxNodes = int(pool.Autoscaling.MaxNodes.ValueInt64())
		wire.WorkerReplicas = wire.MinNodes // backend forces replicas=minNodes for autoscale
	} else {
		wire.WorkerReplicas = int(pool.NodeCount.ValueInt64())
	}

	// ADR-K6: adopt-or-error. If a cluster with this name already exists in the
	// scope, a lost create response (e.g. a 429 on read-back) must not orphan it.
	existingID, adoptErr := r.findClusterIDByName(ctx, plan.Name.ValueString(), opts)
	if adoptErr != nil {
		resp.Diagnostics.AddError("Unable to verify cluster name availability", client.KuberErrorDetail(adoptErr))
		return
	}
	if existingID != 0 {
		resp.Diagnostics.AddError(
			"A cluster with this name already exists",
			fmt.Sprintf("Cluster %q already exists (id %d) in this region/project. Import it "+
				"(terraform import) or choose a different name.", plan.Name.ValueString(), existingID),
		)
		return
	}

	tflog.Debug(ctx, "Creating Kubernetes cluster", map[string]any{
		"name": wire.ClusterName, "region": region, "project_tag": projectTag, "ha": wire.IsHA,
	})

	// RetryOnBusy covers transient 503 (capacity); it deliberately does not retry
	// 627. The create endpoint is not idempotent, but it errors before persisting
	// on a duplicate name, and we adopt-checked above.
	created, err := client.RetryOnBusy(ctx, client.RetryTimeoutLong, func() (*client.Cluster, error) {
		return r.c.CreateCluster(ctx, wire, opts)
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to create Kubernetes cluster", client.KuberErrorDetail(err))
		return
	}

	// Save the id to state immediately (before the long poll) so a mid-poll
	// failure leaves an importable/destroyable resource rather than an orphan.
	plan.ID = types.Int64Value(created.ID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	final, waitErr := r.waitForClusterReady(ctx, created.ID, "", opts)
	result := final
	if result == nil {
		result = created
	}

	// Discover the default pool id (create returns only the cluster id, ADR-K6).
	poolID := r.discoverDefaultPoolID(ctx, created.ID, pool.Name.ValueString(), opts)

	r.applyServerState(ctx, &plan, result, poolID, region, projectTag, false, nil)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)

	if waitErr != nil {
		resp.Diagnostics.AddError(
			"Kubernetes cluster did not reach a ready state",
			fmt.Sprintf("cluster %d: %s", created.ID, waitErr.Error()),
		)
		return
	}
	if result != nil && result.Status == client.ClusterStatusSuccess && result.Kubeconfig == "" {
		resp.Diagnostics.AddWarning(
			"Cluster is ready but its kubeconfig is not yet available",
			fmt.Sprintf("Cluster %d reached SUCCESS but the panel has not populated its kubeconfig yet "+
				"(it is fetched lazily server-side). Run `terraform refresh` once it is available.", created.ID),
		)
	}
}

// ---- Read ----

func (r *K8sClusterResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data K8sClusterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	opts := r.optsFromState(data.Region, data.ProjectTag)
	id := data.ID.ValueInt64()

	cl, err := r.c.GetCluster(ctx, id, opts)
	if err != nil {
		if client.IsKuberNotFound(err) {
			tflog.Warn(ctx, "Cluster not found, removing from state", map[string]any{"id": id})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read Kubernetes cluster", client.KuberErrorDetail(err))
		return
	}
	if cl.Status == client.ClusterStatusDeleted {
		tflog.Warn(ctx, "Cluster reported DELETED, removing from state", map[string]any{"id": id})
		resp.State.RemoveResource(ctx)
		return
	}

	region := valueOrDefault(data.Region, r.c.Region)
	projectTag := valueOrDefault(data.ProjectTag, r.c.ProjectTag)

	poolID := int64(0)
	if data.DefaultNodePool != nil {
		poolID = data.DefaultNodePool.ID.ValueInt64()
	}
	r.applyServerState(ctx, &data, cl, poolID, region, projectTag, true, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// ---- Update ----

func (r *K8sClusterResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state, plan K8sClusterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	opts := r.optsFromState(state.Region, state.ProjectTag)
	id := state.ID.ValueInt64()

	updateTimeout, diags := plan.Timeouts.Update(ctx, k8sDefaultUpdateTime)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, updateTimeout)
	defer cancel()

	// ADR-K7: serialize per-cluster mutations within this process and refuse to
	// act on a FAILed or in-flight (blocked) cluster.
	unlock := lockCluster(id)
	defer unlock()
	if err := r.ensureMutable(ctx, id, opts); err != nil {
		resp.Diagnostics.AddError("Cluster is not in a modifiable state", client.KuberErrorDetail(err))
		return
	}

	// 1) Kubernetes version upgrade (in-place, async).
	if !plan.KubernetesVersion.Equal(state.KubernetesVersion) {
		if _, err := r.c.UpdateClusterVersion(ctx, id, plan.KubernetesVersion.ValueString(), opts); err != nil {
			resp.Diagnostics.AddError("Unable to upgrade Kubernetes version", client.KuberErrorDetail(err))
			return
		}
		if _, waitErr := r.waitForClusterReady(ctx, id, plan.KubernetesVersion.ValueString(), opts); waitErr != nil {
			resp.Diagnostics.AddError("Cluster did not stabilize after version upgrade",
				fmt.Sprintf("cluster %d: %s", id, waitErr.Error()))
			return
		}
	}

	// 2) Master node configuration (flavor) change (in-place, async). The backend
	// rolls the control plane and sets the cluster PROCESSING/blocked; wait for it to
	// converge on the requested flavor so a transient snapshot is not persisted.
	if !plan.MasterFlavorID.Equal(state.MasterFlavorID) {
		if err := r.c.UpdateMasterConfig(ctx, client.UpdateMasterConfigRequest{
			ClusterID:          id,
			MasterNodeConfigID: plan.MasterFlavorID.ValueInt64(),
		}, opts); err != nil {
			resp.Diagnostics.AddError("Unable to update master node configuration", client.KuberErrorDetail(err))
			return
		}
		if _, waitErr := r.waitForMasterConfigReady(ctx, id, plan.MasterFlavorID.ValueInt64(), opts); waitErr != nil {
			resp.Diagnostics.AddError("Cluster did not stabilize after master configuration change",
				fmt.Sprintf("cluster %d: %s", id, waitErr.Error()))
			return
		}
	}

	// 3) Default pool scale / autoscaling transitions (async — wait for the pool
	// to settle so we don't persist a transient PROCESSING snapshot).
	if state.DefaultNodePool != nil && plan.DefaultNodePool != nil {
		poolID := state.DefaultNodePool.ID.ValueInt64()
		changed, err := r.reconcileDefaultPool(ctx, id, poolID, state.DefaultNodePool, plan.DefaultNodePool, opts)
		if err != nil {
			resp.Diagnostics.AddError("Unable to update default node pool", client.KuberErrorDetail(err))
			return
		}
		if changed {
			if waitErr := r.waitForPoolReady(ctx, poolID, plan.DefaultNodePool, opts); waitErr != nil {
				resp.Diagnostics.AddError("Default node pool did not stabilize",
					fmt.Sprintf("pool %d: %s", poolID, waitErr.Error()))
				return
			}
		}
	}

	// Read the cluster back to capture server-owned fields. ModifyPlan marked the
	// volatile computed fields unknown for this update, so they must be resolved from
	// a fresh read before writing state — persisting an unknown trips Terraform's
	// "inconsistent result after apply" check. If the read keeps failing, keep the
	// prior state and ask for a refresh rather than corrupt it.
	final, readErr := r.getClusterWithRetry(ctx, id, opts)
	if readErr != nil {
		resp.Diagnostics.AddError(
			"Cluster updated but its new state could not be read back",
			fmt.Sprintf("cluster %d was modified successfully but reading it back failed: %s. "+
				"Run `terraform refresh` to reconcile Terraform state.", id, client.KuberErrorDetail(readErr)),
		)
		return
	}
	region := valueOrDefault(state.Region, r.c.Region)
	projectTag := valueOrDefault(state.ProjectTag, r.c.ProjectTag)
	poolID := int64(0)
	if state.DefaultNodePool != nil {
		poolID = state.DefaultNodePool.ID.ValueInt64()
	}
	r.applyServerState(ctx, &plan, final, poolID, region, projectTag, false, nil)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// reconcileDefaultPool applies node_count / autoscaling changes to the default
// pool, choosing the right endpoint for each transition (ADR-K4). It returns
// whether a mutating call was issued (so the caller can wait for the pool to
// settle).
func (r *K8sClusterResource) reconcileDefaultPool(ctx context.Context, clusterID, poolID int64, state, plan *K8sDefaultPoolModel, opts *client.RequestOpts) (bool, error) {
	if !defaultPoolChanged(state, plan) {
		return false, nil
	}
	if poolID == 0 {
		return false, fmt.Errorf("the default node pool id for cluster %d is unknown; run `terraform refresh` and try again", clusterID)
	}

	stateAuto := state.Autoscaling != nil
	planAuto := plan.Autoscaling != nil

	switch {
	case !stateAuto && planAuto:
		// fixed -> autoscaling: enable with the new bounds.
		return true, r.c.EnableAutoscaling(ctx, client.ModifyNodePoolRequest{
			ClusterID: clusterID, NodePoolID: poolID,
			MinNodes: int(plan.Autoscaling.MinNodes.ValueInt64()),
			MaxNodes: int(plan.Autoscaling.MaxNodes.ValueInt64()),
		}, opts)

	case stateAuto && !planAuto:
		// autoscaling -> fixed: disable, pin to the requested node_count.
		fixed := 0
		if !plan.NodeCount.IsNull() && !plan.NodeCount.IsUnknown() {
			fixed = int(plan.NodeCount.ValueInt64())
		}
		return true, r.c.DisableAutoscaler(ctx, client.DisableAutoscalerRequest{
			ClusterID: clusterID, NodePoolID: poolID, FixedSize: fixed,
		}, opts)

	case stateAuto && planAuto:
		// bounds change.
		return true, r.c.UpdateAutoscaling(ctx, client.ModifyNodePoolRequest{
			ClusterID: clusterID, NodePoolID: poolID,
			MinNodes: int(plan.Autoscaling.MinNodes.ValueInt64()),
			MaxNodes: int(plan.Autoscaling.MaxNodes.ValueInt64()),
		}, opts)

	default:
		// both fixed: node_count changed.
		return true, r.c.ChangeNodePoolSize(ctx, client.ModifyNodePoolRequest{
			ClusterID: clusterID, NodePoolID: poolID,
			WorkerReplicas: int(plan.NodeCount.ValueInt64()),
		}, opts)
	}
}

// ---- Delete ----

func (r *K8sClusterResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data K8sClusterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	opts := r.optsFromState(data.Region, data.ProjectTag)
	id := data.ID.ValueInt64()

	deleteTimeout, diags := data.Timeouts.Delete(ctx, k8sDefaultDeleteTime)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, deleteTimeout)
	defer cancel()

	unlock := lockCluster(id)
	defer unlock()

	if err := r.c.DeleteCluster(ctx, id, opts); err != nil {
		if client.IsKuberNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Unable to delete Kubernetes cluster", client.KuberErrorDetail(err))
		return
	}

	// Delete is a synchronous soft-delete; confirm the cluster reads back DELETED
	// (or gone). Tolerate a few transient errors, then surface the real one
	// instead of spinning silently to the timeout (ADR-K5).
	var consecutiveErrs int
	var lastErr error
	for {
		cl, err := r.c.GetCluster(ctx, id, opts)
		switch {
		case err == nil:
			consecutiveErrs = 0
			if cl.Status == client.ClusterStatusDeleted {
				return
			}
		case client.IsKuberNotFound(err):
			return
		default:
			consecutiveErrs++
			lastErr = err
			if consecutiveErrs > k8sMaxConsecutiveErrs {
				resp.Diagnostics.AddError("Unable to confirm cluster deletion",
					fmt.Sprintf("cluster %d: %s", id, client.KuberErrorDetail(err)))
				return
			}
		}
		select {
		case <-ctx.Done():
			msg := ctx.Err().Error()
			if lastErr != nil {
				msg = fmt.Sprintf("%s (last error: %s)", msg, lastErr.Error())
			}
			resp.Diagnostics.AddError("Kubernetes cluster did not finish deleting",
				fmt.Sprintf("cluster %d: %s", id, msg))
			return
		case <-time.After(k8sPollInterval):
		}
	}
}

// ---- ImportState ----

func (r *K8sClusterResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id, region, projectTag, err := parseK8sImportID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected a cluster ID or `{region}/{id}@{project_tag}`, got: %q\n\n"+
				"Examples:\n  terraform import prodata_kubernetes_cluster.example 42\n"+
				"  terraform import prodata_kubernetes_cluster.example UZ-5/42@my-project", req.ID),
		)
		return
	}
	if region == "" {
		region = r.c.Region
	}
	if projectTag == "" {
		projectTag = r.c.ProjectTag
	}
	tflog.Info(ctx, "Importing Kubernetes cluster", map[string]any{"id": id, "region": region, "project_tag": projectTag})
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("region"), region)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_tag"), projectTag)...)
}

// ---- helpers ----

func (r *K8sClusterResource) resolveScope(region, projectTag types.String) (string, string) {
	rg := region.ValueString()
	if rg == "" {
		rg = r.c.Region
	}
	pt := projectTag.ValueString()
	if pt == "" {
		pt = r.c.ProjectTag
	}
	return rg, pt
}

func (r *K8sClusterResource) optsFromState(region, projectTag types.String) *client.RequestOpts {
	opts := &client.RequestOpts{}
	if !region.IsNull() && !region.IsUnknown() && region.ValueString() != "" {
		opts.Region = region.ValueString()
	}
	if !projectTag.IsNull() && !projectTag.IsUnknown() && projectTag.ValueString() != "" {
		opts.ProjectTag = projectTag.ValueString()
	}
	return opts
}

// findClusterIDByName returns the id of a non-DELETED cluster with the given name
// in the resolved scope, or 0 if none. Used for create-time adopt-or-error.
func (r *K8sClusterResource) findClusterIDByName(ctx context.Context, name string, opts *client.RequestOpts) (int64, error) {
	clusters, err := r.c.ListClusters(ctx, opts)
	if err != nil {
		return 0, err
	}
	want := strings.ToLower(name)
	for i := range clusters {
		if strings.ToLower(clusters[i].Name) == want && clusters[i].Status != client.ClusterStatusDeleted {
			return clusters[i].ID, nil
		}
	}
	return 0, nil
}

// discoverDefaultPoolID resolves the default pool's id after create. The create
// response carries only the cluster id, so we list the cluster's pools and match
// the one whose name equals the configured default pool name (lowercased). On any
// ambiguity or failure it returns 0 — the caller still has valid cluster state.
func (r *K8sClusterResource) discoverDefaultPoolID(ctx context.Context, clusterID int64, poolName string, opts *client.RequestOpts) int64 {
	want := strings.ToLower(poolName)
	for attempt := 0; ; attempt++ {
		pools, err := r.c.ListNodePools(ctx, clusterID, opts)
		if err == nil {
			for i := range pools {
				if strings.ToLower(pools[i].Name) == want {
					return pools[i].ID
				}
			}
		} else {
			tflog.Warn(ctx, "Could not list node pools to resolve default pool id", map[string]any{
				"cluster_id": clusterID, "error": err.Error(), "attempt": attempt,
			})
		}
		if attempt >= k8sMaxConsecutiveErrs {
			return 0
		}
		select {
		case <-ctx.Done():
			return 0
		case <-time.After(k8sPollInterval):
		}
	}
}

// clusterUpgradeConverged reports whether an in-place version upgrade has settled:
// the cluster is SUCCESS, reports the requested version, and has no operation still
// in flight. It guards waitForClusterReady's upgrade mode against returning on the
// stale pre-upgrade SUCCESS snapshot before the control plane has rolled.
func clusterUpgradeConverged(cl *client.Cluster, wantVersion string) bool {
	return cl.Status == client.ClusterStatusSuccess && cl.KubeVersion == wantVersion && !cl.Blocked
}

// clusterMasterConverged reports whether an in-place master-flavor change has
// settled: the cluster is SUCCESS, reports the requested master flavor, and has no
// operation still in flight. The backend swaps the master configuration and flips
// the cluster to PROCESSING/blocked synchronously, clearing blocked only once the
// control-plane roll completes — so this is edge-correct even if polled before the
// status has visibly flipped.
func clusterMasterConverged(cl *client.Cluster, wantFlavorID int64) bool {
	return cl.Status == client.ClusterStatusSuccess &&
		cl.MasterNodeConfig != nil && cl.MasterNodeConfig.ID == wantFlavorID && !cl.Blocked
}

// getClusterWithRetry reads a cluster, tolerating up to k8sMaxConsecutiveErrs
// transient errors so a post-mutation read-back is not derailed by a transient
// blip. Returns the last error if every attempt fails.
func (r *K8sClusterResource) getClusterWithRetry(ctx context.Context, id int64, opts *client.RequestOpts) (*client.Cluster, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		cl, err := r.c.GetCluster(ctx, id, opts)
		if err == nil {
			return cl, nil
		}
		lastErr = err
		if attempt >= k8sMaxConsecutiveErrs {
			return nil, lastErr
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(k8sPollInterval):
		}
	}
}

// waitForClusterReady polls until the cluster reaches the desired state. In create
// mode (wantVersion == "") SUCCESS with a non-empty kubeconfig is success, with a
// bounded grace for the lazily-fetched kubeconfig (G5). In upgrade mode
// (wantVersion != "") it waits for the cluster to settle on the requested version
// (clusterUpgradeConverged). FAIL or DELETED is a terminal error. Tolerates up to
// k8sMaxConsecutiveErrs transient errors (ADR-K5).
func (r *K8sClusterResource) waitForClusterReady(ctx context.Context, id int64, wantVersion string, opts *client.RequestOpts) (*client.Cluster, error) {
	var consecutiveErrs int
	var last *client.Cluster
	var kubeconfigDeadline time.Time

	for {
		cl, err := r.c.GetCluster(ctx, id, opts)
		switch {
		case err == nil:
			consecutiveErrs = 0
			last = cl
			tflog.Debug(ctx, "Polling cluster", map[string]any{"id": id, "status": cl.Status})
			switch cl.Status {
			case client.ClusterStatusSuccess:
				// Converged means create mode -> SUCCESS, or upgrade mode -> SUCCESS on
				// the requested version with no operation still in flight. Until then,
				// keep polling rather than return a stale pre-upgrade snapshot.
				if wantVersion == "" || clusterUpgradeConverged(cl, wantVersion) {
					if cl.Kubeconfig != "" {
						return cl, nil
					}
					// SUCCESS but the kubeconfig (and private key) are fetched lazily
					// server-side (G5) and can lag — also right after an upgrade rewrites
					// them. Give it a bounded grace, then accept SUCCESS rather than burn
					// the timeout (ADR-K3). The caller warns when it is still empty.
					if kubeconfigDeadline.IsZero() {
						kubeconfigDeadline = time.Now().Add(k8sKubeconfigGrace)
					} else if !time.Now().Before(kubeconfigDeadline) {
						return cl, nil
					}
				}
			case client.ClusterStatusFail, client.ClusterStatusDeleted:
				return cl, fmt.Errorf("terminal status %s", cl.Status)
			}
		case client.IsKuberNotFound(err):
			return last, fmt.Errorf("cluster %d disappeared while waiting", id)
		default:
			consecutiveErrs++
			tflog.Warn(ctx, "Transient error polling cluster", map[string]any{
				"id": id, "error": err.Error(), "consecutive_errors": consecutiveErrs,
			})
			if consecutiveErrs > k8sMaxConsecutiveErrs {
				return last, fmt.Errorf("polling failed after %d consecutive errors: %w", consecutiveErrs, err)
			}
		}

		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(k8sPollInterval):
		}
	}
}

// waitForMasterConfigReady polls until an in-place master-flavor change has
// converged (clusterMasterConverged): SUCCESS, on the requested flavor, no longer
// blocked. FAIL or DELETED is a terminal error. Like the upgrade path it allows a
// bounded grace for the lazily re-fetched kubeconfig (the master swap can rewrite
// it), then accepts SUCCESS rather than burning the timeout. Tolerates up to
// k8sMaxConsecutiveErrs transient errors (ADR-K5).
func (r *K8sClusterResource) waitForMasterConfigReady(ctx context.Context, id, wantFlavorID int64, opts *client.RequestOpts) (*client.Cluster, error) {
	var consecutiveErrs int
	var last *client.Cluster
	var kubeconfigDeadline time.Time

	for {
		cl, err := r.c.GetCluster(ctx, id, opts)
		switch {
		case err == nil:
			consecutiveErrs = 0
			last = cl
			tflog.Debug(ctx, "Polling cluster (master config)", map[string]any{"id": id, "status": cl.Status})
			switch cl.Status {
			case client.ClusterStatusSuccess:
				if clusterMasterConverged(cl, wantFlavorID) {
					if cl.Kubeconfig != "" {
						return cl, nil
					}
					if kubeconfigDeadline.IsZero() {
						kubeconfigDeadline = time.Now().Add(k8sKubeconfigGrace)
					} else if !time.Now().Before(kubeconfigDeadline) {
						return cl, nil
					}
				}
			case client.ClusterStatusFail, client.ClusterStatusDeleted:
				return cl, fmt.Errorf("terminal status %s", cl.Status)
			}
		case client.IsKuberNotFound(err):
			return last, fmt.Errorf("cluster %d disappeared while waiting", id)
		default:
			consecutiveErrs++
			tflog.Warn(ctx, "Transient error polling cluster (master config)", map[string]any{
				"id": id, "error": err.Error(), "consecutive_errors": consecutiveErrs,
			})
			if consecutiveErrs > k8sMaxConsecutiveErrs {
				return last, fmt.Errorf("polling failed after %d consecutive errors: %w", consecutiveErrs, err)
			}
		}

		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(k8sPollInterval):
		}
	}
}

// waitForPoolReady polls a node pool until it has CONVERGED to the requested
// shape: status SUCCESS and its live fields match `want`. Pools rest at SUCCESS,
// and the mutating calls are async, so a plain "status == SUCCESS" check can
// return on the stale pre-mutation snapshot; matching the requested fields makes
// the wait edge-correct regardless of whether the backend has flipped to
// PROCESSING yet. Tolerates up to k8sMaxConsecutiveErrs transient errors (ADR-K5).
func (r *K8sClusterResource) waitForPoolReady(ctx context.Context, poolID int64, want *K8sDefaultPoolModel, opts *client.RequestOpts) error {
	var consecutiveErrs int
	for {
		pool, err := r.c.GetNodePool(ctx, poolID, opts)
		switch {
		case err == nil:
			consecutiveErrs = 0
			tflog.Debug(ctx, "Polling node pool", map[string]any{"id": poolID, "status": pool.Status})
			if pool.Status == client.ClusterStatusSuccess && poolMatchesDesired(pool, want) {
				return nil
			}
		case client.IsKuberNotFound(err):
			return fmt.Errorf("node pool %d disappeared while waiting", poolID)
		default:
			consecutiveErrs++
			if consecutiveErrs > k8sMaxConsecutiveErrs {
				return fmt.Errorf("polling failed after %d consecutive errors: %w", consecutiveErrs, err)
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(k8sPollInterval):
		}
	}
}

// poolMatchesDesired reports whether a live pool reflects the requested default
// pool shape. For an autoscaling pool it checks the flag and bounds (the live
// node_count is autoscaler-owned and not asserted); for a fixed pool it checks
// the flag is off and node_count equals the request (when known).
func poolMatchesDesired(pool *client.NodePool, want *K8sDefaultPoolModel) bool {
	if want == nil {
		return true
	}
	if want.Autoscaling != nil {
		return pool.AutoscaleEnabled &&
			int64(pool.MinNodes) == want.Autoscaling.MinNodes.ValueInt64() &&
			int64(pool.MaxNodes) == want.Autoscaling.MaxNodes.ValueInt64()
	}
	if pool.AutoscaleEnabled {
		return false
	}
	if !want.NodeCount.IsNull() && !want.NodeCount.IsUnknown() {
		return int64(pool.NodeCount) == want.NodeCount.ValueInt64()
	}
	return true
}

// applyServerState writes server-owned fields from a Cluster onto the model.
// Write-once / RequiresReplace inputs that the API does not echo back
// (node_ip_range, public_key, authorize_ssh, node_subnet, and the default pool's
// sizing) are preserved from the existing model rather than overwritten.
//
// fromRead selects the default-pool reconciliation mode: on Read (true) it
// reconstructs the block after import and nulls it on out-of-band deletion; on
// Create/Update (false) it only refreshes the live fields of an existing block.
func (r *K8sClusterResource) applyServerState(ctx context.Context, m *K8sClusterModel, cl *client.Cluster, defaultPoolID int64, region, projectTag string, fromRead bool, diags *diag.Diagnostics) {
	m.ID = types.Int64Value(cl.ID)
	m.Region = types.StringValue(region)
	m.ProjectTag = types.StringValue(projectTag)
	m.Name = types.StringValue(cl.Name)
	m.KubernetesVersion = types.StringValue(cl.KubeVersion)
	m.HighAvailability = types.BoolValue(cl.IsHA)
	m.PublicEndpointEnabled = types.BoolValue(cl.IsPublic)
	m.PodCIDR = types.StringValue(cl.PodSubnet)

	m.APIEndpoint = tfutil.StringOrNull(cl.APIEndpoint)
	m.KubeConfig = kubeConfigObject(ctx, cl.Kubeconfig)
	m.SSHKeyEncoded = tfutil.StringOrNull(cl.SSHKeyEncoded)
	m.PrivateKeyEncoded = tfutil.StringOrNull(cl.PrivateKeyEncoded)
	m.Status = types.StringValue(cl.Status)
	m.Blocked = types.BoolValue(cl.Blocked)
	m.NodePoolCount = types.Int64Value(int64(cl.NodePoolCount))
	m.WorkerNodeCount = types.Int64Value(int64(cl.WorkerNodeCount))
	m.MasterNodeCount = types.Int64Value(int64(cl.MasterNodeCount))
	m.IPAddressesCount = types.Int64Value(cl.IPAddressesCount)
	m.DateCreated = tfutil.StringOrNull(cl.DateCreated)

	if cl.MasterNodeConfig != nil {
		m.MasterFlavorID = types.Int64Value(cl.MasterNodeConfig.ID)
	}

	r.applyDefaultPoolState(ctx, m, cl.ID, defaultPoolID, region, projectTag, fromRead, diags)
}

// applyDefaultPoolState reconciles the default_node_pool block with the server.
//   - block present, pool found  -> refresh live fields (node_count/autoscaling),
//     preserving the immutable sizing inputs already in the model.
//   - block present, pool missing, fromRead -> null the block + warn (ADR-K8
//     phase 1); ModifyPlan then forces a replacement on the next plan.
//   - block absent, fromRead (import) -> reconstruct the whole block from the
//     lowest-id pool, which is the worker pool created with the cluster (ADR-K6).
//   - on Create/Update a transient discovery miss never nulls the block.
func (r *K8sClusterResource) applyDefaultPoolState(ctx context.Context, m *K8sClusterModel, clusterID, defaultPoolID int64, region, projectTag string, fromRead bool, diags *diag.Diagnostics) {
	opts := &client.RequestOpts{Region: region, ProjectTag: projectTag}

	if m.DefaultNodePool == nil {
		if !fromRead {
			return // Create/Update always carry the plan's block
		}
		pool := r.discoverDefaultPool(ctx, clusterID, opts)
		if pool == nil {
			return // unresolved on import; ModifyPlan forces replacement
		}
		m.DefaultNodePool = &K8sDefaultPoolModel{
			ID:        types.Int64Value(pool.ID),
			Name:      types.StringValue(pool.Name),
			VCPU:      types.Int64Value(int64(pool.CPU)),
			RAM:       types.Int64Value(int64(pool.RAM)),
			DiskSize:  types.Int64Value(int64(pool.SSD)),
			NodeCount: types.Int64Value(int64(pool.NodeCount)),
		}
		if pool.AutoscaleEnabled {
			m.DefaultNodePool.Autoscaling = &K8sAutoscalingModel{
				MinNodes: types.Int64Value(int64(pool.MinNodes)),
				MaxNodes: types.Int64Value(int64(pool.MaxNodes)),
			}
		}
		return
	}

	if defaultPoolID == 0 {
		defaultPoolID = m.DefaultNodePool.ID.ValueInt64()
	}
	pool := r.fetchPool(ctx, defaultPoolID, m.DefaultNodePool.Name.ValueString(), clusterID, region, projectTag)
	if pool == nil {
		if fromRead {
			if diags != nil {
				diags.AddWarning(
					"Default node pool not found",
					fmt.Sprintf("The default node pool of cluster %d is no longer present on the server. "+
						"Terraform will plan to replace the cluster to restore it.", clusterID),
				)
			}
			m.DefaultNodePool = nil
		} else {
			// Create/Update: a transient discovery/fetch miss must not leave the
			// block's computed fields unknown in state (that fails Terraform's
			// consistency check). Pin them to concrete known values; a later refresh
			// reconciles the real id/count.
			if defaultPoolID != 0 {
				m.DefaultNodePool.ID = types.Int64Value(defaultPoolID)
			} else if m.DefaultNodePool.ID.IsUnknown() {
				m.DefaultNodePool.ID = types.Int64Null()
			}
			if m.DefaultNodePool.NodeCount.IsUnknown() {
				m.DefaultNodePool.NodeCount = types.Int64Null()
			}
		}
		return
	}
	m.DefaultNodePool.ID = types.Int64Value(pool.ID)
	m.DefaultNodePool.NodeCount = types.Int64Value(int64(pool.NodeCount))
	if pool.AutoscaleEnabled {
		m.DefaultNodePool.Autoscaling = &K8sAutoscalingModel{
			MinNodes: types.Int64Value(int64(pool.MinNodes)),
			MaxNodes: types.Int64Value(int64(pool.MaxNodes)),
		}
	} else {
		m.DefaultNodePool.Autoscaling = nil
	}
}

// discoverDefaultPool resolves a cluster's default pool for import: the lowest-id
// pool, which is the worker pool created with the cluster (before the master
// pool), per ADR-K6. Returns nil if the cluster has no pools or the list fails.
func (r *K8sClusterResource) discoverDefaultPool(ctx context.Context, clusterID int64, opts *client.RequestOpts) *client.NodePool {
	pools, err := r.c.ListNodePools(ctx, clusterID, opts)
	if err != nil || len(pools) == 0 {
		return nil
	}
	lowest := &pools[0]
	for i := range pools {
		if pools[i].ID < lowest.ID {
			lowest = &pools[i]
		}
	}
	return lowest
}

// fetchPool returns the default pool by id, falling back to a name match if the id
// is unknown. Returns nil if it cannot be resolved (drift handled by ModifyPlan).
func (r *K8sClusterResource) fetchPool(ctx context.Context, poolID int64, poolName string, clusterID int64, region, projectTag string) *client.NodePool {
	opts := &client.RequestOpts{Region: region, ProjectTag: projectTag}
	if poolID != 0 {
		pool, err := r.c.GetNodePool(ctx, poolID, opts)
		if err == nil {
			return pool
		}
		if client.IsKuberNotFound(err) {
			// Definitive: this id is gone (deleted out of band). Do NOT fall back to a
			// name match — it could adopt a different pool that shares the name.
			return nil
		}
		// Transient error: recover the SAME pool by id from the cluster's list.
		pools, lerr := r.c.ListNodePools(ctx, clusterID, opts)
		if lerr != nil {
			return nil
		}
		for i := range pools {
			if pools[i].ID == poolID {
				return &pools[i]
			}
		}
		return nil
	}
	// id unknown (create-time discovery / post-import): resolve by name.
	pools, err := r.c.ListNodePools(ctx, clusterID, opts)
	if err != nil {
		return nil
	}
	want := strings.ToLower(poolName)
	for i := range pools {
		if strings.ToLower(pools[i].Name) == want {
			return &pools[i]
		}
	}
	return nil
}

// parseK8sImportID accepts a bare integer id or the composite
// "{region}/{id}@{project_tag}" form (ADR-K6). Region/project_tag are empty for
// the bare form so the caller applies provider defaults.
func parseK8sImportID(s string) (id int64, region, projectTag string, err error) {
	if strings.ContainsAny(s, "/@") {
		slash := strings.IndexByte(s, '/')
		at := strings.LastIndexByte(s, '@')
		if slash <= 0 || at <= slash+1 || at >= len(s)-1 {
			return 0, "", "", fmt.Errorf("malformed composite import id %q", s)
		}
		region = s[:slash]
		idStr := s[slash+1 : at]
		projectTag = s[at+1:]
		parsed, perr := strconv.ParseInt(idStr, 10, 64)
		if perr != nil {
			return 0, "", "", fmt.Errorf("id segment %q is not an integer", idStr)
		}
		return parsed, region, projectTag, nil
	}
	parsed, perr := strconv.ParseInt(s, 10, 64)
	if perr != nil {
		return 0, "", "", fmt.Errorf("%q is not an integer", s)
	}
	return parsed, "", "", nil
}

// valueOrDefault returns the string value or the fallback when null/empty.
func valueOrDefault(v types.String, fallback string) string {
	if v.IsNull() || v.IsUnknown() || v.ValueString() == "" {
		return fallback
	}
	return v.ValueString()
}
