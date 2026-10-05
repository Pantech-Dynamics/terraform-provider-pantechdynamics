package subnet

import (
	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// model maps the resource's attributes to Go.
type model struct {
	ID            types.String   `tfsdk:"id"`
	NetworkID     types.String   `tfsdk:"network_id"`
	Name          types.String   `tfsdk:"name"`
	CIDR          types.String   `tfsdk:"cidr"`
	Region        types.String   `tfsdk:"region"`
	Zone          types.String   `tfsdk:"zone"`
	ObservedState types.String   `tfsdk:"observed_state"`
	CreatedAt     types.String   `tfsdk:"created_at"`
	UpdatedAt     types.String   `tfsdk:"updated_at"`
	Timeouts      timeouts.Value `tfsdk:"timeouts"`
}

func toCreateRequest(plan model) client.CreateSubnetRequest {
	return client.CreateSubnetRequest{Name: plan.Name.ValueString(), CIDR: plan.CIDR.ValueString()}
}

// fromAPIResponse builds the state from the API object. prev supplies the
// timeouts block, which only exists in Terraform.
func fromAPIResponse(prev model, s *client.Subnet) model {
	return model{
		ID:            types.StringValue(s.ID),
		NetworkID:     types.StringValue(s.NetworkID),
		Name:          types.StringValue(s.Name),
		CIDR:          types.StringValue(s.CIDR),
		Region:        types.StringValue(s.Region),
		Zone:          types.StringValue(s.Zone),
		ObservedState: types.StringValue(s.ObservedState),
		CreatedAt:     resourcekit.Timestamp(s.CreatedAt),
		UpdatedAt:     resourcekit.Timestamp(s.UpdatedAt),
		Timeouts:      prev.Timeouts,
	}
}

// pendingModel is the state saved the moment the backend accepts a create. It
// holds the id and no unknown values, which state may not contain.
func pendingModel(plan model, id string) model {
	return model{
		ID:            types.StringValue(id),
		NetworkID:     plan.NetworkID,
		Name:          plan.Name,
		CIDR:          plan.CIDR,
		Region:        types.StringNull(),
		Zone:          types.StringNull(),
		ObservedState: types.StringNull(),
		CreatedAt:     types.StringNull(),
		UpdatedAt:     types.StringNull(),
		Timeouts:      plan.Timeouts,
	}
}
