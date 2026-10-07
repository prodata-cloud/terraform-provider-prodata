package datasources

import (
	"context"
	"fmt"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ datasource.DataSource                     = &LocalNetworkDataSource{}
	_ datasource.DataSourceWithConfigure        = &LocalNetworkDataSource{}
	_ datasource.DataSourceWithConfigValidators = &LocalNetworkDataSource{}
)

type LocalNetworkDataSource struct {
	client *client.Client
}

type LocalNetworkDataSourceModel struct {
	ID         types.Int64  `tfsdk:"id"`
	Region     types.String `tfsdk:"region"`
	ProjectTag types.String `tfsdk:"project_tag"`
	Name       types.String `tfsdk:"name"`
	CIDR       types.String `tfsdk:"cidr"`
	Gateway    types.String `tfsdk:"gateway"`
	Linked     types.Bool   `tfsdk:"linked"`
}

func NewLocalNetworkDataSource() datasource.DataSource {
	return &LocalNetworkDataSource{}
}

func (d *LocalNetworkDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_local_network"
}

func (d *LocalNetworkDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lookup a ProData local network by `id` or by `name` (exactly one is required).",

		Attributes: map[string]schema.Attribute{
			"id": schema.Int64Attribute{
				MarkdownDescription: "The unique identifier of the local network. Conflicts with `name`. " +
					"If you look the network up by `name`, this is populated from the API.",
				Optional: true,
				Computed: true,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The name of the local network. Matched exactly (case-sensitive) among the " +
					"networks of the selected region and project. Conflicts with `id`. " +
					"If you look the network up by `id`, this is populated from the API.",
				Optional: true,
				Computed: true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"region": schema.StringAttribute{
				MarkdownDescription: "Region ID override. If not specified, uses the provider's default region.",
				Optional:            true,
			},
			"project_tag": schema.StringAttribute{
				MarkdownDescription: "Project Tag override. If not specified, uses the provider's default project tag.",
				Optional:            true,
			},
			"cidr": schema.StringAttribute{
				MarkdownDescription: "The CIDR block of the local network.",
				Computed:            true,
			},
			"gateway": schema.StringAttribute{
				MarkdownDescription: "The gateway IP address of the local network.",
				Computed:            true,
			},
			"linked": schema.BoolAttribute{
				MarkdownDescription: "Whether the local network is linked to an instance.",
				Computed:            true,
			},
		},
	}
}

func (d *LocalNetworkDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(
			path.MatchRoot("id"),
			path.MatchRoot("name"),
		),
	}
}

// matchLocalNetworksByName returns the networks whose name equals name exactly.
// The match is case-sensitive on purpose: the panel enforces name uniqueness with a
// case-sensitive check, so "Net" and "net" are distinct networks and folding case here
// would report a false ambiguity. Names are not unique on every panel path (the v2 rename
// endpoint, the v1 create race and migration-created networks do not enforce it), so the
// caller must handle more than one match.
func matchLocalNetworksByName(networks []client.LocalNetwork, name string) []client.LocalNetwork {
	var matches []client.LocalNetwork
	for _, n := range networks {
		if n.Name == name {
			matches = append(matches, n)
		}
	}
	return matches
}

func (d *LocalNetworkDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *LocalNetworkDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data LocalNetworkDataSourceModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// ExactlyOneOf stays silent while either value is still unknown at validate time (e.g.
	// id = prodata_local_network.x.id next to a literal name), so both can reach Read. Without
	// this guard the id branch would win and the configured name would be silently ignored.
	if !data.ID.IsNull() && !data.Name.IsNull() {
		resp.Diagnostics.AddError("Conflicting Local Network Lookup",
			"Only one of `id` or `name` may be set; both were provided.")
		return
	}

	opts := &client.RequestOpts{}
	if !data.Region.IsNull() && !data.Region.IsUnknown() {
		opts.Region = data.Region.ValueString()
	}
	if !data.ProjectTag.IsNull() && !data.ProjectTag.IsUnknown() {
		opts.ProjectTag = data.ProjectTag.ValueString()
	}

	var network *client.LocalNetwork
	if !data.ID.IsNull() && !data.ID.IsUnknown() {
		networkID := data.ID.ValueInt64()

		tflog.Debug(ctx, "Reading local network by id", map[string]any{
			"id":          networkID,
			"region":      opts.Region,
			"project_tag": opts.ProjectTag,
		})

		found, err := d.client.GetLocalNetwork(ctx, networkID, opts)
		if err != nil {
			resp.Diagnostics.AddError("Unable to Read Local Network", err.Error())
			return
		}
		network = found
	} else {
		name := data.Name.ValueString()

		tflog.Debug(ctx, "Reading local network by name", map[string]any{
			"name":        name,
			"region":      opts.Region,
			"project_tag": opts.ProjectTag,
		})

		networks, err := d.client.GetLocalNetworks(ctx, opts)
		if err != nil {
			resp.Diagnostics.AddError("Unable to List Local Networks", err.Error())
			return
		}

		matches := matchLocalNetworksByName(networks, name)
		switch len(matches) {
		case 0:
			resp.Diagnostics.AddError("Local Network Not Found",
				fmt.Sprintf("No local network named %q was found in this region/project.", name))
			return
		case 1:
			network = &matches[0]
		default:
			resp.Diagnostics.AddError("Ambiguous Local Network Name",
				fmt.Sprintf("%d local networks named %q exist in this region/project; look the network up by id instead.",
					len(matches), name))
			return
		}
	}

	data.ID = types.Int64Value(network.ID)
	data.Name = types.StringValue(network.Name)
	data.CIDR = types.StringValue(network.CIDR)
	data.Gateway = types.StringValue(network.Gateway)
	data.Linked = types.BoolValue(network.Linked)

	tflog.Debug(ctx, "Successfully read local network", map[string]any{
		"id":   network.ID,
		"name": network.Name,
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
