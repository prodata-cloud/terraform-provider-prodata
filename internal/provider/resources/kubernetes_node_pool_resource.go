package resources

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource                   = &K8sNodePoolResource{}
	_ resource.ResourceWithConfigure      = &K8sNodePoolResource{}
	_ resource.ResourceWithModifyPlan     = &K8sNodePoolResource{}
	_ resource.ResourceWithImportState    = &K8sNodePoolResource{}
	_ resource.ResourceWithValidateConfig = &K8sNodePoolResource{}
)

// K8sNodePoolResource implements prodata_kubernetes_node_pool — an additional
// worker node pool attached to a prodata_kubernetes_cluster. The cluster owns its
// control plane; every worker pool, including the first, uses this resource.
type K8sNodePoolResource struct {
	c                     *client.Client
	lifecyclePollInterval time.Duration
}

// K8sNodePoolModel mirrors the prodata_kubernetes_node_pool schema. It carries the
// pool's sizing/scaling fields plus the scope (region/project_tag) and parent
// (cluster_id) needed to address a pool on its own. Autoscaling reuses the shared
// K8sAutoscalingModel: its mere presence means "autoscaling enabled".
type K8sNodePoolModel struct {
	ID                  types.Int64          `tfsdk:"id"`
	RequestUUID         types.String         `tfsdk:"request_uuid"`
	DeletionRequestUUID types.String         `tfsdk:"deletion_request_uuid"`
	ClusterID           types.Int64          `tfsdk:"cluster_id"`
	Region              types.String         `tfsdk:"region"`
	ProjectTag          types.String         `tfsdk:"project_tag"`
	Name                types.String         `tfsdk:"name"`
	VCPU                types.Int64          `tfsdk:"vcpu"`
	RAM                 types.Int64          `tfsdk:"ram"`
	DiskSize            types.Int64          `tfsdk:"disk_size"`
	NodeCount           types.Int64          `tfsdk:"node_count"`
	Autoscaling         *K8sAutoscalingModel `tfsdk:"autoscaling"`

	// Computed, server-owned.
	Status     types.String   `tfsdk:"status"`
	NodeSubnet types.Int64    `tfsdk:"node_subnet"`
	Timeouts   timeouts.Value `tfsdk:"timeouts"`
}

// Pool-specific timeouts. Pool operations are minutes, not the hours a whole
// cluster build can take (ADR-K5 gives cluster timeouts; pools are explicitly
// "minutes instead of hours"), so the defaults are far shorter than the cluster's.
const (
	k8sPoolDefaultCreateTime = 30 * time.Minute
	k8sPoolDefaultUpdateTime = 30 * time.Minute
	k8sPoolDefaultDeleteTime = 10 * time.Minute
)

func NewK8sNodePoolResource() resource.Resource {
	return &K8sNodePoolResource{}
}

func (r *K8sNodePoolResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_node_pool"
}

