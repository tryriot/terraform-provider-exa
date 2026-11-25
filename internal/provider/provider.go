package provider

import (
	"cmp"
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/tryriot/terraform-provider-exa/internal/exa"
)

const APIKeyEnvVar = "EXA_SERVICE_KEY"

var _ provider.Provider = &ExaProvider{}

type ExaProvider struct {
	version string
	baseURL string // tests only
}

type providerModel struct {
	APIKey types.String `tfsdk:"api_key"`
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &ExaProvider{version: version}
	}
}

func (p *ExaProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "exa"
	resp.Version = p.version
}

func (p *ExaProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages [Exa](https://exa.ai) API keys through the [Team Management API](https://exa.ai/docs/reference/team-management/create-api-key). " +
			"Exa enables that API per team: ask support@exa.ai for access and a service key.",
		Attributes: map[string]schema.Attribute{
			"api_key": schema.StringAttribute{
				MarkdownDescription: "The team's Exa service key, sent as `x-api-key` to the Team Management API. Defaults to the `" + APIKeyEnvVar + "` environment variable.",
				Optional:            true,
				Sensitive:           true,
			},
		},
	}
}

func (p *ExaProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if config.APIKey.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("api_key"), "Unknown Exa service key",
			"api_key depends on a value that is not known until apply. Set it to a known value or use the "+APIKeyEnvVar+" environment variable.")
		return
	}

	apiKey := config.APIKey.ValueString()
	if apiKey == "" {
		apiKey = os.Getenv(APIKeyEnvVar)
	}
	if apiKey == "" {
		resp.Diagnostics.AddAttributeError(path.Root("api_key"), "Missing Exa service key",
			"Set api_key in the provider block or the "+APIKeyEnvVar+" environment variable.")
		return
	}

	client := &exa.Client{
		BaseURL:    cmp.Or(p.baseURL, exa.DefaultBaseURL),
		APIKey:     apiKey,
		UserAgent:  "terraform-provider-exa/" + p.version,
		HTTPClient: exa.NewHTTPClient(),
	}
	resp.ResourceData = client
	resp.DataSourceData = client
}

func (p *ExaProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewAPIKeyResource,
	}
}

func (p *ExaProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return nil
}
