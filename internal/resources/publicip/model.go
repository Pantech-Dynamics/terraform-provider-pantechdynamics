package publicip

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
	NetworkName   types.String   `tfsdk:"network_name"`
	Purpose       types.String   `tfsdk:"purpose"`
	InstanceID    types.String   `tfsdk:"instance_id"`
	InstanceName  types.String   `tfsdk:"instance_name"`
	Address       types.String   `tfsdk:"address"`
	Region        types.String   `tfsdk:"region"`
	Zone          types.String   `tfsdk:"zone"`
	ObservedState types.String   `tfsdk:"observed_state"`
	InSync        types.Bool     `tfsdk:"in_sync"`
	CreatedAt     types.String   `tfsdk:"created_at"`
	UpdatedAt     types.String   `tfsdk:"updated_at"`
	Timeouts      timeouts.Value `tfsdk:"timeouts"`
}

// toCreateRequest builds the create body. instance_id is sent only when set:
// port_forwarding and load_balancer addresses must not carry one, and a
// static_nat address without one is reserved unattached.
func toCreateRequest(plan model) client.CreatePublicIPRequest {
	return client.CreatePublicIPRequest{
		NetworkID:  plan.NetworkID.ValueString(),
		Purpose:    plan.Purpose.ValueString(),
		InstanceID: plan.InstanceID.ValueString(),
	}
}

// fromAPIResponse builds the state from the API object. prev supplies the
// timeouts block, which only exists in Terraform.
func fromAPIResponse(prev model, ip *client.PublicIP) model {
	return model{
		ID:            types.StringValue(ip.ID),
		NetworkID:     types.StringValue(ip.NetworkID),
		NetworkName:   resourcekit.OptionalString(ip.NetworkName),
		Purpose:       types.StringValue(ip.Purpose),
		InstanceID:    resourcekit.OptionalString(ip.InstanceID),
		InstanceName:  resourcekit.OptionalString(ip.InstanceName),
		Address:       resourcekit.OptionalString(ip.Address),
		Region:        resourcekit.OptionalString(ip.Region),
		Zone:          resourcekit.OptionalString(ip.Zone),
		ObservedState: types.StringValue(ip.ObservedState),
		InSync:        types.BoolValue(ip.InSync),
		CreatedAt:     resourcekit.Timestamp(ip.CreatedAt),
		UpdatedAt:     resourcekit.Timestamp(ip.UpdatedAt),
		Timeouts:      prev.Timeouts,
	}
}

// pendingModel is the state saved the moment the backend accepts a create. It
// holds the id and no unknown values, which state may not contain.
func pendingModel(plan model, id string) model {
	return model{
		ID:            types.StringValue(id),
		NetworkID:     plan.NetworkID,
		NetworkName:   types.StringNull(),
		Purpose:       plan.Purpose,
		InstanceID:    plan.InstanceID,
		InstanceName:  types.StringNull(),
		Address:       types.StringNull(),
		Region:        types.StringNull(),
		Zone:          types.StringNull(),
		ObservedState: types.StringNull(),
		InSync:        types.BoolNull(),
		CreatedAt:     types.StringNull(),
		UpdatedAt:     types.StringNull(),
		Timeouts:      plan.Timeouts,
	}
}
