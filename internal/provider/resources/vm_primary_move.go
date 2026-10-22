package resources

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

const (
	// vmDefaultUpdateTime is a CEILING, like vmDefaultCreateTime: the update returns as soon as
	// it is done. It bounds the whole of Update (a rename, a resize, a move of the network), which
	// had no bound of its own before. A move of the main network needs vmMoveMinTimeout of it.
	vmDefaultUpdateTime = 30 * time.Minute

	// vmMoveMinTimeout is the least timeouts.update a plan that moves the VM to another network
	// is accepted with. The move is checked first (up to 2 minutes), then the VM is stopped (5
	// minutes, and 3 more for a stop the panel refuses), the move itself gets at least 5 minutes,
	// and vmStartReserve (8 minutes) is kept for starting the VM again.
	vmMoveMinTimeout = 20 * time.Minute
)

// networkChange is what a plan does to the main network of a VM: leaves it, moves it, or moves it
// to a network that is not known until apply (an unknown id is a move, the way the panel will not
// know otherwise until apply either).
type networkChange int

const (
	networkStays networkChange = iota
	networkMoves
	networkUnknown
)

// networkChangeOf compares the local_network_id of the state and of the plan.
func networkChangeOf(state, plan types.Int64) networkChange {
	switch {
	case plan.IsUnknown():
		return networkUnknown
	case state.Equal(plan):
		return networkStays
	default:
		return networkMoves
	}
}

// moves reports whether the main network is to change, or may change.
func (c networkChange) moves() bool { return c != networkStays }

// networkChangeInPlan is networkChangeOf for the state and the plan of a plan modifier.
func networkChangeInPlan(ctx context.Context, state tfsdk.State, plan tfsdk.Plan) (networkChange, diag.Diagnostics) {
	var stateNetwork, planNetwork types.Int64
	diags := state.GetAttribute(ctx, path.Root("local_network_id"), &stateNetwork)
	diags.Append(plan.GetAttribute(ctx, path.Root("local_network_id"), &planNetwork)...)
	return networkChangeOf(stateNetwork, planNetwork), diags
}

// privateIPReplaces reports whether the plan replaces the VM because of private_ip: the address
// changes and the network does not. An address that changes with the network is part of the move.
func privateIPReplaces(state, plan *VmResourceModel) bool {
	return !state.PrivateIP.Equal(plan.PrivateIP) && !networkChangeOf(state.LocalNetworkID, plan.LocalNetworkID).moves()
}

// privateIPFromState is UseStateForUnknown for private_ip while the VM stays in its network. When
// the network changes, an address that is not set is left unknown: the panel picks one in the new
// network, and the one in the state is of the old.
func privateIPFromState() planmodifier.String { return privateIPFromStateModifier{} }

type privateIPFromStateModifier struct{}

func (privateIPFromStateModifier) Description(context.Context) string {
	return "Uses the address of the state while the VM stays in its network; otherwise the address is known after apply."
}

func (m privateIPFromStateModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (privateIPFromStateModifier) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	// Creating or destroying; an address that is set; an address that is known already.
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() || !req.ConfigValue.IsNull() || !req.PlanValue.IsUnknown() || req.StateValue.IsNull() {
		return
	}
	change, diags := networkChangeInPlan(ctx, req.State, req.Plan)
	resp.Diagnostics.Append(diags...)
	if diags.HasError() || change.moves() {
		return
	}
	resp.PlanValue = req.StateValue
}

// privateIPReplace replaces the VM when private_ip changes and local_network_id does not.
func privateIPReplace() planmodifier.String {
	return stringplanmodifier.RequiresReplaceIf(
		func(ctx context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
			change, diags := networkChangeInPlan(ctx, req.State, req.Plan)
			resp.Diagnostics.Append(diags...)
			resp.RequiresReplace = !diags.HasError() && !change.moves()
		},
		"Replaces the VM when private_ip changes and local_network_id does not.",
		"Replaces the VM when `private_ip` changes and `local_network_id` does not. Together with a new network the address is part of the move.",
	)
}

