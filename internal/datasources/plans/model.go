package plans

import (
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// model maps the data source's attributes to Go.
type model struct {
	ID        types.String `tfsdk:"id"`
	Placement types.String `tfsdk:"placement"`
	Plans     []planModel  `tfsdk:"plans"`
}

type planModel struct {
	ID                   types.String `tfsdk:"id"`
	Slug                 types.String `tfsdk:"slug"`
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

// fromAPIResponse builds the state. placement is echoed back as configured.
func fromAPIResponse(placement types.String, plans []client.Plan) model {
	m := model{
		ID:        types.StringValue("plans"),
		Placement: placement,
		Plans:     make([]planModel, 0, len(plans)),
	}
	for _, p := range plans {
		m.Plans = append(m.Plans, toPlanModel(p))
	}
	return m
}

// toPlanModel maps one plan. The price fields are null when the backend sends
// no price, so an unpriced plan is visible as such instead of showing zeros.
func toPlanModel(p client.Plan) planModel {
	m := planModel{
		ID:                   types.StringValue(p.ID),
		Slug:                 types.StringValue(p.Slug),
		Name:                 types.StringValue(p.Name),
		VCPU:                 types.Int64Value(p.VCPU),
		MemoryMB:             types.Int64Value(p.MemoryMB),
		DiskGB:               types.Int64Value(p.DiskGB),
		PriceCurrency:        types.StringNull(),
		MonthlyEstimateMinor: types.Int64Null(),
		StorageFloorMinor:    types.Int64Null(),
		InitialPaymentMinor:  types.Int64Null(),
		UnpricedReason:       types.StringPointerValue(p.UnpricedReason),
	}
	if p.Price != nil {
		m.PriceCurrency = types.StringValue(p.Price.Currency)
		m.MonthlyEstimateMinor = types.Int64Value(p.Price.MonthlyEstimateMinor)
		m.StorageFloorMinor = types.Int64Value(p.Price.StorageFloorMinor)
		m.InitialPaymentMinor = types.Int64Value(p.Price.InitialPaymentMinor)
	}
	return m
}
