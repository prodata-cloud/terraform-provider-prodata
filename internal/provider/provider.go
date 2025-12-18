package provider

import (
	"context"
	"os"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Ensure ProDataProvider satisfies various provider interfaces.
var (
	_ provider.Provider = &ProDataProvider{}
)

// ProDataProvider defines the provider implementation.
type ProDataProvider struct {
	version string
}

// ProDataProviderModel describes the provider data model.
type ProDataProviderModel struct {
	ApiBaseUrl   types.String `tfsdk:"api_base_url"`
	ApiKeyId     types.String `tfsdk:"api_key_id"`
	ApiSecretKey types.String `tfsdk:"api_secret_key"`
	Region       types.String `tfsdk:"region"`
	ProjectId    types.String `tfsdk:"project_id"`
}

func (p *ProDataProvider) Metadata(ctx context.Context, req provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "prodata"
	resp.Version = p.version
}

func (p *ProDataProvider) Schema(ctx context.Context, req provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The ProData provider allows you to interact with ProData Cloud resources.",
		Attributes: map[string]schema.Attribute{
			"api_base_url": schema.StringAttribute{
				MarkdownDescription: "The base URL of the ProData API. Can also be set via `PRODATA_API_BASE_URL` environment variable.",
				Optional:            true,
			},
			"api_key_id": schema.StringAttribute{
				MarkdownDescription: "The API Key ID for authentication. Can also be set via `PRODATA_API_KEY_ID` environment variable.",
				Optional:            true,
			},
			"api_secret_key": schema.StringAttribute{
				MarkdownDescription: "The API Secret Key for authentication. Can also be set via `PRODATA_API_SECRET_KEY` environment variable.",
				Optional:            true,
				Sensitive:           true,
			},
			"region": schema.StringAttribute{
				MarkdownDescription: "The region to use. Can also be set via `PRODATA_REGION` environment variable.",
				Optional:            true,
			},
			"project_id": schema.StringAttribute{
				MarkdownDescription: "The project to use. Can also be set via `PRODATA_PROJECT_ID` environment variable.",
				Optional:            true,
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

	// Environment variable fallbacks
	apiBaseUrl := os.Getenv("PRODATA_API_BASE_URL")
	apiKeyId := os.Getenv("PRODATA_API_KEY_ID")
	apiSecretKey := os.Getenv("PRODATA_API_SECRET_KEY")
	region := os.Getenv("PRODATA_REGION")
	projectId := os.Getenv("PRODATA_PROJECT_ID")

	// Data values override environment variables
	if !data.ApiBaseUrl.IsNull() {
		apiBaseUrl = data.ApiBaseUrl.ValueString()
	}
	if !data.ApiKeyId.IsNull() {
		apiKeyId = data.ApiKeyId.ValueString()
	}
	if !data.ApiSecretKey.IsNull() {
		apiSecretKey = data.ApiSecretKey.ValueString()
	}
	if !data.Region.IsNull() {
		region = data.Region.ValueString()
	}
	if !data.ProjectId.IsNull() {
		projectId = data.ProjectId.ValueString()
	}

	// Validation
	if apiBaseUrl == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("api_base_url"),
			"Missing API Base URL",
			"The provider requires api_base_url to be set either in the configuration or via PRODATA_API_URL environment variable.",
		)
	}
	if apiKeyId == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("api_key_id"),
			"Missing API Key ID",
			"The provider requires api_key_id to be set either in the configuration or via PRODATA_API_KEY_ID environment variable.",
		)
	}
	if apiSecretKey == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("api_secret_key"),
			"Missing API Secret Key",
			"The provider requires api_secret_key to be set either in the configuration or via PRODATA_API_SECRET_KEY environment variable.",
		)
	}
	if region == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("region"),
			"Missing Region",
			"The provider requires region to be set either in the configuration or via PRODATA_REGION environment variable.",
		)
	}
	if projectId == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("project_id"),
			"Missing Project",
			"The provider requires project to be set either in the configuration or via PRODATA_PROJECT_ID environment variable.",
		)
	}

	if resp.Diagnostics.HasError() {
		return
	}

	// Create client
	client, err := client.NewClient(&client.ClientConfig{
		ApiBaseUrl:   apiBaseUrl,
		ApiKeyId:     apiKeyId,
		ApiSecretKey: apiSecretKey,
		Region:       region,
		Project:      projectId,
	})
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to create ProData client",
			err.Error(),
		)
		return
	}

	// Make client available to resources
	resp.DataSourceData = client
	resp.ResourceData = client
}

// Resources defines the resources implemented in the provider.
func (p *ProDataProvider) Resources(ctx context.Context) []func() resource.Resource {
	return []func() resource.Resource{}
}

// DataSources defines the data sources implemented in the provider.
func (p *ProDataProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{}
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &ProDataProvider{
			version: version,
		}
	}
}