// moveWarning is what the user sees before apply of a plan that moves the VM to another network.
// It is the only place the user sees that a running VM will be stopped: the plan shows only the
// attributes.
func moveWarning(change networkChange) (summary, detail string) {
	what := "This update moves the main network interface of the VM to another local network; the VM is not recreated."
	summary = "Changing local_network_id moves the virtual machine to another network"
	if change == networkUnknown {
		// The network may turn out to be the one the VM is in: then nothing is moved.
		what = "The new network is known only after other resources are applied. If it is not the network the VM " +
			"is in now, this update moves the main network interface of the VM to another local network; the VM " +
			"is not recreated."
		summary = "Changing local_network_id may move the virtual machine to another network"
	}
	detail = what + " A running VM is stopped for it (powered off, the guest is not shut down) and started " +
		"again afterwards; a stopped VM stays stopped. The VM gets a new address in the new network: the old one " +
		"is released at once and may be given to another VM of its network. Its default gateway and security " +
		"groups move with it. On a VM without cloud-init nothing configures the guest: set the new address, " +
		"mask and gateway inside the VM yourself. See \"Moving a VM to another network\" in the documentation " +
		"of prodata_vm."
	return summary, detail
}

// checkMoveTimeout is an error for a plan that moves the VM and sets timeouts.update below
// vmMoveMinTimeout. A timeout that is not set, or not known yet, is vmDefaultUpdateTime, which is
// long enough for a move.
func checkMoveTimeout(ctx context.Context, t timeouts.Value) diag.Diagnostics {
	timeout, diags := t.Update(ctx, vmDefaultUpdateTime)
	if diags.HasError() {
		return diags
	}
	if timeout < vmMoveMinTimeout {
		diags.AddAttributeError(
			path.Root("timeouts").AtName("update"),
			"timeouts.update is too short to move the virtual machine",
			fmt.Sprintf("Moving a VM to another network takes up to %s: the check, the stop of the VM, the move, "+
				"and the time kept for starting the VM again. Set timeouts.update to %s or more (the default is %s).",
				vmMoveMinTimeout, vmMoveMinTimeout, vmDefaultUpdateTime),
		)
	}
	return diags
}

// updateStepError is the failure of one step of an update: the summary of the diagnostic it
// becomes, and the error.
type updateStepError struct {
	summary string
	err     error
}

func (e *updateStepError) Error() string { return e.summary + ": " + e.err.Error() }
func (e *updateStepError) Unwrap() error { return e.err }

// moveRefusal turns the panel's refusal of the move of the main network into the summary and the
// detail of a diagnostic that leads to what to do. err is the error of the client.
func moveRefusal(err error, networkID int64) (summary, detail string) {
	var apiErr *client.APIError
	panelText := err.Error()
	if errors.As(err, &apiErr) && apiErr.Message != "" {
		panelText = apiErr.Message
	}

	switch {
	case client.IsRouteMissing(err):
		return "The panel cannot move a virtual machine to another network",
			"This panel does not have the call that changes local_network_id of a VM in place (PUT /api/v2/vms/{id}/primary-local-network). " +
				"Update the panel, or recreate the VM (terraform apply -replace), which deletes its disks."
	case client.IsAPIError(err, client.PanelCodeVMNotFound):
		return "The virtual machine is not found",
			panelText + " The VM is not in the region and project of this resource (region, project_tag), or it was deleted after the plan was made."
	case client.IsAPIError(err, client.PanelCodeVMInAutoScalingGroup), client.IsAPIError(err, client.PanelCodeBadVMRole):
		return "This virtual machine cannot be moved to another network", panelText
	case client.IsAPIError(err, client.PanelCodeAlreadyInNetwork):
		return "The virtual machine already has an interface in the network",
			fmt.Sprintf("%s The VM has an interface in network %d besides its main one: detach it first (prodata_local_network_attachment) "+
				"or choose another network.", panelText, networkID)
	case client.IsAPIError(err, client.PanelCodeAddressTaken):
		return "The address is taken", panelText + " Choose another private_ip, or remove it so that the panel picks a free one."
	case client.IsAPIError(err, client.PanelCodeBadVMStatus):
		return "The virtual machine is in a transitional status",
			panelText + " Wait until the VM leaves the status and apply again."
	case client.IsAPIError(err, client.PanelCodeVMNotStopped):
		return "The virtual machine is not stopped",
			panelText + " The provider stops a running VM itself, so it is probably in a transitional status, or somebody started it while it " +
				"was being moved: wait until the VM settles and apply again."
	case client.IsAPIError(err, client.PanelCodeLoadBalancerBackend):
		return "The virtual machine is a backend of a load balancer",
			panelText + " Remove the VM from the load balancer first."
	case client.IsAPIError(err, client.PanelCodeGatewayNotMovable):
		return "The gateway of the virtual machine cannot be moved",
			panelText + " Set the gateway of the prodata_local_network (changing it recreates the network), or move the VM to a network that has one."
	case client.IsAPIError(err, client.PanelCodePrimaryUnclear):
		return "The main network interface of the virtual machine cannot be told from the others",
			panelText + " A likely cause is another interface of the VM that carries a default gateway: the platform counts it as a main one too. " +
				"Move the gateway to the main interface (see \"Destroying the attachment\" in the documentation of prodata_local_network_attachment) " +
				"and apply again. If that is not the cause, contact support."
	case client.IsAPIError(err, client.PanelCodeSameNetworkAddress):
		return "The address cannot be changed inside the network",
			panelText + " private_ip alone replaces the VM; to move it to another network set private_ip together with a local_network_id that is known at plan time."
	case client.IsAPIError(err, client.PanelCodeSecurityGroups):
		return "The security groups of the virtual machine cannot be moved",
			panelText + " Leave the interface one group that has rules."
	case client.IsAPIError(err, client.PanelCodeMoveNeedsOperator):
		return "An earlier move of the virtual machine needs the operator",
			panelText + " The unfinished move of this VM cannot be continued or taken back by the platform; contact support."
	case client.IsMoveOutcomeUnknown(err):
		return "The outcome of the move of the virtual machine is unknown",
			err.Error() + ". The panel finishes or takes back a move that was cut short when it is asked again: repeat apply."
	default:
		return "Unable to move the virtual machine to another network", err.Error()
	}
}

