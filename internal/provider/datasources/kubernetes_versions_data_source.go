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
	_ datasource.DataSource              = &K8sVersionsDataSource{}
	_ datasource.DataSourceWithConfigure = &K8sVersionsDataSource{}
)

type K8sVersionsDataSource struct {
	c *client.Client
}

type K8sVersionsDataSourceModel struct {
	Region        types.String        `tfsdk:"region"`
	ProjectTag    types.String        `tfsdk:"project_tag"`
	IncludeDebug  types.Bool          `tfsdk:"include_debug"`
	LatestVersion types.String        `tfsdk:"latest_version"`
	Versions      []K8sVersionSummary `tfsdk:"versions"`
}

type K8sVersionSummary struct {
	ID      types.Int64  `tfsdk:"id"`
	Version types.String `tfsdk:"version"`
	IsDebug types.Bool   `tfsdk:"is_debug"`
}

func NewK8sVersionsDataSource() datasource.DataSource {
	return &K8sVersionsDataSource{}
}

func (d *K8sVersionsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_versions"
}

func (d *K8sVersionsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "List the selectable Kubernetes versions and the latest stable one. " +
			"NOTE: the backend returns versions account-wide rather than per-region, so a version listed " +
			"here is not guaranteed to be offered in every region; cluster create validates the version " +
			"against the target region and errors if it is unavailable.",
		Attributes: map[string]schema.Attribute{
			"region": schema.StringAttribute{
				MarkdownDescription: "Region ID override. If omitted, uses the provider default.",
				Optional:            true,
			},
			"project_tag": schema.StringAttribute{
				MarkdownDescription: "Project tag override. If omitted, uses the provider default.",
				Optional:            true,
			},
			"include_debug": schema.BoolAttribute{
				MarkdownDescription: "Include internal debug builds in `versions`. Defaults to false. " +
					"`latest_version` never considers debug builds.",
				Optional: true,
			},
			"latest_version": schema.StringAttribute{
				MarkdownDescription: "The highest non-debug version, by numeric `major.minor.patch` order. " +
					"Null if no stable version is available.",
				Computed: true,
			},
			"versions": schema.ListNestedAttribute{
				MarkdownDescription: "Available versions, in the order returned by the server.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.Int64Attribute{
							MarkdownDescription: "Version ID.",
							Computed:            true,
						},
						"version": schema.StringAttribute{
							MarkdownDescription: "Version string (e.g. `v1.31.4`).",
							Computed:            true,
						},
						"is_debug": schema.BoolAttribute{
							MarkdownDescription: "Whether this is an internal debug build.",
							Computed:            true,
						},
					},
				},
			},
		},
	}
}

func (d *K8sVersionsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T. Please report this issue to the provider developers.", req.ProviderData))
		return
	}
	d.c = c
}

func (d *K8sVersionsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data K8sVersionsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	opts := scopeOpts(data.Region, data.ProjectTag)
	includeDebug := data.IncludeDebug.ValueBool()

	tflog.Debug(ctx, "Listing Kubernetes versions", map[string]any{"include_debug": includeDebug})
	versions, err := d.c.ListKuberVersions(ctx, opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to list Kubernetes versions", client.KuberErrorDetail(err))
		return
	}

	data.Versions = make([]K8sVersionSummary, 0, len(versions))
	latest := ""
	for _, v := range versions {
		if !v.IsDebug && (latest == "" || compareK8sVersion(v.Version, latest) > 0) {
			latest = v.Version
		}
		if v.IsDebug && !includeDebug {
			continue
		}
		data.Versions = append(data.Versions, K8sVersionSummary{
			ID:      types.Int64Value(v.ID),
			Version: types.StringValue(v.Version),
			IsDebug: types.BoolValue(v.IsDebug),
		})
	}
	if latest != "" {
		data.LatestVersion = types.StringValue(latest)
	} else {
		data.LatestVersion = types.StringNull()
	}

	tflog.Debug(ctx, "Listed Kubernetes versions", map[string]any{"count": len(data.Versions), "latest": latest})
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
