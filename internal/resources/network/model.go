package network

import (
	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// model maps the resource's attributes to Go.
type model struct {
	ID            types.String   `tfsdk:"id"`
	Name          types.String   `tfsdk:"name"`
	CIDR          types.String   `tfsdk:"cidr"`
	Region        types.String   `tfsdk:"region"`
	Zone          types.String   `tfsdk:"zone"`
	ObservedState types.String   `tfsdk:"observed_state"`
	CreatedAt     types.String   `tfsdk:"created_at"`
	UpdatedAt     types.String   `tfsdk:"updated_at"`
	Timeouts      timeouts.Value `tfsdk:"timeouts"`
}

// toCreateRequest builds the create body. An unset region is omitted so the
// backend picks the account's default.
func toCreateRequest(plan model) client.CreateNetworkRequest {
	req := client.CreateNetworkRequest{Name: plan.Name.ValueString(), CIDR: plan.CIDR.ValueString()}
	if !plan.Region.IsNull() && !plan.Region.IsUnknown() {
		req.Region = plan.Region.ValueString()
	}
	return req
}

// fromAPIResponse builds the state from the API object. prev supplies the
// timeouts block, which only exists in Terraform.
func fromAPIResponse(prev model, n *client.Network) model {
	return model{
		ID:            types.StringValue(n.ID),
		Name:          types.StringValue(n.Name),
		CIDR:          types.StringValue(n.CIDR),
		Region:        types.StringValue(n.Region),
		Zone:          types.StringValue(n.Zone),
		ObservedState: types.StringValue(n.ObservedState),
		CreatedAt:     resourcekit.Timestamp(n.CreatedAt),
		UpdatedAt:     resourcekit.Timestamp(n.UpdatedAt),
		Timeouts:      prev.Timeouts,
	}
}

// pendingModel is the state saved the moment the backend accepts a create. It
// holds the id, so a timeout cannot orphan the network, and no unknown values,
// which state may not contain.
func pendingModel(plan model, id string) model {
	region := plan.Region
	if region.IsUnknown() {
		region = types.StringNull()
	}
	return model{
		ID:            types.StringValue(id),
		Name:          plan.Name,
		CIDR:          plan.CIDR,
		Region:        region,
		Zone:          types.StringNull(),
		ObservedState: types.StringNull(),
		CreatedAt:     types.StringNull(),
		UpdatedAt:     types.StringNull(),
		Timeouts:      plan.Timeouts,
	}
}
