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

// networkAPI is the part of the API client the lookup needs.
type networkAPI interface {
	ListNetworks(ctx context.Context) ([]client.Network, error)
}

var (
	_ datasource.DataSourceWithConfigure      = &NetworkDataSource{}
	_ datasource.DataSourceWithValidateConfig = &NetworkDataSource{}
)

// NetworkDataSource looks up one VPC network by id or name.
type NetworkDataSource struct {
	api networkAPI
}

// NewNetwork is the factory the provider registers.
func NewNetwork() datasource.DataSource { return &NetworkDataSource{} }

type networkModel struct {
	ID            types.String `tfsdk:"id"`
	Name          types.String `tfsdk:"name"`
	CIDR          types.String `tfsdk:"cidr"`
	Region        types.String `tfsdk:"region"`
	Zone          types.String `tfsdk:"zone"`
	ObservedState types.String `tfsdk:"observed_state"`
	CreatedAt     types.String `tfsdk:"created_at"`
}

// Metadata sets the type name: pantechdynamics_network.
func (d *NetworkDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_network"
}

// Schema describes the attributes.
func (d *NetworkDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := selectorAttributes("network", "net_", false)
	attrs["cidr"] = schema.StringAttribute{Computed: true, Description: "IPv4 address range of the network."}
	attrs["region"] = schema.StringAttribute{Computed: true, Description: "Region the network is in."}
	attrs["zone"] = schema.StringAttribute{Computed: true, Description: "Zone the network is in."}
	attrs["observed_state"] = schema.StringAttribute{Computed: true, Description: "State of the network as the platform sees it, for example \"active\"."}
	attrs["created_at"] = schema.StringAttribute{Computed: true, Description: "When the network was created, in RFC 3339 UTC, to the second."}
	resp.Schema = schema.Schema{
		Description: "Looks up one existing VPC network by id or name, to place subnets, public IPs or databases in it without managing it. Failed and deleted networks are not found.",
		Attributes:  attrs,
	}
}

// Configure receives the API client from the provider.
func (d *NetworkDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if api, ok := configureAPI[networkAPI](req, resp); ok {
		d.api = api
	}
}

// ValidateConfig requires exactly one of id and name.
func (d *NetworkDataSource) ValidateConfig(ctx context.Context, req datasource.ValidateConfigRequest, resp *datasource.ValidateConfigResponse) {
	validateSelector(ctx, req, "network", &resp.Diagnostics)
}

// Read lists the networks and picks the one asked for.
func (d *NetworkDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg networkModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	networks, err := d.api.ListNetworks(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Error reading networks", err.Error())
		return
	}
	id, name := selector(cfg.ID, cfg.Name)
	n, err := lookup.Pick(networks, "network", id, name,
		func(n client.Network) string { return n.ID }, func(n client.Network) string { return n.Name })
	if err != nil {
		resp.Diagnostics.AddError("Network not found", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, networkModel{
		ID: types.StringValue(n.ID), Name: types.StringValue(n.Name), CIDR: types.StringValue(n.CIDR),
		Region: types.StringValue(n.Region), Zone: types.StringValue(n.Zone),
		ObservedState: types.StringValue(n.ObservedState), CreatedAt: resourcekit.Timestamp(n.CreatedAt),
	})...)
}
