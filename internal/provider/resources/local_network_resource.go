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
	_ resource.Resource              = &LocalNetworkResource{}
	_ resource.ResourceWithConfigure = &LocalNetworkResource{}
)

type LocalNetworkResource struct {
	client *client.Client
}

type LocalNetworkResourceModel struct {
	ID         types.Int64  `tfsdk:"id"`
	Region     types.String `tfsdk:"region"`
	ProjectTag types.String `tfsdk:"project_tag"`
	Name       types.String `tfsdk:"name"`
	CIDR       types.String `tfsdk:"cidr"`
	Gateway    types.String `tfsdk:"gateway"`
}

func NewLocalNetworkResource() resource.Resource {
	return &LocalNetworkResource{}
}

func (r *LocalNetworkResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_local_network"
}

func (r *LocalNetworkResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a ProData local network.",

		Attributes: map[string]schema.Attribute{
			"id": schema.Int64Attribute{
				MarkdownDescription: "The unique identifier of the local network.",
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
				MarkdownDescription: "Project tag where the local network will be created. If not specified, uses the provider's default project_tag.",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The name of the local network. This is the only attribute that can be updated in-place.",
				Required:            true,
			},
			"cidr": schema.StringAttribute{
				MarkdownDescription: "The CIDR block for the local network (e.g., 10.0.0.0/24).",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"gateway": schema.StringAttribute{
				MarkdownDescription: "The gateway IP address for the local network (e.g., 10.0.0.1).",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
		},
	}
}

func (r *LocalNetworkResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *LocalNetworkResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data LocalNetworkResourceModel

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

	createReq := client.CreateLocalNetworkRequest{
		Region:     region,
		ProjectTag: projectTag,
		Name:       data.Name.ValueString(),
		CIDR:       data.CIDR.ValueString(),
		Gateway:    data.Gateway.ValueString(),
	}

	tflog.Debug(ctx, "Creating local network", map[string]any{
		"name":        createReq.Name,
		"region":      createReq.Region,
		"project_tag": createReq.ProjectTag,
		"cidr":        createReq.CIDR,
		"gateway":     createReq.Gateway,
	})

	network, err := r.client.CreateLocalNetwork(ctx, createReq)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Create Local Network", err.Error())
		return
	}

	data.ID = types.Int64Value(network.ID)
	data.Region = types.StringValue(region)
	data.ProjectTag = types.StringValue(projectTag)
	data.Name = types.StringValue(network.Name)
	data.CIDR = types.StringValue(network.CIDR)
	data.Gateway = types.StringValue(network.Gateway)

	tflog.Debug(ctx, "Created local network", map[string]any{
		"id":   network.ID,
		"name": network.Name,
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *LocalNetworkResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data LocalNetworkResourceModel

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

	networkID := data.ID.ValueInt64()

	tflog.Debug(ctx, "Reading local network", map[string]any{
		"id":          networkID,
		"region":      opts.Region,
		"project_tag": opts.ProjectTag,
	})

	network, err := r.client.GetLocalNetwork(ctx, networkID, opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Local Network", err.Error())
		return
	}

	data.Name = types.StringValue(network.Name)
	data.CIDR = types.StringValue(network.CIDR)
	data.Gateway = types.StringValue(network.Gateway)

	tflog.Debug(ctx, "Read local network", map[string]any{
		"id":   networkID,
		"name": network.Name,
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *LocalNetworkResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan LocalNetworkResourceModel
	var state LocalNetworkResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	networkID := state.ID.ValueInt64()

	// Only name can be updated via API, region and projectTag in request body
	updateReq := client.UpdateLocalNetworkRequest{
		Name: plan.Name.ValueString(),
	}
	if !plan.Region.IsNull() && !plan.Region.IsUnknown() {
		updateReq.Region = plan.Region.ValueString()
	}
	if !plan.ProjectTag.IsNull() && !plan.ProjectTag.IsUnknown() {
		updateReq.ProjectTag = plan.ProjectTag.ValueString()
	}

	tflog.Debug(ctx, "Updating local network", map[string]any{
		"id":          networkID,
		"name":        updateReq.Name,
		"region":      updateReq.Region,
		"project_tag": updateReq.ProjectTag,
	})

	network, err := r.client.UpdateLocalNetwork(ctx, networkID, updateReq)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Update Local Network", err.Error())
		return
	}

	plan.ID = state.ID
	plan.Name = types.StringValue(network.Name)
	plan.CIDR = types.StringValue(network.CIDR)
	plan.Gateway = types.StringValue(network.Gateway)

	tflog.Debug(ctx, "Updated local network", map[string]any{
		"id":   networkID,
		"name": network.Name,
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *LocalNetworkResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data LocalNetworkResourceModel

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

	networkID := data.ID.ValueInt64()

	tflog.Debug(ctx, "Deleting local network", map[string]any{
		"id":          networkID,
		"region":      opts.Region,
		"project_tag": opts.ProjectTag,
	})

	err := r.client.DeleteLocalNetwork(ctx, networkID, opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Delete Local Network", err.Error())
		return
	}

	tflog.Debug(ctx, "Deleted local network", map[string]any{
		"id": networkID,
	})
}
