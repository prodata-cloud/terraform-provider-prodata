package resources

import (
	"context"
	"fmt"
	"regexp"
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
)

var (
	_ resource.Resource                = &VmFromBackupResource{}
	_ resource.ResourceWithConfigure   = &VmFromBackupResource{}
	_ resource.ResourceWithImportState = &VmFromBackupResource{}
	_ resource.ResourceWithModifyPlan  = &VmFromBackupResource{}
)

type VmFromBackupResource struct {
	client *client.Client
	poll   time.Duration
}
type vmFromBackupModel struct {
	ID             types.Int64    `tfsdk:"id"`
	BackupID       types.Int64    `tfsdk:"backup_id"`
	Name           types.String   `tfsdk:"name"`
	CPU            types.Int64    `tfsdk:"cpu_cores"`
	RAM            types.Int64    `tfsdk:"ram"`
	Description    types.String   `tfsdk:"description"`
	Password       types.String   `tfsdk:"password"`
	RestartPending types.Bool     `tfsdk:"restart_pending"`
	Status         types.String   `tfsdk:"status"`
	Region         types.String   `tfsdk:"region"`
	Project        types.String   `tfsdk:"project_tag"`
	Timeouts       timeouts.Value `tfsdk:"timeouts"`
}

func NewVmFromBackupResource() resource.Resource { return &VmFromBackupResource{} }
func (r *VmFromBackupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm_from_backup"
}
func (r *VmFromBackupResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Restores a backup as a new VM in its source project and region. The source VM is preserved. Destroying this resource deletes the restored VM and its disks.", Attributes: map[string]schema.Attribute{
		"id":              schema.Int64Attribute{Computed: true, MarkdownDescription: "ID of the restored VM.", PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}},
		"backup_id":       schema.Int64Attribute{Required: true, MarkdownDescription: "Successful panel backup ID. Changing it replaces the restored VM.", Validators: []validator.Int64{int64validator.AtLeast(1)}, PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
		"name":            schema.StringAttribute{Required: true, MarkdownDescription: "Name of the restored VM. Updated in place.", Validators: []validator.String{stringvalidator.RegexMatches(regexp.MustCompile(`^[A-Za-z0-9-]{3,63}$`), "Use 3–63 letters, digits and hyphens."), stringvalidator.RegexMatches(regexp.MustCompile(`[A-Za-z]`), "Include at least one letter.")}},
		"cpu_cores":       schema.Int64Attribute{Required: true, MarkdownDescription: "CPU cores of the restored VM.", Validators: []validator.Int64{int64validator.Between(1, 2147483647)}},
		"ram":             schema.Int64Attribute{Required: true, MarkdownDescription: "RAM in GB. The API converts it to MiB exactly.", Validators: []validator.Int64{int64validator.Between(1, 2097151)}},
		"description":     schema.StringAttribute{Optional: true, MarkdownDescription: "Description. Updated in place without stopping the VM."},
		"password":        schema.StringAttribute{Computed: true, Sensitive: true, MarkdownDescription: "One-time generated password, stored in Terraform state. May be unavailable after recovery of a lost response or import.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"status":          schema.StringAttribute{Computed: true, MarkdownDescription: "Latest VM status."},
		"region":          schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Region of the backup; defaults to the provider region.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()}},
		"project_tag":     schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Project of the backup; defaults to the provider project.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()}},
		"restart_pending": schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether Terraform must retry starting a VM stopped for an update."},
		"timeouts":        timeouts.Attributes(ctx, timeouts.Opts{Create: true, Read: true, Update: true, Delete: true}),
	}}
}
func (r *VmFromBackupResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	var state vmFromBackupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.RestartPending.ValueBool() {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("restart_pending"), types.BoolUnknown())...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("status"), types.StringUnknown())...)
	}
}
func (r *VmFromBackupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	var ok bool
	r.client, ok = req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Resource Configure Type", fmt.Sprintf("Expected *client.Client, got %T.", req.ProviderData))
	}
}
func (r *VmFromBackupResource) opts(d vmFromBackupModel) *client.RequestOpts {
	region, project := d.Region.ValueString(), d.Project.ValueString()
	if region == "" {
		region = r.client.Region
	}
	if project == "" {
		project = r.client.ProjectTag
	}
	return &client.RequestOpts{Region: region, ProjectTag: project}
}
func (r *VmFromBackupResource) wait(ctx context.Context, id int64, opts *client.RequestOpts) (*client.Vm, error) {
	delay := r.poll
	if delay == 0 {
		delay = 5 * time.Second
	}
	for {
		v, err := r.client.GetVmStatus(ctx, id, opts)
		if err != nil {
			return nil, err
		}
		switch v.Status {
		case "RUNNING", "STOPPED", "SUCCESS":
			return v, nil
		case "ERROR", "LOST", "DELETED":
			return v, fmt.Errorf("restored VM %d reached %s; its identity is kept in state", id, v.Status)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return v, ctx.Err()
		case <-timer.C:
		}
	}
}
func (r *VmFromBackupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var d vmFromBackupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &d)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, ds := d.Timeouts.Create(ctx, time.Hour)
	resp.Diagnostics.Append(ds...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	opts := r.opts(d)
	out, err := r.client.RestoreVmBackup(ctx, d.BackupID.ValueInt64(), client.VmBackupRestoreRequest{Name: d.Name.ValueString(), CPUCores: d.CPU.ValueInt64(), RAM: d.RAM.ValueInt64(), Description: d.Description.ValueString()}, opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Restore VM Backup", err.Error())
		return
	}
	d.ID = types.Int64Value(out.VmID)
	d.Region = types.StringValue(opts.Region)
	d.Project = types.StringValue(opts.ProjectTag)
	d.Password = types.StringNull()
	if out.Password != "" {
		d.Password = types.StringValue(out.Password)
	}
	d.Status = types.StringValue("NEW")
	d.RestartPending = types.BoolValue(false)
	resp.Diagnostics.Append(resp.State.Set(context.WithoutCancel(ctx), &d)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// The only request allowed to create a remote VM happens after its canonical ID is in state.
	submitted, submitErr := submitKnownReservation(ctx,
		func(call context.Context) (*client.VmBackupRestoreOperation, error) {
			return r.client.SubmitVmBackupRestore(call, d.BackupID.ValueInt64(), out.VmID, opts)
		},
		func(call context.Context) (*client.VmBackupRestoreOperation, error) {
			return r.client.GetVmBackupRestore(call, d.BackupID.ValueInt64(), out.VmID, opts)
		},
		func(value *client.VmBackupRestoreOperation) string { return value.Status }, r.poll)
	if submitted != nil {
		d.Status = types.StringValue(submitted.Status)
		resp.Diagnostics.Append(resp.State.Set(context.WithoutCancel(ctx), &d)...)
	}

	if submitErr != nil {
		resp.Diagnostics.AddError("Unable to Confirm VM Restore Submission", submitErr.Error())
		return
	}
	if submitted.Password != "" {
		d.Password = types.StringValue(submitted.Password)
	}
	d.Status = types.StringValue(submitted.Status)
	resp.Diagnostics.Append(resp.State.Set(context.WithoutCancel(ctx), &d)...)
	if submitted.Status == "UNVERIFIED" || submitted.Status == "RESERVED" {
		resp.Diagnostics.AddError("VM Restore Submission Unverified", "The VM ID is retained in state. No new restore will be submitted; reconcile this operation before continuing.")
		return
	}
	v, err := r.wait(ctx, out.VmID, opts)
	if v != nil {
		d.Status = types.StringValue(v.Status)
		resp.Diagnostics.Append(resp.State.Set(context.WithoutCancel(ctx), &d)...)
	}
	if err != nil {
		resp.Diagnostics.AddError("VM Backup Restore Incomplete", err.Error())
	}
}
func (r *VmFromBackupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var d vmFromBackupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &d)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, ds := d.Timeouts.Read(ctx, 5*time.Minute)
	resp.Diagnostics.Append(ds...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	operation, operationErr := r.client.GetVmBackupRestore(ctx, d.BackupID.ValueInt64(), d.ID.ValueInt64(), r.opts(d))
	if operationErr != nil && !client.IsAPIError(operationErr, 601) {
		resp.Diagnostics.AddError("Unable to Read VM Restore Identity", operationErr.Error())
		return
	}
	if operation != nil && (operation.Status == "UNVERIFIED" || operation.Status == "RESERVED") {
		d.Status = types.StringValue(operation.Status)
		resp.Diagnostics.Append(resp.State.Set(ctx, &d)...)
		resp.Diagnostics.AddWarning("VM Restore Submission Unverified", "The known VM reservation is retained. This operation requires reconciliation before its state can be refreshed.")
		return
	}
	v, err := r.client.GetVmStatus(ctx, d.ID.ValueInt64(), r.opts(d))
	if err != nil {
		if client.IsAPIError(err, 601) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to Read Restored VM", err.Error())
		return
	}
	d.Status = types.StringValue(v.Status)
	if d.RestartPending.IsNull() || d.RestartPending.IsUnknown() {
		d.RestartPending = types.BoolValue(false)
	}
	d.Name = types.StringValue(v.Name)
	d.CPU = types.Int64Value(v.CPUCores)
	d.RAM = types.Int64Value(v.RAM)
	// Null and empty descriptions are interchangeable in the VM API; retain an omitted description.
	if !d.Description.IsNull() || v.Description != "" {
		d.Description = types.StringValue(v.Description)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &d)...)
}
func (r *VmFromBackupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state, plan vmFromBackupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, ds := plan.Timeouts.Update(ctx, vmDefaultUpdateTime)
	resp.Diagnostics.Append(ds...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	opts := r.opts(state)
	id := state.ID.ValueInt64()
	operation, operationErr := r.client.GetVmBackupRestore(ctx, state.BackupID.ValueInt64(), id, opts)
	if operationErr != nil {
		resp.Diagnostics.AddError("Unable to Verify Restored VM Identity", operationErr.Error())
		return
	}
	if operation.Status == "RESERVED" || operation.Status == "UNVERIFIED" {
		resp.Diagnostics.AddError("VM Restore Incomplete", "Reconcile the known restore operation before updating this VM.")
		return
	}
	// Preserve the owned VM and generated credentials across every metadata/resource update.
	plan.ID = state.ID
	plan.Password = state.Password
	plan.Region = state.Region
	plan.Project = state.Project
	if state.RestartPending.ValueBool() {
		if err := resumePendingVM(ctx, r.client, id, opts, (&VmResource{poll: r.poll}).pollInterval()); err != nil {
			resp.Diagnostics.AddError("Unable to Restart Restored VM", err.Error())
			return
		}
	}
	plan.RestartPending = types.BoolValue(false)
	current, err := r.client.GetVmStatus(ctx, id, opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Restored VM for Update", err.Error())
		return
	}
	checkpoint := state
	checkpoint.Timeouts = plan.Timeouts
	checkpoint.RestartPending = types.BoolValue(false)
	checkpoint.Status = types.StringValue(current.Status)
	checkpoint.Name = types.StringValue(current.Name)
	checkpoint.CPU = types.Int64Value(current.CPUCores)
	checkpoint.RAM = types.Int64Value(current.RAM)
	if !checkpoint.Description.IsNull() || current.Description != "" {
		checkpoint.Description = types.StringValue(current.Description)
	}
	if current.Name != plan.Name.ValueString() {
		if err := r.client.RenameVm(ctx, id, client.RenameVmRequest{Name: plan.Name.ValueString()}, opts); err != nil {
			resp.Diagnostics.AddError("Unable to Rename Restored VM", err.Error())
			return
		}

		checkpoint.Name = plan.Name
	}
	if current.Description != plan.Description.ValueString() {
		if err := r.client.UpdateVmDescription(ctx, id, plan.Description.ValueString(), opts); err != nil {
			resp.Diagnostics.AddError("Unable to Update Restored VM Description", err.Error())
			return
		}

		checkpoint.Description = plan.Description
	}
	if current.CPUCores != plan.CPU.ValueInt64() || current.RAM != plan.RAM.ValueInt64() {
		lifecycle := &VmResource{client: r.client, poll: r.poll}
		old := VmResourceModel{CPUCores: types.Int64Value(current.CPUCores), RAM: types.Int64Value(current.RAM)}
		desired := VmResourceModel{CPUCores: plan.CPU, RAM: plan.RAM}
		// The shared stopped-update helper submits absolute CPU/RAM values. Preserve restart intent
		// even if apply fails after stop: the next apply resumes this same VM before changing it.
		restart := false
		needsRestart, stopErr := lifecycle.stopIfRunning(ctx, id, opts)
		if stopErr != nil {
			if current.Status == "RUNNING" {
				checkpoint.RestartPending = types.BoolValue(true)
				resp.Diagnostics.Append(resp.State.Set(context.WithoutCancel(ctx), &checkpoint)...)
			}
			resp.Diagnostics.AddError("Unable to Stop Restored VM", stopErr.Error())
			return
		}
		if needsRestart {
			checkpoint.Status = types.StringValue("STOPPED")
		}
		if failed := lifecycle.updateStopped(ctx, id, &old, &desired, opts); failed != nil {
			resp.Diagnostics.AddError(failed.summary, failed.err.Error())
		} else {
			checkpoint.CPU = plan.CPU
			checkpoint.RAM = plan.RAM
		}
		if needsRestart {
			startCtx, startCancel := reserveContext(ctx)
			if startErr := lifecycle.startAndWait(startCtx, id, opts); startErr != nil {
				restart = true
				resp.Diagnostics.AddError("Unable to Restart Restored VM", startErr.Error())
			} else {
				checkpoint.Status = types.StringValue("RUNNING")
			}
			startCancel()
		}
		checkpoint.RestartPending = types.BoolValue(restart)
	}
	readCtx, readCancel := readBackContext(ctx)
	defer readCancel()
	actual, readErr := r.client.GetVmStatus(readCtx, id, opts)
	if readErr != nil {
		resp.Diagnostics.AddError("Unable to Read Restored VM After Update", readErr.Error())
	} else {
		checkpoint.Status = types.StringValue(actual.Status)
		checkpoint.Name = types.StringValue(actual.Name)
		checkpoint.CPU = types.Int64Value(actual.CPUCores)
		checkpoint.RAM = types.Int64Value(actual.RAM)
		if !plan.Description.IsNull() || actual.Description != "" {
			checkpoint.Description = types.StringValue(actual.Description)
		}
	}
	resp.Diagnostics.Append(resp.State.Set(context.WithoutCancel(ctx), &checkpoint)...)
}
func (r *VmFromBackupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var d vmFromBackupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &d)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, ds := d.Timeouts.Delete(ctx, time.Hour)
	resp.Diagnostics.Append(ds...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cancelled, cancelErr := r.client.CancelVmBackupRestoreReservation(ctx, d.BackupID.ValueInt64(), d.ID.ValueInt64(), r.opts(d))
	if cancelErr != nil && !client.IsAPIError(cancelErr, 601) {
		resp.Diagnostics.AddError("Unable to Verify VM Restore Before Deletion", cancelErr.Error())
		return
	}
	if cancelled {
		return
	}
	if err := r.client.DeleteVm(ctx, d.ID.ValueInt64(), r.opts(d)); err != nil && !client.IsAPIError(err, 601) {
		resp.Diagnostics.AddError("Unable to Delete Restored VM", err.Error())
		return
	}
	// The VM delete API already polls deletion; confirm the stable status endpoint too.
	if _, err := r.client.GetVmStatus(ctx, d.ID.ValueInt64(), r.opts(d)); err == nil {
		resp.Diagnostics.AddError("Deletion Not Confirmed", "The restored VM is still present; state is retained.")
	} else if !client.IsAPIError(err, 601) {
		resp.Diagnostics.AddError("Unable to Verify Restored VM Deletion", err.Error())
	}
}
func (r *VmFromBackupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, ":")
	if len(parts) != 2 {
		resp.Diagnostics.AddError("Invalid Import ID", "Use <backup_id>:<vm_id>.")
		return
	}
	backupID, e1 := strconv.ParseInt(parts[0], 10, 64)
	id, e2 := strconv.ParseInt(parts[1], 10, 64)
	if e1 != nil || e2 != nil || backupID < 1 || id < 1 {
		resp.Diagnostics.AddError("Invalid Import ID", "Both IDs must be positive integers.")
		return
	}
	for k, v := range map[string]any{"id": id, "backup_id": backupID, "region": r.client.Region, "project_tag": r.client.ProjectTag, "restart_pending": false} {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(k), v)...)
	}
}