func (r *K8sNodePoolResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a worker node pool on a ProData Managed Kubernetes cluster. " +
			"The cluster is control-plane-only; use this resource for every worker pool, including the first. " +
			"Pool creation and scaling are asynchronous; " +
			"`terraform apply` blocks until the pool converges or the timeout elapses.",
		Attributes: map[string]schema.Attribute{
			"request_uuid":          schema.StringAttribute{Computed: true, MarkdownDescription: "Durable creation request UUID, retained when the canonical pool ID has not yet been confirmed.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"deletion_request_uuid": schema.StringAttribute{Computed: true, MarkdownDescription: "Durable deletion request UUID while an identity-bound destroy attempt is pending.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"id": schema.Int64Attribute{
				MarkdownDescription: "Canonical node pool ID, committed by the panel before physical submission.",
				Computed:            true,
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"cluster_id": schema.Int64Attribute{
				MarkdownDescription: "ID of the cluster this pool belongs to. Changing it forces a new resource " +
					"(a pool cannot be moved between clusters).",
				Required:      true,
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()},
				Validators:    []validator.Int64{int64validator.AtLeast(1)},
			},
			"region": schema.StringAttribute{
				MarkdownDescription: "Region ID. If omitted, uses the provider default. Must match the cluster's " +
					"region. Changing this forces a new resource.",
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
				MarkdownDescription: "Pool name. 3-24 characters, lowercase letters / digits / hyphens, must not " +
					"start or end with a hyphen. Must be unique within the cluster (the backend enforces this). " +
					"Changing it forces a new resource.",
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
			"vcpu": schema.Int64Attribute{
				MarkdownDescription: "vCPUs per worker node. Updated by rolling the workers while preserving the pool ID.",
				Required:            true,
				Validators:          []validator.Int64{int64validator.AtLeast(1)},
			},
			"ram": schema.Int64Attribute{
				MarkdownDescription: "RAM per worker node, in GB. Updated by rolling the workers while preserving the pool ID.",
				Required:            true,
				Validators:          []validator.Int64{int64validator.AtLeast(1)},
			},
			"disk_size": schema.Int64Attribute{
				MarkdownDescription: "Disk size per worker node, in GB. Increases roll the workers while preserving the pool ID. Shrinking is not supported.",
				Required:            true,
				Validators:          []validator.Int64{int64validator.AtLeast(1)},
			},
			"node_count": schema.Int64Attribute{
				MarkdownDescription: "Number of worker nodes. Updated in place. Must be omitted when `autoscaling` " +
					"is set (the autoscaler owns the count); the live value is then exported as a computed attribute.",
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
				Validators:    []validator.Int64{int64validator.AtLeast(1)},
			},
			"autoscaling": schema.SingleNestedAttribute{
				MarkdownDescription: "Enable cluster-autoscaler for this pool. Presence enables it; omit the block " +
					"for a fixed-size pool. Mutually exclusive with `node_count`.",
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

			// ---- computed, server-owned ----
			"status": schema.StringAttribute{
				MarkdownDescription: "Lifecycle status. Creation and deletion keep their durable operation identity while pending or unverified; `SUCCESS` means converged.",
				Computed:            true,
			},
			"node_subnet": schema.Int64Attribute{
				MarkdownDescription: "Node subnet prefix length assigned to the pool by the backend.",
				Computed:            true,
				// The subnet is fixed at pool creation; pin it across in-place updates so a
				// scale/autoscale change does not render a spurious "(known after apply)".
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

func (r *K8sNodePoolResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
// cannot both be set (the autoscaler owns the count), and exactly one must be
// present. Terraform validates the configuration before it plans, with variables and
// data sources still unknown, so a value that is not known yet is neither set nor
// unset: it is waited out here and checked once Terraform validates with the real one.
func (r *K8sNodePoolResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	// The two attributes are read on their own: a whole-model Get cannot place an
	// autoscaling block that is unknown as a whole (autoscaling = var.<object>, or a
	// conditional) into the *K8sAutoscalingModel field and fails with a conversion error.
	var nodeCount types.Int64
	var autoscaling types.Object
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("node_count"), &nodeCount)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("autoscaling"), &autoscaling)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// An unknown block may turn out absent or present, and either answer decides which of
	// the checks below applies — none of them can run yet.
	if autoscaling.IsUnknown() {
		return
	}
	autoscalingSet := !autoscaling.IsNull()
	nodeCountSet := !nodeCount.IsNull() && !nodeCount.IsUnknown()
	if autoscalingSet && nodeCountSet {
		resp.Diagnostics.AddAttributeError(
			path.Root("node_count"),
			"node_count conflicts with autoscaling",
			"When autoscaling is set, the autoscaler owns the node count. "+
				"Remove node_count (its live value is exported as a computed attribute).",
		)
	}
	// Only flag a concretely-absent node_count: an unknown (interpolated) value may
	// resolve to a real count at apply time, so ValidateConfig must stay a no-op on it.
	if !autoscalingSet && nodeCount.IsNull() {
		resp.Diagnostics.AddAttributeError(
			path.Root("node_count"),
			"node_count is required without autoscaling",
			"Set node_count for a fixed-size pool, or add an autoscaling block.",
		)
	}
	if autoscalingSet {
		var bounds K8sAutoscalingModel
		resp.Diagnostics.Append(autoscaling.As(ctx, &bounds, basetypes.ObjectAsOptions{})...)
		if resp.Diagnostics.HasError() {
			return
		}
		if !bounds.MinNodes.IsUnknown() && !bounds.MaxNodes.IsUnknown() && bounds.MinNodes.ValueInt64() > bounds.MaxNodes.ValueInt64() {
			resp.Diagnostics.AddAttributeError(
				path.Root("autoscaling").AtName("max_nodes"),
				"Invalid autoscaling bounds",
				"max_nodes must be greater than or equal to min_nodes.",
			)
		}
	}
}

// ModifyPlan marks the volatile computed fields unknown when an in-place change
// (node_count or autoscaling) rolls the pool through PROCESSING, so Terraform's
// "computed output must be consistent" check does not fail after apply (ADR-K3).
// A standalone pool needs none of the cluster's kubeconfig/credential blanking,
// Durable operation evidence is handled independently of volatile field planning.
func (r *K8sNodePoolResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	// Destroy plan — nothing to do.
	if req.Plan.Raw.IsNull() {
		return
	}
	// Create plan — no prior state to diff.
	if req.State.Raw.IsNull() {
		return
	}

	// An autoscaling block that is unknown as a whole (a conditional on a value known only
	// after apply) cannot be decoded into the pointer-struct model below. Whether the pool is
	// scaled by the autoscaler after this apply is not known either, so keep both volatile
	// fields open: status always, node_count too unless the configuration pins it.
	var planAutoscaling types.Object
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("autoscaling"), &planAutoscaling)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if planAutoscaling.IsUnknown() {
		var cfgNodeCount types.Int64
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("node_count"), &cfgNodeCount)...)
		if resp.Diagnostics.HasError() {
			return
		}
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("status"), types.StringUnknown())...)
		// A node_count written in the configuration is planned as exactly that value;
		// only the one the provider fills in (UseStateForUnknown) may be reopened.
		if cfgNodeCount.IsNull() {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("node_count"), types.Int64Unknown())...)
		}
		return
	}

	var state, plan K8sNodePoolModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if !plan.DiskSize.IsUnknown() && !state.DiskSize.IsNull() && plan.DiskSize.ValueInt64() < state.DiskSize.ValueInt64() {
		resp.Diagnostics.AddAttributeError(path.Root("disk_size"), "Worker disk shrinking is not supported", "Increase disk_size, or create a separate pool and migrate workloads explicitly.")
		return
	}
	if nodePoolHardwareChanged(&state, &plan) {
		resp.Diagnostics.AddWarning("Worker machines will be replaced gradually", "The node pool keeps its ID. Worker machines are drained and replaced one at a time. A pool with one worker has no available worker during its replacement; arrange application capacity before applying. Local ephemeral data on their boot disks is lost. Application data must use storage with suitable retention. The update requires a compatible panel API and Kubernetes service.")
	}
	if !nodePoolChanged(&state, &plan) && !nodePoolHardwareChanged(&state, &plan) {
		return
	}

	// The scale/autoscale change transits the pool through PROCESSING, so its status
	// must be unknown in the plan.
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("status"), types.StringUnknown())...)

	// When the pool is autoscaling after this change, the autoscaler owns the live
	// node_count — it is server-chosen, so it must be unknown in the plan
	// (UseStateForUnknown would otherwise pin the stale prior value and trip the
	// inconsistent-result check when the autoscaler rebalances). ADR-K4.
	if plan.Autoscaling != nil {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("node_count"), types.Int64Unknown())...)
	}
}

