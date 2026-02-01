package resources

import (
	"context"
	"fmt"

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
	_ resource.Resource              = &PublicIPAttachmentResource{}
	_ resource.ResourceWithConfigure = &PublicIPAttachmentResource{}
)

type PublicIPAttachmentResource struct {
	client *client.Client
}

type PublicIPAttachmentResourceModel struct {
	VmID       types.Int64  `tfsdk:"vm_id"`
	PublicIPID types.Int64  `tfsdk:"public_ip_id"`
	PublicIP   types.String `tfsdk:"public_ip"`
	Region     types.String `tfsdk:"region"`
	ProjectTag types.String `tfsdk:"project_tag"`
}

func NewPublicIPAttachmentResource() resource.Resource {
	return &PublicIPAttachmentResource{}
}

func (r *PublicIPAttachmentResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_public_ip_attachment"
}

func (r *PublicIPAttachmentResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Attaches a ProData public IP to a virtual machine. " +
			"Destroying this resource detaches the public IP from the VM.",

		Attributes: map[string]schema.Attribute{
			"vm_id": schema.Int64Attribute{
				MarkdownDescription: "The ID of the virtual machine to attach the public IP to.",
				Required:            true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"public_ip_id": schema.Int64Attribute{
				MarkdownDescription: "The ID of the public IP to attach.",
				Required:            true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"public_ip": schema.StringAttribute{
				MarkdownDescription: "The public IP address string assigned to the VM.",
				Computed:            true,
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
				MarkdownDescription: "Project tag override. If not specified, uses the provider's default project_tag.",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *PublicIPAttachmentResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *PublicIPAttachmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data PublicIPAttachmentResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Build request opts for region/project overrides
	opts := r.buildOpts(&data)

	attachReq := client.AttachPublicIPRequest{
		PublicIPID: data.PublicIPID.ValueInt64(),
	}

	vmID := data.VmID.ValueInt64()

	tflog.Debug(ctx, "Attaching public IP to VM", map[string]any{
		"vm_id":        vmID,
		"public_ip_id": attachReq.PublicIPID,
	})

	vm, err := r.client.AttachPublicIP(ctx, vmID, attachReq, opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Attach Public IP", err.Error())
		return
	}

	data.PublicIP = types.StringValue(vm.PublicIP)

	// Store the resolved region/project
	region := data.Region.ValueString()
	if region == "" {
		region = r.client.Region
	}
	projectTag := data.ProjectTag.ValueString()
	if projectTag == "" {
		projectTag = r.client.ProjectTag
	}
	data.Region = types.StringValue(region)
	data.ProjectTag = types.StringValue(projectTag)

	tflog.Debug(ctx, "Attached public IP to VM", map[string]any{
		"vm_id":     vmID,
		"public_ip": vm.PublicIP,
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *PublicIPAttachmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data PublicIPAttachmentResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	opts := r.buildOpts(&data)
	vmID := data.VmID.ValueInt64()

	tflog.Debug(ctx, "Reading public IP attachment", map[string]any{
		"vm_id": vmID,
	})

	vm, err := r.client.GetVm(ctx, vmID, opts)
	if err != nil {
		// If VM is gone, remove from state
		tflog.Warn(ctx, "VM not found, removing public IP attachment from state", map[string]any{
			"vm_id": vmID,
			"error": err.Error(),
		})
		resp.State.RemoveResource(ctx)
		return
	}

	// If VM has no public IP, remove from state
	if vm.PublicIP == "" {
		tflog.Warn(ctx, "VM has no public IP attached, removing from state", map[string]any{
			"vm_id": vmID,
		})
		resp.State.RemoveResource(ctx)
		return
	}

	data.PublicIP = types.StringValue(vm.PublicIP)

	tflog.Debug(ctx, "Read public IP attachment", map[string]any{
		"vm_id":     vmID,
		"public_ip": vm.PublicIP,
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *PublicIPAttachmentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// All attributes are ForceNew, so Update should never be called.
	resp.Diagnostics.AddError(
		"Update Not Supported",
		"All attributes of prodata_public_ip_attachment require replacement. This is a bug in the provider.",
	)
}

func (r *PublicIPAttachmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data PublicIPAttachmentResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	opts := r.buildOpts(&data)
	vmID := data.VmID.ValueInt64()

	tflog.Debug(ctx, "Detaching public IP from VM", map[string]any{
		"vm_id": vmID,
	})

	err := r.client.DetachPublicIP(ctx, vmID, opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Detach Public IP", err.Error())
		return
	}

	tflog.Debug(ctx, "Detached public IP from VM", map[string]any{
		"vm_id": vmID,
	})
}

func (r *PublicIPAttachmentResource) buildOpts(data *PublicIPAttachmentResourceModel) *client.RequestOpts {
	opts := &client.RequestOpts{}
	if !data.Region.IsNull() && !data.Region.IsUnknown() {
		opts.Region = data.Region.ValueString()
	}
	if !data.ProjectTag.IsNull() && !data.ProjectTag.IsUnknown() {
		opts.ProjectTag = data.ProjectTag.ValueString()
	}
	return opts
}
