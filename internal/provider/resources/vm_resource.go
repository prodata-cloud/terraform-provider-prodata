package resources

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"terraform-provider-prodata/internal/client"
	"terraform-provider-prodata/internal/tfutil"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
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
	_ resource.Resource                = &VmResource{}
	_ resource.ResourceWithConfigure   = &VmResource{}
	_ resource.ResourceWithModifyPlan  = &VmResource{}
	_ resource.ResourceWithImportState = &VmResource{}
)

type VmResource struct {
	client *client.Client
}

type VmResourceModel struct {
	ID             types.Int64  `tfsdk:"id"`
	Guid           types.String `tfsdk:"guid"`
	Region         types.String `tfsdk:"region"`
	ProjectTag     types.String `tfsdk:"project_tag"`
	Name           types.String `tfsdk:"name"`
	ImageID        types.Int64  `tfsdk:"image_id"`
	ImageName      types.String `tfsdk:"image_name"`
	ImageSlug      types.String `tfsdk:"image_slug"`
	CPUCores       types.Int64  `tfsdk:"cpu_cores"`
	RAM            types.Int64  `tfsdk:"ram"`
	DiskSize       types.Int64  `tfsdk:"disk_size"`
	DiskType       types.String `tfsdk:"disk_type"`
	LocalNetworkID types.Int64  `tfsdk:"local_network_id"`
	PrivateIP      types.String `tfsdk:"private_ip"`
	PublicIPID     types.Int64  `tfsdk:"public_ip_id"`
	PublicIP       types.String `tfsdk:"public_ip"`
	Password       types.String `tfsdk:"password"`
	SSHPublicKey   types.String `tfsdk:"ssh_public_key"`
	Description    types.String `tfsdk:"description"`
	// UserData is write-only: read from config at create, never stored in state. Change
	// detection is provider-computed (sha256 in private state), so there is no hash field.
	UserData types.String   `tfsdk:"user_data"`
	Status   types.String   `tfsdk:"status"`
	Timeouts timeouts.Value `tfsdk:"timeouts"`
}

func NewVmResource() resource.Resource {
	return &VmResource{}
}

func (r *VmResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm"
}

