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
	_ resource.Resource              = &PublicIPResource{}
	_ resource.ResourceWithConfigure = &PublicIPResource{}
)

type PublicIPResource struct {
	client *client.Client
}

type PublicIPResourceModel struct {
	ID         types.Int64  `tfsdk:"id"`
	Region     types.String `tfsdk:"region"`
	ProjectTag types.String `tfsdk:"project_tag"`
	Name       types.String `tfsdk:"name"`
	IP         types.String `tfsdk:"ip"`
	Mask       types.String `tfsdk:"mask"`
	Gateway    types.String `tfsdk:"gateway"`
}

func NewPublicIPResource() resource.Resource {
	return &PublicIPResource{}
}

func (r *PublicIPResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_public_ip"
}

func (r *PublicIPResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a ProData public IP address.",

		Attributes: map[string]schema.Attribute{
			"id": schema.Int64Attribute{
				MarkdownDescription: "The unique identifier of the public IP.",
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
				MarkdownDescription: "Project tag where the public IP will be created. If not specified, uses the provider's default project_tag.",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The name of the public IP. This is the only attribute that can be updated in-place.",
				Required:            true,
			},
			"ip": schema.StringAttribute{
				MarkdownDescription: "The allocated public IP address.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"mask": schema.StringAttribute{
				MarkdownDescription: "The subnet mask of the public IP (e.g., /24).",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"gateway": schema.StringAttribute{
				MarkdownDescription: "The gateway IP address.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *PublicIPResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *PublicIPResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data PublicIPResourceModel

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

	createReq := client.CreatePublicIPRequest{
		Region:     region,
		ProjectTag: projectTag,
		Name:       data.Name.ValueString(),
	}

	tflog.Debug(ctx, "Creating public IP", map[string]any{
		"name":        createReq.Name,
		"region":      createReq.Region,
		"project_tag": createReq.ProjectTag,
	})

	ip, err := r.client.CreatePublicIP(ctx, createReq)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Create Public IP", err.Error())
		return
	}

	data.ID = types.Int64Value(ip.ID)
	data.Region = types.StringValue(region)
	data.ProjectTag = types.StringValue(projectTag)
	data.Name = types.StringValue(ip.Name)
	data.IP = types.StringValue(ip.IP)
	data.Mask = types.StringValue(ip.Mask)
	data.Gateway = types.StringValue(ip.Gateway)

	tflog.Debug(ctx, "Created public IP", map[string]any{
		"id":   ip.ID,
		"name": ip.Name,
		"ip":   ip.IP,
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *PublicIPResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data PublicIPResourceModel

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

	ipID := data.ID.ValueInt64()

	tflog.Debug(ctx, "Reading public IP", map[string]any{
		"id":          ipID,
		"region":      opts.Region,
		"project_tag": opts.ProjectTag,
	})

	ip, err := r.client.GetPublicIP(ctx, ipID, opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Public IP", err.Error())
		return
	}

	data.Name = types.StringValue(ip.Name)
	data.IP = types.StringValue(ip.IP)
	data.Mask = types.StringValue(ip.Mask)
	data.Gateway = types.StringValue(ip.Gateway)

	tflog.Debug(ctx, "Read public IP", map[string]any{
		"id":   ipID,
		"name": ip.Name,
		"ip":   ip.IP,
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *PublicIPResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan PublicIPResourceModel
	var state PublicIPResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	ipID := state.ID.ValueInt64()

	// Only name can be updated via API, region and projectTag in request body
	updateReq := client.UpdatePublicIPRequest{
		Name: plan.Name.ValueString(),
	}
	if !plan.Region.IsNull() && !plan.Region.IsUnknown() {
		updateReq.Region = plan.Region.ValueString()
	}
	if !plan.ProjectTag.IsNull() && !plan.ProjectTag.IsUnknown() {
		updateReq.ProjectTag = plan.ProjectTag.ValueString()
	}

	tflog.Debug(ctx, "Updating public IP", map[string]any{
		"id":          ipID,
		"name":        updateReq.Name,
		"region":      updateReq.Region,
		"project_tag": updateReq.ProjectTag,
	})

	ip, err := r.client.UpdatePublicIP(ctx, ipID, updateReq)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Update Public IP", err.Error())
		return
	}

	plan.ID = state.ID
	plan.Name = types.StringValue(ip.Name)
	plan.IP = types.StringValue(ip.IP)
	plan.Mask = types.StringValue(ip.Mask)
	plan.Gateway = types.StringValue(ip.Gateway)

	tflog.Debug(ctx, "Updated public IP", map[string]any{
		"id":   ipID,
		"name": ip.Name,
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *PublicIPResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data PublicIPResourceModel

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

	ipID := data.ID.ValueInt64()

	tflog.Debug(ctx, "Deleting public IP", map[string]any{
		"id":          ipID,
		"region":      opts.Region,
		"project_tag": opts.ProjectTag,
	})

	err := r.client.DeletePublicIP(ctx, ipID, opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Delete Public IP", err.Error())
		return
	}

	tflog.Debug(ctx, "Deleted public IP", map[string]any{
		"id": ipID,
	})
}
