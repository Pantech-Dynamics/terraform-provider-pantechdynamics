// Package provider wires the Pantech Dynamics Terraform provider together.
package provider

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/datasources/diskofferings"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/datasources/images"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/datasources/plans"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/datasources/regions"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resources/instance"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resources/securitygroup"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resources/sshkey"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resources/volume"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const (
	envBaseURL        = "PANTECHDYNAMICS_BASE_URL"
	envAPIKey         = "PANTECHDYNAMICS_API_KEY"
	envRequestTimeout = "PANTECHDYNAMICS_REQUEST_TIMEOUT"
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
	BaseURL        types.String `tfsdk:"base_url"`
	APIKey         types.String `tfsdk:"api_key"`
	RequestTimeout types.String `tfsdk:"request_timeout"`
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
			"request_timeout": schema.StringAttribute{
				Description: "How long a single API request may take before it is abandoned, as a duration such as \"90s\" or \"2m\". Defaults to 60s. Raise it if the API is slow: some calls have taken over a minute. A request that times out is retried where it is safe to, so a call can take up to three times this long. It does not limit how long an apply waits for a resource: that is the timeouts block on each resource. Can also be set with the " + envRequestTimeout + " environment variable.",
				Optional:    true,
			},
		},
	}
}

// Configure builds the API client once and hands it to every resource and data
// source. Terraform calls it on each run, after the provider block is evaluated.
func (p *PantechDynamicsProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	c, diags := newClient(cfg, p.version)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.ResourceData = c
	resp.DataSourceData = c
}

// newClient resolves the settings (HCL first, then environment) and builds the
// API client. It is separate from Configure so it can be tested without a
// Terraform run.
func newClient(cfg providerModel, version string) (*client.Client, diag.Diagnostics) {
	var diags diag.Diagnostics

	baseURL := valueOrEnv(cfg.BaseURL, envBaseURL)
	apiKey := valueOrEnv(cfg.APIKey, envAPIKey)

	if baseURL == "" {
		diags.AddAttributeError(path.Root("base_url"), "Missing base_url",
			"Set base_url in the provider block or the "+envBaseURL+" environment variable.")
	}
	if apiKey == "" {
		diags.AddAttributeError(path.Root("api_key"), "Missing api_key",
			"Set api_key in the provider block or the "+envAPIKey+" environment variable.")
	}
	if diags.HasError() {
		return nil, diags
	}

	opts, optDiags := clientOptions(cfg)
	diags.Append(optDiags...)
	if diags.HasError() {
		return nil, diags
	}

	c, err := client.New(baseURL, apiKey, version, opts...)
	if err != nil {
		// client.New never echoes the API key in its errors.
		diags.AddAttributeError(path.Root("base_url"), "Invalid provider configuration", err.Error())
		return nil, diags
	}
	return c, diags
}

// clientOptions turns the optional settings into client options. An unset
// request_timeout adds none, so the client keeps its own default.
func clientOptions(cfg providerModel) ([]client.Option, diag.Diagnostics) {
	var diags diag.Diagnostics
	raw := valueOrEnv(cfg.RequestTimeout, envRequestTimeout)
	if raw == "" {
		return nil, diags
	}
	d, err := parseRequestTimeout(raw)
	if err != nil {
		diags.AddAttributeError(path.Root("request_timeout"), "Invalid request_timeout", err.Error())
		return nil, diags
	}
	return []client.Option{client.WithTimeout(d)}, diags
}

// parseRequestTimeout reads a Go duration such as "90s" or "2m" and requires it
// to be positive. A bare number such as "90" is refused, because it has no unit.
func parseRequestTimeout(raw string) (time.Duration, error) {
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%q is not a duration. Use a number with a unit, for example \"90s\" or \"2m\"", raw)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%q must be greater than zero", raw)
	}
	return d, nil
}

// Resources lists the resources this provider offers.
func (p *PantechDynamicsProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		instance.New,
		securitygroup.New,
		sshkey.New,
		volume.New,
	}
}

// DataSources lists the data sources this provider offers.
func (p *PantechDynamicsProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		diskofferings.New,
		images.New,
		images.NewSingle,
		plans.New,
		plans.NewSingle,
		regions.New,
		regions.NewSingle,
	}
}

// valueOrEnv prefers an explicit HCL value over the environment variable.
func valueOrEnv(v types.String, env string) string {
	if !v.IsNull() && !v.IsUnknown() {
		return v.ValueString()
	}
	return os.Getenv(env)
}