func (r *VmResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a ProData virtual machine.",

		Attributes: map[string]schema.Attribute{
			"id": schema.Int64Attribute{
				MarkdownDescription: "The unique identifier of the virtual machine.",
				Computed:            true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"guid": schema.StringAttribute{
				MarkdownDescription: "The VM's globally-unique identifier assigned by the panel. " +
					"Use this to reference the VM as a load balancer backend (`prodata_lb.backend_group.vm_ids`).",
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"region": schema.StringAttribute{
				MarkdownDescription: "Region ID override. If not specified, uses the provider's default region.",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"project_tag": schema.StringAttribute{
				MarkdownDescription: "Project tag where the VM will be created. If not specified, uses the provider's default project_tag.",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The name of the virtual machine. Must be 3-63 characters, contain at least one letter, only letters, numbers, and hyphens.",
				Required:            true,
			},
			"image_id": schema.Int64Attribute{
				MarkdownDescription: "The ID of the image to use for the virtual machine.",
				Required:            true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"image_name": schema.StringAttribute{
				MarkdownDescription: "The name of the OS image (e.g., 'Ubuntu 22.04'). Populated from the API.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"image_slug": schema.StringAttribute{
				MarkdownDescription: "The slug of the OS template (e.g., 'ubuntu-22.04'). Null for custom images and VMs created before this feature.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"cpu_cores": schema.Int64Attribute{
				MarkdownDescription: "The number of CPU cores for the virtual machine. Minimum 1. Changing this forces a VM reboot.",
				Required:            true,
			},
			"ram": schema.Int64Attribute{
				MarkdownDescription: "The amount of RAM in GB for the virtual machine. Minimum 1. Changing this forces a VM reboot.",
				Required:            true,
			},
			"disk_size": schema.Int64Attribute{
				MarkdownDescription: "The size of the disk in GB. Minimum 10. Can only be increased. Changing this forces a VM reboot.",
				Required:            true,
			},
			"disk_type": schema.StringAttribute{
				MarkdownDescription: "The type of disk (HDD, SSD, or NVME). Can only be upgraded (e.g. HDD -> SSD). Changing this forces a VM reboot.",
				Required:            true,
			},
			"local_network_id": schema.Int64Attribute{
				MarkdownDescription: "The ID of the local network to attach the VM to.",
				Required:            true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"private_ip": schema.StringAttribute{
				MarkdownDescription: "The private IP address for the virtual machine. If not specified, an available IP will be auto-assigned from the local network.",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"public_ip_id": schema.Int64Attribute{
				MarkdownDescription: "The ID of the public IP to attach to the VM (optional). Changing this forces a new resource.",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"public_ip": schema.StringAttribute{
				MarkdownDescription: "The public IP address assigned to the virtual machine (if any).",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"password": schema.StringAttribute{
				MarkdownDescription: "The password for the virtual machine. Required when creating. Write-only: not read back from API.",
				Optional:            true,
				Sensitive:           true,
				PlanModifiers: []planmodifier.String{
					WriteOnceString(),
				},
			},
			"ssh_public_key": schema.StringAttribute{
				MarkdownDescription: "SSH public key for authentication (optional). Write-only: not read back from API.",
				Optional:            true,
				PlanModifiers: []planmodifier.String{
					WriteOnceString(),
				},
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Description of the virtual machine (optional).",
				Optional:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"user_data": schema.StringAttribute{
				MarkdownDescription: "Cloud-init user data applied at first boot via a NoCloud ISO. " +
					"Must begin with `#cloud-config` or a shebang (`#!`) and not exceed 64 KiB. " +
					"**Write-only**: the raw value is never stored in Terraform state nor shown in a " +
					"plan (this requires Terraform >= 1.11). The provider detects changes by hashing " +
					"the payload (sha256) and **replaces** the VM when it changes (cloud-init only runs " +
					"on first boot, and the API accepts user_data only at create time) — you do NOT set " +
					"a hash yourself. Note: a cloud-init failure inside the guest is not reported back " +
					"by the API, so a successful apply does not by itself prove the script ran without errors.",
				Optional:  true,
				WriteOnly: true,
				Validators: []validator.String{
					stringvalidator.LengthAtMost(65536),
					UserDataPrefix(),
				},
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "The current status of the virtual machine.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{
				Create: true,
			}),
		},
	}
}

func (r *VmResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	// Destroying — nothing to plan.
	if req.Plan.Raw.IsNull() {
		return
	}

	var planData VmResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &planData)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Creating — no replacement analysis; Create seeds the user_data baseline.
	if req.State.Raw.IsNull() {
		return
	}

	var stateData VmResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &stateData)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// user_data is write-only (null in plan and state), so it cannot be diffed directly.
	// Read it from the config, hash it, and compare against the sha256 baseline stored in
	// private state at the last Create. A write-only attribute can only force a diff by
	// being appended to RequiresReplace here.
	var configData VmResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &configData)...)
	if resp.Diagnostics.HasError() {
		return
	}
	udState := classifyUserData(configData.UserData)
	var hexNow string
	if udState == userDataPresent {
		hexNow = userDataHashHex(configData.UserData.ValueString())
	}
	storedBlob, diags := req.Private.GetKey(ctx, userDataHashPrivateKey)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	storedHash, storedOK := unmarshalUserDataHash(storedBlob)

	userDataReplace := userDataReplaceNeeded(storedHash, storedOK, udState, hexNow)
	if userDataReplace {
		resp.RequiresReplace = append(resp.RequiresReplace, path.Root("user_data"))
		resp.Diagnostics.AddWarning(
			"user_data change replaces the virtual machine",
			"The user_data payload changed, so this VM will be destroyed and recreated to re-run "+
				"cloud-init at first boot (cloud-init only runs once, and the API accepts user_data "+
				"only at create). A cloud-init failure on the guest is not reported by the API, so a "+
				"successful apply does not by itself prove the new script ran without errors.",
		)
	}

	// Determine whether ANY attribute forces replacement, to warn about the
	// create_before_destroy + same-name uniqueness constraint. The image/network/etc.
	// replacements are driven by their own RequiresReplace plan modifiers; we mirror the
	// readable ones here (write-only password/ssh are null in state post-import, so are
	// excluded) plus the user_data signal computed above.
	requiresReplace := userDataReplace ||
		!stateData.ImageID.Equal(planData.ImageID) ||
		!stateData.LocalNetworkID.Equal(planData.LocalNetworkID) ||
		!stateData.PrivateIP.Equal(planData.PrivateIP) ||
		!stateData.Region.Equal(planData.Region) ||
		!stateData.ProjectTag.Equal(planData.ProjectTag) ||
		!stateData.Description.Equal(planData.Description) ||
		(!stateData.PublicIPID.IsNull() && !stateData.PublicIPID.Equal(planData.PublicIPID))
	if !requiresReplace {
		return
	}

	// Warn that create_before_destroy will fail due to VM name uniqueness constraint.
	if stateData.Name.Equal(planData.Name) {
		resp.Diagnostics.AddWarning(
			"Resource replacement with same name",
			"This VM is being replaced but the name is unchanged. If you are using "+
				"lifecycle { create_before_destroy = true }, the existing VM will be automatically "+
				"renamed to allow the new VM to be created with the original name.",
		)
	}
}

func (r *VmResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	r.client = c
}

const (
	vmPollInterval = 5 * time.Second
	// vmDefaultCreateTime is a CEILING, not a fixed wait: the provider polls and returns
	// as soon as the VM is ready, so the common Linux path (~10-12m) is unaffected by a
	// higher value. The 30m default covers the longer in-guest cloud-init wait on Windows
	// guests (~1200s) plus the subsequent stop / detach-ISO / restart cycle and polling
	// slack. Overridable via the timeouts{} block.
	vmDefaultCreateTime = 30 * time.Minute
)

// waitForVmReady polls the VM until it reaches a terminal state (RUNNING, STOPPED, or ERROR).
// Tolerates up to 3 consecutive transient polling errors. The overall deadline is carried by
// ctx (the caller wraps it with context.WithTimeout using the timeouts{} create value), so a
// VM whose first boot runs a long cloud-init is bounded by that single timeout.
//
// Note: the backend does not surface a cloud-init failure as VM status ERROR — a VM whose
// cloud-init failed still reports RUNNING — so reaching RUNNING here does not prove the
// user_data script succeeded.
func (r *VmResource) waitForVmReady(ctx context.Context, vmID int64, opts *client.RequestOpts, expectCPU, expectRAM, expectDisk int64) (*client.Vm, error) {
	const (
		maxConsecutiveErrs = 3
		// maxSettleAttempts bounds how long we wait, after the VM is already RUNNING/
		// STOPPED, for the readback cpu/ram/disk to match the request before returning.
		// It eliminates the brief post-create transient where the endpoint reports an
		// intermediate value (which a racing refresh would surface as a spurious diff).
		// Non-convergence is never fatal — we proceed once this budget is spent.
		maxSettleAttempts = 6
	)

	consecutiveErrs := 0
	settleAttempts := 0

	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("timed out waiting for VM %d to become ready: %w", vmID, err)
		}

		vm, err := r.client.GetVmStatus(ctx, vmID, opts)
		if err != nil {
			consecutiveErrs++
			tflog.Warn(ctx, "Transient error polling VM status", map[string]any{
				"id":                 vmID,
				"error":              err.Error(),
				"consecutive_errors": consecutiveErrs,
			})
			if consecutiveErrs >= maxConsecutiveErrs {
				return nil, fmt.Errorf("polling VM %d status: %w (after %d consecutive failures)", vmID, err, consecutiveErrs)
			}
		} else {
			consecutiveErrs = 0

			tflog.Debug(ctx, "Polling VM status", map[string]any{
				"id":     vmID,
				"status": vm.Status,
			})

			switch vm.Status {
			case "RUNNING", "STOPPED":
				// Settle: wait (bounded) for the readback to match the requested sizing so
				// the first refresh doesn't catch a transient intermediate value. Skip the
				// check when no expectation was given; never fail on non-convergence.
				if (expectCPU == 0 && expectRAM == 0 && expectDisk == 0) ||
					(vm.CPUCores == expectCPU && vm.RAM == expectRAM && vm.DiskSize >= expectDisk) {
					return vm, nil
				}
				settleAttempts++
				if settleAttempts >= maxSettleAttempts {
					tflog.Warn(ctx, "VM reached terminal status but cpu/ram/disk did not settle to the requested values within the settle window; proceeding", map[string]any{
						"id":       vmID,
						"want_cpu": expectCPU, "got_cpu": vm.CPUCores,
						"want_ram": expectRAM, "got_ram": vm.RAM,
						"want_disk": expectDisk, "got_disk": vm.DiskSize,
					})
					return vm, nil
				}
			case "ERROR":
				return vm, fmt.Errorf("VM creation failed (id=%d, status=ERROR)", vmID)
			}
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("timed out waiting for VM %d to become ready: %w", vmID, ctx.Err())
		case <-time.After(vmPollInterval):
		}
	}
}

func (r *VmResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data VmResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Validate password is provided (Required for creation, Optional for import)
	if data.Password.IsNull() || data.Password.ValueString() == "" {
		resp.Diagnostics.AddError(
			"Password Required",
			"The password attribute is required when creating a new virtual machine.",
		)
		return
	}

	// Bound the entire create — the async create call plus the subsequent polling that
	// waits out the backend's in-guest cloud-init wait — by the create timeout (default
	// 30m, overridable via the timeouts{} block).
	createTimeout, diags := data.Timeouts.Create(ctx, vmDefaultCreateTime)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()

	// Use provider defaults if not specified in resource
	region := data.Region.ValueString()
	if region == "" {
		region = r.client.Region
	}
	projectTag := data.ProjectTag.ValueString()
	if projectTag == "" {
		projectTag = r.client.ProjectTag
	}

	createReq := client.CreateVmRequest{
		Region:         region,
		ProjectTag:     projectTag,
		Name:           data.Name.ValueString(),
		ImageID:        data.ImageID.ValueInt64(),
		CPUCores:       data.CPUCores.ValueInt64(),
		RAM:            data.RAM.ValueInt64(),
		DiskSize:       data.DiskSize.ValueInt64(),
		DiskType:       data.DiskType.ValueString(),
		LocalNetworkID: data.LocalNetworkID.ValueInt64(),
		Password:       data.Password.ValueString(),
	}

	if !data.PrivateIP.IsNull() && !data.PrivateIP.IsUnknown() {
		privateIP := data.PrivateIP.ValueString()
		createReq.PrivateIP = &privateIP
	}

	if !data.PublicIPID.IsNull() && !data.PublicIPID.IsUnknown() {
		publicIPID := data.PublicIPID.ValueInt64()
		createReq.PublicIPID = &publicIPID
	}

	if !data.SSHPublicKey.IsNull() && !data.SSHPublicKey.IsUnknown() {
		sshKey := data.SSHPublicKey.ValueString()
		createReq.SSHPublicKey = &sshKey
	}

	if !data.Description.IsNull() && !data.Description.IsUnknown() {
		desc := data.Description.ValueString()
		createReq.Description = &desc
	}

	// user_data is write-only: it is null in the plan, so read it from the config and
	// forward it. It is never written back to state (enforced before State.Set below).
	var configData VmResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &configData)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !configData.UserData.IsNull() && !configData.UserData.IsUnknown() {
		createReq.UserData = configData.UserData.ValueStringPointer()
	}

	tflog.Debug(ctx, "Creating virtual machine", map[string]any{
		"name":             createReq.Name,
		"region":           createReq.Region,
		"project_tag":      createReq.ProjectTag,
		"image_id":         createReq.ImageID,
		"cpu_cores":        createReq.CPUCores,
		"ram":              createReq.RAM,
		"disk_size":        createReq.DiskSize,
		"disk_type":        createReq.DiskType,
		"local_network_id": createReq.LocalNetworkID,
		"private_ip":       createReq.PrivateIP,
	})

	createVm := func() (*client.Vm, error) {
		return client.RetryOnBusy(ctx, client.RetryTimeoutLong, func() (*client.Vm, error) {
			return r.client.CreateVm(ctx, createReq)
		})
	}

	vm, err := createVm()

	// Error 666: name conflict — likely a create_before_destroy replacement.
	// Rename the existing VM, then retry.
	if err != nil && client.IsAPIError(err, 666) {
		tflog.Info(ctx, "VM name conflict detected, attempting to rename existing VM", map[string]any{
			"name": createReq.Name,
		})

		opts := &client.RequestOpts{Region: region, ProjectTag: projectTag}
		vms, listErr := r.client.GetVms(ctx, opts)
		if listErr != nil {
			// Recovery needs the conflicting VM's id; without the list we cannot
			// rename it, so the original 666 surfaces below. Make the skipped
			// recovery visible rather than silent.
			tflog.Warn(ctx, "Name conflict (666) but listing VMs to locate the conflict failed; cannot auto-recover", map[string]any{
				"name":  createReq.Name,
				"error": listErr.Error(),
			})
		}
		if listErr == nil {
			for _, existing := range vms {
				if existing.Name == createReq.Name {
					newName := existing.Name + "-replacing"
					tflog.Info(ctx, "Renaming existing VM", map[string]any{
						"id":       existing.ID,
						"old_name": existing.Name,
						"new_name": newName,
					})
					renameErr := r.client.RenameVm(ctx, existing.ID, client.RenameVmRequest{Name: newName}, opts)
					if renameErr != nil {
						resp.Diagnostics.AddError(
							"Unable to Create Virtual Machine",
							fmt.Sprintf("Name conflict: a VM with name %q already exists (id=%d). "+
								"Attempted to rename it but failed: %s", createReq.Name, existing.ID, renameErr.Error()),
						)
						return
					}
					vm, err = createVm()
					if err != nil {
						// The existing VM was renamed to free the name but the new create
						// still failed — it is now orphaned under newName. Tell the user
						// exactly how to recover instead of leaving a silently-renamed VM.
						resp.Diagnostics.AddError(
							"Unable to Create Virtual Machine",
							fmt.Sprintf("Name-conflict recovery left an orphaned VM: the existing VM (id=%d) was "+
								"renamed to %q to free the name %q, but creating the new VM still failed: %s. "+
								"Rename VM %d back to %q or delete it.",
								existing.ID, newName, createReq.Name, err.Error(), existing.ID, createReq.Name),
						)
						return
					}
					break
				}
			}
		}
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to Create Virtual Machine", err.Error())
		return
	}

	tflog.Info(ctx, "VM creation initiated, waiting for it to become ready", map[string]any{
		"id":     vm.ID,
		"name":   vm.Name,
		"status": vm.Status,
	})

	// Poll until the VM reaches a ready state (RUNNING/STOPPED) or fails (ERROR).
	opts := &client.RequestOpts{Region: region, ProjectTag: projectTag}
	readyVm, waitErr := r.waitForVmReady(ctx, vm.ID, opts, createReq.CPUCores, createReq.RAM, createReq.DiskSize)

	// Save state even if VM ended up in ERROR — prevents desync on retry.
	resultVm := readyVm
	if resultVm == nil {
		resultVm = vm
	}

	// Set Computed-only attributes from API response
	data.ID = types.Int64Value(resultVm.ID)
	data.Guid = tfutil.StringOrNull(resultVm.Guid)
	data.Region = types.StringValue(region)
	data.ProjectTag = types.StringValue(projectTag)
	data.Status = types.StringValue(resultVm.Status)
	data.PrivateIP = types.StringValue(resultVm.PrivateIP)

	if resultVm.PublicIP != "" {
		data.PublicIP = types.StringValue(resultVm.PublicIP)
	} else {
		data.PublicIP = types.StringNull()
	}

	if resultVm.PublicIPID != 0 {
		data.PublicIPID = types.Int64Value(resultVm.PublicIPID)
	} else {
		data.PublicIPID = types.Int64Null()
	}

	if resultVm.ImageName != "" {
		data.ImageName = types.StringValue(resultVm.ImageName)
	} else {
		data.ImageName = types.StringNull()
	}
	if resultVm.ImageSlug != "" {
		data.ImageSlug = types.StringValue(resultVm.ImageSlug)
	} else {
		data.ImageSlug = types.StringNull()
	}

	// Keep plan values for Required+ForceNew attributes (name, cpu_cores, ram,
	// disk_size, disk_type). The API may temporarily return different values during
	// provisioning (e.g., template defaults before final config is applied).
	// These attributes are immutable (RequiresReplace), so the plan values are
	// the source of truth.
	data.Name = types.StringValue(resultVm.Name)
	if resultVm.Description != "" {
		data.Description = types.StringValue(resultVm.Description)
	}

	// user_data is write-only — guarantee the raw payload never reaches state, on the
	// success path and the error path below.
	data.UserData = types.StringNull()

	// Save state BEFORE returning error — prevents desync on retry
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)

	// Seed the user_data change-detection baseline (sha256 of the write-only payload) in
	// private state, on the same save as state — including the error-but-created path below
	// — so a created VM never persists without its baseline. ALWAYS write it: a VM created
	// WITHOUT user_data gets sha256("") as a "no payload" sentinel (an empty payload is
	// rejected by the validator, so the sentinel can never collide with a real one), so that
	// later ADDING user_data is detected as a change and replaces the VM. Absent private
	// state is thereby reserved for import/upgrade adoption. This is the ONLY place the
	// baseline is written: user_data changes always force replacement (-> Create), and
	// apply-time Update config can differ from plan for unknowns, so Update/Read never stamp it.
	userDataPayload := ""
	if !configData.UserData.IsNull() && !configData.UserData.IsUnknown() {
		userDataPayload = configData.UserData.ValueString()
	}
	if hashBlob, mErr := marshalUserDataHash(userDataHashHex(userDataPayload)); mErr == nil {
		resp.Diagnostics.Append(resp.Private.SetKey(ctx, userDataHashPrivateKey, hashBlob)...)
	}

	if waitErr != nil {
		resp.Diagnostics.AddError(
			"Virtual Machine Not Ready",
			fmt.Sprintf("VM was created (id=%d) but failed to reach a ready state: %s", resultVm.ID, waitErr.Error()),
		)
		return
	}

	tflog.Info(ctx, "Virtual machine is ready", map[string]any{
		"id":     resultVm.ID,
		"name":   resultVm.Name,
		"status": resultVm.Status,
	})
}

