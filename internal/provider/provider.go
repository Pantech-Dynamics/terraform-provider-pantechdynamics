// Package provider wires the Pantech Dynamics Terraform provider together.
package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const (
	envBaseURL = "PANTECHDYNAMICS_BASE_URL"
	envAPIKey  = "PANTECHDYNAMICS_API_KEY"
)

var _ provider.Provider = &PantechDynamicsProvider{}

// PantechDynamicsProvider implements provider.Provider.
type PantechDynamicsProvider struct {
	// version is "dev" for local builds, "test" in acceptance tests, and the
	// release version otherwise.
	version string
}

// providerModel maps the provider block in HCL to Go.
type providerModel struct {
	BaseURL types.String `tfsdk:"base_url"`
	APIKey  types.String `tfsdk:"api_key"`
}

// New returns a provider factory, as the framework expects.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &PantechDynamicsProvider{version: version}
	}
}

// Metadata sets the provider type name, which prefixes every resource name.
func (p *PantechDynamicsProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "pantechdynamics"
	resp.Version = p.version
}

// Schema describes the provider block.
func (p *PantechDynamicsProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manage Pantech Dynamics cloud resources.",
		Attributes: map[string]schema.Attribute{
			"base_url": schema.StringAttribute{
				Description: "Base URL of the Pantech Dynamics API, including the version prefix. Can also be set with the " + envBaseURL + " environment variable.",
				Optional:    true,
			},
			"api_key": schema.StringAttribute{
				Description: "API key (starts with PAN_). Can also be set with the " + envAPIKey + " environment variable.",
				Optional:    true,
				Sensitive:   true,
			},
		},
	}
}

// Configure resolves the provider settings. The API client is built here once
// it exists, and handed to resources and data sources.
func (p *PantechDynamicsProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	baseURL := valueOrEnv(cfg.BaseURL, envBaseURL)
	apiKey := valueOrEnv(cfg.APIKey, envAPIKey)

	if baseURL == "" {
		resp.Diagnostics.AddError("Missing base_url", "Set base_url in the provider block or the "+envBaseURL+" environment variable.")
	}
	if apiKey == "" {
		resp.Diagnostics.AddError("Missing api_key", "Set api_key in the provider block or the "+envAPIKey+" environment variable.")
	}
}

// Resources lists the resources this provider offers.
func (p *PantechDynamicsProvider) Resources(_ context.Context) []func() resource.Resource {
	return nil
}

// DataSources lists the data sources this provider offers.
func (p *PantechDynamicsProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return nil
}

// valueOrEnv prefers an explicit HCL value over the environment variable.
func valueOrEnv(v types.String, env string) string {
	if !v.IsNull() && !v.IsUnknown() {
		return v.ValueString()
	}
	return os.Getenv(env)
}
