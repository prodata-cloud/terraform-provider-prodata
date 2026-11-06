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
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = &vmProtectionResource{}
	_ resource.ResourceWithConfigure   = &vmProtectionResource{}
	_ resource.ResourceWithImportState = &vmProtectionResource{}
)

type vmProtectionResource struct {
	client *client.Client
	kind   string
	poll   time.Duration
}

func NewVmBackupResource() resource.Resource   { return &vmProtectionResource{kind: "backups"} }
func NewVmSnapshotResource() resource.Resource { return &vmProtectionResource{kind: "snapshots"} }
func NewVmBackupScheduleResource() resource.Resource {
	return &vmProtectionResource{kind: "backup-schedules"}
}
func (r *vmProtectionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	suffix := map[string]string{"backups": "vm_backup", "snapshots": "vm_snapshot", "backup-schedules": "vm_backup_schedule"}[r.kind]
	resp.TypeName = req.ProviderTypeName + "_" + suffix
}
func (r *vmProtectionResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	mutable := []planmodifier.String(nil)
	if r.kind != "backup-schedules" {
		mutable = replace
	}
	namePattern := "^[A-Za-z0-9_.-]{3,63}$"
	if r.kind == "snapshots" {
		namePattern = "^[A-Za-z0-9_.]{3,63}$"
	}
	a := map[string]schema.Attribute{
		"id":          schema.Int64Attribute{Computed: true, MarkdownDescription: "Persistent panel object ID.", PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}},
		"vm_id":       schema.Int64Attribute{Required: true, MarkdownDescription: "Source VM ID in the selected project and region.", Validators: []validator.Int64{int64validator.AtLeast(1)}, PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
		"name":        schema.StringAttribute{Required: true, MarkdownDescription: "Name unique within this VM and object kind. Existing objects require explicit import.", Validators: []validator.String{stringvalidator.RegexMatches(regexp.MustCompile(namePattern), "Use 3–63 letters, digits, underscores or periods; backup names also allow hyphens.")}, PlanModifiers: mutable},
		"status":      schema.StringAttribute{Computed: true, MarkdownDescription: "Latest asynchronous operation status reported by the platform."},
		"region":      schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Region override; defaults to the provider region.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()}},
		"project_tag": schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Project override; defaults to the provider project.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()}},
		"timeouts":    timeouts.Attributes(ctx, timeouts.Opts{Create: true, Read: true, Update: true, Delete: true}),
	}
	if r.kind == "snapshots" {
		a["include_ram"] = schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), MarkdownDescription: "Include the running VM's memory. Changing this creates a new snapshot.", PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()}}
	} else {
		a["mode"] = schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("snapshot"), MarkdownDescription: "Backup mode. The current backup service supports snapshot only.", Validators: []validator.String{stringvalidator.OneOf("snapshot")}, PlanModifiers: mutable}
	}
	if r.kind == "backup-schedules" {
		a["cron"] = schema.StringAttribute{Required: true, MarkdownDescription: "Six-field Spring cron expression (seconds first), interpreted in Asia/Tashkent. Retention follows the platform quota.", Validators: []validator.String{stringvalidator.RegexMatches(regexp.MustCompile(`^\S+(\s+\S+){5}$`), "Use a six-field Spring cron expression, including seconds.")}}
	}
	resp.Schema = schema.Schema{MarkdownDescription: "Manages VM " + r.kind + " using the project-scoped v2 API. Backup and snapshot creation wait for completion; destroying a schedule preserves existing backups.", Attributes: a}
}
func (r *vmProtectionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	var ok bool
	r.client, ok = req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Resource Configure Type", fmt.Sprintf("Expected *client.Client, got %T.", req.ProviderData))
	}
}

type protectionGetter interface {
	GetAttribute(context.Context, path.Path, any) diag.Diagnostics
}
type protectionData struct {
	ID, VMID                                  types.Int64
	Name, Region, Project, Status, Mode, Cron types.String
	IncludeRAM                                types.Bool
	Timeouts                                  timeouts.Value
}