func (r *VmResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data VmResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Preserve write-only attributes before API call (never returned by API)
	password := data.Password
	sshPublicKey := data.SSHPublicKey

	opts := &client.RequestOpts{}
	if !data.Region.IsNull() && !data.Region.IsUnknown() {
		opts.Region = data.Region.ValueString()
	}
	if !data.ProjectTag.IsNull() && !data.ProjectTag.IsUnknown() {
		opts.ProjectTag = data.ProjectTag.ValueString()
	}

	vmID := data.ID.ValueInt64()

	tflog.Debug(ctx, "Reading virtual machine", map[string]any{
		"id":          vmID,
		"region":      opts.Region,
		"project_tag": opts.ProjectTag,
	})

	vm, err := r.client.GetVm(ctx, vmID, opts)
	if err != nil {
		if client.IsNotFound(err) {
			tflog.Warn(ctx, "VM not found, removing from state", map[string]any{"id": vmID})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to Read Virtual Machine", err.Error())
		return
	}

	data.Guid = tfutil.StringOrNull(vm.Guid)
	data.Name = types.StringValue(vm.Name)
	data.Status = types.StringValue(vm.Status)
	data.CPUCores = types.Int64Value(vm.CPUCores)
	data.RAM = types.Int64Value(vm.RAM)
	data.DiskSize = types.Int64Value(vm.DiskSize)
	data.DiskType = types.StringValue(vm.DiskType)
	data.PrivateIP = types.StringValue(vm.PrivateIP)
	data.LocalNetworkID = types.Int64Value(vm.LocalNetworkID)

	// Populate image fields from API (supports import + drift detection)
	if vm.ImageID != 0 {
		data.ImageID = types.Int64Value(vm.ImageID)
	}
	if vm.ImageName != "" {
		data.ImageName = types.StringValue(vm.ImageName)
	} else {
		data.ImageName = types.StringNull()
	}
	if vm.ImageSlug != "" {
		data.ImageSlug = types.StringValue(vm.ImageSlug)
	} else {
		data.ImageSlug = types.StringNull()
	}

	if vm.PublicIP != "" {
		data.PublicIP = types.StringValue(vm.PublicIP)
	} else {
		data.PublicIP = types.StringNull()
	}

	if vm.Description != "" {
		data.Description = types.StringValue(vm.Description)
	} else if !data.Description.IsNull() {
		data.Description = types.StringNull()
	}

	// Restore write-only attributes (never returned by API)
	data.Password = password
	data.SSHPublicKey = sshPublicKey
	// user_data is write-only — kept null in state. The change-detection baseline lives in
	// private state (untouched here; Read has no config to recompute it).
	data.UserData = types.StringNull()

	// public_ip_id: always reflect what the API reports so import works correctly.
	// Computed+UseStateForUnknown ensures that if the user omits it from config,
	// Terraform keeps the state value without showing a diff.
	if vm.PublicIPID != 0 {
		data.PublicIPID = types.Int64Value(vm.PublicIPID)
	} else {
		data.PublicIPID = types.Int64Null()
	}

	tflog.Debug(ctx, "Read virtual machine", map[string]any{
		"id":     vmID,
		"name":   vm.Name,
		"status": vm.Status,
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *VmResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state, plan VmResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	opts := &client.RequestOpts{}
	if !state.Region.IsNull() && !state.Region.IsUnknown() {
		opts.Region = state.Region.ValueString()
	}
	if !state.ProjectTag.IsNull() && !state.ProjectTag.IsUnknown() {
		opts.ProjectTag = state.ProjectTag.ValueString()
	}

	vmID := state.ID.ValueInt64()

	// Rename VM if name changed
	if !state.Name.Equal(plan.Name) {
		newName := plan.Name.ValueString()

		tflog.Info(ctx, "Renaming virtual machine", map[string]any{
			"id":       vmID,
			"old_name": state.Name.ValueString(),
			"new_name": newName,
		})

		err := r.client.RenameVm(ctx, vmID, client.RenameVmRequest{Name: newName}, opts)
		if err != nil {
			resp.Diagnostics.AddError("Unable to Rename Virtual Machine", err.Error())
			return
		}
	}

	cpuChanged := !state.CPUCores.Equal(plan.CPUCores)
	ramChanged := !state.RAM.Equal(plan.RAM)
	diskSizeChanged := !state.DiskSize.Equal(plan.DiskSize)
	diskTypeChanged := !state.DiskType.Equal(plan.DiskType)

	// Validate disk size before stopping the VM
	if diskSizeChanged && plan.DiskSize.ValueInt64() < state.DiskSize.ValueInt64() {
		resp.Diagnostics.AddError(
			"Invalid Disk Size",
			fmt.Sprintf("Disk size can only be increased. Current: %d GB, requested: %d GB.",
				state.DiskSize.ValueInt64(), plan.DiskSize.ValueInt64()),
		)
		return
	}

	needsUpdate := cpuChanged || ramChanged || diskSizeChanged || diskTypeChanged

	if needsUpdate {
		// Stop once before all updates
		needsRestart, err := r.stopIfRunning(ctx, vmID, opts)
		if err != nil {
			resp.Diagnostics.AddError("Unable to Stop VM for update", err.Error())
			return
		}

		// Apply CPU/RAM update
		if cpuChanged || ramChanged {
			tflog.Info(ctx, "Updating VM resources", map[string]any{
				"id":      vmID,
				"old_cpu": state.CPUCores.ValueInt64(),
				"new_cpu": plan.CPUCores.ValueInt64(),
				"old_ram": state.RAM.ValueInt64(),
				"new_ram": plan.RAM.ValueInt64(),
			})

			updateReq := client.UpdateVmResourcesRequest{
				CPUCores: plan.CPUCores.ValueInt64(),
				RAM:      plan.RAM.ValueInt64(),
			}

			if err := r.client.UpdateVmResources(ctx, vmID, updateReq, opts); err != nil {
				if needsRestart {
					tflog.Warn(ctx, "Resource update failed, attempting to restart VM", map[string]any{"id": vmID})
					_ = r.client.StartVm(ctx, vmID, opts)
				}
				resp.Diagnostics.AddError("Unable to Update VM Resources", err.Error())
				return
			}

			tflog.Info(ctx, "VM resources updated", map[string]any{
				"id":        vmID,
				"cpu_cores": plan.CPUCores.ValueInt64(),
				"ram":       plan.RAM.ValueInt64(),
			})
		}

		// Apply disk update
		if diskSizeChanged || diskTypeChanged {
			tflog.Info(ctx, "Updating VM disk", map[string]any{
				"id":            vmID,
				"old_disk_size": state.DiskSize.ValueInt64(),
				"new_disk_size": plan.DiskSize.ValueInt64(),
				"old_disk_type": state.DiskType.ValueString(),
				"new_disk_type": plan.DiskType.ValueString(),
			})

			updateReq := client.UpdateVmDiskRequest{}
			if diskSizeChanged {
				size := plan.DiskSize.ValueInt64()
				updateReq.DiskSize = &size
			}
			if diskTypeChanged {
				dt := plan.DiskType.ValueString()
				updateReq.DiskType = &dt
			}

			if err := r.client.UpdateVmDisk(ctx, vmID, updateReq, opts); err != nil {
				if needsRestart {
					tflog.Warn(ctx, "Disk update failed, attempting to restart VM", map[string]any{"id": vmID})
					_ = r.client.StartVm(ctx, vmID, opts)
				}
				resp.Diagnostics.AddError("Unable to Update VM Disk", err.Error())
				return
			}

			tflog.Info(ctx, "VM disk updated", map[string]any{
				"id":        vmID,
				"disk_size": plan.DiskSize.ValueInt64(),
				"disk_type": plan.DiskType.ValueString(),
			})
		}

		// Start once after all updates
		if needsRestart {
			if err := r.startAndWait(ctx, vmID, opts); err != nil {
				resp.Diagnostics.AddWarning(
					"VM updated but not restarted",
					fmt.Sprintf("Changes were applied, but the VM failed to restart: %s", err.Error()),
				)
			}
		}
	}

	// Read back the current VM state. Use GetVmStatus (not GetVm): the /status endpoint
	// includes VMs in ERROR status, so a VM that errored mid-update is still read and its
	// partially-applied state persisted, instead of failing the read and losing state.
	vm, err := r.client.GetVmStatus(ctx, state.ID.ValueInt64(), opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Virtual Machine after update", err.Error())
		return
	}

	plan.ID = state.ID
	plan.Region = state.Region
	plan.ProjectTag = state.ProjectTag
	// Keep the planned name rather than echoing the API name: a rename read-back can
	// race and briefly return the old name, which would trip "Provider produced
	// inconsistent result after apply" (the plan promised the new name).
	plan.Status = types.StringValue(vm.Status)
	plan.CPUCores = types.Int64Value(vm.CPUCores)
	plan.RAM = types.Int64Value(vm.RAM)
	plan.DiskSize = types.Int64Value(vm.DiskSize)
	plan.DiskType = types.StringValue(vm.DiskType)
	plan.PrivateIP = types.StringValue(vm.PrivateIP)
	plan.LocalNetworkID = types.Int64Value(vm.LocalNetworkID)

	// Image fields from API
	if vm.ImageID != 0 {
		plan.ImageID = types.Int64Value(vm.ImageID)
	} else {
		plan.ImageID = state.ImageID
	}
	if vm.ImageName != "" {
		plan.ImageName = types.StringValue(vm.ImageName)
	} else {
		plan.ImageName = types.StringNull()
	}
	if vm.ImageSlug != "" {
		plan.ImageSlug = types.StringValue(vm.ImageSlug)
	} else {
		plan.ImageSlug = types.StringNull()
	}

	if vm.PublicIP != "" {
		plan.PublicIP = types.StringValue(vm.PublicIP)
	} else {
		plan.PublicIP = types.StringNull()
	}

	if vm.Description != "" {
		plan.Description = types.StringValue(vm.Description)
	} else if !plan.Description.IsNull() {
		plan.Description = types.StringNull()
	}

	if vm.PublicIPID != 0 {
		plan.PublicIPID = types.Int64Value(vm.PublicIPID)
	} else {
		plan.PublicIPID = types.Int64Null()
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *VmResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id, err := strconv.ParseInt(req.ID, 10, 64)
	if err != nil {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected integer VM ID, got: %s\n\n"+
				"Usage: terraform import prodata_vm.example <vm_id>\n"+
				"Example: terraform import prodata_vm.example 123", req.ID),
		)
		return
	}

	tflog.Info(ctx, "Importing virtual machine", map[string]any{"id": id})
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("region"), r.client.Region)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_tag"), r.client.ProjectTag)...)
}

func (r *VmResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data VmResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	opts := &client.RequestOpts{}
	if !data.Region.IsNull() && !data.Region.IsUnknown() {
		opts.Region = data.Region.ValueString()
	}
	if !data.ProjectTag.IsNull() && !data.ProjectTag.IsUnknown() {
		opts.ProjectTag = data.ProjectTag.ValueString()
	}

	vmID := data.ID.ValueInt64()

	tflog.Debug(ctx, "Deleting virtual machine", map[string]any{"id": vmID})

	err := r.client.DeleteVm(ctx, vmID, opts)
	if err != nil {
		if client.IsNotFound(err) {
			return // already gone
		}
		resp.Diagnostics.AddError("Unable to Delete Virtual Machine", err.Error())
		return
	}

	tflog.Debug(ctx, "Deleted virtual machine", map[string]any{"id": vmID})
}

// stopIfRunning stops the VM if it is RUNNING and waits for STOPPED state.
// Returns true if the VM was stopped (and should be restarted after the operation).
func (r *VmResource) stopIfRunning(ctx context.Context, vmID int64, opts *client.RequestOpts) (bool, error) {
	// GetVmStatus (not GetVm) so an ERROR-status VM is still readable here and treated
	// as not-running, rather than failing the read on a VM the plain endpoint omits.
	vm, err := r.client.GetVmStatus(ctx, vmID, opts)
	if err != nil {
		return false, fmt.Errorf("read VM: %w", err)
	}
	if vm.Status != "RUNNING" {
		return false, nil
	}

	tflog.Info(ctx, "VM is running, stopping before update", map[string]any{"id": vmID})

	if err := r.client.StopVm(ctx, vmID, opts); err != nil {
		return false, fmt.Errorf("stop VM: %w", err)
	}
	if err := r.client.WaitForVmStatus(ctx, vmID, "STOPPED", 5*time.Minute, opts); err != nil {
		return false, err
	}
	return true, nil
}

// startAndWait starts the VM and waits for RUNNING state.
func (r *VmResource) startAndWait(ctx context.Context, vmID int64, opts *client.RequestOpts) error {
	tflog.Info(ctx, "Restarting VM after update", map[string]any{"id": vmID})
	if err := r.client.StartVm(ctx, vmID, opts); err != nil {
		return fmt.Errorf("start VM: %w", err)
	}
	return r.client.WaitForVmStatus(ctx, vmID, "RUNNING", 5*time.Minute, opts)
}
