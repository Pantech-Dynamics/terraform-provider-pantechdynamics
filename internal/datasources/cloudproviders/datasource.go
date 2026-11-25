// Package cloudproviders implements the pantechdynamics_cloud_providers data
// source: the clouds Pantech Migrate can read.
package cloudproviders

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// providersAPI is the part of the API client this data source needs.
type providersAPI interface {
	ListCloudProviders(ctx context.Context) ([]client.CloudProvider, error)
}

var _ datasource.DataSourceWithConfigure = &DataSource{}

// DataSource lists the clouds Pantech Migrate can connect.
type DataSource struct {
	api providersAPI
}

// New is the factory the provider registers.
func New() datasource.DataSource { return &DataSource{} }

type model struct {
	ID        types.String    `tfsdk:"id"`
	Providers []providerModel `tfsdk:"providers"`
}

type providerModel struct {
	Slug           types.String `tfsdk:"slug"`
	Name           types.String `tfsdk:"name"`
	Access         types.String `tfsdk:"access"`
	CostReadable   types.Bool   `tfsdk:"cost_readable"`
	PeopleReadable types.Bool   `tfsdk:"people_readable"`
	Available      types.Bool   `tfsdk:"available"`
}

// Metadata sets the type name: pantechdynamics_cloud_providers.
func (d *DataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cloud_providers"
}

// Schema describes the attributes.
func (d *DataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists every cloud Pantech Migrate can connect with a pantechdynamics_cloud_connection, how Pantech reads it, and whether a scan reads its spend and its people.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "Always \"cloud_providers\"."},
			"providers": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The clouds, in the order the API lists them.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"slug": schema.StringAttribute{Computed: true, Description: "The value for a pantechdynamics_cloud_connection's cloud, such as \"aws\", \"gcp\" or \"hetzner\"."},
					"name": schema.StringAttribute{Computed: true, Description: "The cloud's name."},
					"access": schema.StringAttribute{Computed: true, Description: "\"federated\": Pantech reads the cloud with short-lived tokens it signs and the cloud trusts. " +
						"\"agent\": the customer runs the Pantech Migrate agent, which keeps the read-only token on their machine."},
					"cost_readable":   schema.BoolAttribute{Computed: true, Description: "Whether a scan reads the source's spend."},
					"people_readable": schema.BoolAttribute{Computed: true, Description: "Whether a scan lists the people with access."},
					"available":       schema.BoolAttribute{Computed: true, Description: "Whether this platform can connect it now."},
				}},
			},
		},
	}
}

// Configure receives the API client from the provider.
func (d *DataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(providersAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	d.api = api
}

// Read fetches the providers.
func (d *DataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	list, err := d.api.ListCloudProviders(ctx)
	if err != nil {
		resourcekit.AddHintedError(&resp.Diagnostics, "Error reading cloud providers", err, map[string]string{
			client.CodeMigrateUnavailable: "Pantech Migrate is not available on this platform yet.",
		})
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(list))...)
}

// fromAPIResponse builds the state. The list is never null.
func fromAPIResponse(list []client.CloudProvider) model {
	m := model{ID: types.StringValue("cloud_providers"), Providers: make([]providerModel, 0, len(list))}
	for _, p := range list {
		m.Providers = append(m.Providers, providerModel{
			Slug: types.StringValue(p.Slug), Name: types.StringValue(p.Name), Access: types.StringValue(p.Access),
			CostReadable: types.BoolValue(p.CostReadable), PeopleReadable: types.BoolValue(p.PeopleReadable), Available: types.BoolValue(p.Available),
		})
	}
	return m
}
