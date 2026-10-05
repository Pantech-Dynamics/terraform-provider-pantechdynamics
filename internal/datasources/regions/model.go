package regions

import (
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// model maps the data source's attributes to Go.
type model struct {
	ID      types.String  `tfsdk:"id"`
	Regions []regionModel `tfsdk:"regions"`
}

type regionModel struct {
	Code       types.String     `tfsdk:"code"`
	Name       types.String     `tfsdk:"name"`
	Placements []placementModel `tfsdk:"placements"`
}

type placementModel struct {
	Kind               types.String `tfsdk:"kind"`
	Zone               types.String `tfsdk:"zone"`
	Available          types.Bool   `tfsdk:"available"`
	UnavailableReason  types.String `tfsdk:"unavailable_reason"`
	PrivateNetworkCIDR types.String `tfsdk:"private_network_cidr"`
}

// fromAPIResponse builds the state from the API's regions.
func fromAPIResponse(regions []client.Region) model {
	m := model{ID: types.StringValue("regions"), Regions: make([]regionModel, 0, len(regions))}
	for _, r := range regions {
		placements := make([]placementModel, 0, len(r.Placements))
		for _, p := range r.Placements {
			placements = append(placements, placementModel{
				Kind:               types.StringValue(p.Kind),
				Zone:               types.StringValue(p.Zone),
				Available:          types.BoolValue(p.Available),
				UnavailableReason:  types.StringPointerValue(p.UnavailableReason),
				PrivateNetworkCIDR: types.StringPointerValue(p.PrivateNetworkCIDR),
			})
		}
		m.Regions = append(m.Regions, regionModel{
			Code:       types.StringValue(r.Code),
			Name:       types.StringValue(r.Name),
			Placements: placements,
		})
	}
	return m
}
