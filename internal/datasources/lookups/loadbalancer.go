package lookups

import (
	"context"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/datasources/lookup"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// loadBalancerAPI is the part of the API client the lookup needs.
type loadBalancerAPI interface {
	ListLoadBalancers(ctx context.Context) ([]client.LoadBalancer, error)
}

var (
	_ datasource.DataSourceWithConfigure      = &LoadBalancerDataSource{}
	_ datasource.DataSourceWithValidateConfig = &LoadBalancerDataSource{}
)

// LoadBalancerDataSource looks up one load balancer by id or name.
type LoadBalancerDataSource struct {
	api loadBalancerAPI
}

// NewLoadBalancer is the factory the provider registers.
func NewLoadBalancer() datasource.DataSource { return &LoadBalancerDataSource{} }

type loadBalancerModel struct {
	ID              types.String `tfsdk:"id"`
	Name            types.String `tfsdk:"name"`
	PublicIPID      types.String `tfsdk:"public_ip_id"`
	PublicIPAddress types.String `tfsdk:"public_ip_address"`
	NetworkID       types.String `tfsdk:"network_id"`
	SubnetID        types.String `tfsdk:"subnet_id"`
	Protocol        types.String `tfsdk:"protocol"`
	Algorithm       types.String `tfsdk:"algorithm"`
	PublicPort      types.Int64  `tfsdk:"public_port"`
	PrivatePort     types.Int64  `tfsdk:"private_port"`
	CIDRList        types.Set    `tfsdk:"cidr_list"`
	InstanceIDs     types.Set    `tfsdk:"instance_ids"`
	ObservedState   types.String `tfsdk:"observed_state"`
	CreatedAt       types.String `tfsdk:"created_at"`
}

// Metadata sets the type name: pantechdynamics_load_balancer.
func (d *LoadBalancerDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_load_balancer"
}

// Schema describes the attributes.
func (d *LoadBalancerDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := selectorAttributes("load balancer", "lb_", false)
	attrs["public_ip_id"] = schema.StringAttribute{Computed: true, Description: "Id of the public IP clients connect to."}
	attrs["public_ip_address"] = schema.StringAttribute{Computed: true, Description: "The public IPv4 address clients connect to."}
	attrs["network_id"] = schema.StringAttribute{Computed: true, Description: "Id of the VPC network the load balancer is in."}
	attrs["subnet_id"] = schema.StringAttribute{Computed: true, Description: "Id of the subnet the targets are in."}
	attrs["protocol"] = schema.StringAttribute{Computed: true, Description: "Protocol balanced. Always \"tcp\"."}
	attrs["algorithm"] = schema.StringAttribute{Computed: true, Description: "How connections are spread: \"roundrobin\", \"leastconn\" or \"source\"."}
	attrs["public_port"] = schema.Int64Attribute{Computed: true, Description: "TCP port clients connect to."}
	attrs["private_port"] = schema.Int64Attribute{Computed: true, Description: "Port on each target."}
	attrs["cidr_list"] = schema.SetAttribute{Computed: true, ElementType: types.StringType, Description: "Source CIDRs allowed to connect. Empty allows any source."}
	attrs["instance_ids"] = schema.SetAttribute{Computed: true, ElementType: types.StringType, Description: "Ids of the target instances, leaving out any being removed."}
	attrs["observed_state"] = schema.StringAttribute{Computed: true, Description: "State of the load balancer as the platform sees it, for example \"active\"."}
	attrs["created_at"] = schema.StringAttribute{Computed: true, Description: "When the load balancer was created, in RFC 3339 UTC, to the second."}
	resp.Schema = schema.Schema{
		Description: "Looks up one existing load balancer by id or name, to read its address, port or targets without managing it. Failed and deleted load balancers are not found.",
		Attributes:  attrs,
	}
}

// Configure receives the API client from the provider.
func (d *LoadBalancerDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if api, ok := configureAPI[loadBalancerAPI](req, resp); ok {
		d.api = api
	}
}

// ValidateConfig requires exactly one of id and name.
func (d *LoadBalancerDataSource) ValidateConfig(ctx context.Context, req datasource.ValidateConfigRequest, resp *datasource.ValidateConfigResponse) {
	validateSelector(ctx, req, "load balancer", &resp.Diagnostics)
}

// Read lists the load balancers and picks the one asked for.
func (d *LoadBalancerDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg loadBalancerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	lbs, err := d.api.ListLoadBalancers(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Error reading load balancers", err.Error())
		return
	}
	id, name := selector(cfg.ID, cfg.Name)
	lb, err := lookup.Pick(lbs, "load balancer", id, name,
		func(l client.LoadBalancer) string { return l.ID }, func(l client.LoadBalancer) string { return l.Name })
	if err != nil {
		resp.Diagnostics.AddError("Load balancer not found", err.Error())
		return
	}
	cidrs, diags := types.SetValueFrom(ctx, types.StringType, orEmpty(lb.CIDRList))
	resp.Diagnostics.Append(diags...)
	targets, diags := types.SetValueFrom(ctx, types.StringType, loadBalancerTargets(lb))
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.Set(ctx, loadBalancerModel{
		ID: types.StringValue(lb.ID), Name: types.StringValue(lb.Name),
		PublicIPID: types.StringValue(lb.PublicIPID), PublicIPAddress: resourcekit.OptionalString(lb.PublicIPAddress),
		NetworkID: resourcekit.OptionalString(lb.NetworkID), SubnetID: types.StringValue(lb.SubnetID),
		Protocol: types.StringValue(lb.Protocol), Algorithm: types.StringValue(lb.Algorithm),
		PublicPort: types.Int64Value(lb.PublicPort), PrivatePort: types.Int64Value(lb.PrivatePort),
		CIDRList: cidrs, InstanceIDs: targets,
		ObservedState: types.StringValue(lb.ObservedState), CreatedAt: resourcekit.Timestamp(lb.CreatedAt),
	})...)
}

// loadBalancerTargets lists the targets that are not being removed, sorted.
func loadBalancerTargets(lb client.LoadBalancer) []string {
	ids := []string{}
	for _, m := range lb.Members {
		if m.DesiredState != "deleted" {
			ids = append(ids, m.InstanceID)
		}
	}
	slices.Sort(ids)
	return ids
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