// nodePoolChanged reports whether the in-place-updatable fields of a pool
// (node_count, autoscaling presence/bounds) differ between state and plan.
func nodePoolChanged(state, plan *K8sNodePoolModel) bool {
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

func (r *K8sNodePoolResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	r.createDurablePool(ctx, req, resp)
}

// ---- Read ----

func (r *K8sNodePoolResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data K8sNodePoolModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.State = req.State
	opts := r.optsFromState(data.Region, data.ProjectTag)
	if r.readPoolOperationState(ctx, &data, opts, resp) {
		return
	}
	if data.ID.ValueInt64() < 1 {
		resp.Diagnostics.AddWarning("Node pool identity remains unverified", "The request UUID has no confirmed canonical pool ID; state is retained.")
		return
	}
	id := data.ID.ValueInt64()

	pool, err := r.c.GetNodePool(ctx, id, opts)
	if err != nil {
		nodePoolReadFailure(resp, "Node pool readback unverified", err)
		return
	}
	if pool.ID != id || pool.ClusterID != data.ClusterID.ValueInt64() {
		resp.Diagnostics.AddWarning("Node pool identity unverified", "Canonical readback returned another pool or cluster; state is retained.")
		return
	}
	if pool.Status == client.ClusterStatusDeleted {
		resp.Diagnostics.AddWarning("Node pool absence unverified", "A legacy DELETED row is not UID-bound physical absence proof; existing identity is retained.")
		return
	}

	region := valueOrDefault(data.Region, r.c.Region)
	projectTag := valueOrDefault(data.ProjectTag, r.c.ProjectTag)
	previous := data
	r.applyServerState(&data, pool, region, projectTag, true)
	if hardware, err := r.c.NodePoolHardware(ctx, id, opts); err == nil {
		data.VCPU = types.Int64Value(hardware.VCPU)
		data.RAM = types.Int64Value(hardware.RAM)
		data.DiskSize = types.Int64Value(hardware.DiskSize)
		if !*hardware.Ready && pool.Status != client.ClusterStatusFail && pool.Status != client.ClusterStatusDeleting {
			data.Status = types.StringValue("PROCESSING")
		}
	} else {
		// A failed CAPI read does not revoke ownership or prevent deleting a broken pool.
		// Retain previously verified sizing rather than publishing the panel's desired sizing.
		if !previous.VCPU.IsNull() && !previous.VCPU.IsUnknown() {
			data.VCPU = previous.VCPU
		}
		if !previous.RAM.IsNull() && !previous.RAM.IsUnknown() {
			data.RAM = previous.RAM
		}
		if !previous.DiskSize.IsNull() && !previous.DiskSize.IsUnknown() {
			data.DiskSize = previous.DiskSize
		}
		resp.Diagnostics.AddWarning("Worker hardware could not be verified", err.Error()+". The pool identity and prior sizing are retained; deletion remains available. Hardware updates require the matching panel and Kubernetes API.")
	}
	if pool.CreationRequestUUID != "" {
		if data.RequestUUID.ValueString() != "" && data.RequestUUID.ValueString() != pool.CreationRequestUUID {
			resp.Diagnostics.AddWarning("Node pool creation identity changed", "The prior operation UUID is retained; the API advertised another creation association.")
			return
		}
		if data.RequestUUID.ValueString() == "" {
			data.RequestUUID = types.StringValue(pool.CreationRequestUUID)
			if r.readPoolOperationState(ctx, &data, opts, resp) {
				return
			}
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// ---- Update ----

func (r *K8sNodePoolResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state, plan K8sNodePoolModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	plan.RequestUUID = state.RequestUUID
	plan.DeletionRequestUUID = state.DeletionRequestUUID
	opts := r.optsFromState(state.Region, state.ProjectTag)
	clusterID := state.ClusterID.ValueInt64()
	poolID := state.ID.ValueInt64()

	updateTimeout, diags := plan.Timeouts.Update(ctx, k8sPoolDefaultUpdateTime)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, updateTimeout)
	defer cancel()

	// ADR-K7: serialize per-cluster mutations and refuse to act on a FAILed or
	// in-flight (blocked) cluster.
	unlock := lockCluster(clusterID)
	defer unlock()
	if err := r.ensureClusterMutable(ctx, clusterID, opts); err != nil {
		resp.Diagnostics.AddError("Cluster is not in a modifiable state", client.KuberErrorDetail(err))
		return
	}

	if nodePoolHardwareChanged(&state, &plan) {
		if err := r.resizePoolHardware(ctx, poolID, &state, &plan, opts); err != nil {
			resp.Diagnostics.AddError("Unable to update worker hardware", err.Error())
			return
		}
	}

	changed, err := r.reconcilePool(ctx, clusterID, poolID, &state, &plan, opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to update node pool", client.KuberErrorDetail(err))
		return
	}
	var converged *client.NodePool
	if changed {
		c, waitErr := r.waitForPoolReady(ctx, poolID, &plan, opts)
		if waitErr != nil {
			resp.Diagnostics.AddError("Node pool did not stabilize",
				fmt.Sprintf("node pool %d: %s", poolID, waitErr.Error()))
			return
		}
		converged = c
	}

	region := valueOrDefault(state.Region, r.c.Region)
	projectTag := valueOrDefault(state.ProjectTag, r.c.ProjectTag)

	// Refresh the computed fields from a concrete snapshot so nothing ModifyPlan
	// blanked to unknown (status, autoscaler-owned node_count) survives into state.
	// Prefer a fresh read, then the converged snapshot; if both are unavailable
	// (a transient read failure on an otherwise unchanged update), carry the prior
	// values forward rather than persisting unknowns.
	final := converged
	if fresh, ferr := r.c.GetNodePool(ctx, poolID, opts); ferr == nil && fresh != nil {
		final = fresh
	}
	if final != nil {
		r.applyServerState(&plan, final, region, projectTag, false)
	} else {
		plan.Status = state.Status
		plan.NodeSubnet = state.NodeSubnet
		plan.NodeCount = state.NodeCount
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func nodePoolHardwareChanged(state, plan *K8sNodePoolModel) bool {
	return !state.VCPU.Equal(plan.VCPU) || !state.RAM.Equal(plan.RAM) || !state.DiskSize.Equal(plan.DiskSize)
}

func (r *K8sNodePoolResource) resizePoolHardware(ctx context.Context, id int64, state, plan *K8sNodePoolModel, opts *client.RequestOpts) error {
	if plan.VCPU.IsUnknown() || plan.RAM.IsUnknown() || plan.DiskSize.IsUnknown() || plan.VCPU.ValueInt64() < 1 || plan.RAM.ValueInt64() < 1 {
		return fmt.Errorf("worker hardware must be known and positive before updating")
	}
	if plan.DiskSize.ValueInt64() < state.DiskSize.ValueInt64() {
		return fmt.Errorf("shrinking worker disks is not supported")
	}
	order := client.NodePoolHardwareRequest{VCPU: plan.VCPU.ValueInt64(), RAM: plan.RAM.ValueInt64(), DiskSize: plan.DiskSize.ValueInt64()}
	view, err := r.c.ResizeNodePoolHardware(ctx, id, order, opts)
	if err != nil {
		return err
	}
	for {
		if view.VCPU != order.VCPU || view.RAM != order.RAM || view.DiskSize != order.DiskSize {
			return fmt.Errorf("worker hardware readback does not match the requested configuration; refresh and retry")
		}
		if *view.Ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(k8sPollInterval):
		}
		view, err = r.c.NodePoolHardware(ctx, id, opts)
		if err != nil {
			return err
		}
	}
}

// reconcilePool applies node_count / autoscaling changes, choosing the right
// endpoint for each transition (ADR-K4). It returns whether a mutating call was
// issued (so the caller waits for the pool to settle).
func (r *K8sNodePoolResource) reconcilePool(ctx context.Context, clusterID, poolID int64, state, plan *K8sNodePoolModel, opts *client.RequestOpts) (bool, error) {
	if !nodePoolChanged(state, plan) {
		return false, nil
	}
	if poolID == 0 {
		return false, fmt.Errorf("the node pool id for cluster %d is unknown; run `terraform refresh` and try again", clusterID)
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

func (r *K8sNodePoolResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	r.deleteDurablePool(ctx, req, resp)
}

// ---- ImportState ----

func (r *K8sNodePoolResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	clusterID, poolID, region, projectTag, err := parseK8sPoolImportID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected `{cluster_id}/{pool_id}` or `{region}/{cluster_id}/{pool_id}@{project_tag}`, got: %q\n\n"+
				"Examples:\n  terraform import prodata_kubernetes_node_pool.example 42/7\n"+
				"  terraform import prodata_kubernetes_node_pool.example UZ-5/42/7@my-project", req.ID),
		)
		return
	}
	if region == "" {
		region = r.c.Region
	}
	if projectTag == "" {
		projectTag = r.c.ProjectTag
	}
	tflog.Info(ctx, "Importing Kubernetes node pool", map[string]any{
		"cluster_id": clusterID, "id": poolID, "region": region, "project_tag": projectTag,
	})
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), poolID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cluster_id"), clusterID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("region"), region)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_tag"), projectTag)...)
}

