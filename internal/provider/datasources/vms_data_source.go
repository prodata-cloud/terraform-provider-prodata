package datasources

import (
	"context"
	"fmt"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ datasource.DataSource              = &VmsDataSource{}
	_ datasource.DataSourceWithConfigure = &VmsDataSource{}
)

type VmsDataSource struct {
	client *client.Client
}

type VmsDataSourceModel struct {
	Region     types.String `tfsdk:"region"`
	ProjectTag types.String `tfsdk:"project_tag"`
	Vms        []VmModel    `tfsdk:"vms"`
}

type VmModel struct {
	ID             types.Int64  `tfsdk:"id"`
	Name           types.String `tfsdk:"name"`
	Status         types.String `tfsdk:"status"`
	CPUCores       types.Int64  `tfsdk:"cpu_cores"`
	RAM            types.Int64  `tfsdk:"ram"`
	DiskSize       types.Int64  `tfsdk:"disk_size"`
	DiskType       types.String `tfsdk:"disk_type"`
	PrivateIP      types.String `tfsdk:"private_ip"`
	PublicIP       types.String `tfsdk:"public_ip"`
	LocalNetworkID types.Int64  `tfsdk:"local_network_id"`
	Description    types.String `tfsdk:"description"`
}

func NewVmsDataSource() datasource.DataSource {
	return &VmsDataSource{}
}

func (d *VmsDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vms"
}

func (d *VmsDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "List all available ProData virtual machines.",

		Attributes: map[string]schema.Attribute{
			"region": schema.StringAttribute{
				MarkdownDescription: "Region ID override. If not specified, uses the provider's default region.",
				Optional:            true,
			},
			"project_tag": schema.StringAttribute{
				MarkdownDescription: "Project Tag override. If not specified, uses the provider's default project tag.",
				Optional:            true,
			},
			"vms": schema.ListNestedAttribute{
				MarkdownDescription: "List of available virtual machines.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.Int64Attribute{
							MarkdownDescription: "The unique identifier of the virtual machine.",
							Computed:            true,
						},
						"name": schema.StringAttribute{
							MarkdownDescription: "The name of the virtual machine.",
							Computed:            true,
						},
						"status": schema.StringAttribute{
							MarkdownDescription: "The current status of the virtual machine.",
							Computed:            true,
						},
						"cpu_cores": schema.Int64Attribute{
							MarkdownDescription: "The number of CPU cores.",
							Computed:            true,
						},
						"ram": schema.Int64Attribute{
							MarkdownDescription: "The amount of RAM in GB.",
							Computed:            true,
						},
						"disk_size": schema.Int64Attribute{
							MarkdownDescription: "The size of the disk in GB.",
							Computed:            true,
						},
						"disk_type": schema.StringAttribute{
							MarkdownDescription: "The type of disk (HDD, SSD, or NVME).",
							Computed:            true,
						},
						"private_ip": schema.StringAttribute{
							MarkdownDescription: "The private IP address of the virtual machine.",
							Computed:            true,
						},
						"public_ip": schema.StringAttribute{
							MarkdownDescription: "The public IP address of the virtual machine (if any).",
							Computed:            true,
						},
						"local_network_id": schema.Int64Attribute{
							MarkdownDescription: "The ID of the local network the VM is attached to.",
							Computed:            true,
						},
						"description": schema.StringAttribute{
							MarkdownDescription: "Description of the virtual machine.",
							Computed:            true,
						},
					},
				},
			},
		},
	}
}

func (d *VmsDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	d.client = c
}

func (d *VmsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data VmsDataSourceModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
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

	tflog.Debug(ctx, "Listing virtual machines", map[string]any{
		"region":      opts.Region,
		"project_tag": opts.ProjectTag,
	})

	vms, err := d.client.GetVms(ctx, opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to List Virtual Machines", err.Error())
		return
	}

	data.Vms = make([]VmModel, len(vms))
	for i, vm := range vms {
		vmModel := VmModel{
			ID:             types.Int64Value(vm.ID),
			Name:           types.StringValue(vm.Name),
			Status:         types.StringValue(vm.Status),
			CPUCores:       types.Int64Value(vm.CPUCores),
			RAM:            types.Int64Value(vm.RAM),
			DiskSize:       types.Int64Value(vm.DiskSize),
			DiskType:       types.StringValue(vm.DiskType),
			PrivateIP:      types.StringValue(vm.PrivateIP),
			LocalNetworkID: types.Int64Value(vm.LocalNetworkID),
		}

		if vm.PublicIP != "" {
			vmModel.PublicIP = types.StringValue(vm.PublicIP)
		} else {
			vmModel.PublicIP = types.StringNull()
		}

		if vm.Description != "" {
			vmModel.Description = types.StringValue(vm.Description)
		} else {
			vmModel.Description = types.StringNull()
		}

		data.Vms[i] = vmModel
	}

	tflog.Debug(ctx, "Successfully listed virtual machines", map[string]any{
		"count": len(vms),
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