func (r *vmProtectionResource) load(ctx context.Context, source protectionGetter) (protectionData, diag.Diagnostics) {
	var d protectionData
	var ds diag.Diagnostics
	fields := map[string]any{"id": &d.ID, "vm_id": &d.VMID, "name": &d.Name, "region": &d.Region, "project_tag": &d.Project, "status": &d.Status, "timeouts": &d.Timeouts}
	if r.kind == "snapshots" {
		fields["include_ram"] = &d.IncludeRAM
	} else {
		fields["mode"] = &d.Mode
	}
	if r.kind == "backup-schedules" {
		fields["cron"] = &d.Cron
	}
	for k, v := range fields {
		ds.Append(source.GetAttribute(ctx, path.Root(k), v)...)
	}
	return d, ds
}
func (r *vmProtectionResource) opts(d protectionData) *client.RequestOpts {
	region, project := d.Region.ValueString(), d.Project.ValueString()
	if region == "" {
		region = r.client.Region
	}
	if project == "" {
		project = r.client.ProjectTag
	}
	return &client.RequestOpts{Region: region, ProjectTag: project}
}
func (r *vmProtectionResource) request(d protectionData) client.VmProtectionRequest {
	out := client.VmProtectionRequest{Name: d.Name.ValueString(), Mode: d.Mode.ValueString(), Cron: d.Cron.ValueString()}
	if r.kind == "snapshots" {
		v := d.IncludeRAM.ValueBool()
		out.IncludeRAM = &v
	}
	return out
}
func (r *vmProtectionResource) store(ctx context.Context, s *tfsdk.State, d protectionData, v *client.VmProtection) diag.Diagnostics {
	var ds diag.Diagnostics
	o := r.opts(d)
	fields := map[string]any{"id": v.ID, "vm_id": v.VmID, "name": v.Name, "status": v.Status, "region": o.Region, "project_tag": o.ProjectTag}
	if r.kind == "snapshots" {
		fields["include_ram"] = v.IncludeRAM
	} else {
		fields["mode"] = v.Mode
	}
	if r.kind == "backup-schedules" {
		fields["cron"] = v.Cron
	}
	for k, val := range fields {
		ds.Append(s.SetAttribute(ctx, path.Root(k), val)...)
	}
	return ds
}
func (r *vmProtectionResource) wait(ctx context.Context, d protectionData) (*client.VmProtection, error) {
	delay := r.poll
	if delay == 0 {
		delay = 5 * time.Second
	}
	for {
		v, err := r.client.GetVmProtection(ctx, d.VMID.ValueInt64(), r.kind, d.ID.ValueInt64(), r.opts(d))
		if err != nil {
			return nil, err
		}
		if !v.Present {
			return v, fmt.Errorf("VM protection object %d disappeared before completion", v.ID)
		}
		if v.Status == "SUCCESS" || (r.kind == "backup-schedules" && v.Status == "SCHEDULE") {
			return v, nil
		}
		switch v.Status {
		case "NEW", "SCHEDULE", "SCHEDULED":
		default:
			return v, fmt.Errorf("VM protection operation ended with status %s; its ID remains in state", v.Status)
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
func (r *vmProtectionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	d, ds := r.load(ctx, req.Plan)
	resp.Diagnostics.Append(ds...)
	if resp.Diagnostics.HasError() {
		return
	}
	duration, ds := d.Timeouts.Create(ctx, time.Hour)
	resp.Diagnostics.Append(ds...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	v, err := r.client.SaveVmProtection(ctx, d.VMID.ValueInt64(), r.kind, 0, r.request(d), r.opts(d))
	if err != nil {
		resp.Diagnostics.AddError("Unable to Create VM Protection Object", err.Error())
		return
	}
	resp.State.Raw = req.Plan.Raw
	d.ID = types.Int64Value(v.ID)
	resp.Diagnostics.Append(r.store(ctx, &resp.State, d, v)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.kind != "backup-schedules" {
		submitted, submitErr := r.client.SubmitVmProtection(ctx, d.VMID.ValueInt64(), r.kind, d.ID.ValueInt64(), r.opts(d))
		if submitErr != nil {
			// A lost acknowledgement is reconciled only by the ID already recorded in state.
			// Never look up by name and never issue a second create request.
			submitted, submitErr = r.client.GetVmProtection(ctx, d.VMID.ValueInt64(), r.kind, d.ID.ValueInt64(), r.opts(d))
		}
		if submitted != nil {
			resp.Diagnostics.Append(r.store(context.WithoutCancel(ctx), &resp.State, d, submitted)...)
		}
		if submitErr != nil {
			resp.Diagnostics.AddError("VM Protection Submission Unconfirmed", submitErr.Error())
			return
		}
	}
	v, err = r.wait(ctx, d)
	if v != nil {
		resp.Diagnostics.Append(r.store(context.WithoutCancel(ctx), &resp.State, d, v)...)
	}
	if err != nil {
		resp.Diagnostics.AddError("VM Protection Operation Incomplete", err.Error())
	}
}
func (r *vmProtectionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	d, ds := r.load(ctx, req.State)
	resp.Diagnostics.Append(ds...)
	if resp.Diagnostics.HasError() {
		return
	}
	duration, ds := d.Timeouts.Read(ctx, 5*time.Minute)
	resp.Diagnostics.Append(ds...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	v, err := r.client.GetVmProtection(ctx, d.VMID.ValueInt64(), r.kind, d.ID.ValueInt64(), r.opts(d))
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read VM Protection Object", err.Error())
		return
	}
	if !v.Present {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(r.store(ctx, &resp.State, d, v)...)
	if v.Status == "UNVERIFIED" || v.Status == "RESERVED" {
		resp.Diagnostics.AddError("VM Protection Submission Unverified", "The durable reservation is retained. The platform cannot confirm the upstream submit; it will not queue another job or delete this ownership record. Reconcile the operation before continuing.")
	}
}
func (r *vmProtectionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if r.kind != "backup-schedules" {
		resp.Diagnostics.AddError("Replacement Required", "Backup and snapshot specifications are immutable.")
		return
	}
	d, ds := r.load(ctx, req.Plan)
	resp.Diagnostics.Append(ds...)
	if resp.Diagnostics.HasError() {
		return
	}
	duration, ds := d.Timeouts.Update(ctx, 5*time.Minute)
	resp.Diagnostics.Append(ds...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	v, err := r.client.SaveVmProtection(ctx, d.VMID.ValueInt64(), r.kind, d.ID.ValueInt64(), r.request(d), r.opts(d))
	if err != nil {
		resp.Diagnostics.AddError("Unable to Update Backup Schedule", err.Error())
		return
	}
	resp.State.Raw = req.Plan.Raw
	resp.Diagnostics.Append(r.store(ctx, &resp.State, d, v)...)
}
func (r *vmProtectionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	d, ds := r.load(ctx, req.State)
	resp.Diagnostics.Append(ds...)
	if resp.Diagnostics.HasError() {
		return
	}
	duration, ds := d.Timeouts.Delete(ctx, time.Hour)
	resp.Diagnostics.Append(ds...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	v, err := r.client.GetVmProtection(ctx, d.VMID.ValueInt64(), r.kind, d.ID.ValueInt64(), r.opts(d))
	if err != nil {
		resp.Diagnostics.AddError("Unable to Verify VM Protection Object", err.Error())
		return
	}
	if !v.Present {
		return
	}
	if v.Status == "UNVERIFIED" {
		resp.Diagnostics.AddError("VM Protection Submission Unverified", "The operation may already exist upstream. Reconcile it before deletion; its ownership record is retained.")
		return
	}
	if v.Status == "NEW" || (r.kind != "backup-schedules" && (v.Status == "SCHEDULE" || v.Status == "SCHEDULED")) {
		settled, waitErr := r.wait(ctx, d)
		if settled != nil && !settled.Present {
			return
		}
		if waitErr != nil && (settled == nil || settled.Status == "NEW" || settled.Status == "SCHEDULE" || settled.Status == "SCHEDULED") {
			resp.Diagnostics.AddError("Unable to Wait Before Deletion", waitErr.Error())
			return
		}
		// A confirmed terminal failed job can be deleted too. Its failure is not a failed delete.
	}
	if err = r.client.DeleteVmProtection(ctx, d.VMID.ValueInt64(), r.kind, d.ID.ValueInt64(), r.opts(d)); err != nil {
		resp.Diagnostics.AddError("Unable to Delete VM Protection Object", err.Error())
		return
	}
	v, err = r.client.GetVmProtection(ctx, d.VMID.ValueInt64(), r.kind, d.ID.ValueInt64(), r.opts(d))
	if err != nil {
		resp.Diagnostics.AddError("Unable to Confirm Deletion", err.Error())
		return
	}
	if v.Present {
		resp.Diagnostics.AddError("Deletion Not Confirmed", "The API still reports this object; Terraform state is retained.")
	}
}
func (r *vmProtectionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, ":")
	if len(parts) != 2 {
		resp.Diagnostics.AddError("Invalid Import ID", "Use <vm_id>:<object_id>.")
		return
	}
	vmID, e1 := strconv.ParseInt(parts[0], 10, 64)
	id, e2 := strconv.ParseInt(parts[1], 10, 64)
	if e1 != nil || e2 != nil || vmID < 1 || id < 1 {
		resp.Diagnostics.AddError("Invalid Import ID", "Both IDs must be positive integers.")
		return
	}
	for k, v := range map[string]any{"id": id, "vm_id": vmID, "region": r.client.Region, "project_tag": r.client.ProjectTag} {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(k), v)...)
	}
}