// ---- helpers ----

func (r *K8sNodePoolResource) resolveScope(region, projectTag types.String) (string, string) {
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

func (r *K8sNodePoolResource) optsFromState(region, projectTag types.String) *client.RequestOpts {
	opts := &client.RequestOpts{}
	if !region.IsNull() && !region.IsUnknown() && region.ValueString() != "" {
		opts.Region = region.ValueString()
	}
	if !projectTag.IsNull() && !projectTag.IsUnknown() && projectTag.ValueString() != "" {
		opts.ProjectTag = projectTag.ValueString()
	}
	return opts
}

// ensureClusterMutable verifies the parent cluster can be mutated (ADR-K7): it
// refuses to mutate a FAILed cluster and waits out an in-flight (blocked) operation
// until the cluster is unblocked or the context deadline hits. Mirrors the cluster
// resource's ensureMutable; a pool op must wait on its parent cluster's state.
func (r *K8sNodePoolResource) ensureClusterMutable(ctx context.Context, clusterID int64, opts *client.RequestOpts) error {
	for {
		cl, err := r.c.GetCluster(ctx, clusterID, opts)
		if err != nil {
			return err
		}
		if cl.Status == client.ClusterStatusFail {
			return fmt.Errorf("cluster %d is in FAIL state and cannot be modified; inspect it in the panel and recreate", clusterID)
		}
		if !cl.Blocked {
			return nil
		}
		tflog.Debug(ctx, "Cluster is blocked by an in-flight operation, waiting", map[string]any{"id": clusterID})
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for cluster %d to become unblocked: %w", clusterID, ctx.Err())
		case <-time.After(k8sPollInterval):
		}
	}
}

