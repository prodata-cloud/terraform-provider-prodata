package resources

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"terraform-provider-prodata/internal/client"
	"terraform-provider-prodata/internal/tfutil"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
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

	// poll, moveOptions and settledAsk replace the pace of the waits for a VM, of the repeats of a
	// move and the bound of the question settledMove asks again in tests; zero values are the real ones.
	poll        time.Duration
	moveOptions []client.MoveOption
	settledAsk  time.Duration
}

// pollInterval is the interval between VM status reads while a VM is stopped and started.
func (r *VmResource) pollInterval() time.Duration {
	if r.poll > 0 {
		return r.poll
	}
	return vmRestartPollInterval
}

// settledAskTime is how long settledMove waits for the answer to the question it asks again.
func (r *VmResource) settledAskTime() time.Duration {
	if r.settledAsk > 0 {
		return r.settledAsk
	}
	return vmSettledAskTime
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
				Validators: []validator.String{
					stringvalidator.LengthBetween(3, 63),
					stringvalidator.RegexMatches(
						regexp.MustCompile(`^[0-9-]*[A-Za-z][A-Za-z0-9-]*$`),
						"must contain only letters, digits and hyphens, and at least one letter",
					),
				},
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
				Validators: []validator.Int64{
					int64validator.AtLeast(1),
				},
			},
			"ram": schema.Int64Attribute{
				MarkdownDescription: "The amount of RAM in GB for the virtual machine. Minimum 1. Changing this forces a VM reboot.",
				Required:            true,
				Validators: []validator.Int64{
					int64validator.AtLeast(1),
				},
			},
			"disk_size": schema.Int64Attribute{
				MarkdownDescription: "The size of the disk in GB. Minimum 10. Can only be increased. Changing this forces a VM reboot.",
				Required:            true,
				Validators: []validator.Int64{
					int64validator.AtLeast(10),
				},
			},
			"disk_type": schema.StringAttribute{
				MarkdownDescription: "The type of disk (HDD, SSD, or NVME). Can only be upgraded (e.g. HDD -> SSD). Changing this forces a VM reboot.",
				Required:            true,
			},
			"local_network_id": schema.Int64Attribute{
				MarkdownDescription: "The ID of the local network of the VM's main network interface. " +
					"Changing it moves the interface to another network in place: the VM is not recreated, " +
					"a running VM is stopped (powered off) and started again, and the VM gets a new address " +
					"(see `private_ip`). See \"Moving a VM to another network\" below.",
				Required: true,
			},
			"private_ip": schema.StringAttribute{
				MarkdownDescription: "The private IP address of the virtual machine in its local network. If not " +
					"specified, an available IP is auto-assigned from the local network. Changing it together with " +
					"`local_network_id` sets the address in the new network; changing it alone forces a new resource.",
				Optional: true,
				Computed: true,
				Validators: []validator.String{
					IPv4Address(),
				},
				PlanModifiers: []planmodifier.String{
					privateIPReplace(),
					privateIPFromState(),
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
				Update: true,
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
	// create_before_destroy + same-name uniqueness constraint. The image/etc. replacements are
	// driven by their own RequiresReplace plan modifiers; we mirror the readable ones here
	// (write-only password/ssh are null in state post-import, so are excluded) plus the user_data
	// signal computed above. A new private_ip replaces the VM only while the network stays: with a
	// new network it is part of the move (privateIPReplaces is what the attribute's modifier
	// decides on, too).
	requiresReplace := userDataReplace ||
		!stateData.ImageID.Equal(planData.ImageID) ||
		privateIPReplaces(&stateData, &planData) ||
		!stateData.Region.Equal(planData.Region) ||
		!stateData.ProjectTag.Equal(planData.ProjectTag) ||
		!stateData.Description.Equal(planData.Description) ||
		(!stateData.PublicIPID.IsNull() && !stateData.PublicIPID.Equal(planData.PublicIPID))
	if requiresReplace {
		// Warn that create_before_destroy will fail due to VM name uniqueness constraint.
		if stateData.Name.Equal(planData.Name) {
			resp.Diagnostics.AddWarning(
				"Resource replacement with same name",
				"This VM is being replaced but the name is unchanged. If you are using "+
					"lifecycle { create_before_destroy = true }, the existing VM will be automatically "+
					"renamed to allow the new VM to be created with the original name.",
			)
		}
		return
	}

	// An update in place. A change of the network is a move of the main interface, which stops a
	// running VM: this warning is the only place the user sees it before apply.
	move := networkChangeOf(stateData.LocalNetworkID, planData.LocalNetworkID)
	if move.moves() {
		summary, detail := moveWarning(move)
		resp.Diagnostics.AddWarning(summary, detail)
		resp.Diagnostics.Append(checkMoveTimeout(ctx, planData.Timeouts)...)
	}

	// Whatever stops and starts the VM leaves its status for apply to say: the plan is made from a
	// state that may be behind the VM, and a start that fails would otherwise contradict a status
	// the plan promised.
	if move.moves() ||
		!stateData.CPUCores.Equal(planData.CPUCores) ||
		!stateData.RAM.Equal(planData.RAM) ||
		!stateData.DiskSize.Equal(planData.DiskSize) ||
		!stateData.DiskType.Equal(planData.DiskType) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("status"), types.StringUnknown())...)
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

	// timeouts.update is the ceiling of the whole of Update: the rename, the resize and the move of
	// the network.
	updateTimeout, diags := plan.Timeouts.Update(ctx, vmDefaultUpdateTime)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, updateTimeout)
	defer cancel()

	opts := &client.RequestOpts{}
	if !state.Region.IsNull() && !state.Region.IsUnknown() {
		opts.Region = state.Region.ValueString()
	}
	if !state.ProjectTag.IsNull() && !state.ProjectTag.IsUnknown() {
		opts.ProjectTag = state.ProjectTag.ValueString()
	}

	vmID := state.ID.ValueInt64()

	cpuChanged := !state.CPUCores.Equal(plan.CPUCores)
	ramChanged := !state.RAM.Equal(plan.RAM)
	diskSizeChanged := !state.DiskSize.Equal(plan.DiskSize)
	diskTypeChanged := !state.DiskType.Equal(plan.DiskType)

	// Validate disk size before anything is done to the VM
	if diskSizeChanged && plan.DiskSize.ValueInt64() < state.DiskSize.ValueInt64() {
		resp.Diagnostics.AddError(
			"Invalid Disk Size",
			fmt.Sprintf("Disk size can only be increased. Current: %d GB, requested: %d GB.",
				state.DiskSize.ValueInt64(), plan.DiskSize.ValueInt64()),
		)
		return
	}

	// A move of the main network is checked by the panel before the VM is touched, the rename
	// included: a refusal leaves everything as it was.
	var forecast *client.MovedPrimaryLocalNetwork
	if wantsMove(&state, &plan) {
		var err error
		forecast, err = r.client.MovePrimaryLocalNetwork(ctx, vmID, r.moveRequest(&plan, true), opts)
		if err != nil {
			summary, detail := moveRefusal(err, plan.LocalNetworkID.ValueInt64())
			resp.Diagnostics.AddError(summary, detail)
			return
		}
	}

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

	needsUpdate := cpuChanged || ramChanged || diskSizeChanged || diskTypeChanged

	// moved is the panel's answer to a move that was made: the guest may have to be configured.
	var moved *client.MovedPrimaryLocalNetwork
	switch {
	case forecast != nil && (forecast.Changed || forecast.PendingRecovery):
		// The main interface moves, and the VM has to be stopped for it. What else changes with it
		// is done in the same stop. A VM with an earlier move that was never settled has no forecast
		// (the panel answers changed=false and pendingRecovery=true): whether it has to move is
		// found out by the move itself, which settles the earlier one first, and it needs the VM
		// stopped if it goes on.
		moved = r.updateWithMove(ctx, &resp.Diagnostics, vmID, &state, &plan, opts, forecast)
	case forecast != nil:
		// The interface is in the network already and only the panel's record of it is off: the
		// record is put right, and the VM is not stopped for that.
		if _, err := r.movePrimaryNetwork(ctx, vmID, &plan, opts, forecast); err != nil {
			summary, detail := moveRefusal(err, plan.LocalNetworkID.ValueInt64())
			resp.Diagnostics.AddError(summary, detail)
			return
		}
		if needsUpdate {
			resp.Diagnostics.Append(r.updateWithoutMove(ctx, vmID, &state, &plan, opts)...)
		}
	case needsUpdate:
		resp.Diagnostics.Append(r.updateWithoutMove(ctx, vmID, &state, &plan, opts)...)
	}
	if resp.Diagnostics.HasError() {
		return
	}

	// Read back the current VM state. Use GetVmStatus (not GetVm): the /status endpoint
	// includes VMs in ERROR status, so a VM that errored mid-update is still read and its
	// partially-applied state persisted, instead of failing the read and losing state.
	readCtx, cancelRead := readBackContext(ctx)
	defer cancelRead()
	vm, err := r.client.GetVmStatus(readCtx, state.ID.ValueInt64(), opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Virtual Machine after update", err.Error())
		return
	}

	// The plan promised the network; Terraform would report a different one as an inconsistent
	// result, which says nothing of what happened.
	if forecast != nil && vm.LocalNetworkID != plan.LocalNetworkID.ValueInt64() {
		resp.Diagnostics.AddError(
			"The virtual machine is not in the network it was moved to",
			fmt.Sprintf("The panel accepted the move of VM %d to network %d, but it reports the VM in network %d. "+
				"Run terraform plan to see where the VM is, and apply again. If it happens again, contact support.",
				vmID, plan.LocalNetworkID.ValueInt64(), vm.LocalNetworkID),
		)
		return
	}
	// moved comes only from a move that was due: the panel says changed=false to a request that is
	// repeated after a reply that was lost, and the VM was moved all the same.
	if moved != nil && moved.RequiresGuestConfiguration {
		summary, detail := guestConfigurationWarning(moved)
		resp.Diagnostics.AddWarning(summary, detail)
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

// updateWithoutMove changes cpu, ram and disk of a VM whose network stays: a running VM is stopped
// once for all of it and started once afterwards. The ceiling of the update (timeouts.update), or an
// interruption, can end ctx at any step: a VM that the update stopped is started on a context of
// its own (see reserveContext), however the update ended.
func (r *VmResource) updateWithoutMove(ctx context.Context, vmID int64, state, plan *VmResourceModel, opts *client.RequestOpts) diag.Diagnostics {
	var diags diag.Diagnostics

	// Stop once before all updates
	needsRestart, err := r.stopIfRunning(ctx, vmID, opts)
	if err != nil {
		diags.AddError("Unable to Stop VM for update", err.Error())
		return diags
	}

	if failed := r.updateStopped(ctx, vmID, state, plan, opts); failed != nil {
		detail := failed.err.Error()
		if needsRestart {
			tflog.Warn(ctx, "VM update failed, attempting to restart VM", map[string]any{"id": vmID, "step": failed.summary})
			startCtx, cancel := reserveContext(ctx)
			defer cancel()
			if err := r.startUpdatedVM(startCtx, vmID, opts); err != nil {
				detail += fmt.Sprintf(" The VM could not be started again: %s", err)
			} else {
				detail += " The VM was started again."
			}
		}
		diags.AddError(failed.summary, detail)
		return diags
	}

	// Start once after all updates
	if needsRestart {
		startCtx, cancel := reserveContext(ctx)
		defer cancel()
		if err := r.startAndWait(startCtx, vmID, opts); err != nil {
			diags.AddWarning(
				"VM updated but not restarted",
				fmt.Sprintf("Changes were applied, but the VM failed to restart: %s", err.Error()),
			)
		}
	}
	return diags
}

// updateStopped applies the changes of cpu, ram and disk that the plan asks for to a VM that is
// stopped, in this order. It does not start the VM, whatever happens.
func (r *VmResource) updateStopped(ctx context.Context, vmID int64, state, plan *VmResourceModel, opts *client.RequestOpts) *updateStepError {
	if !state.CPUCores.Equal(plan.CPUCores) || !state.RAM.Equal(plan.RAM) {
		if err := r.updateResources(ctx, vmID, state, plan, opts); err != nil {
			return &updateStepError{"Unable to Update VM Resources", err}
		}
	}
	if !state.DiskSize.Equal(plan.DiskSize) || !state.DiskType.Equal(plan.DiskType) {
		if err := r.updateDisk(ctx, vmID, state, plan, opts); err != nil {
			return &updateStepError{"Unable to Update VM Disk", err}
		}
	}
	return nil
}

// updateResources applies the new cpu and ram to a stopped VM.
func (r *VmResource) updateResources(ctx context.Context, vmID int64, state, plan *VmResourceModel, opts *client.RequestOpts) error {
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
		return err
	}

	tflog.Info(ctx, "VM resources updated", map[string]any{
		"id":        vmID,
		"cpu_cores": plan.CPUCores.ValueInt64(),
		"ram":       plan.RAM.ValueInt64(),
	})
	return nil
}