// moveOutcome is what the update of a VM that moves to another network found out.
type moveOutcome struct {
	// moved is the answer of the panel to the move itself, nil until there is one.
	moved *client.MovedPrimaryLocalNetwork
	// failed is the step of the update that failed, nil when none did.
	failed *updateStepError
}

// failure is the step that failed, or a step of the move itself for an error that came without one.
func (o *moveOutcome) failure(err error) *updateStepError {
	if o.failed != nil {
		return o.failed
	}
	return &updateStepError{"Unable to move the virtual machine to another network", err}
}

// wantsMove reports whether Update asks the panel to move the main interface: the network changes,
// or the address does while the network stays. Terraform does not bring the second to Update: it
// replaces the VM, and a plan made with a network that was not known yet, which turns out to be the
// VM's own, stops with "Provider produced inconsistent final plan" before any call. The branch is a
// safety net: were Update called so, the panel answers with a refusal of its own (967) where doing
// nothing would leave the state wrong.
func wantsMove(state, plan *VmResourceModel) bool {
	if !state.LocalNetworkID.Equal(plan.LocalNetworkID) {
		return true
	}
	// ValueString of an address that is not set, or not known yet, is "".
	return plan.PrivateIP.ValueString() != "" && !state.PrivateIP.Equal(plan.PrivateIP)
}