// waitForPoolReady polls a node pool until it has CONVERGED to the requested shape:
// status SUCCESS and its live fields match `want`. Pools rest at SUCCESS and the
// mutating calls are async, so a plain "status == SUCCESS" check can return on the
// stale pre-mutation snapshot; matching the requested fields makes the wait
// edge-correct. Pools never reach FAIL/DELETED, so there is no terminal-error
// branch (a vanished pool is reported as not-found). ADR-K5.
func (r *K8sNodePoolResource) waitForPoolReady(ctx context.Context, poolID int64, want *K8sNodePoolModel, opts *client.RequestOpts) (*client.NodePool, error) {
	var consecutiveErrs int
	var last *client.NodePool
	for {
		pool, err := r.c.GetNodePool(ctx, poolID, opts)
		switch {
		case err == nil:
			consecutiveErrs = 0
			last = pool
			tflog.Debug(ctx, "Polling node pool", map[string]any{"id": poolID, "status": pool.Status})
			if pool.Status == client.ClusterStatusSuccess && nodePoolMatchesDesired(pool, want) {
				return pool, nil
			}
		case client.IsKuberNotFound(err):
			return last, fmt.Errorf("node pool %d disappeared while waiting", poolID)
		default:
			consecutiveErrs++
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

// nodePoolMatchesDesired reports whether a live pool reflects the requested shape.
// For an autoscaling pool it checks the flag and bounds (the live node_count is
// autoscaler-owned and not asserted); for a fixed pool it checks the flag is off and
// node_count equals the request (when known).
func nodePoolMatchesDesired(pool *client.NodePool, want *K8sNodePoolModel) bool {
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

// applyServerState writes server-owned fields from a NodePool onto the model. The
// immutable RequiresReplace inputs (name, vcpu, ram, disk_size, cluster_id) are
// populated from the server only on Read after an import (when the model field is
// still null); on a normal Read/Create/Update they are preserved from the existing
// model rather than overwritten, since the API does not guarantee the read-back unit
// of `ram` matches the create input (GB vs MB is unverified — see step 1.5).
func (r *K8sNodePoolResource) applyServerState(m *K8sNodePoolModel, pool *client.NodePool, region, projectTag string, fromRead bool) {
	m.ID = types.Int64Value(pool.ID)
	m.Region = types.StringValue(region)
	m.ProjectTag = types.StringValue(projectTag)

	if fromRead {
		if m.ClusterID.IsNull() || m.ClusterID.IsUnknown() {
			m.ClusterID = types.Int64Value(pool.ClusterID)
		}
		if m.Name.IsNull() || m.Name.IsUnknown() {
			m.Name = types.StringValue(pool.Name)
		}
		if m.VCPU.IsNull() || m.VCPU.IsUnknown() {
			m.VCPU = types.Int64Value(int64(pool.CPU))
		}
		if m.RAM.IsNull() || m.RAM.IsUnknown() {
			m.RAM = types.Int64Value(int64(pool.RAM))
		}
		if m.DiskSize.IsNull() || m.DiskSize.IsUnknown() {
			m.DiskSize = types.Int64Value(int64(pool.SSD))
		}
	}

	m.NodeCount = types.Int64Value(int64(pool.NodeCount))
	m.Status = types.StringValue(pool.Status)
	m.NodeSubnet = types.Int64Value(int64(pool.NodeSubnet))
	if pool.AutoscaleEnabled {
		m.Autoscaling = &K8sAutoscalingModel{
			MinNodes: types.Int64Value(int64(pool.MinNodes)),
			MaxNodes: types.Int64Value(int64(pool.MaxNodes)),
		}
	} else {
		m.Autoscaling = nil
	}
}

// parseK8sPoolImportID accepts the composite "{cluster_id}/{pool_id}" form or the
// scoped "{region}/{cluster_id}/{pool_id}@{project_tag}" form (ADR-K6).
// Region/project_tag are empty for the bare form so the caller applies provider
// defaults.
func parseK8sPoolImportID(s string) (clusterID, poolID int64, region, projectTag string, err error) {
	if at := strings.LastIndexByte(s, '@'); at >= 0 {
		if at == 0 || at >= len(s)-1 {
			return 0, 0, "", "", fmt.Errorf("malformed composite import id %q", s)
		}
		projectTag = s[at+1:]
		parts := strings.SplitN(s[:at], "/", 3)
		if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
			return 0, 0, "", "", fmt.Errorf("malformed composite import id %q", s)
		}
		region = parts[0]
		if clusterID, err = strconv.ParseInt(parts[1], 10, 64); err != nil {
			return 0, 0, "", "", fmt.Errorf("cluster_id segment %q is not an integer", parts[1])
		}
		if poolID, err = strconv.ParseInt(parts[2], 10, 64); err != nil {
			return 0, 0, "", "", fmt.Errorf("pool_id segment %q is not an integer", parts[2])
		}
		return clusterID, poolID, region, projectTag, nil
	}

	parts := strings.Split(s, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return 0, 0, "", "", fmt.Errorf("expected {cluster_id}/{pool_id}, got %q", s)
	}
	if clusterID, err = strconv.ParseInt(parts[0], 10, 64); err != nil {
		return 0, 0, "", "", fmt.Errorf("cluster_id segment %q is not an integer", parts[0])
	}
	if poolID, err = strconv.ParseInt(parts[1], 10, 64); err != nil {
		return 0, 0, "", "", fmt.Errorf("pool_id segment %q is not an integer", parts[1])
	}
	return clusterID, poolID, "", "", nil
}