// updateDisk applies the new disk size and type to a stopped VM.
func (r *VmResource) updateDisk(ctx context.Context, vmID int64, state, plan *VmResourceModel, opts *client.RequestOpts) error {
	tflog.Info(ctx, "Updating VM disk", map[string]any{
		"id":            vmID,
		"old_disk_size": state.DiskSize.ValueInt64(),
		"new_disk_size": plan.DiskSize.ValueInt64(),
		"old_disk_type": state.DiskType.ValueString(),
		"new_disk_type": plan.DiskType.ValueString(),
	})

	updateReq := client.UpdateVmDiskRequest{}
	if !state.DiskSize.Equal(plan.DiskSize) {
		size := plan.DiskSize.ValueInt64()
		updateReq.DiskSize = &size
	}
	if !state.DiskType.Equal(plan.DiskType) {
		dt := plan.DiskType.ValueString()
		updateReq.DiskType = &dt
	}
	if err := r.client.UpdateVmDisk(ctx, vmID, updateReq, opts); err != nil {
		return err
	}

	tflog.Info(ctx, "VM disk updated", map[string]any{
		"id":        vmID,
		"disk_size": plan.DiskSize.ValueInt64(),
		"disk_type": plan.DiskType.ValueString(),
	})
	return nil
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
// Returns true if the VM was stopped (and should be restarted after the operation). When the update
// goes no further and the VM is off or on its way to it because of this stop, as it may be after a
// stop whose answer says nothing final, after a wait that fails and after ctx ends, nothing else was
// done to the VM: it is started again, and the error says what became of it. A stop that the panel
// refuses for good leaves the VM as it was, and the error is the refusal.
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
		stopErr := fmt.Errorf("stop VM: %w", err)
		if !inconclusive(err) {
			return false, stopErr
		}
		// Carried out, perhaps, although the answer says nothing final or did not come. A call that
		// ctx cut short fails that way too (it is no answer of the panel); an answer that is final is
		// the panel's last word, whether or not ctx ended as it was read.
		return false, r.startedAgainAfter(ctx, vmID, opts, stopErr)
	}
	if err := r.client.WaitForVmStatusEvery(ctx, vmID, "STOPPED", 5*time.Minute, r.pollInterval(), opts); err != nil {
		return false, r.startedAgainAfter(ctx, vmID, opts, err)
	}
	return true, nil
}

