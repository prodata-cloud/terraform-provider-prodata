package resources

import (
	"context"
	"errors"
	"fmt"
	"time"

	uuid "github.com/hashicorp/go-uuid"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"terraform-provider-prodata/internal/client"
)

func (r *K8sClusterResource) clusterOperationPoll() time.Duration {
	if r.creationPoll > 0 {
		return r.creationPoll
	}
	return k8sPollInterval
}

func (r *K8sClusterResource) initializeClusterCreation(ctx context.Context, data *K8sClusterModel, region, project string, resp *resource.CreateResponse) error {
	id, err := uuid.GenerateUUID()
	if err != nil {
		return err
	}
	data.RequestUUID = types.StringValue(id)
	data.ID = types.Int64Null()
	data.Region, data.ProjectTag = types.StringValue(region), types.StringValue(project)
	data.Status = types.StringValue("RESERVING")
	data.Blocked = types.BoolValue(true)
	data.APIEndpoint, data.SSHKeyEncoded, data.PrivateKeyEncoded, data.DateCreated = types.StringNull(), types.StringNull(), types.StringNull(), types.StringNull()
	data.KubeConfig = types.ObjectNull(kubeConfigAttrTypes())
	data.MasterNodeCount, data.WorkerNodeCount, data.NodePoolCount, data.IPAddressesCount = types.Int64Null(), types.Int64Null(), types.Int64Null(), types.Int64Null()
	if data.MasterFlavorID.IsUnknown() {
		data.MasterFlavorID = types.Int64Null()
	}
	if data.NodeIPRange.IsUnknown() {
		data.NodeIPRange = types.StringNull()
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, data)...)
	if resp.Diagnostics.HasError() {
		return fmt.Errorf("the pre-HTTP cluster identity could not be checkpointed")
	}
	return nil
}

func clusterCreationMatches(data *K8sClusterModel, out *client.ClusterCreationOperation) error {
	if out == nil || data.RequestUUID.ValueString() != out.RequestUUID || data.ID.ValueInt64() > 0 && (out.ClusterID == nil || *out.ClusterID != data.ID.ValueInt64()) || out.Status != "NOT_SUBMITTED" && !data.Name.IsNull() && !data.Name.IsUnknown() && data.Name.ValueString() != out.Name {
		return fmt.Errorf("the API returned another canonical cluster creation identity; the original checkpoint was retained")
	}
	return nil
}
func checkpointClusterCreation(data *K8sClusterModel, out *client.ClusterCreationOperation) error {
	if err := clusterCreationMatches(data, out); err != nil {
		return err
	}
	if out.ClusterID != nil {
		data.ID = types.Int64Value(*out.ClusterID)
	}
	// A completed creation is historical: it must not erase a newer hardware/teardown phase
	// when the canonical projection is temporarily unavailable.
	if out.Status != "SUCCESS" || data.Status.IsNull() || data.Status.IsUnknown() || data.Status.ValueString() == "RESERVING" || data.Status.ValueString() == "RESERVED" || data.Status.ValueString() == "SUBMITTING" || data.Status.ValueString() == "UNVERIFIED" || data.Status.ValueString() == "RUNNING" {
		data.Status = types.StringValue(out.Status)
		data.Blocked = types.BoolValue(!*out.Ready)
	}
	return nil
}
func sameClusterCreation(before, after *client.ClusterCreationOperation) bool {
	return before == nil || before.Epoch == after.Epoch && before.Fingerprint == after.Fingerprint && (before.ClusterID == nil || after.ClusterID != nil && *before.ClusterID == *after.ClusterID)
}
func clusterCreationNeedsSubmit(out *client.ClusterCreationOperation) bool {
	return out != nil && (out.Status == "RESERVED" || out.Status == "SUBMITTING" || out.Status == "UNVERIFIED")
}

