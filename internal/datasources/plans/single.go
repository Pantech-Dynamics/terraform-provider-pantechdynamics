package plans

import (
	"context"
	"fmt"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/datasources/lookup"
)

var pathSlug = path.Root("slug")

var (
	_ datasource.DataSource              = &SingleDataSource{}
	_ datasource.DataSourceWithConfigure = &SingleDataSource{}
)

// SingleDataSource looks up one compute plan by its slug.
type SingleDataSource struct {
	api planAPI
}

// NewSingle is the factory the provider registers.
func NewSingle() datasource.DataSource {
	return &SingleDataSource{}
}

// singleModel maps the data source's attributes to Go. The fields after Placement
// are the same as planModel's.
type singleModel struct {
	Slug                 types.String `tfsdk:"slug"`
	Placement            types.String `tfsdk:"placement"`
	ID                   types.String `tfsdk:"id"`
	Name                 types.String `tfsdk:"name"`
	VCPU                 types.Int64  `tfsdk:"vcpu"`
	MemoryMB             types.Int64  `tfsdk:"memory_mb"`
	DiskGB               types.Int64  `tfsdk:"disk_gb"`
	PriceCurrency        types.String `tfsdk:"price_currency"`
	MonthlyEstimateMinor types.Int64  `tfsdk:"monthly_estimate_minor"`
	StorageFloorMinor    types.Int64  `tfsdk:"storage_floor_minor"`
	InitialPaymentMinor  types.Int64  `tfsdk:"initial_payment_minor"`
	UnpricedReason       types.String `tfsdk:"unpriced_reason"`
}

// Metadata sets the type name: pantechdynamics_plan.
func (d *SingleDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_plan"
}

// Schema describes the attributes: slug and placement go in, the rest comes out.
func (d *SingleDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := planAttributes()
	attrs["slug"] = schema.StringAttribute{
		Description: "Slug of the plan to look up, for example \"individual\". It must match exactly. The lookup fails if no plan has it.",
		Required:    true,
	}
	attrs["placement"] = schema.StringAttribute{
		Description: "Which kind of instance to look the plan up for: \"standard\" (a VPS with a public IP, the default) or \"vpc\" (a VM in a customer VPC). Plans and prices differ between the two.",
		Optional:    true,
		Validators:  []validator.String{oneOf{allowed: []string{client.PlacementStandard, client.PlacementVPC}}},
	}
	attrs["id"] = schema.StringAttribute{Computed: true, Description: "Identifier of the plan."}

	resp.Schema = schema.Schema{
		Description: "Looks up one compute plan by slug. Use it to fail early on a wrong slug, and to read a plan's size and price. Use pantechdynamics_plans to list them all.",
		Attributes:  attrs,
	}
}

// Configure receives the API client from the provider.
func (d *SingleDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	api, ok := req.ProviderData.(planAPI)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("Expected an API client, got %T. This is a bug in the provider.", req.ProviderData))
		return
	}
	d.api = api
}

// Read fetches the plans for the placement and picks the one with the slug.
func (d *SingleDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config singleModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	plans, err := d.api.ListPlans(ctx, config.Placement.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading plans", err.Error())
		return
	}

	slug := config.Slug.ValueString()
	i := slices.IndexFunc(plans, func(p client.Plan) bool { return p.Slug == slug })
	if i < 0 {
		resp.Diagnostics.AddAttributeError(pathSlug, "Plan not found", notFound(slug, config.Placement.ValueString(), plans))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, toSingleModel(config.Placement, plans[i]))...)
}

// toSingleModel maps one plan, echoing placement as configured.
func toSingleModel(placement types.String, p client.Plan) singleModel {
	m := toPlanModel(p)
	return singleModel{
		Slug: m.Slug, Placement: placement, ID: m.ID, Name: m.Name, VCPU: m.VCPU, MemoryMB: m.MemoryMB, DiskGB: m.DiskGB,
		PriceCurrency: m.PriceCurrency, MonthlyEstimateMinor: m.MonthlyEstimateMinor, StorageFloorMinor: m.StorageFloorMinor,
		InitialPaymentMinor: m.InitialPaymentMinor, UnpricedReason: m.UnpricedReason,
	}
}

// notFound names the slug and the placement that was searched.
func notFound(slug, placement string, plans []client.Plan) string {
	if placement == "" {
		placement = client.PlacementStandard
	}
	slugs := make([]string, 0, len(plans))
	for _, p := range plans {
		slugs = append(slugs, p.Slug)
	}
	return lookup.NotFound("plan", "slug", slug, "for the "+placement+" placement", slugs)
}