// updateWithMove updates a VM whose main network is to change; forecast is the answer of the panel's
// check of the move. The move needs the VM stopped, so a running VM goes through
// restartRunningVMWith and the work is done in its hook, with the VM stopped: cpu and ram, the
// disk, the move (see runMove). It returns the answer of the panel to the move, and nil when the VM
// was not moved: diags then says why.
func (r *VmResource) updateWithMove(ctx context.Context, diags *diag.Diagnostics, vmID int64, state, plan *VmResourceModel, opts *client.RequestOpts, forecast *client.MovedPrimaryLocalNetwork) *client.MovedPrimaryLocalNetwork {
	var work moveOutcome
	cycle, err := restartRunningVMWith(ctx, r.client, vmID, opts, r.pollInterval(), func(hookCtx context.Context) error {
		return r.runMove(hookCtx, vmID, state, plan, opts, forecast, &work)
	})

	switch {
	case errors.Is(err, errHookTimeShort):
		diags.AddError("Not enough time to move the virtual machine",
			fmt.Sprintf("%s. The VM was not stopped and nothing was moved: raise timeouts.update (a move needs %s at least) and apply again.",
				err, vmMoveMinTimeout))
		return nil

	case cycle.hookSkipped:
		detail := fmt.Sprintf("VM %d was stopped for the move, but less than %s was left to do it in, so it was started again and nothing "+
			"was moved. Raise timeouts.update (a move needs %s at least) and apply again.", vmID, vmHookMinBudget, vmMoveMinTimeout)
		if err != nil {
			detail += " Starting the VM failed too: " + err.Error()
		}
		diags.AddError("Not enough time to move the virtual machine", detail)
		return nil

	case cycle.hookRan && cycle.hookErr != nil:
		failed := work.failure(cycle.hookErr)
		detail := failed.err.Error()
		if err != nil {
			detail += fmt.Sprintf(" The VM could not be started again: %s", err)
		} else {
			detail += " The VM was started again."
		}
		diags.AddError(failed.summary, detail)
		return nil

	case cycle.hookRan:
		if err != nil {
			diags.AddWarning(
				"VM updated but not restarted",
				fmt.Sprintf("Changes were applied, but the VM failed to restart: %s", err.Error()),
			)
		}
		return work.moved

	case err != nil:
		diags.AddError("Unable to Stop VM for update", err.Error())
		return nil

	case cycle.cycled:
		diags.AddError("The virtual machine was started while it was being moved",
			fmt.Sprintf("VM %d was stopped for the move and somebody started it again before the move began, so nothing was changed. Apply again.", vmID))
		return nil
	}

	// The VM was stopped to begin with: the work is done on it as it is, and it stays stopped, the
	// way it does for a change of cpu, ram or disk.
	if err := r.runMove(ctx, vmID, state, plan, opts, forecast, &work); err != nil {
		failed := work.failure(err)
		diags.AddError(failed.summary, failed.err.Error())
		return nil
	}
	return work.moved
}

// vmSettledAskTime bounds the question that settledMove asks the panel again after a 627 to a move
// that went through. The panel answers it at once; one that does not must not hold the VM off for
// the rest of the time the move has.
const vmSettledAskTime = time.Minute

// vmReadBackTime is the time the read that ends an update has when the update has none left.
const vmReadBackTime = 2 * time.Minute

// readBackContext is the context of the read that ends an update. The work is done by then, and a
// deadline that ran out during it, or an interruption, must not fail the read and leave the state
// without what was done.
func readBackContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx.Err() == nil {
		return ctx, func() {}
	}
	return context.WithTimeout(context.WithoutCancel(ctx), vmReadBackTime)
}

// runMove does the work on a stopped VM, in order: cpu and ram, the disk, and the move of the
// network. The first step that fails ends the chain, and out says which; the chain does not start
// the VM, the cycle around it does.
func (r *VmResource) runMove(ctx context.Context, vmID int64, state, plan *VmResourceModel, opts *client.RequestOpts, forecast *client.MovedPrimaryLocalNetwork, out *moveOutcome) error {
	if failed := r.updateStopped(ctx, vmID, state, plan, opts); failed != nil {
		out.failed = failed
		return failed
	}

	moved, err := r.movePrimaryNetwork(ctx, vmID, plan, opts, forecast)
	if err != nil {
		summary, detail := moveRefusal(err, plan.LocalNetworkID.ValueInt64())
		out.failed = &updateStepError{summary, errors.New(detail)}
		return out.failed
	}
	out.moved = moved
	return nil
}

