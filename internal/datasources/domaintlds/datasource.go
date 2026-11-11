// Package domaintlds implements the pantechdynamics_domain_tlds data source.
package domaintlds

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// tldAPI is the part of the API client this data source needs.
type tldAPI interface {
	ListDomainTLDs(ctx context.Context) ([]client.TLDOffer, error)
}

var _ datasource.DataSourceWithConfigure = &DataSource{}

// DataSource lists the TLDs Pantech sells and their prices.
type DataSource struct {
	api tldAPI
}

// New is the factory the provider registers.
func New() datasource.DataSource { return &DataSource{} }

type model struct {
	ID   types.String `tfsdk:"id"`
	TLDs []tldModel   `tfsdk:"tlds"`
}

type tldModel struct {
	TLD           types.String `tfsdk:"tld"`
	Currency      types.String `tfsdk:"currency"`
	RegisterMinor types.Int64  `tfsdk:"register_minor"`
	RenewMinor    types.Int64  `tfsdk:"renew_minor"`
	TransferMinor types.Int64  `tfsdk:"transfer_minor"`
	Featured      types.Bool   `tfsdk:"featured"`
	PrivacyOn     types.Bool   `tfsdk:"privacy_always_on"`
}

// Metadata sets the type name: pantechdynamics_domain_tlds.
func (d *DataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_domain_tlds"
}

// Schema describes the attributes.
func (d *DataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists every top-level domain Pantech sells, with its yearly prices in your organization's currency, in minor units (kobo or cents), before VAT. " +
			"Registering for n years costs register_minor + renew_minor x (n - 1); a transfer costs transfer_minor + renew_minor x (n - 1).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "Always \"domain_tlds\"."},
			"tlds": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The TLDs, in the order the API lists them.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"tld":               schema.StringAttribute{Computed: true, Description: "The TLD, without a leading dot, such as \"com\"."},
					"currency":          schema.StringAttribute{Computed: true, Description: "Currency of the prices: \"NGN\" or \"USD\"."},
					"register_minor":    schema.Int64Attribute{Computed: true, Description: "Price of the first year of a registration, in minor units."},
					"renew_minor":       schema.Int64Attribute{Computed: true, Description: "Price of a renewal year, in minor units."},
					"transfer_minor":    schema.Int64Attribute{Computed: true, Description: "Price of a transfer to Pantech (which adds one year), in minor units."},
					"featured":          schema.BoolAttribute{Computed: true, Description: "Whether Pantech features the TLD."},
					"privacy_always_on": schema.BoolAttribute{Computed: true, Description: "True when the TLD's registry hides every registrant's contact details itself (.ng and its second levels): privacy is always on and cannot be turned off."},
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
	api, ok := req.ProviderData.(tldAPI)
	if !ok {
		resourcekit.ConfigureError(&resp.Diagnostics, req.ProviderData)
		return
	}
	d.api = api
}

// Read fetches the TLDs.
func (d *DataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	list, err := d.api.ListDomainTLDs(ctx)
	if err != nil {
		resourcekit.AddHintedError(&resp.Diagnostics, "Error reading domain TLDs", err, map[string]string{
			client.CodeDomainsUnavailable: "Domains are not sold on this platform yet.",
		})
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(list))...)
}

// fromAPIResponse builds the state. The list is never null.
func fromAPIResponse(list []client.TLDOffer) model {
	m := model{ID: types.StringValue("domain_tlds"), TLDs: make([]tldModel, 0, len(list))}
	for _, t := range list {
		m.TLDs = append(m.TLDs, tldModel{
			TLD: types.StringValue(t.TLD), Currency: types.StringValue(t.Currency), RegisterMinor: types.Int64Value(t.RegisterMinor),
			RenewMinor: types.Int64Value(t.RenewMinor), TransferMinor: types.Int64Value(t.TransferMinor), Featured: types.BoolValue(t.Featured),
			PrivacyOn: types.BoolValue(t.PrivacyAlwaysOn),
		})
	}
	return m
}
