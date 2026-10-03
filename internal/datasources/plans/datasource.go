// Package plans implements the pantechdynamics_plans data source.
package plans

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// planAPI is the part of the API client this data source needs, defined here by
// the consumer so tests can fake it.
type planAPI interface {
	ListPlans(ctx context.Context, placement string) ([]client.Plan, error)
}

var (
	_ datasource.DataSource              = &DataSource{}
	_ datasource.DataSourceWithConfigure = &DataSource{}
)

// DataSource lists the compute plans a customer can choose.
type DataSource struct {
	api planAPI
}

// New is the factory the provider registers.
func New() datasource.DataSource {
	return &DataSource{}
}

// Metadata sets the type name: pantechdynamics_plans.
func (d *DataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_plans"
}

// Schema describes the attributes.
func (d *DataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the compute plans available for instances. Use a plan's slug as the plan when creating an instance.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Always \"plans\". Present because every data source needs an id.",
				Computed:    true,
			},
			"placement": schema.StringAttribute{
				Description: "Which kind of instance to price the plans for: \"standard\" (a VPS with a public IP, the default) or \"vpc\" (a VM in a customer VPC). Prices differ between the two.",
				Optional:    true,
				Validators:  []validator.String{oneOf{allowed: []string{client.PlacementStandard, client.PlacementVPC}}},
			},
			"plans": schema.ListNestedAttribute{
				Description: "The available plans.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id":                     schema.StringAttribute{Computed: true, Description: "Identifier of the plan."},
						"slug":                   schema.StringAttribute{Computed: true, Description: "Stable short name of the plan, used when creating an instance."},
						"name":                   schema.StringAttribute{Computed: true, Description: "Display name of the plan."},
						"vcpu":                   schema.Int64Attribute{Computed: true, Description: "Number of virtual CPUs."},
						"memory_mb":              schema.Int64Attribute{Computed: true, Description: "Memory in megabytes."},
						"disk_gb":                schema.Int64Attribute{Computed: true, Description: "Root disk size in gigabytes."},
						"price_currency":         schema.StringAttribute{Computed: true, Description: "Currency of the prices below, in your account's currency. Null when the plan is unpriced."},
						"monthly_estimate_minor": schema.Int64Attribute{Computed: true, Description: "Estimated cost of running the plan for a full month, in minor currency units (for example kobo or cents). An estimate from a display-only cache, not a quote."},
						"storage_floor_minor":    schema.Int64Attribute{Computed: true, Description: "Estimated cost of a full month while the instance is stopped, in minor currency units."},
						"initial_payment_minor":  schema.Int64Attribute{Computed: true, Description: "Estimated amount due when the instance is ordered, in minor currency units."},
						"unpriced_reason":        schema.StringAttribute{Computed: true, Description: "Why no price is available, or null when the plan is priced."},
					},
				},
			},
		},
	}
}

// Configure receives the API client from the provider. Terraform calls it early
// with no data, so a nil ProviderData is normal and ignored.
func (d *DataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

// Read fetches the plans and stores them in state.
func (d *DataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config model
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	plans, err := d.api.ListPlans(ctx, config.Placement.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading plans", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPIResponse(config.Placement, plans))...)
}
