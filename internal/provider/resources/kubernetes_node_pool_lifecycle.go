package resources

import (
	"context"
	"errors"
	"fmt"
	"time"

	"terraform-provider-prodata/internal/client"

	uuid "github.com/hashicorp/go-uuid"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func (r *K8sNodePoolResource) poolOperationPoll() time.Duration {
	if r.lifecyclePollInterval > 0 {
		return r.lifecyclePollInterval
	}
	return k8sPollInterval
}
func poolCreateOrder(plan *K8sNodePoolModel) client.CreateNodePoolRequest {
	wire := client.CreateNodePoolRequest{ClusterID: plan.ClusterID.ValueInt64(), NodePoolName: plan.Name.ValueString(), WorkerCPU: int(plan.VCPU.ValueInt64()), WorkerRAM: int(plan.RAM.ValueInt64()), WorkerDiskSize: int(plan.DiskSize.ValueInt64()), AutoScaleEnabled: plan.Autoscaling != nil}
	if plan.Autoscaling != nil {
		wire.MinNodes = int(plan.Autoscaling.MinNodes.ValueInt64())
		wire.MaxNodes = int(plan.Autoscaling.MaxNodes.ValueInt64())
		wire.WorkerReplicas = wire.MinNodes
	} else {
		wire.WorkerReplicas = int(plan.NodeCount.ValueInt64())
	}
	return wire
}
func poolOperationMatches(data *K8sNodePoolModel, view *client.NodePoolOperation) error {
	nameNotRequired := view.Operation == client.NodePoolDeleteOperation && view.Status == "SUCCESS" && view.NodePoolName == ""
	if view.ClusterID != data.ClusterID.ValueInt64() || (data.ID.ValueInt64() > 0 && (view.PoolID == nil || *view.PoolID != data.ID.ValueInt64())) ||
		view.Status != "NOT_SUBMITTED" && !nameNotRequired && !data.Name.IsNull() && !data.Name.IsUnknown() && view.NodePoolName != data.Name.ValueString() {
		return fmt.Errorf("the API returned another canonical node pool identity; the prior checkpoint was retained")
	}
	return nil
}
func checkpointPoolOperation(data *K8sNodePoolModel, view *client.NodePoolOperation) error {
	if err := poolOperationMatches(data, view); err != nil {
		return err
	}
	if view.PoolID != nil {
		data.ID = types.Int64Value(*view.PoolID)
	}
	data.Status = types.StringValue(view.Status)
	return nil
}

func (r *K8sNodePoolResource) createDurablePool(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data K8sNodePoolModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	createTimeout, diags := data.Timeouts.Create(ctx, k8sPoolDefaultCreateTime)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()
	region, project := r.resolveScope(data.Region, data.ProjectTag)
	requestUUID, err := uuid.GenerateUUID()
	if err != nil {
		resp.Diagnostics.AddError("Unable to reserve node pool identity", err.Error())
		return
	}
	data.RequestUUID = types.StringValue(requestUUID)
	data.DeletionRequestUUID = types.StringNull()
	data.ID = types.Int64Null()
	data.Status = types.StringValue("RESERVING")
	data.NodeSubnet = types.Int64Null()
	if data.NodeCount.IsUnknown() {
		data.NodeCount = types.Int64Null()
	}
	data.Region = types.StringValue(region)
	data.ProjectTag = types.StringValue(project)
	// Terraform stores this checkpoint when the RPC returns. Main's durable ledger protects the
	// operation in flight; a canonical pool is never discovered or adopted by name/id-set diff.
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	opts := &client.RequestOpts{Region: region, ProjectTag: project}
	clusterID := data.ClusterID.ValueInt64()
	if err = r.c.CheckNodePoolLifecycleAPI(ctx, clusterID, opts); err != nil {
		resp.State.RemoveResource(ctx)
		resp.Diagnostics.AddError("Durable node pool API required", err.Error())
		return
	}
	if err = requireClusterInProject(ctx, r.c, clusterID, opts); err != nil {
		resp.State.RemoveResource(ctx)
		resp.Diagnostics.AddError("Unable to verify node pool project", err.Error())
		return
	}
	unlock := lockCluster(clusterID)
	defer unlock()
	if err = r.ensureClusterMutable(ctx, clusterID, opts); err != nil {
		resp.State.RemoveResource(ctx)
		resp.Diagnostics.AddError("Cluster is not modifiable", client.KuberErrorDetail(err))
		return
	}
	wire := poolCreateOrder(&data)
	current, err := r.reservePoolOperation(ctx, clusterID, requestUUID, client.NodePoolOperationRequest{Operation: client.NodePoolCreateOperation, Create: &wire}, opts)
	if current != nil {
		if ce := checkpointPoolOperation(&data, current); ce != nil {
			resp.Diagnostics.AddError("Node pool identity changed", ce.Error())
			return
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	}
	if err != nil {
		resp.Diagnostics.AddError("Node pool reservation unverified", err.Error())
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	if current.Status == "NOT_SUBMITTED" || !*current.Exists {
		resp.State.RemoveResource(ctx)
		resp.Diagnostics.AddError("Node pool reservation no longer active", "The exact operation positively confirmed no owned pool remains.")
		return
	}
	if poolNeedsSubmit(current) {
		current, err = r.submitPoolOperation(ctx, clusterID, requestUUID, client.NodePoolCreateOperation, current, opts)
		if current != nil {
			if ce := checkpointPoolOperation(&data, current); ce != nil {
				resp.Diagnostics.AddError("Node pool identity changed", ce.Error())
				return
			}
			resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
		}
		if err != nil {
			resp.Diagnostics.AddError("Node pool submission unverified", err.Error())
			return
		}
	}
	current, err = r.waitPoolOperation(ctx, clusterID, requestUUID, client.NodePoolCreateOperation, current, opts)
	if current != nil {
		if ce := checkpointPoolOperation(&data, current); ce != nil {
			resp.Diagnostics.AddError("Node pool identity changed", ce.Error())
			return
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	}
	if err != nil {
		resp.Diagnostics.AddError("Node pool creation not complete", err.Error())
		return
	}
	if current.Status != "SUCCESS" || !*current.Exists {
		resp.Diagnostics.AddError("Node pool not active", "The durable operation did not confirm an active pool; its identity is retained.")
		return
	}
	pool, err := r.c.GetNodePool(ctx, data.ID.ValueInt64(), opts)
	if err != nil {
		// Exact UUID SUCCESS/ready already proves creation. A transient legacy projection miss
		// must not taint and recreate the new pool; computed fields remain fully known/null.
		resp.Diagnostics.AddWarning("Node pool readback unverified", client.KuberErrorDetail(err)+". The completed operation and canonical pool ID are retained; refresh can populate the remaining computed fields.")
		return
	}
	if pool.ID != data.ID.ValueInt64() || pool.ClusterID != clusterID || pool.Name != data.Name.ValueString() {
		resp.Diagnostics.AddError("Node pool identity changed", "Canonical readback returned another pool; the original operation checkpoint was retained.")
		return
	}
	r.applyServerState(&data, pool, region, project, false)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *K8sNodePoolResource) reservePoolOperation(ctx context.Context, clusterID int64, requestUUID string, body client.NodePoolOperationRequest, opts *client.RequestOpts) (*client.NodePoolOperation, error) {
	result, err := r.c.ReserveNodePoolOperation(ctx, clusterID, requestUUID, body, opts)
	if err == nil {
		return result, nil
	}
	// The response may be lost after Main committed canonical ownership. Query only the exact UUID;
	// no unrelated pool name/list can supply its identity.
	recovered, readErr := r.c.GetNodePoolOperation(ctx, clusterID, requestUUID, body.Operation, opts)
	if readErr == nil {
		return recovered, nil
	}
	return result, err
}
func (r *K8sNodePoolResource) waitPoolOperation(ctx context.Context, clusterID int64, requestUUID, operation string, current *client.NodePoolOperation, opts *client.RequestOpts) (*client.NodePoolOperation, error) {
	for {
		if current == nil {
			return nil, fmt.Errorf("operation identity was not confirmed")
		}
		switch current.Status {
		case "SUCCESS", "NOT_SUBMITTED":
			return current, nil
		case "RESERVED", "SUBMITTING", "UNVERIFIED":
			resumed, err := r.submitPoolOperation(ctx, clusterID, requestUUID, operation, current, opts)
			if resumed != nil {
				current = resumed
			}
			if err != nil {
				return current, err
			}
			continue
		}
		select {
		case <-ctx.Done():
			return current, ctx.Err()
		case <-time.After(r.poolOperationPoll()):
		}
		next, err := r.c.GetNodePoolOperation(ctx, clusterID, requestUUID, operation, opts)
		if err != nil {
			return current, err
		}
		if current.PoolID != nil && (next.PoolID == nil || *current.PoolID != *next.PoolID) || next.Epoch != current.Epoch || next.Fingerprint != current.Fingerprint {
			return current, fmt.Errorf("the operation's immutable identity changed during polling")
		}
		current = next
	}
}

func (r *K8sNodePoolResource) deleteDurablePool(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data K8sNodePoolModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	deleteTimeout, diags := data.Timeouts.Delete(ctx, k8sPoolDefaultDeleteTime)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, deleteTimeout)
	defer cancel()
	opts := r.optsFromState(data.Region, data.ProjectTag)
	clusterID := data.ClusterID.ValueInt64()
	unlock := lockCluster(clusterID)
	defer unlock()
	// Resolve pending CREATE first. Only positive no-submission/gone proof can release a nullable-ID
	// resource. A running CREATE may be compensated only through Main's UID-bound DELETE protocol.
	if data.RequestUUID.ValueString() != "" {
		creation, err := r.c.CancelNodePoolCreation(ctx, clusterID, data.RequestUUID.ValueString(), opts)
		if err != nil {
			resp.Diagnostics.AddError("Node pool creation cancellation unverified", err.Error())
			return
		}
		if err = poolOperationMatches(&data, creation); err != nil {
			resp.Diagnostics.AddError("Node pool identity changed", err.Error())
			return
		}
		if !*creation.Exists && (creation.Status == "NOT_SUBMITTED" || creation.Status == "SUCCESS") {
			return
		}
		if creation.PoolID != nil {
			data.ID = types.Int64Value(*creation.PoolID)
			resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
		}
		if resp.Diagnostics.HasError() {
			return
		}
		if creation.PoolID == nil {
			resp.Diagnostics.AddError("Node pool creation remains unverified", "The known creation still owns its slot and target; deletion requires a positive canonical pool and UID association.")
			return
		}
	}
	if data.ID.ValueInt64() < 1 {
		resp.Diagnostics.AddError("Node pool identity unavailable", "No canonical pool or positive creation cancellation was confirmed; existing state is retained.")
		return
	}
	if err := r.c.CheckNodePoolLifecycleAPI(ctx, clusterID, opts); err != nil {
		resp.Diagnostics.AddError("Durable node pool API required", err.Error())
		return
	}
	requestUUID := data.DeletionRequestUUID.ValueString()
	var current *client.NodePoolOperation
	if requestUUID != "" {
		known, err := r.c.GetNodePoolOperation(ctx, clusterID, requestUUID, client.NodePoolDeleteOperation, opts)
		if err == nil {
			if err = poolOperationMatches(&data, known); err != nil {
				resp.Diagnostics.AddError("Node pool identity changed", err.Error())
				return
			}
			if known.Status == "SUCCESS" && !*known.Exists {
				return
			}
			if known.Status == "NOT_SUBMITTED" {
				requestUUID = ""
			} else {
				current = known
			}
		}
	}
	if requestUUID == "" {
		var err error
		requestUUID, err = uuid.GenerateUUID()
		if err != nil {
			resp.Diagnostics.AddError("Unable to reserve node pool deletion identity", err.Error())
			return
		}
		data.DeletionRequestUUID = types.StringValue(requestUUID)
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	if current == nil {
		id := data.ID.ValueInt64()
		var err error
		current, err = r.reservePoolOperation(ctx, clusterID, requestUUID, client.NodePoolOperationRequest{Operation: client.NodePoolDeleteOperation, PoolID: &id}, opts)
		if current != nil {
			if ce := poolOperationMatches(&data, current); ce != nil {
				resp.Diagnostics.AddError("Node pool identity changed", ce.Error())
				return
			}
		}
		if err != nil {
			resp.Diagnostics.AddError("Node pool deletion reservation unverified", err.Error())
			return
		}
	}
	if current.Status == "NOT_SUBMITTED" {
		resp.Diagnostics.AddError("Node pool deletion not submitted", "The deletion attempt was positively cancelled; the live pool and identity are retained for a fresh destroy attempt.")
		return
	}
	if poolNeedsSubmit(current) {
		var err error
		current, err = r.submitPoolOperation(ctx, clusterID, requestUUID, client.NodePoolDeleteOperation, current, opts)
		if current != nil {
			if ce := poolOperationMatches(&data, current); ce != nil {
				resp.Diagnostics.AddError("Node pool identity changed", ce.Error())
				return
			}
		}
		if err != nil {
			resp.Diagnostics.AddError("Node pool deletion submission unverified", err.Error())
			return
		}
	}
	current, err := r.waitPoolOperation(ctx, clusterID, requestUUID, client.NodePoolDeleteOperation, current, opts)
	if current != nil {
		if ce := poolOperationMatches(&data, current); ce != nil {
			resp.Diagnostics.AddError("Node pool identity changed", ce.Error())
			return
		}
	}
	if err != nil {
		resp.Diagnostics.AddError("Node pool deletion unverified", err.Error())
		return
	}
	if current.Status != "SUCCESS" || *current.Exists {
		resp.Diagnostics.AddError("Node pool deletion not confirmed", "Only exact UID-bound physical absence proof may release Terraform ownership.")
	}
}

func poolNeedsSubmit(current *client.NodePoolOperation) bool {
	return current != nil && (current.Status == "RESERVED" || current.Status == "SUBMITTING" || current.Status == "UNVERIFIED")
}
func samePoolOperation(before, after *client.NodePoolOperation) bool {
	return before == nil || before.Epoch == after.Epoch && before.Fingerprint == after.Fingerprint &&
		(before.PoolID == nil || after.PoolID != nil && *before.PoolID == *after.PoolID)
}

// This re-drives only the versioned known UUID. Main/CAPI fence the underlying physical POST;
// SUBMITTING/UNVERIFIED can also mean a pre-claim refusal, so a GET-only waiter cannot recover it.
func (r *K8sNodePoolResource) submitPoolOperation(ctx context.Context, clusterID int64, requestUUID, operation string, current *client.NodePoolOperation, opts *client.RequestOpts) (*client.NodePoolOperation, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		submitted, err := r.c.SubmitNodePoolOperation(ctx, clusterID, requestUUID, operation, opts)
		lastErr = err
		if submitted != nil {
			if !samePoolOperation(current, submitted) {
				return current, fmt.Errorf("known node pool submission returned another immutable operation")
			}
			current = submitted
		}
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && (apiErr.StatusCode == 400 || apiErr.StatusCode == 401 || apiErr.StatusCode == 403 || apiErr.HasCode(714)) {
			return current, err
		}
		if err == nil && !poolNeedsSubmit(current) {
			return current, nil
		}
		observed, readErr := r.c.GetNodePoolOperation(ctx, clusterID, requestUUID, operation, opts)
		if readErr == nil {
			if !samePoolOperation(current, observed) {
				return current, fmt.Errorf("known node pool lookup returned another immutable operation")
			}
			current = observed
			if !poolNeedsSubmit(current) {
				return current, nil
			}
		}
		if ctx.Err() != nil {
			return current, ctx.Err()
		}
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return current, ctx.Err()
			case <-time.After(r.poolOperationPoll()):
			}
		}
	}
	if lastErr != nil {
		return current, fmt.Errorf("known UUID submission remains unverified after bounded retries: %w", lastErr)
	}
	return current, fmt.Errorf("known UUID submission remains unverified after bounded retries; canonical ownership was retained")
}

// Resource.Read is strictly observational. Only the versioned operation's positive typed
// cancellation/deletion proof may remove identity; raw legacy/API misses never do.
func (r *K8sNodePoolResource) readPoolOperationState(ctx context.Context, data *K8sNodePoolModel, opts *client.RequestOpts, resp *resource.ReadResponse) bool {
	clusterID := data.ClusterID.ValueInt64()
	if data.DeletionRequestUUID.ValueString() != "" {
		view, err := r.c.GetNodePoolOperation(ctx, clusterID, data.DeletionRequestUUID.ValueString(), client.NodePoolDeleteOperation, opts)
		if err != nil {
			nodePoolReadFailure(resp, "Node pool deletion remains unverified", err)
			return true
		}
		if err = poolOperationMatches(data, view); err != nil {
			resp.Diagnostics.AddWarning("Node pool identity unverified", err.Error())
			return true
		}
		if view.Status == "SUCCESS" && !*view.Exists {
			resp.State.RemoveResource(ctx)
			return true
		}
		if view.Status != "NOT_SUBMITTED" {
			data.Status = types.StringValue("DELETING")
			resp.Diagnostics.Append(resp.State.Set(ctx, data)...)
			resp.Diagnostics.AddWarning("Node pool deletion pending", "The exact deletion operation has not positively confirmed physical absence; no submission was made by Read.")
			return true
		}
		data.DeletionRequestUUID = types.StringNull() // positive cancellation retains the live pool
	}
	if data.RequestUUID.ValueString() == "" {
		return false
	}
	view, err := r.c.GetNodePoolOperation(ctx, clusterID, data.RequestUUID.ValueString(), client.NodePoolCreateOperation, opts)
	if err != nil {
		nodePoolReadFailure(resp, "Node pool creation remains unverified", err)
		return true
	}
	if err = poolOperationMatches(data, view); err != nil {
		resp.Diagnostics.AddWarning("Node pool identity unverified", err.Error())
		return true
	}
	if !*view.Exists && (view.Status == "NOT_SUBMITTED" || view.Status == "SUCCESS") {
		resp.State.RemoveResource(ctx)
		return true
	}
	if err = checkpointPoolOperation(data, view); err != nil {
		resp.Diagnostics.AddWarning("Node pool identity unverified", err.Error())
		return true
	}
	if view.Status != "SUCCESS" || !*view.Ready {
		resp.Diagnostics.Append(resp.State.Set(ctx, data)...)
		resp.Diagnostics.AddWarning("Node pool creation pending", "The stored request owns this pool; Read does not submit or guess its outcome.")
		return true
	}
	return false
}

func nodePoolReadFailure(resp *resource.ReadResponse, title string, err error) {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && (apiErr.HasCode(714) || apiErr.HasCode(936) || apiErr.StatusCode == 401) {
		resp.Diagnostics.AddError(title, client.KuberErrorDetail(err))
		return
	}
	resp.Diagnostics.AddWarning(title, client.KuberErrorDetail(err)+". This is not positive absence; the operation UUID and canonical identity are retained.")
}
