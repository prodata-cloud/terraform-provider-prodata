package provider

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"terraform-provider-prodata/internal/client"
	"terraform-provider-prodata/internal/provider/datasources"
	"terraform-provider-prodata/internal/provider/resources"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ provider.Provider = &ProDataProvider{}

type ProDataProvider struct {
	version string
}

type ProDataProviderModel struct {
	APIBaseURL   types.String `tfsdk:"api_base_url"`
	APIKeyID     types.String `tfsdk:"api_key_id"`
	APISecretKey types.String `tfsdk:"api_secret_key"`
	Region       types.String `tfsdk:"region"`
	ProjectID    types.Int64  `tfsdk:"project_id"`
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &ProDataProvider{version: version}
	}
}

func (p *ProDataProvider) Metadata(ctx context.Context, req provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "prodata"
	resp.Version = p.version
}

func (p *ProDataProvider) Schema(ctx context.Context, req provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manage ProData Cloud resources.",
		Attributes: map[string]schema.Attribute{
			"api_base_url": schema.StringAttribute{
				MarkdownDescription: "ProData API base URL (e.g., `https://my.pro-data.tech`). " +
					"Can also be set via `PRODATA_API_BASE_URL` environment variable.",
				Optional: true,
			},
			"api_key_id": schema.StringAttribute{
				MarkdownDescription: "API Key ID for authentication. " +
					"Can also be set via `PRODATA_API_KEY_ID` environment variable.",
				Optional: true,
			},
			"api_secret_key": schema.StringAttribute{
				MarkdownDescription: "API Secret Key for authentication. " +
					"Can also be set via `PRODATA_API_SECRET_KEY` environment variable.",
				Optional:  true,
				Sensitive: true,
			},
			"region": schema.StringAttribute{
				MarkdownDescription: "Default region ID (e.g., `UZ-5`, `UZ-3`, `KZ-1`). " +
					"Can also be set via `PRODATA_REGION` environment variable.",
				Optional: true,
			},
			"project_id": schema.Int64Attribute{
				MarkdownDescription: "Default project ID. " +
					"Can also be set via `PRODATA_PROJECT_ID` environment variable.",
				Optional: true,
			},
		},
	}
}

func (p *ProDataProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var data ProDataProviderModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Build config: explicit config takes precedence, env vars as fallback.
	cfg := client.Config{}

	if !data.APIBaseURL.IsNull() && !data.APIBaseURL.IsUnknown() {
		cfg.APIBaseURL = data.APIBaseURL.ValueString()
	} else {
		cfg.APIBaseURL = os.Getenv("PRODATA_API_BASE_URL")
	}

	if !data.APIKeyID.IsNull() && !data.APIKeyID.IsUnknown() {
		cfg.APIKeyID = data.APIKeyID.ValueString()
	} else {
		cfg.APIKeyID = os.Getenv("PRODATA_API_KEY_ID")
	}

	if !data.APISecretKey.IsNull() && !data.APISecretKey.IsUnknown() {
		cfg.APISecretKey = data.APISecretKey.ValueString()
	} else {
		cfg.APISecretKey = os.Getenv("PRODATA_API_SECRET_KEY")
	}

	if !data.Region.IsNull() && !data.Region.IsUnknown() {
		cfg.Region = data.Region.ValueString()
	} else {
		cfg.Region = os.Getenv("PRODATA_REGION")
	}

	if !data.ProjectID.IsNull() && !data.ProjectID.IsUnknown() {
		cfg.ProjectID = data.ProjectID.ValueInt64()
	} else if env := os.Getenv("PRODATA_PROJECT_ID"); env != "" {
		if v, err := strconv.ParseInt(env, 10, 64); err != nil {
			resp.Diagnostics.AddWarning(
				"Invalid PRODATA_PROJECT_ID",
				fmt.Sprintf("Could not parse %q as integer: %s", env, err),
			)
		} else {
			cfg.ProjectID = v
		}
	}

	// Validate required fields.
	if cfg.APIBaseURL == "" {
		resp.Diagnostics.AddAttributeError(path.Root("api_base_url"), "Missing API Base URL",
			"Set api_base_url in config or PRODATA_API_BASE_URL environment variable.")
	}
	if cfg.APIKeyID == "" {
		resp.Diagnostics.AddAttributeError(path.Root("api_key_id"), "Missing API Key ID",
			"Set api_key_id in config or PRODATA_API_KEY_ID environment variable.")
	}
	if cfg.APISecretKey == "" {
		resp.Diagnostics.AddAttributeError(path.Root("api_secret_key"), "Missing API Secret Key",
			"Set api_secret_key in config or PRODATA_API_SECRET_KEY environment variable.")
	}
	if resp.Diagnostics.HasError() {
		return
	}

	cfg.UserAgent = "terraform-provider-prodata/" + p.version

	c, err := client.New(cfg)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create client", err.Error())
		return
	}

	resp.DataSourceData = c
	resp.ResourceData = c
}

func (p *ProDataProvider) Resources(ctx context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		resources.NewVolumeResource,
	}
}

func (p *ProDataProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		datasources.NewImageDataSource,
		datasources.NewImagesDataSource,
		datasources.NewVolumeDataSource,
		datasources.NewVolumesDataSource,
	}
}
