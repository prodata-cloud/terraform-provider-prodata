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
// worker node pool attached to a prodata_kubernetes_cluster. The cluster's inline
// default pool is owned by prodata_kubernetes_cluster, not this resource.
type K8sNodePoolResource struct {
	c *client.Client
}

// K8sNodePoolModel mirrors the prodata_kubernetes_node_pool schema. It is the
// standalone analogue of the cluster resource's inline K8sDefaultPoolModel, plus
// the scope (region/project_tag) and parent (cluster_id) needed to address a pool
// on its own. Autoscaling reuses the shared K8sAutoscalingModel: its mere presence
// means "autoscaling enabled".
type K8sNodePoolModel struct {
	ID          types.Int64          `tfsdk:"id"`
	ClusterID   types.Int64          `tfsdk:"cluster_id"`
	Region      types.String         `tfsdk:"region"`
	ProjectTag  types.String         `tfsdk:"project_tag"`
	Name        types.String         `tfsdk:"name"`
	VCPU        types.Int64          `tfsdk:"vcpu"`
	RAM         types.Int64          `tfsdk:"ram"`
	DiskSize    types.Int64          `tfsdk:"disk_size"`
	NodeCount   types.Int64          `tfsdk:"node_count"`
	Autoscaling *K8sAutoscalingModel `tfsdk:"autoscaling"`

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

	// k8sPoolDiscoveryAttempts bounds how many times Create re-lists the cluster's
	// pools to find the id of the just-created pool. createNewNodePool returns no
	// id (ADR-K6), so the id is recovered via an id-set diff; the row is persisted
	// synchronously by the backend, so it is normally found on the first attempt.
	k8sPoolDiscoveryAttempts = 5
)

func NewK8sNodePoolResource() resource.Resource {
	return &K8sNodePoolResource{}
}

func (r *K8sNodePoolResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_node_pool"
}

