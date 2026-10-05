package portforward

import (
	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// model maps the resource's attributes to Go.
type model struct {
	ID               types.String   `tfsdk:"id"`
	PublicIPID       types.String   `tfsdk:"public_ip_id"`
	InstanceID       types.String   `tfsdk:"instance_id"`
	Protocol         types.String   `tfsdk:"protocol"`
	PublicPortStart  types.Int64    `tfsdk:"public_port_start"`
	PublicPortEnd    types.Int64    `tfsdk:"public_port_end"`
	PrivatePortStart types.Int64    `tfsdk:"private_port_start"`
	PrivatePortEnd   types.Int64    `tfsdk:"private_port_end"`
	ObservedState    types.String   `tfsdk:"observed_state"`
	CreatedAt        types.String   `tfsdk:"created_at"`
	UpdatedAt        types.String   `tfsdk:"updated_at"`
	Timeouts         timeouts.Value `tfsdk:"timeouts"`
}

// toCreateRequest builds the create body. Unset end ports and private start are
// omitted, so the backend defaults them from the public start.
func toCreateRequest(plan model) client.CreatePortForwardingRuleRequest {
	return client.CreatePortForwardingRuleRequest{
		InstanceID:       plan.InstanceID.ValueString(),
		Protocol:         plan.Protocol.ValueString(),
		PublicPortStart:  plan.PublicPortStart.ValueInt64(),
		PublicPortEnd:    resourcekit.IntPtr(plan.PublicPortEnd),
		PrivatePortStart: resourcekit.IntPtr(plan.PrivatePortStart),
		PrivatePortEnd:   resourcekit.IntPtr(plan.PrivatePortEnd),
	}
}

// fromAPIResponse builds the state from the API object. prev supplies the
// timeouts block, which only exists in Terraform.
func fromAPIResponse(prev model, r *client.PortForwardingRule) model {
	return model{
		ID:               types.StringValue(r.ID),
		PublicIPID:       types.StringValue(r.PublicIPID),
		InstanceID:       types.StringValue(r.InstanceID),
		Protocol:         types.StringValue(r.Protocol),
		PublicPortStart:  types.Int64Value(r.PublicPortStart),
		PublicPortEnd:    types.Int64Value(r.PublicPortEnd),
		PrivatePortStart: types.Int64Value(r.PrivatePortStart),
		PrivatePortEnd:   types.Int64Value(r.PrivatePortEnd),
		ObservedState:    types.StringValue(r.ObservedState),
		CreatedAt:        resourcekit.Timestamp(r.CreatedAt),
		UpdatedAt:        resourcekit.Timestamp(r.UpdatedAt),
		Timeouts:         prev.Timeouts,
	}
}

// pendingModel is the state saved the moment the backend accepts a create. It
// holds the id and no unknown values, which state may not contain.
func pendingModel(plan model, id string) model {
	return model{
		ID:               types.StringValue(id),
		PublicIPID:       plan.PublicIPID,
		InstanceID:       plan.InstanceID,
		Protocol:         plan.Protocol,
		PublicPortStart:  plan.PublicPortStart,
		PublicPortEnd:    knownOrNull(plan.PublicPortEnd),
		PrivatePortStart: knownOrNull(plan.PrivatePortStart),
		PrivatePortEnd:   knownOrNull(plan.PrivatePortEnd),
		ObservedState:    types.StringNull(),
		CreatedAt:        types.StringNull(),
		UpdatedAt:        types.StringNull(),
		Timeouts:         plan.Timeouts,
	}
}

func knownOrNull(v types.Int64) types.Int64 {
	if v.IsUnknown() {
		return types.Int64Null()
	}
	return v
}
