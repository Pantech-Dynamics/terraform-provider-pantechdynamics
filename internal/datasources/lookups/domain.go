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

// domainAPI is the part of the API client the lookup needs.
type domainAPI interface {
	ListDomains(ctx context.Context, q string) ([]client.Domain, error)
}

var (
	_ datasource.DataSourceWithConfigure      = &DomainDataSource{}
	_ datasource.DataSourceWithValidateConfig = &DomainDataSource{}
)

// DomainDataSource looks up one domain of the organization by id or name.
type DomainDataSource struct {
	api domainAPI
}

// NewDomain is the factory the provider registers.
func NewDomain() datasource.DataSource { return &DomainDataSource{} }

type domainModel struct {
	ID              types.String `tfsdk:"id"`
	Name            types.String `tfsdk:"name"`
	TLD             types.String `tfsdk:"tld"`
	Status          types.String `tfsdk:"status"`
	AutoRenew       types.Bool   `tfsdk:"auto_renew"`
	TransferLock    types.Bool   `tfsdk:"transfer_lock"`
	Privacy         types.Bool   `tfsdk:"privacy"`
	NameServerMode  types.String `tfsdk:"name_server_mode"`
	NameServers     types.List   `tfsdk:"name_servers"`
	ZoneID          types.String `tfsdk:"zone_id"`
	RegisteredAt    types.String `tfsdk:"registered_at"`
	ExpiresAt       types.String `tfsdk:"expires_at"`
	ContactVerified types.Bool   `tfsdk:"contact_verified"`
}

// Metadata sets the type name: pantechdynamics_domain.
func (d *DomainDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_domain"
}

// Schema describes the attributes. The registrant contact is personal data
// and is not exposed here.
func (d *DomainDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := selectorAttributes("domain", "dom_", true)
	attrs["tld"] = schema.StringAttribute{Computed: true, Description: "The domain's top-level domain, such as \"com\"."}
	attrs["status"] = schema.StringAttribute{Computed: true, Description: "State of the domain, for example \"active\" or \"expired\"."}
	attrs["auto_renew"] = schema.BoolAttribute{Computed: true, Description: "Whether the domain renews from credit before it expires."}
	attrs["transfer_lock"] = schema.BoolAttribute{Computed: true, Description: "Whether transfers to another registrar are refused."}
	attrs["privacy"] = schema.BoolAttribute{Computed: true, Description: "Whether the registrant is hidden from public lookups."}
	attrs["name_server_mode"] = schema.StringAttribute{Computed: true, Description: "\"pantech\" (Pantech DNS) or \"custom\"."}
	attrs["name_servers"] = schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "The name servers the domain is delegated to."}
	attrs["zone_id"] = schema.StringAttribute{Computed: true, Description: "Id of the Pantech DNS zone the domain is delegated to, or null."}
	attrs["registered_at"] = schema.StringAttribute{Computed: true, Description: "When the domain was registered, in RFC 3339 UTC, to the second."}
	attrs["expires_at"] = schema.StringAttribute{Computed: true, Description: "When the registration expires, in RFC 3339 UTC, to the second."}
	attrs["contact_verified"] = schema.BoolAttribute{Computed: true, Description: "Whether the registrant verified their email with the registry."}
	resp.Schema = schema.Schema{
		Description: "Looks up one domain registered to your organization, by id or name (such as \"example.com\"), for example to point records at it or to read when it expires. The registrant contact is not exposed.",
		Attributes:  attrs,
	}
}

// Configure receives the API client from the provider.
func (d *DomainDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if api, ok := configureAPI[domainAPI](req, resp); ok {
		d.api = api
	}
}

// ValidateConfig requires exactly one of id and name.
func (d *DomainDataSource) ValidateConfig(ctx context.Context, req datasource.ValidateConfigRequest, resp *datasource.ValidateConfigResponse) {
	validateSelector(ctx, req, "domain", &resp.Diagnostics)
}

// Read lists the domains (narrowed by name when looking one up by name) and
// picks the one asked for.
func (d *DomainDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg domainModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, name := selector(cfg.ID, cfg.Name)
	domains, err := d.api.ListDomains(ctx, name)
	if err != nil {
		resourcekit.AddHintedError(&resp.Diagnostics, "Error reading domains", err, map[string]string{
			client.CodeDomainsUnavailable: "Domains are not sold on this platform yet.",
		})
		return
	}
	dom, err := lookup.Pick(domains, "domain", id, name,
		func(d client.Domain) string { return d.ID }, func(d client.Domain) string { return d.Name })
	if err != nil {
		resp.Diagnostics.AddError("Domain not found", err.Error())
		return
	}
	servers := dom.NameServers
	if servers == nil {
		servers = []string{}
	}
	list, diags := types.ListValueFrom(ctx, types.StringType, servers)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.Set(ctx, domainModel{
		ID: types.StringValue(dom.ID), Name: types.StringValue(dom.Name), TLD: types.StringValue(dom.TLD), Status: types.StringValue(dom.Status),
		AutoRenew: types.BoolValue(dom.AutoRenew), TransferLock: types.BoolValue(dom.TransferLock), Privacy: types.BoolValue(dom.Privacy),
		NameServerMode: types.StringValue(dom.NameServerMode), NameServers: list, ZoneID: resourcekit.OptionalString(dom.ZoneID),
		RegisteredAt: resourcekit.Timestamp(dom.RegisteredAt), ExpiresAt: resourcekit.Timestamp(dom.ExpiresAt), ContactVerified: types.BoolValue(dom.ContactVerified),
	})...)
}