// movePrimaryNetwork asks the panel to move the main interface, and settles an answer that does not
// say whether the move happened: the panel's 627 comes after moves that went through as well as
// after those that did not, and only the VM's network says which.
func (r *VmResource) movePrimaryNetwork(ctx context.Context, vmID int64, plan *VmResourceModel, opts *client.RequestOpts, forecast *client.MovedPrimaryLocalNetwork) (*client.MovedPrimaryLocalNetwork, error) {
	networkID := plan.LocalNetworkID.ValueInt64()
	moved, err := r.client.MovePrimaryLocalNetwork(ctx, vmID, r.moveRequest(plan, false), opts, r.moveOptions...)
	if err == nil {
		return moved, nil
	}
	if !client.IsAPIError(err, client.PanelCodePlatformError) || client.IsMoveOutcomeUnknown(err) {
		return nil, err
	}

	vm, readErr := r.client.GetVmStatus(ctx, vmID, opts)
	if readErr != nil {
		return nil, fmt.Errorf("%w; reading the VM to find out whether the move happened failed too: %v; repeat apply", err, readErr)
	}
	if vm.LocalNetworkID == networkID {
		tflog.Warn(ctx, "The panel answered 627 to the move, but the VM is in the new network: the move went through", map[string]any{"id": vmID})
		return r.settledMove(ctx, vmID, plan, opts, forecast, vm), nil
	}
	return nil, fmt.Errorf("%w; the VM is still in network %d and nothing was changed that the panel did not take back; repeat apply", err, vm.LocalNetworkID)
}

// settledMove is the answer to a move that went through although the panel said 627. What the user
// is told about the guest comes from the VM as it is now and not from the forecast of the check,
// which was made before the VM was stopped: a VM with an earlier move that was never settled has no
// forecast (no address, and requiresGuestConfiguration=false whatever the VM is), and the address
// that was forecast may have been taken by another VM in the minutes since.
//
// The move is idempotent, and asked again for a VM that is in the network it answers with the
// interface as it is: the address, the mac, the gateway and whether the guest has to be configured,
// with changed=false. The question is bounded (vmSettledAskTime): the VM is stopped meanwhile, and
// the answer only decides what the user is warned of. Should it fail, or not come, the address is the
// one just read from the VM, and the guest is taken to need configuring when the forecast said so or
// said nothing (pendingRecovery): a warning that was not needed costs less than one that was left out.
func (r *VmResource) settledMove(ctx context.Context, vmID int64, plan *VmResourceModel, opts *client.RequestOpts, forecast *client.MovedPrimaryLocalNetwork, vm *client.Vm) *client.MovedPrimaryLocalNetwork {
	answer := *forecast
	askCtx, cancel := context.WithTimeout(ctx, r.settledAskTime())
	defer cancel()
	if again, err := r.client.MovePrimaryLocalNetwork(askCtx, vmID, r.moveRequest(plan, false), opts, r.moveOptions...); err == nil {
		answer = *again
		answer.PreviousNetworkID = forecast.PreviousNetworkID
	} else {
		tflog.Warn(ctx, "The panel answered 627 to the move and could not be asked again what it did", map[string]any{"id": vmID, "error": err.Error()})
		if vm.PrivateIP != "" {
			answer.IP = &vm.PrivateIP
		}
		answer.RequiresGuestConfiguration = forecast.RequiresGuestConfiguration || forecast.PendingRecovery
	}
	answer.Changed = true
	return &answer
}

// moveRequest is the request of a move of the VM to the network of the plan.
func (r *VmResource) moveRequest(plan *VmResourceModel, dryRun bool) client.MovePrimaryLocalNetworkRequest {
	// An address that is not set, or not known yet, is "": the panel picks one.
	return client.MovePrimaryLocalNetworkRequest{
		NetworkID: plan.LocalNetworkID.ValueInt64(),
		IP:        plan.PrivateIP.ValueString(),
		DryRun:    dryRun,
	}
}

// guestConfigurationWarning says what is left to do inside a guest that nothing configured.
func guestConfigurationWarning(moved *client.MovedPrimaryLocalNetwork) (summary, detail string) {
	var what []string
	if moved.IP != nil && *moved.IP != "" {
		what = append(what, "the address "+*moved.IP)
	}
	if moved.NetworkName != nil && *moved.NetworkName != "" {
		what = append(what, fmt.Sprintf("the mask of the network %q", *moved.NetworkName))
	}
	if moved.Gateway != nil && *moved.Gateway != "" {
		what = append(what, "the gateway "+*moved.Gateway)
	}
	detail = "The VM has no cloud-init, so nothing configured the new network inside the guest."
	if len(what) > 0 {
		detail += " Set " + joinWithAnd(what) + " inside the VM yourself; until then it has no network."
	}
	return "The guest of the virtual machine is not configured", detail
}

// joinWithAnd lists the items in a sentence: "a", "a and b", "a, b and c".
func joinWithAnd(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}