func (r *K8sNodePoolResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an additional worker node pool on a ProData Managed Kubernetes cluster. " +
			"The cluster's first (default) worker pool is managed inline by the `prodata_kubernetes_cluster` " +
			"resource; use this resource for every pool beyond it. Pool creation and scaling are asynchronous; " +
			"`terraform apply` blocks until the pool converges or the timeout elapses.",
		Attributes: map[string]schema.Attribute{
			"id": schema.Int64Attribute{
				MarkdownDescription: "Node pool ID, assigned by the panel and discovered after creation.",
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
				MarkdownDescription: "Lifecycle status: `PROCESSING` while a change is rolling out, `SUCCESS` when " +
					"converged.",
				Computed: true,
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
// present. Validators are no-ops on unknown values.
func (r *K8sNodePoolResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg K8sNodePoolModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	nodeCountSet := !cfg.NodeCount.IsNull() && !cfg.NodeCount.IsUnknown()
	if cfg.Autoscaling != nil && nodeCountSet {
		resp.Diagnostics.AddAttributeError(
			path.Root("node_count"),
			"node_count conflicts with autoscaling",
			"When autoscaling is set, the autoscaler owns the node count. "+
				"Remove node_count (its live value is exported as a computed attribute).",
		)
	}
	// Only flag a concretely-absent node_count: an unknown (interpolated) value may
	// resolve to a real count at apply time, so ValidateConfig must stay a no-op on it.
	if cfg.Autoscaling == nil && cfg.NodeCount.IsNull() {
		resp.Diagnostics.AddAttributeError(
			path.Root("node_count"),
			"node_count is required without autoscaling",
			"Set node_count for a fixed-size pool, or add an autoscaling block.",
		)
	}
	if cfg.Autoscaling != nil {
		minNodes, maxNodes := cfg.Autoscaling.MinNodes, cfg.Autoscaling.MaxNodes
		if !minNodes.IsUnknown() && !maxNodes.IsUnknown() && minNodes.ValueInt64() > maxNodes.ValueInt64() {
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
// and ADR-K8's out-of-band-delete handling is cluster-only (a deleted pool simply
// reads back not-found → RemoveResource).
func (r *K8sNodePoolResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	// Destroy plan — nothing to do.
	if req.Plan.Raw.IsNull() {
		return
	}
	// Create plan — no prior state to diff.
	if req.State.Raw.IsNull() {
		return
	}

	var state, plan K8sNodePoolModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if !nodePoolChanged(&state, &plan) {
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
// (node_count, autoscaling presence/bounds) differ between state and plan. Mirrors
// defaultPoolChanged for the standalone model.
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
	var plan K8sNodePoolModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	region, projectTag := r.resolveScope(plan.Region, plan.ProjectTag)
	opts := &client.RequestOpts{Region: region, ProjectTag: projectTag}
	clusterID := plan.ClusterID.ValueInt64()

	createTimeout, diags := plan.Timeouts.Create(ctx, k8sPoolDefaultCreateTime)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()

	// ADR-K7: serialize per-cluster mutations within this process and refuse to act
	// on a FAILed or in-flight (blocked) cluster. A pool create mutates the parent
	// cluster, so it takes the same lock keyed by cluster id.
	unlock := lockCluster(clusterID)
	defer unlock()
	if err := r.ensureClusterMutable(ctx, clusterID, opts); err != nil {
		resp.Diagnostics.AddError("Cluster is not in a modifiable state", client.KuberErrorDetail(err))
		return
	}

	// ADR-K6: snapshot the existing pool ids and adopt-or-error on a name collision.
	// A lost create response (e.g. a 429 on read-back) must not orphan a pool; the
	// backend (G9) also rejects duplicate names, but checking up front gives a clear
	// "import it" message instead of a generic conflict.
	before, err := r.c.ListNodePools(ctx, clusterID, opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to verify node pool name availability", client.KuberErrorDetail(err))
		return
	}
	want := strings.ToLower(plan.Name.ValueString())
	beforeIDs := make(map[int64]bool, len(before))
	for i := range before {
		beforeIDs[before[i].ID] = true
		if strings.ToLower(before[i].Name) == want {
			resp.Diagnostics.AddError(
				"A node pool with this name already exists",
				fmt.Sprintf("Node pool %q already exists (id %d) in cluster %d. Import it "+
					"(terraform import) or choose a different name.", plan.Name.ValueString(), before[i].ID, clusterID),
			)
			return
		}
	}

	autoscale := plan.Autoscaling != nil
	wire := client.CreateNodePoolRequest{
		ClusterID:        clusterID,
		NodePoolName:     plan.Name.ValueString(),
		WorkerCPU:        int(plan.VCPU.ValueInt64()),
		WorkerRAM:        int(plan.RAM.ValueInt64()),
		WorkerDiskSize:   int(plan.DiskSize.ValueInt64()),
		AutoScaleEnabled: autoscale,
	}
	if autoscale {
		wire.MinNodes = int(plan.Autoscaling.MinNodes.ValueInt64())
		wire.MaxNodes = int(plan.Autoscaling.MaxNodes.ValueInt64())
		wire.WorkerReplicas = wire.MinNodes // backend forces replicas=minNodes for autoscale
	} else {
		wire.WorkerReplicas = int(plan.NodeCount.ValueInt64())
	}

	tflog.Debug(ctx, "Creating Kubernetes node pool", map[string]any{
		"cluster_id": clusterID, "name": wire.NodePoolName, "autoscale": autoscale,
	})

	// RetryOnBusy covers transient 503 (capacity); it deliberately does not retry
	// 627. createNewNodePool returns no id, so success is signalled only by a nil
	// error; the id is recovered below via an id-set diff.
	if _, err := client.RetryOnBusy(ctx, client.RetryTimeoutLong, func() (struct{}, error) {
		return struct{}{}, r.c.CreateNodePool(ctx, wire, opts)
	}); err != nil {
		resp.Diagnostics.AddError("Unable to create node pool", client.KuberErrorDetail(err))
		return
	}

	// ADR-K6: recover the new pool (createNewNodePool returns no id). The discovered
	// pool also serves as the always-present fallback for state below — unlike the
	// cluster resource there is no create-response object to fall back on, so without
	// it a read-back miss would persist unknown Computed values.
	discovered, err := r.resolveNewPoolID(ctx, clusterID, want, beforeIDs, opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to resolve the created node pool", client.KuberErrorDetail(err))
		return
	}
	poolID := discovered.ID

	// Save the id to state immediately (before the long poll) so a mid-poll failure
	// leaves an importable/destroyable resource rather than an orphan (ADR-K6).
	plan.ID = types.Int64Value(poolID)
	plan.Region = types.StringValue(region)
	plan.ProjectTag = types.StringValue(projectTag)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	converged, waitErr := r.waitForPoolReady(ctx, poolID, &plan, opts)

	// Always apply a concrete pool snapshot so no Computed field (status, node_subnet,
	// and the autoscaler-owned node_count) is left unknown in state — even on the
	// error/taint path. Prefer a fresh read, then the converged snapshot, then the
	// pool discovered right after create.
	final := discovered
	if converged != nil {
		final = converged
	}
	if fresh, ferr := r.c.GetNodePool(ctx, poolID, opts); ferr == nil && fresh != nil {
		final = fresh
	}
	r.applyServerState(&plan, final, region, projectTag, false)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)

	if waitErr != nil {
		resp.Diagnostics.AddError(
			"Node pool did not reach a ready state",
			fmt.Sprintf("node pool %d (cluster %d): %s", poolID, clusterID, waitErr.Error()),
		)
		return
	}
}

// ---- Read ----

func (r *K8sNodePoolResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data K8sNodePoolModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	opts := r.optsFromState(data.Region, data.ProjectTag)
	id := data.ID.ValueInt64()

	pool, err := r.c.GetNodePool(ctx, id, opts)
	if err != nil {
		if client.IsKuberNotFound(err) {
			tflog.Warn(ctx, "Node pool not found, removing from state", map[string]any{"id": id})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read node pool", client.KuberErrorDetail(err))
		return
	}
	// Pools hard-delete (no sticky DELETED status, unlike clusters), but guard
	// defensively so a DELETED row is treated as gone rather than written to state.
	if pool.Status == client.ClusterStatusDeleted {
		tflog.Warn(ctx, "Node pool reported DELETED, removing from state", map[string]any{"id": id})
		resp.State.RemoveResource(ctx)
		return
	}

	region := valueOrDefault(data.Region, r.c.Region)
	projectTag := valueOrDefault(data.ProjectTag, r.c.ProjectTag)
	r.applyServerState(&data, pool, region, projectTag, true)
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

// reconcilePool applies node_count / autoscaling changes, choosing the right
// endpoint for each transition (ADR-K4). It returns whether a mutating call was
// issued (so the caller waits for the pool to settle). Mirrors reconcileDefaultPool
// for the standalone model.
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
	var data K8sNodePoolModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	opts := r.optsFromState(data.Region, data.ProjectTag)
	clusterID := data.ClusterID.ValueInt64()
	id := data.ID.ValueInt64()

	deleteTimeout, diags := data.Timeouts.Delete(ctx, k8sPoolDefaultDeleteTime)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, deleteTimeout)
	defer cancel()

	unlock := lockCluster(clusterID)
	defer unlock()

	// ADR-K7: wait out an in-flight (blocked) parent cluster so the delete does not
	// race a cluster mid-roll. Unlike Create/Update we do NOT refuse a FAILed cluster
	// — a pool must stay deletable while tearing down a broken one.
	if err := r.waitClusterDeletable(ctx, clusterID, opts); err != nil {
		resp.Diagnostics.AddError("Cluster is not in a modifiable state", client.KuberErrorDetail(err))
		return
	}

	if err := r.c.DeleteNodePool(ctx, id, opts); err != nil {
		if client.IsKuberNotFound(err) {
			return
		}
		// 756 (last worker pool) and the "Cannot delete master node pool" guard both
		// surface here; KuberErrorDetail maps 756 to a clear message and others fall
		// through to the backend text. The pool still exists, so do not remove state.
		resp.Diagnostics.AddError("Unable to delete node pool", client.KuberErrorDetail(err))
		return
	}

	// Confirm the pool is gone. deleteNodePool hard-deletes the row, so the pool
	// disappears from the API (not-found); tolerate a few transient errors, then
	// surface the real one instead of spinning to the timeout (ADR-K5).
	var consecutiveErrs int
	var lastErr error
	for {
		pool, err := r.c.GetNodePool(ctx, id, opts)
		switch {
		case err == nil:
			consecutiveErrs = 0
			if pool.Status == client.ClusterStatusDeleted {
				return
			}
		case client.IsKuberNotFound(err):
			return
		default:
			consecutiveErrs++
			lastErr = err
			if consecutiveErrs > k8sMaxConsecutiveErrs {
				resp.Diagnostics.AddError("Unable to confirm node pool deletion",
					fmt.Sprintf("node pool %d: %s", id, client.KuberErrorDetail(err)))
				return
			}
		}
		select {
		case <-ctx.Done():
			msg := ctx.Err().Error()
			if lastErr != nil {
				msg = fmt.Sprintf("%s (last error: %s)", msg, lastErr.Error())
			}
			resp.Diagnostics.AddError("Node pool did not finish deleting",
				fmt.Sprintf("node pool %d: %s", id, msg))
			return
		case <-time.After(k8sPollInterval):
		}
	}
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

// waitClusterDeletable waits out an in-flight (blocked) parent cluster before a pool
// delete (ADR-K7). Unlike ensureClusterMutable it does NOT refuse a FAILed cluster:
// a pool must stay deletable while tearing down a broken cluster (and a FAILed
// cluster stays blocked until MR-D, so waiting it out would otherwise hang). A
// vanished parent means the pool is gone too, so it lets the delete proceed.
func (r *K8sNodePoolResource) waitClusterDeletable(ctx context.Context, clusterID int64, opts *client.RequestOpts) error {
	for {
		cl, err := r.c.GetCluster(ctx, clusterID, opts)
		if err != nil {
			if client.IsKuberNotFound(err) {
				return nil
			}
			return err
		}
		if cl.Status == client.ClusterStatusFail || !cl.Blocked {
			return nil
		}
		tflog.Debug(ctx, "Cluster is blocked by an in-flight operation, waiting before pool delete", map[string]any{"id": clusterID})
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for cluster %d to become unblocked: %w", clusterID, ctx.Err())
		case <-time.After(k8sPollInterval):
		}
	}
}

// resolveNewPoolID recovers the id of the pool just created in clusterID, given the
// set of pool ids that existed before the create (ADR-K6). It prefers a unique new
// id; on ambiguity it disambiguates by the (lowercased) pool name and errors loudly
// if more than one new pool carries that name. The backend persists the row
// synchronously, so the pool is normally present on the first attempt; a few bounded
// retries cover replica lag.
func (r *K8sNodePoolResource) resolveNewPoolID(ctx context.Context, clusterID int64, wantName string, beforeIDs map[int64]bool, opts *client.RequestOpts) (*client.NodePool, error) {
	var consecutiveErrs, emptyAttempts int
	for {
		pools, err := r.c.ListNodePools(ctx, clusterID, opts)
		if err != nil {
			// The create already succeeded; tolerate a few transient list errors
			// rather than aborting and orphaning the pool (ADR-K5).
			consecutiveErrs++
			if consecutiveErrs > k8sMaxConsecutiveErrs {
				return nil, fmt.Errorf("could not list node pools to resolve the created pool: %w", err)
			}
		} else {
			consecutiveErrs = 0
			// Match strictly by name (the backend lowercases names and G9 enforces
			// per-cluster uniqueness); never adopt an unrelated new pool on a name
			// mismatch, which could bind state to a pool created out-of-band.
			var nameMatches []*client.NodePool
			for i := range pools {
				if beforeIDs[pools[i].ID] {
					continue
				}
				if strings.ToLower(pools[i].Name) == wantName {
					nameMatches = append(nameMatches, &pools[i])
				}
			}
			switch {
			case len(nameMatches) == 1:
				return nameMatches[0], nil
			case len(nameMatches) > 1:
				return nil, fmt.Errorf("ambiguous node pool discovery: %d new pools named %q in cluster %d",
					len(nameMatches), wantName, clusterID)
			}
			emptyAttempts++
			if emptyAttempts >= k8sPoolDiscoveryAttempts {
				return nil, fmt.Errorf("could not resolve the id of node pool %q in cluster %d after creation; "+
					"run `terraform import` once it appears in the panel", wantName, clusterID)
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(k8sPollInterval):
		}
	}
}

// waitForPoolReady polls a node pool until it has CONVERGED to the requested shape:
// status SUCCESS and its live fields match `want`. Pools rest at SUCCESS and the
// mutating calls are async, so a plain "status == SUCCESS" check can return on the
// stale pre-mutation snapshot; matching the requested fields makes the wait
// edge-correct. Pools never reach FAIL/DELETED, so there is no terminal-error
// branch (a vanished pool is reported as not-found). Mirrors the cluster resource's
// waitForPoolReady for the standalone model. ADR-K5.
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
// node_count equals the request (when known). Mirrors poolMatchesDesired.
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