func (r *K8sClusterResource) createDurableCluster(ctx context.Context, data *K8sClusterModel, wire client.CreateClusterRequest, opts *client.RequestOpts, resp *resource.CreateResponse) {
	id := data.RequestUUID.ValueString()
	current, err := r.c.ReserveClusterCreation(ctx, id, wire, opts)
	if err != nil {
		// Only the same UUID can recover a lost reservation response; no names/list adoption.
		if observed, readErr := r.c.GetClusterCreation(ctx, id, opts); readErr == nil {
			current, err = observed, nil
		}
	}
	checkpoint := func() bool {
		if current == nil {
			return true
		}
		if e := checkpointClusterCreation(data, current); e != nil {
			resp.Diagnostics.AddError("Cluster Creation Identity Changed", e.Error())
			return false
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, data)...)
		return !resp.Diagnostics.HasError()
	}
	if !checkpoint() {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cluster Reservation Is Unverified", err.Error()+" The known UUID is retained for exact recovery or cancellation.")
		return
	}
	if current.Status == "NOT_SUBMITTED" || !*current.Exists {
		resp.State.RemoveResource(ctx)
		resp.Diagnostics.AddError("Cluster Creation No Longer Active", "The exact operation positively confirmed no owned cluster remains.")
		return
	}
	for current.Status != "SUCCESS" && current.Status != "NOT_SUBMITTED" {
		if clusterCreationNeedsSubmit(current) {
			current, err = r.submitClusterCreation(ctx, id, current, opts)
		} else {
			select {
			case <-ctx.Done():
				err = ctx.Err()
			case <-time.After(r.clusterOperationPoll()):
				observed, e := r.c.GetClusterCreation(ctx, id, opts)
				err = e
				if e == nil {
					if !sameClusterCreation(current, observed) {
						err = fmt.Errorf("cluster immutable epoch or fingerprint changed")
					} else {
						current = observed
					}
				}
			}
		}
		if !checkpoint() {
			return
		}
		if err != nil {
			resp.Diagnostics.AddError("Cluster Creation Is Unverified", err.Error()+" Canonical ownership and the original request UUID are retained.")
			return
		}
	}
	if current.Status != "SUCCESS" || !*current.Exists {
		resp.Diagnostics.AddError("Cluster Is Not Active", "The original operation did not confirm a live cluster; its identity is retained.")
		return
	}
	// Ready proof is already durable. A projection/credentials outage cannot justify recreation.
	actual, readErr := r.waitForClusterReady(ctx, data.ID.ValueInt64(), "", opts)
	if actual != nil {
		if actual.ID != data.ID.ValueInt64() || actual.Name != data.Name.ValueString() {
			resp.Diagnostics.AddError("Cluster Identity Changed", "Canonical readback returned another cluster; its original ownership is retained.")
			return
		}
		r.applyServerState(ctx, data, actual, opts.Region, opts.ProjectTag)
		resp.Diagnostics.Append(resp.State.Set(ctx, data)...)
	}
	if readErr != nil {
		resp.Diagnostics.AddWarning("Cluster Readback Is Unverified", client.KuberErrorDetail(readErr)+". The completed creation and canonical cluster ID are retained; refresh can populate computed fields.")
		return
	}
	if actual != nil && actual.Kubeconfig == "" {
		summary, detail := kubeconfigPendingDiag(actual.ID, data.ExcludeCredentialsFromState)
		resp.Diagnostics.AddWarning(summary, detail)
	}
}

// Re-drive only this idempotent public UUID. Read remains observational even if preclaim was busy.
func (r *K8sClusterResource) submitClusterCreation(ctx context.Context, id string, current *client.ClusterCreationOperation, opts *client.RequestOpts) (*client.ClusterCreationOperation, error) {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		next, err := r.c.SubmitClusterCreation(ctx, id, opts)
		last = err
		if next != nil {
			if !sameClusterCreation(current, next) {
				return current, fmt.Errorf("cluster submission returned another immutable identity")
			}
			current = next
		}
		var api *client.APIError
		if errors.As(err, &api) && (api.StatusCode == 400 || api.StatusCode == 401 || api.StatusCode == 403 || api.HasCode(714)) {
			return current, err
		}
		if err == nil && !clusterCreationNeedsSubmit(current) {
			return current, nil
		}
		if next, e := r.c.GetClusterCreation(ctx, id, opts); e == nil {
			if !sameClusterCreation(current, next) {
				return current, fmt.Errorf("cluster lookup returned another immutable identity")
			}
			current = next
			if !clusterCreationNeedsSubmit(current) {
				return current, nil
			}
		}
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return current, ctx.Err()
			case <-time.After(r.clusterOperationPoll()):
			}
		}
	}
	if last != nil {
		return current, fmt.Errorf("known cluster submission remains unverified: %w", last)
	}
	return current, fmt.Errorf("known cluster submission remains pending after bounded retries; retry its original UUID")
}

func (r *K8sClusterResource) readClusterCreation(ctx context.Context, data *K8sClusterModel, opts *client.RequestOpts, resp *resource.ReadResponse) bool {
	if data.RequestUUID.ValueString() == "" {
		return false
	}
	out, err := r.c.GetClusterCreation(ctx, data.RequestUUID.ValueString(), opts)
	if err != nil {
		nodePoolReadFailure(resp, "Cluster Creation Is Unverified", err)
		return true
	}
	if err = checkpointClusterCreation(data, out); err != nil {
		resp.Diagnostics.AddWarning("Cluster Creation Identity Is Unverified", err.Error())
		return true
	}
	if !*out.Exists && (out.Status == "NOT_SUBMITTED" || out.Status == "SUCCESS") {
		resp.State.RemoveResource(ctx)
		return true
	}
	if out.Status != "SUCCESS" {
		resp.Diagnostics.Append(resp.State.Set(ctx, data)...)
		resp.Diagnostics.AddWarning("Cluster Creation Is Pending", "The stored request owns this cluster; Read observes it without submitting or allocating another attempt.")
		return true
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, data)...)
	return false
}

func (r *K8sClusterResource) cancelClusterCreation(ctx context.Context, data *K8sClusterModel, opts *client.RequestOpts) (bool, error) {
	out, err := r.c.CancelClusterCreation(ctx, data.RequestUUID.ValueString(), opts)
	if err != nil {
		return false, err
	}
	if err = checkpointClusterCreation(data, out); err != nil {
		return false, err
	}
	if !*out.Exists && (out.Status == "NOT_SUBMITTED" || out.Status == "SUCCESS") {
		return true, nil
	}
	if data.ID.ValueInt64() < 1 {
		return false, fmt.Errorf("the original creation has no positively owned canonical cluster for compensation")
	}
	// The canonical cluster deletion API must bind compensation to the original positive UID.
	return false, nil
}
