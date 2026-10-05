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

// instanceAPI is the part of the API client the lookup needs.
type instanceAPI interface {
	ListInstances(ctx context.Context) ([]client.Instance, error)
}

var (
	_ datasource.DataSourceWithConfigure      = &InstanceDataSource{}
	_ datasource.DataSourceWithValidateConfig = &InstanceDataSource{}
)

// InstanceDataSource looks up one instance by id or name.
type InstanceDataSource struct {
	api instanceAPI
}

// NewInstance is the factory the provider registers.
func NewInstance() datasource.DataSource { return &InstanceDataSource{} }

type instanceModel struct {
	ID              types.String `tfsdk:"id"`
	Name            types.String `tfsdk:"name"`
	PlanSlug        types.String `tfsdk:"plan_slug"`
	ImageSlug       types.String `tfsdk:"image_slug"`
	Region          types.String `tfsdk:"region"`
	Zone            types.String `tfsdk:"zone"`
	NetworkID       types.String `tfsdk:"network_id"`
	SubnetID        types.String `tfsdk:"subnet_id"`
	SecurityGroupID types.String `tfsdk:"security_group_id"`
	PublicIPv4      types.String `tfsdk:"public_ipv4"`
	PrivateIPv4     types.String `tfsdk:"private_ipv4"`
	DesiredState    types.String `tfsdk:"desired_state"`
	ObservedState   types.String `tfsdk:"observed_state"`
	Tags            types.Map    `tfsdk:"tags"`
	CreatedAt       types.String `tfsdk:"created_at"`
}

// Metadata sets the type name: pantechdynamics_instance.
func (d *InstanceDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_instance"
}

// Schema describes the attributes.
func (d *InstanceDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := selectorAttributes("instance", "vm_", false)
	computed := map[string]string{
		"plan_slug":         "Plan the instance runs on.",
		"image_slug":        "Image the instance was created from.",
		"region":            "Region the instance is in.",
		"zone":              "Zone the instance is in.",
		"network_id":        "VPC network of the instance, or null for a standard instance.",
		"subnet_id":         "VPC subnet of the instance, or null for a standard instance.",
		"security_group_id": "Security group of a standard instance.",
		"public_ipv4":       "Public IPv4 address, or null when the instance has none.",
		"private_ipv4":      "IPv4 address of the instance on its network.",
		"desired_state":     "\"running\" or \"stopped\", as last requested.",
		"observed_state":    "State of the instance as the platform sees it, for example \"running\".",
		"created_at":        "When the instance was created, in RFC 3339 UTC, to the second.",
	}
	for name, desc := range computed {
		attrs[name] = schema.StringAttribute{Computed: true, Description: desc}
	}
	attrs["tags"] = schema.MapAttribute{Computed: true, ElementType: types.StringType, Description: "Key and value labels of the instance."}
	resp.Schema = schema.Schema{
		Description: "Looks up one existing instance by id or name, for example to point a public IP, volume or database access rule at a machine Terraform does not manage. Deleted instances are not found.",
		Attributes:  attrs,
	}
}

// Configure receives the API client from the provider.
func (d *InstanceDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if api, ok := configureAPI[instanceAPI](req, resp); ok {
		d.api = api
	}
}

// ValidateConfig requires exactly one of id and name.
func (d *InstanceDataSource) ValidateConfig(ctx context.Context, req datasource.ValidateConfigRequest, resp *datasource.ValidateConfigResponse) {
	validateSelector(ctx, req, "instance", &resp.Diagnostics)
}

// Read lists the live instances and picks the one asked for.
func (d *InstanceDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg instanceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	all, err := d.api.ListInstances(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Error reading instances", err.Error())
		return
	}
	live := make([]client.Instance, 0, len(all))
	for _, inst := range all {
		if inst.ObservedState != client.InstanceDeleted {
			live = append(live, inst)
		}
	}
	id, name := selector(cfg.ID, cfg.Name)
	inst, err := lookup.Pick(live, "instance", id, name,
		func(i client.Instance) string { return i.ID }, func(i client.Instance) string { return i.Name })
	if err != nil {
		resp.Diagnostics.AddError("Instance not found", err.Error())
		return
	}
	tags, diags := types.MapValueFrom(ctx, types.StringType, inst.Tags)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.Set(ctx, instanceModel{
		ID: types.StringValue(inst.ID), Name: types.StringValue(inst.Name), PlanSlug: types.StringValue(inst.PlanSlug),
		ImageSlug: types.StringValue(inst.ImageSlug), Region: types.StringValue(inst.Region), Zone: types.StringValue(inst.Zone),
		NetworkID: resourcekit.OptionalString(inst.NetworkID), SubnetID: resourcekit.OptionalString(inst.SubnetID),
		SecurityGroupID: types.StringValue(inst.SecurityGroupID), PublicIPv4: resourcekit.OptionalString(inst.PublicIPv4),
		PrivateIPv4: resourcekit.OptionalString(inst.PrivateIPv4), DesiredState: types.StringValue(inst.DesiredState),
		ObservedState: types.StringValue(inst.ObservedState), Tags: tags, CreatedAt: resourcekit.Timestamp(inst.CreatedAt),
	})...)
}