// startedAgainAfter adds to err what became of a VM that a stop of ours has stopped, or may have
// stopped, when the update goes no further: the stop that was accepted goes on after the wait for it
// is given up, and one that failed without saying may have been carried out. The VM is looked at and
// started again on a context of its own (see reserveContext). Neither kind of stop shows in the
// status at once, so a VM that reads RUNNING is looked at for as long as a lag of the panel's view
// can last before it is taken for up (see startAgain).
func (r *VmResource) startedAgainAfter(ctx context.Context, vmID int64, opts *client.RequestOpts, err error) error {
	return fmt.Errorf("%w; %s", err, startAfterInterruption(ctx, r.client, vmID, opts, r.pollInterval(), true))
}

// startAndWait starts the VM and waits for RUNNING state.
func (r *VmResource) startAndWait(ctx context.Context, vmID int64, opts *client.RequestOpts) error {
	tflog.Info(ctx, "Restarting VM after update", map[string]any{"id": vmID})
	if err := r.startUpdatedVM(ctx, vmID, opts); err != nil {
		return fmt.Errorf("start VM: %w", err)
	}
	return r.client.WaitForVmStatusEvery(ctx, vmID, "RUNNING", 5*time.Minute, r.pollInterval(), opts)
}

// startUpdatedVM starts a VM that the update stopped. An answer that says nothing final (see
// inconclusive) does not end it: the start may have been carried out although its reply did not come,
// and the panel may still be busy with the step that the VM was stopped for, as it is with a step that
// the ceiling of the update cut short. The VM is then looked at and started until it is up, for as long
// as a start after an interruption of the restart cycle is (see startAgain). An answer that is final,
// such as a refusal, is the result at once. ctx is one the VM can still be started on (see
// reserveContext).
func (r *VmResource) startUpdatedVM(ctx context.Context, vmID int64, opts *client.RequestOpts) error {
	err := r.client.StartVm(ctx, vmID, opts)
	if err == nil || !inconclusive(err) {
		return err
	}
	if up, said := startAgain(ctx, r.client, vmID, opts, r.pollInterval(), false); !up {
		return fmt.Errorf("%w; %s", err, said)
	}
	return nil
}
