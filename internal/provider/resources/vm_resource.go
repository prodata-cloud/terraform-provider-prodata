package resources

import (
	"context"
	"fmt"
	"strings"
	"time"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource               = &VmResource{}
	_ resource.ResourceWithConfigure  = &VmResource{}
	_ resource.ResourceWithModifyPlan = &VmResource{}
)

type VmResource struct {
	client *client.Client
}

type VmResourceModel struct {
	ID             types.Int64  `tfsdk:"id"`
	Region         types.String `tfsdk:"region"`
	ProjectTag     types.String `tfsdk:"project_tag"`
	Name           types.String `tfsdk:"name"`
	ImageID        types.Int64  `tfsdk:"image_id"`
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
	Status         types.String `tfsdk:"status"`
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
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"image_id": schema.Int64Attribute{
				MarkdownDescription: "The ID of the image to use for the virtual machine.",
				Required:            true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"cpu_cores": schema.Int64Attribute{
				MarkdownDescription: "The number of CPU cores for the virtual machine. Minimum 1.",
				Required:            true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"ram": schema.Int64Attribute{
				MarkdownDescription: "The amount of RAM in GB for the virtual machine. Minimum 1.",
				Required:            true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"disk_size": schema.Int64Attribute{
				MarkdownDescription: "The size of the disk in GB. Minimum 10.",
				Required:            true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"disk_type": schema.StringAttribute{
				MarkdownDescription: "The type of disk (HDD, SSD, or NVME).",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
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
				MarkdownDescription: "The ID of the public IP to attach to the VM (optional).",
				Optional:            true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"public_ip": schema.StringAttribute{
				MarkdownDescription: "The public IP address assigned to the virtual machine (if any).",
				Computed:            true,
			},
			"password": schema.StringAttribute{
				MarkdownDescription: "The password for the virtual machine.",
				Required:            true,
				Sensitive:           true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"ssh_public_key": schema.StringAttribute{
				MarkdownDescription: "SSH public key for authentication (optional).",
				Optional:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Description of the virtual machine (optional).",
				Optional:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "The current status of the virtual machine.",
				Computed:            true,
			},
		},
	}
}

func (r *VmResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	// Skip if creating or destroying (not replacing)
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}

	// This is a replacement (both state and plan exist, but RequiresReplace triggered).
	// Warn that create_before_destroy will fail due to VM name uniqueness constraint.
	var stateData, planData VmResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &stateData)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &planData)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// If the name stays the same during replacement with create_before_destroy,
	// the existing VM will be automatically renamed to "{name}-replacing" to avoid conflict.
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
	vmPollInterval  = 5 * time.Second
	vmCreateTimeout = 5 * time.Minute
)

// waitForVmReady polls the VM until it reaches a terminal state (RUNNING, STOPPED, or ERROR).
func (r *VmResource) waitForVmReady(ctx context.Context, vmID int64, opts *client.RequestOpts) (*client.Vm, error) {
	deadline := time.Now().Add(vmCreateTimeout)

	for {
		vm, err := r.client.GetVmStatus(ctx, vmID, opts)
		if err != nil {
			return nil, fmt.Errorf("polling VM status: %w", err)
		}

		tflog.Debug(ctx, "Polling VM status", map[string]any{
			"id":     vmID,
			"status": vm.Status,
		})

		switch vm.Status {
		case "RUNNING", "STOPPED":
			return vm, nil
		case "ERROR":
			return nil, fmt.Errorf("VM creation failed (id=%d, status=ERROR)", vmID)
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for VM %d to become ready (last status: %s)", vmID, vm.Status)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
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
		"private_ip":       createReq.PrivateIP, // may be nil if auto-assigning
	})

	vm, err := r.client.CreateVm(ctx, createReq)
	if err != nil && strings.Contains(err.Error(), "666") {
		// Name conflict — likely a create_before_destroy replacement.
		// Find the existing VM with that name and rename it, then retry.
		tflog.Info(ctx, "VM name conflict detected, attempting to rename existing VM", map[string]any{
			"name": createReq.Name,
		})

		opts := &client.RequestOpts{Region: region, ProjectTag: projectTag}
		vms, listErr := r.client.GetVms(ctx, opts)
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
					// Retry create after rename
					vm, err = r.client.CreateVm(ctx, createReq)
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
	readyVm, waitErr := r.waitForVmReady(ctx, vm.ID, opts)
	if waitErr != nil {
		resp.Diagnostics.AddError(
			"Virtual Machine Not Ready",
			fmt.Sprintf("VM was created (id=%d) but failed to reach a ready state: %s", vm.ID, waitErr.Error()),
		)
		return
	}

	data.ID = types.Int64Value(readyVm.ID)
	data.Region = types.StringValue(region)
	data.ProjectTag = types.StringValue(projectTag)
	data.Name = types.StringValue(readyVm.Name)
	data.Status = types.StringValue(readyVm.Status)
	data.CPUCores = types.Int64Value(readyVm.CPUCores)
	data.RAM = types.Int64Value(readyVm.RAM)
	data.DiskSize = types.Int64Value(readyVm.DiskSize)
	data.DiskType = types.StringValue(readyVm.DiskType)
	data.PrivateIP = types.StringValue(readyVm.PrivateIP)

	if readyVm.PublicIP != "" {
		data.PublicIP = types.StringValue(readyVm.PublicIP)
	} else {
		data.PublicIP = types.StringNull()
	}

	tflog.Info(ctx, "Virtual machine is ready", map[string]any{
		"id":     readyVm.ID,
		"name":   readyVm.Name,
		"status": readyVm.Status,
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *VmResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
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

	tflog.Debug(ctx, "Reading virtual machine", map[string]any{
		"id":          vmID,
		"region":      opts.Region,
		"project_tag": opts.ProjectTag,
	})

	vm, err := r.client.GetVm(ctx, vmID, opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Virtual Machine", err.Error())
		return
	}

	data.Name = types.StringValue(vm.Name)
	data.Status = types.StringValue(vm.Status)
	data.CPUCores = types.Int64Value(vm.CPUCores)
	data.RAM = types.Int64Value(vm.RAM)
	data.DiskSize = types.Int64Value(vm.DiskSize)
	data.DiskType = types.StringValue(vm.DiskType)
	data.PrivateIP = types.StringValue(vm.PrivateIP)
	data.LocalNetworkID = types.Int64Value(vm.LocalNetworkID)

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

	tflog.Debug(ctx, "Read virtual machine", map[string]any{
		"id":     vmID,
		"name":   vm.Name,
		"status": vm.Status,
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *VmResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// VM updates are not supported - all changes require replacement
	resp.Diagnostics.AddError(
		"Update Not Supported",
		"Virtual machine updates are not supported. All changes require resource replacement.",
	)
}

func (r *VmResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data VmResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Only set opts if explicitly provided in resource (overrides provider defaults)
	opts := &client.RequestOpts{}
	if !data.Region.IsNull() && !data.Region.IsUnknown() {
		opts.Region = data.Region.ValueString()
	}
	if !data.ProjectTag.IsNull() && !data.ProjectTag.IsUnknown() {
		opts.ProjectTag = data.ProjectTag.ValueString()
	}

	vmID := data.ID.ValueInt64()

	tflog.Debug(ctx, "Deleting virtual machine", map[string]any{
		"id": vmID,
	})

	err := r.client.DeleteVm(ctx, vmID, opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Delete Virtual Machine", err.Error())
		return
	}

	tflog.Debug(ctx, "Deleted virtual machine", map[string]any{
		"id": vmID,
	})
}
