package lookups

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/datasources/lookup"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// dnsZoneAPI is the part of the API client the lookup needs.
type dnsZoneAPI interface {
	ListDNSZones(ctx context.Context, q string) ([]client.DNSZone, error)
}

var (
	_ datasource.DataSourceWithConfigure      = &DNSZoneDataSource{}
	_ datasource.DataSourceWithValidateConfig = &DNSZoneDataSource{}
)

// DNSZoneDataSource looks up one Pantech DNS zone by id or name.
type DNSZoneDataSource struct {
	api dnsZoneAPI
}

// NewDNSZone is the factory the provider registers.
func NewDNSZone() datasource.DataSource { return &DNSZoneDataSource{} }

type dnsZoneModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Status      types.String `tfsdk:"status"`
	NameServers types.List   `tfsdk:"name_servers"`
	DomainID    types.String `tfsdk:"domain_id"`
	RecordCount types.Int64  `tfsdk:"record_count"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

// Metadata sets the type name: pantechdynamics_dns_zone.
func (d *DNSZoneDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dns_zone"
}

// Schema describes the attributes.
func (d *DNSZoneDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := selectorAttributes("DNS zone", "dnsz_", true)
	attrs["description"] = schema.StringAttribute{Computed: true, Description: "The zone's description."}
	attrs["status"] = schema.StringAttribute{Computed: true, Description: "State of the zone, for example \"awaiting_delegation\" or \"active\"."}
	attrs["name_servers"] = schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "The Pantech DNS name servers that serve the zone."}
	attrs["domain_id"] = schema.StringAttribute{Computed: true, Description: "Id of the Pantech domain delegated to the zone, or null."}
	attrs["record_count"] = schema.Int64Attribute{Computed: true, Description: "How many record sets the zone holds."}
	attrs["created_at"] = schema.StringAttribute{Computed: true, Description: "When the zone was created, in RFC 3339 UTC, to the second."}
	resp.Schema = schema.Schema{
		Description: "Looks up one existing Pantech DNS zone by id or name (such as \"example.com\"), to add records to it without managing the zone.",
		Attributes:  attrs,
	}
}

// Configure receives the API client from the provider.
func (d *DNSZoneDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if api, ok := configureAPI[dnsZoneAPI](req, resp); ok {
		d.api = api
	}
}

// ValidateConfig requires exactly one of id and name.
func (d *DNSZoneDataSource) ValidateConfig(ctx context.Context, req datasource.ValidateConfigRequest, resp *datasource.ValidateConfigResponse) {
	validateSelector(ctx, req, "DNS zone", &resp.Diagnostics)
}

// Read lists the zones (narrowed by name when looking one up by name) and
// picks the one asked for.
func (d *DNSZoneDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg dnsZoneModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, name := selector(cfg.ID, cfg.Name)
	zones, err := d.api.ListDNSZones(ctx, name)
	if err != nil {
		resourcekit.AddHintedError(&resp.Diagnostics, "Error reading DNS zones", err, map[string]string{
			client.CodeDNSUnavailable: "Pantech DNS is not available on this platform yet.",
		})
		return
	}
	z, err := lookup.Pick(zones, "DNS zone", id, name,
		func(z client.DNSZone) string { return z.ID }, func(z client.DNSZone) string { return z.Name })
	if err != nil {
		resp.Diagnostics.AddError("DNS zone not found", err.Error())
		return
	}
	servers := z.NameServers
	if servers == nil {
		servers = []string{}
	}
	list, diags := types.ListValueFrom(ctx, types.StringType, servers)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.Set(ctx, dnsZoneModel{
		ID: types.StringValue(z.ID), Name: types.StringValue(z.Name), Description: types.StringValue(z.Description),
		Status: types.StringValue(z.Status), NameServers: list, DomainID: resourcekit.OptionalString(z.DomainID),
		RecordCount: types.Int64Value(z.RecordCount), CreatedAt: resourcekit.Timestamp(z.CreatedAt),
	})...)
}
