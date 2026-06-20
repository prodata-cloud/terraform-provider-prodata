package resources

import (
	"context"
	"fmt"
	"strconv"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource                = &LocalNetworkResource{}
	_ resource.ResourceWithConfigure   = &LocalNetworkResource{}
	_ resource.ResourceWithImportState = &LocalNetworkResource{}
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

	network, err := client.RetryOnBusy(ctx, client.RetryTimeoutShort, func() (*client.LocalNetwork, error) {
		return r.client.CreateLocalNetwork(ctx, createReq)
	})
	if err != nil {
		// Error 614: network with this name already exists — adopt it into state.
		// This happens when a previous create succeeded on the server but Terraform
		// lost track of the state (e.g., timeout, interrupted apply).
		if client.IsAPIError(err, 614) {
			existing, adoptErr := r.findLocalNetworkByName(ctx, createReq.Name, createReq.Region, createReq.ProjectTag)
			if adoptErr != nil {
				resp.Diagnostics.AddError("Unable to Create Local Network",
					fmt.Sprintf("network %q already exists but failed to look it up: %s (original error: %s)", createReq.Name, adoptErr, err))
				return
			}
			// cidr and gateway are Required + RequiresReplace. Adopting a network whose
			// CIDR/gateway differ from config would write the server's values into state,
			// so every subsequent plan would force a destroy/recreate (which hits 614
			// again) — a permanent diff / adoption loop. Refuse to adopt a mismatch.
			if existing.CIDR != createReq.CIDR || existing.Gateway != createReq.Gateway {
				resp.Diagnostics.AddError(
					"Conflicting Local Network",
					fmt.Sprintf("a local network named %q already exists with cidr %q and gateway %q, "+
						"which differ from the configured cidr %q and gateway %q. Import the existing "+
						"network (terraform import) or choose a different name — refusing to adopt a "+
						"mismatched network, which would force a destroy/recreate on every apply.",
						createReq.Name, existing.CIDR, existing.Gateway, createReq.CIDR, createReq.Gateway),
				)
				return
			}
			tflog.Info(ctx, "Local network already exists, adopting into state", map[string]any{
				"id":   existing.ID,
				"name": existing.Name,
			})
			network = existing
		} else {
			resp.Diagnostics.AddError("Unable to Create Local Network", err.Error())
			return
		}
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

// findLocalNetworkByName looks up an existing local network by name.
func (r *LocalNetworkResource) findLocalNetworkByName(ctx context.Context, name, region, projectTag string) (*client.LocalNetwork, error) {
	opts := &client.RequestOpts{
		Region:     region,
		ProjectTag: projectTag,
	}
	networks, err := r.client.GetLocalNetworks(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to list local networks: %w", err)
	}
	for i := range networks {
		if networks[i].Name == name {
			return &networks[i], nil
		}
	}
	return nil, fmt.Errorf("local network %q not found in list", name)
}

func (r *LocalNetworkResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data LocalNetworkResourceModel

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

	networkID := data.ID.ValueInt64()

	tflog.Debug(ctx, "Reading local network", map[string]any{
		"id":          networkID,
		"region":      opts.Region,
		"project_tag": opts.ProjectTag,
	})

	network, err := r.client.GetLocalNetwork(ctx, networkID, opts)
	if err != nil {
		if client.IsNotFound(err) {
			tflog.Warn(ctx, "Local network not found, removing from state", map[string]any{"id": networkID})
			resp.State.RemoveResource(ctx)
			return
		}
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

	err := client.RetryVoidOnBusy(ctx, client.RetryTimeoutShort, func() error {
		return r.client.DeleteLocalNetwork(ctx, networkID, opts)
	})
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Unable to Delete Local Network", err.Error())
		return
	}

	tflog.Debug(ctx, "Deleted local network", map[string]any{"id": networkID})
}

func (r *LocalNetworkResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id, err := strconv.ParseInt(req.ID, 10, 64)
	if err != nil {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected integer local network ID, got: %s\n\n"+
				"Usage: terraform import prodata_local_network.example <network_id>\n"+
				"Example: terraform import prodata_local_network.example 123", req.ID),
		)
		return
	}

	tflog.Info(ctx, "Importing local network", map[string]any{"id": id})
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("region"), r.client.Region)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_tag"), r.client.ProjectTag)...)
}
