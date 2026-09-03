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
// resource owns the cluster's control plane only; all worker node pools —
// including the first — are managed by the separate prodata_kubernetes_node_pool resource.
type K8sClusterResource struct {
	c *client.Client
}

// K8sClusterModel mirrors the prodata_kubernetes_cluster schema.
type K8sClusterModel struct {
	ID                    types.Int64  `tfsdk:"id"`
	Region                types.String `tfsdk:"region"`
	ProjectTag            types.String `tfsdk:"project_tag"`
	Name                  types.String `tfsdk:"name"`
	KubernetesVersion     types.String `tfsdk:"kubernetes_version"`
	HighAvailability      types.Bool   `tfsdk:"high_availability"`
	NetworkID             types.Int64  `tfsdk:"network_id"`
	PodCIDR               types.String `tfsdk:"pod_cidr"`
	NodeIPRange           types.String `tfsdk:"node_ip_range"`
	PublicKey             types.String `tfsdk:"public_key"`
	SSHAccessEnabled      types.Bool   `tfsdk:"ssh_access_enabled"`
	PublicEndpointEnabled types.Bool   `tfsdk:"public_endpoint_enabled"`
	MasterFlavorID        types.Int64  `tfsdk:"master_flavor_id"`
	ControlPlaneSize      types.String `tfsdk:"control_plane_size"`

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

// K8sAutoscalingModel is the optional autoscaling sub-block. Its mere presence
// means "autoscaling enabled"; absence means a fixed-size pool (node_count).
type K8sAutoscalingModel struct {
	MinNodes types.Int64 `tfsdk:"min_nodes"`
	MaxNodes types.Int64 `tfsdk:"max_nodes"`
}

const (
	k8sPollInterval      = 30 * time.Second
	k8sDefaultCreateTime = 90 * time.Minute
	k8sDefaultUpdateTime = 60 * time.Minute
	// k8sDefaultDeleteTime is deliberately long: teardown is async — after the delete
	// ack the backend finalizer runs in the background and its own timeout is 30-45m,
	// at which point the cluster goes FAIL (still reserving its name) rather than
	// DELETED. The provider must wait past that window to observe the real terminal
	// verdict instead of giving up while teardown is still legitimately in progress.
	k8sDefaultDeleteTime  = 45 * time.Minute
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
	mu, ok := m.(*sync.Mutex)
	if !ok {
		// Unreachable: clusterLocks only ever stores *sync.Mutex.
		panic("clusterLocks held a non-*sync.Mutex value")
	}
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
		MarkdownDescription: "Manages a ProData Managed Kubernetes cluster's control plane. Cluster " +
			"creation is asynchronous; `terraform apply` blocks until the cluster reaches a usable " +
			"state (SUCCESS with a kubeconfig) or the create timeout elapses. Worker capacity is " +
			"managed as independent `prodata_kubernetes_node_pool` resources — a cluster with zero " +
			"node pools is a valid, control-plane-only steady state.",
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
			"node_ip_range": schema.StringAttribute{
				MarkdownDescription: "Control-plane IP range within the local network, as `start-end` " +
					"(e.g. `10.0.0.10-10.0.0.20`). Optional: when omitted, the platform auto-allocates a free " +
					"contiguous range from `network_id` (sized for the master and worker node capacity) and " +
					"reports it back here. When set explicitly, the value is used as-is. Changing it forces a " +
					"new resource.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
				Validators: []validator.String{
					stringvalidator.RegexMatches(
						regexp.MustCompile(`^(\d{1,3}\.){3}\d{1,3}-(\d{1,3}\.){3}\d{1,3}$`),
						"must be an IPv4 range as start-end, e.g. 10.0.0.10-10.0.0.20",
					),
				},
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
					"`prodata_kubernetes_flavors` data source. Mutually exclusive with " +
					"`control_plane_size` — set exactly one. When you set `control_plane_size` instead, " +
					"this is resolved and exported as a computed value. Changing it forces a new resource: " +
					"resizing the control plane in place is not yet supported, so a different master " +
					"flavor recreates the cluster.",
				Optional:   true,
				Computed:   true,
				Validators: []validator.Int64{int64validator.AtLeast(1)},
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"control_plane_size": schema.StringAttribute{
				MarkdownDescription: "Control-plane size class — `small`, `medium`, or `large` — a " +
					"convenience alias that selects the master flavor for you based on " +
					"`high_availability` (the provider maps the size onto the region's master-flavor " +
					"catalog by capacity). Mutually exclusive with `master_flavor_id` — set exactly one. " +
					"Changing it forces a new resource.",
				Optional: true,
				Validators: []validator.String{
					stringvalidator.OneOf("small", "medium", "large"),
				},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
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
				MarkdownDescription: "Lifecycle status: `NEW`, `PROCESSING`, `SUCCESS`, `FAIL`, `DELETING`, or " +
					"`DELETED`. `DELETING` is a lingering state while the cluster's asynchronous teardown runs.",
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

// ValidateConfig enforces that exactly one of master_flavor_id / control_plane_size
// is set. Validators are no-ops on unknown values.
func (r *K8sClusterResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg K8sClusterModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Exactly one of master_flavor_id / control_plane_size must be set.
	flavorSet := !cfg.MasterFlavorID.IsNull() && !cfg.MasterFlavorID.IsUnknown()
	sizeSet := !cfg.ControlPlaneSize.IsNull() && !cfg.ControlPlaneSize.IsUnknown()
	switch {
	case flavorSet && sizeSet:
		resp.Diagnostics.AddAttributeError(
			path.Root("control_plane_size"),
			"master_flavor_id and control_plane_size are mutually exclusive",
			"Set exactly one of master_flavor_id (an explicit flavor ID) or control_plane_size "+
				"(small/medium/large, resolved for you).",
		)
	case !flavorSet && !sizeSet:
		resp.Diagnostics.AddAttributeError(
			path.Root("control_plane_size"),
			"a control-plane size is required",
			"Set control_plane_size (small/medium/large) or master_flavor_id.",
		)
	}
}

// ModifyPlan: when the kubernetes_version changes in place, the backend rewrites
// the kubeconfig / api_endpoint and the cluster transits PROCESSING, so those
// computed values must be unknown in the plan (ADR-K3) — otherwise Terraform's
// "computed output must be consistent" check fails after apply.
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

	versionChanged := !plan.KubernetesVersion.Equal(state.KubernetesVersion)

	// ADR-K3: a version upgrade rolls the control plane and can rewrite the kubeconfig /
	// api_endpoint (and the credentials), and transits the cluster through
	// PROCESSING / blocked, so those and the volatile status/count fields must be
	// unknown in the plan — otherwise Terraform's "provider produced inconsistent
	// result after apply" check fails. kube_config is a nested object, blanked as a
	// whole ObjectUnknown. (A master-flavor change forces replacement, so the
	// framework handles it, not here.)
	if versionChanged {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("kube_config"), types.ObjectUnknown(kubeConfigAttrTypes()))...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("api_endpoint"), types.StringUnknown())...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("ssh_key_encoded"), types.StringUnknown())...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("private_key_encoded"), types.StringUnknown())...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("status"), types.StringUnknown())...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("blocked"), types.BoolUnknown())...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("node_pool_count"), types.Int64Unknown())...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("worker_node_count"), types.Int64Unknown())...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("master_node_count"), types.Int64Unknown())...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("ip_addresses_count"), types.Int64Unknown())...)
	}
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

	wire := client.CreateClusterRequest{
		ClusterName:  plan.Name.ValueString(),
		KuberVersion: plan.KubernetesVersion.ValueString(),
		NeedPublicIP: plan.PublicEndpointEnabled.ValueBool(),
		PublicKey:    plan.PublicKey.ValueString(),
		AuthorizeSSH: plan.SSHAccessEnabled.ValueBool(),
		PodSubnet:    plan.PodCIDR.ValueString(),
		LocalNetID:   plan.NetworkID.ValueInt64(),
		IsHA:         plan.HighAvailability.ValueBool(),
	}
	// node_ip_range is Optional+Computed: only pin it on the wire when the user set it.
	// When omitted (unknown/null in the plan) the backend auto-allocates a free range
	// and echoes it back, which Read reflects into state.
	if !plan.NodeIPRange.IsNull() && !plan.NodeIPRange.IsUnknown() {
		wire.Addresses = []string{plan.NodeIPRange.ValueString()}
	}

	// Resolve the master flavor from the region's HA-filtered catalog. Two paths:
	//   - control_plane_size set -> map small/medium/large onto a flavor by capacity rank;
	//   - master_flavor_id set   -> validate it is an available flavor for this region/HA.
	// The fetch is also a useful preflight: an invalid or cross-region master_flavor_id
	// otherwise fails as an opaque backend HTTP 500 from the create POST.
	haDesc := "standard"
	if plan.HighAvailability.ValueBool() {
		haDesc = "high-availability"
	}
	useSize := !plan.ControlPlaneSize.IsNull() && !plan.ControlPlaneSize.IsUnknown()
	flavors, ferr := r.c.GetMasterNodeConfigs(ctx, plan.HighAvailability.ValueBool(), opts)
	switch {
	case useSize:
		// control_plane_size needs the catalog to resolve — fail loudly if we can't read it.
		if ferr != nil {
			resp.Diagnostics.AddError(
				"Unable to resolve control_plane_size",
				fmt.Sprintf("Could not list %s master flavors for region %q to map control_plane_size: %s",
					haDesc, region, client.KuberErrorDetail(ferr)),
			)
			return
		}
		size := plan.ControlPlaneSize.ValueString()
		id, ok := client.FlavorIDBySize(flavors, size)
		if !ok {
			resp.Diagnostics.AddAttributeError(
				path.Root("control_plane_size"),
				"Cannot map control_plane_size to a master flavor",
				fmt.Sprintf("No %s master flavor matches control_plane_size %q in region %q. The "+
					"control-plane catalog must be a small/medium/large (3-tier) ladder for this to work; "+
					"set master_flavor_id explicitly instead.", haDesc, size, region),
			)
			return
		}
		wire.MasterNodeConfigID = id
	default:
		wantID := plan.MasterFlavorID.ValueInt64()
		wire.MasterNodeConfigID = wantID
		if ferr == nil { // best-effort preflight; transient read failures fall through to the POST
			found := false
			ids := make([]string, 0, len(flavors))
			for i := range flavors {
				ids = append(ids, strconv.FormatInt(flavors[i].ID, 10))
				if flavors[i].ID == wantID {
					found = true
				}
			}
			if !found {
				resp.Diagnostics.AddError(
					"Invalid master_flavor_id for this region",
					fmt.Sprintf("master_flavor_id %d is not an available %s master flavor in region %q. "+
						"Available ids: [%s]. Use the prodata_kubernetes_flavors data source (with the same "+
						"high_availability value) to select a valid flavor, or set control_plane_size.",
						wantID, haDesc, region, strings.Join(ids, ", ")),
				)
				return
			}
		}
	}

	// ADR-K6: adopt-or-error. If a cluster with this name already exists in the
	// scope, a lost create response (e.g. a 429 on read-back) must not orphan it.
	// The message branches on the found cluster's status: a DELETING or FAILED
	// same-name cluster still holds the name but must not be imported (see
	// adoptConflictDiag), whereas a live one can be adopted via terraform import.
	existing, adoptErr := r.findClusterByName(ctx, plan.Name.ValueString(), opts)
	if adoptErr != nil {
		resp.Diagnostics.AddError("Unable to verify cluster name availability", client.KuberErrorDetail(adoptErr))
		return
	}
	if existing != nil {
		summary, detail := adoptConflictDiag(existing)
		resp.Diagnostics.AddError(summary, detail)
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
		detail := client.KuberErrorDetail(err)
		// The backend returns a generic HTTP 500 ("Could not create kubernetes cluster")
		// when it cannot provision the control plane. With the master flavor preflighted
		// and the version/network/addresses validated above, this is a server-side
		// provisioning failure (typically the region lacks Managed-Kubernetes capacity),
		// not a configuration error — say so, so it isn't read as a provider bug.
		if client.IsAPIError(err, 500) {
			detail += "\n\nThis is a server-side failure to provision the cluster, not a configuration problem " +
				"(the master flavor, version, network and node IP range were validated before this call). " +
				"The target region may not currently have Managed Kubernetes capacity/availability — retry " +
				"later or contact ProData support if it persists."
		}
		resp.Diagnostics.AddError("Unable to create Kubernetes cluster", detail)
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

	r.applyServerState(ctx, &plan, result, region, projectTag)
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
	// A soft-deleted cluster reads DELETED forever (there is no 404), so treat that
	// as "gone" and drop it from state. DELETING is deliberately NOT handled here:
	// the cluster still exists while its async teardown runs, so it must stay in
	// state — it falls through to applyServerState below and keeps its DELETING
	// status visible until it finally reads DELETED.
	if cl.Status == client.ClusterStatusDeleted {
		tflog.Warn(ctx, "Cluster reported DELETED, removing from state", map[string]any{"id": id})
		resp.State.RemoveResource(ctx)
		return
	}

	region := valueOrDefault(data.Region, r.c.Region)
	projectTag := valueOrDefault(data.ProjectTag, r.c.ProjectTag)

	r.applyServerState(ctx, &data, cl, region, projectTag)
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

	// Kubernetes version upgrade (in-place, async).
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
	r.applyServerState(ctx, &plan, final, region, projectTag)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
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
			switch classifyDeletePoll(cl.Status) {
			case deletePollDone:
				// DELETED — teardown finished; the destroy succeeded.
				return
			case deletePollFailed:
				// The backend put the cluster in FAIL (e.g. its teardown finalizer
				// timed out). It is not gone and still reserves its name, so leave it
				// in state (do NOT RemoveResource) and surface the failure.
				resp.Diagnostics.AddError(
					"Kubernetes cluster teardown failed",
					fmt.Sprintf("Kubernetes cluster %d teardown failed — it is in FAILED state and still "+
						"reserves its name. Re-run `terraform destroy` to retry, or resolve it in the panel.", id),
				)
				return
			case deletePollPending:
				// DELETING (or a still-transitional status) — teardown is in progress;
				// keep polling until DELETED, FAIL, or the delete timeout.
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

// findClusterByName returns the non-DELETED cluster with the given name in the
// resolved scope, or nil if none exists. Used for create-time adopt-or-error, which
// branches on the returned cluster's Status (a DELETING or FAILED same-name cluster
// is reported differently from a live collision — see adoptConflictDiag).
func (r *K8sClusterResource) findClusterByName(ctx context.Context, name string, opts *client.RequestOpts) (*client.Cluster, error) {
	clusters, err := r.c.ListClusters(ctx, opts)
	if err != nil {
		return nil, err
	}
	want := strings.ToLower(name)
	for i := range clusters {
		if strings.ToLower(clusters[i].Name) == want && clusters[i].Status != client.ClusterStatusDeleted {
			return &clusters[i], nil
		}
	}
	return nil, nil
}

// adoptConflictDiag builds the (summary, detail) diagnostic for a same-named cluster
// found at create time, branching on its lifecycle status. A DELETING cluster's name
// frees up once teardown finishes, and a FAILED cluster keeps reserving its name
// until it is deleted — neither should be adopted via terraform import, so those get
// tailored guidance; any other (live) status is a genuine collision the user can
// import instead.
func adoptConflictDiag(existing *client.Cluster) (summary, detail string) {
	switch existing.Status {
	case client.ClusterStatusDeleting:
		return "A cluster with this name is still being deleted",
			fmt.Sprintf("a cluster named %q is still being deleted; wait for teardown to finish, then retry", existing.Name)
	case client.ClusterStatusFail:
		return "A cluster with this name exists in FAILED state",
			fmt.Sprintf("a cluster named %q exists in FAILED state; delete it before recreating", existing.Name)
	default:
		return "A cluster with this name already exists",
			fmt.Sprintf("Cluster %q already exists (id %d) in this region/project. Import it "+
				"(terraform import) or choose a different name.", existing.Name, existing.ID)
	}
}

// deletePollOutcome classifies one delete-time observation of a cluster's status
// (see classifyDeletePoll).
type deletePollOutcome int

const (
	// deletePollPending means teardown is still in progress — including the dedicated
	// DELETING state — so the caller keeps polling.
	deletePollPending deletePollOutcome = iota
	// deletePollDone means the cluster reads DELETED: teardown finished and the
	// destroy succeeded.
	deletePollDone
	// deletePollFailed means the cluster reads FAIL: teardown failed (e.g. the backend
	// finalizer timed out). The cluster still reserves its name and must be left in state.
	deletePollFailed
)

// classifyDeletePoll maps a cluster status observed while waiting out a delete to a
// deletePollOutcome. DELETING is the lingering async-teardown state and stays
// pending; only DELETED is success and only FAIL is a failure.
func classifyDeletePoll(status string) deletePollOutcome {
	switch status {
	case client.ClusterStatusDeleted:
		return deletePollDone
	case client.ClusterStatusFail:
		return deletePollFailed
	default:
		// NEW / PROCESSING / SUCCESS / DELETING — teardown not finished; keep polling.
		return deletePollPending
	}
}

// clusterUpgradeConverged reports whether an in-place version upgrade has settled:
// the cluster is SUCCESS, reports the requested version, and has no operation still
// in flight. It guards waitForClusterReady's upgrade mode against returning on the
// stale pre-upgrade SUCCESS snapshot before the control plane has rolled.
func clusterUpgradeConverged(cl *client.Cluster, wantVersion string) bool {
	return cl.Status == client.ClusterStatusSuccess && cl.KubeVersion == wantVersion && !cl.Blocked
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

// applyServerState writes server-owned fields from a Cluster onto the model.
// Write-once / RequiresReplace inputs the API does not echo back (public_key,
// ssh_access_enabled) are preserved from the existing model rather than
// overwritten. node_ip_range IS echoed (the backend may auto-allocate it), so it
// is read back here like any server-known value.
func (r *K8sClusterResource) applyServerState(ctx context.Context, m *K8sClusterModel, cl *client.Cluster, region, projectTag string) {
	m.ID = types.Int64Value(cl.ID)
	m.Region = types.StringValue(region)
	m.ProjectTag = types.StringValue(projectTag)
	m.Name = types.StringValue(cl.Name)
	m.KubernetesVersion = types.StringValue(cl.KubeVersion)
	m.HighAvailability = types.BoolValue(cl.IsHA)
	m.PublicEndpointEnabled = types.BoolValue(cl.IsPublic)
	m.PodCIDR = types.StringValue(cl.PodSubnet)
	// node_ip_range is now echoed by the API (the backend may auto-allocate it), so it
	// is read back like any server-known value rather than preserved write-once.
	m.NodeIPRange = types.StringValue(cl.NodeIPRange)

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
