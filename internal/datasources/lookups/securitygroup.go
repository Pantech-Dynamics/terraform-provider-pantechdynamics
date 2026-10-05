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

// securityGroupAPI is the part of the API client the lookup needs.
type securityGroupAPI interface {
	ListSecurityGroups(ctx context.Context) ([]client.SecurityGroup, error)
}

var (
	_ datasource.DataSourceWithConfigure      = &SecurityGroupDataSource{}
	_ datasource.DataSourceWithValidateConfig = &SecurityGroupDataSource{}
)

// SecurityGroupDataSource looks up one security group by id or name.
type SecurityGroupDataSource struct {
	api securityGroupAPI
}

// NewSecurityGroup is the factory the provider registers.
func NewSecurityGroup() datasource.DataSource { return &SecurityGroupDataSource{} }

type securityGroupModel struct {
	ID            types.String `tfsdk:"id"`
	Name          types.String `tfsdk:"name"`
	Rules         []ruleModel  `tfsdk:"rules"`
	ObservedState types.String `tfsdk:"observed_state"`
	CreatedAt     types.String `tfsdk:"created_at"`
}

type ruleModel struct {
	Direction types.String `tfsdk:"direction"`
	Protocol  types.String `tfsdk:"protocol"`
	PortRange types.String `tfsdk:"port_range"`
	CIDR      types.String `tfsdk:"cidr"`
}

// Metadata sets the type name: pantechdynamics_security_group.
func (d *SecurityGroupDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_security_group"
}

// Schema describes the attributes.
func (d *SecurityGroupDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := selectorAttributes("security group", "sg_", true)
	attrs["rules"] = schema.ListNestedAttribute{
		Description: "The group's rules.",
		Computed:    true,
		NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"direction":  schema.StringAttribute{Computed: true, Description: "\"ingress\" or \"egress\"."},
			"protocol":   schema.StringAttribute{Computed: true, Description: "\"tcp\", \"udp\", \"icmp\" or \"all\"."},
			"port_range": schema.StringAttribute{Computed: true, Description: "A port (\"22\"), a range (\"8000-8080\"), or empty for every port and for icmp."},
			"cidr":       schema.StringAttribute{Computed: true, Description: "IPv4 range the rule applies to."},
		}},
	}
	attrs["observed_state"] = schema.StringAttribute{Computed: true, Description: "State of the group as the platform sees it, for example \"active\"."}
	attrs["created_at"] = schema.StringAttribute{Computed: true, Description: "When the group was created, in RFC 3339 UTC, to the second."}
	resp.Schema = schema.Schema{
		Description: "Looks up one existing security group by id or name, for example the account's \"default\" group, to use it without managing it.",
		Attributes:  attrs,
	}
}

// Configure receives the API client from the provider.
func (d *SecurityGroupDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if api, ok := configureAPI[securityGroupAPI](req, resp); ok {
		d.api = api
	}
}

// ValidateConfig requires exactly one of id and name.
func (d *SecurityGroupDataSource) ValidateConfig(ctx context.Context, req datasource.ValidateConfigRequest, resp *datasource.ValidateConfigResponse) {
	validateSelector(ctx, req, "security group", &resp.Diagnostics)
}

// Read lists the groups and picks the one asked for.
func (d *SecurityGroupDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg securityGroupModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	groups, err := d.api.ListSecurityGroups(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Error reading security groups", err.Error())
		return
	}
	id, name := selector(cfg.ID, cfg.Name)
	sg, err := lookup.Pick(groups, "security group", id, name,
		func(g client.SecurityGroup) string { return g.ID }, func(g client.SecurityGroup) string { return g.Name })
	if err != nil {
		resp.Diagnostics.AddError("Security group not found", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, toSecurityGroupModel(sg))...)
}

func toSecurityGroupModel(sg client.SecurityGroup) securityGroupModel {
	m := securityGroupModel{
		ID: types.StringValue(sg.ID), Name: types.StringValue(sg.Name), Rules: []ruleModel{},
		ObservedState: types.StringValue(sg.ObservedState), CreatedAt: resourcekit.Timestamp(sg.CreatedAt),
	}
	for _, r := range sg.Rules {
		m.Rules = append(m.Rules, ruleModel{
			Direction: types.StringValue(r.Direction), Protocol: types.StringValue(r.Protocol),
			PortRange: types.StringValue(r.PortRange), CIDR: types.StringValue(r.CIDR),
		})
	}
	return m
}
