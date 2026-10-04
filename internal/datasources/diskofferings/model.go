package diskofferings

import (
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// model maps the data source's attributes to Go.
type model struct {
	ID            types.String    `tfsdk:"id"`
	DiskOfferings []offeringModel `tfsdk:"disk_offerings"`
}

type offeringModel struct {
	Slug             types.String `tfsdk:"slug"`
	Name             types.String `tfsdk:"name"`
	SizeGB           types.Int64  `tfsdk:"size_gb"`
	CustomSize       types.Bool   `tfsdk:"custom_size"`
	StorageType      types.String `tfsdk:"storage_type"`
	Currency         types.String `tfsdk:"currency"`
	HourlyPriceMinor types.Int64  `tfsdk:"hourly_price_minor"`
}

// fromAPIResponse builds the state. A customized offering has no size of its own,
// so size_gb is null for it, not zero.
func fromAPIResponse(offerings []client.DiskOffering) model {
	m := model{ID: types.StringValue("disk_offerings"), DiskOfferings: make([]offeringModel, 0, len(offerings))}
	for _, o := range offerings {
		m.DiskOfferings = append(m.DiskOfferings, offeringModel{
			Slug:             types.StringValue(o.Slug),
			Name:             types.StringValue(o.Name),
			SizeGB:           types.Int64PointerValue(o.SizeGB),
			CustomSize:       types.BoolValue(o.CustomSize),
			StorageType:      types.StringValue(o.StorageType),
			Currency:         types.StringValue(o.Currency),
			HourlyPriceMinor: types.Int64Value(o.HourlyPriceMinor),
		})
	}
	return m
}
