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
	_ datasource.DataSource              = &VmDataSource{}
	_ datasource.DataSourceWithConfigure = &VmDataSource{}
)

type VmDataSource struct {
	client *client.Client
}

type VmDataSourceModel struct {
	ID         types.Int64  `tfsdk:"id"`
	Region     types.String `tfsdk:"region"`
	ProjectTag types.String `tfsdk:"project_tag"`
	Name       types.String `tfsdk:"name"`
}

func NewVmDataSource() datasource.DataSource {
	return &VmDataSource{}
}

func (d *VmDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm"
}

func (d *VmDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lookup a ProData virtual machine by ID.",

		Attributes: map[string]schema.Attribute{
			"id": schema.Int64Attribute{
				MarkdownDescription: "The unique identifier of the virtual machine.",
				Required:            true,
			},
			"region": schema.StringAttribute{
				MarkdownDescription: "Region ID override. If not specified, uses the provider's default region.",
				Optional:            true,
			},
			"project_tag": schema.StringAttribute{
				MarkdownDescription: "Project Tag override. If not specified, uses the provider's default project tag.",
				Optional:            true,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The name of the virtual machine.",
				Computed:            true,
			},
		},
	}
}

func (d *VmDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *VmDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data VmDataSourceModel

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

	vmID := data.ID.ValueInt64()

	tflog.Debug(ctx, "Reading virtual machine", map[string]any{
		"id":          vmID,
		"region":      opts.Region,
		"project_tag": opts.ProjectTag,
	})

	vm, err := d.client.GetVm(ctx, vmID, opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Virtual Machine", err.Error())
		return
	}

	data.Name = types.StringValue(vm.Name)

	tflog.Debug(ctx, "Successfully read virtual machine", map[string]any{
		"id":   vmID,
		"name": vm.Name,
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
