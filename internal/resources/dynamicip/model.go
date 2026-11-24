package dynamicip

import (
	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// model maps the resource's attributes to Go.
type model struct {
	ID               types.String   `tfsdk:"id"`
	InstanceID       types.String   `tfsdk:"instance_id"`
	Schedule         types.String   `tfsdk:"schedule"`
	Connections      types.String   `tfsdk:"connections"`
	OnDemand         types.Bool     `tfsdk:"on_demand"`
	State            types.String   `tfsdk:"state"`
	CurrentAddress   types.String   `tfsdk:"current_address"`
	NextRotationAt   types.String   `tfsdk:"next_rotation_at"`
	LastRotatedAt    types.String   `tfsdk:"last_rotated_at"`
	SurchargePercent types.Float64  `tfsdk:"surcharge_percent"`
	OnDemandUsed     types.Int64    `tfsdk:"on_demand_used"`
	OnDemandLimit    types.Int64    `tfsdk:"on_demand_limit"`
	FailureReason    types.String   `tfsdk:"failure_reason"`
	Timeouts         timeouts.Value `tfsdk:"timeouts"`
}

// toPutRequest turns the plan into the full PUT request. Every configurable
// field is sent explicitly so nothing depends on backend defaults.
func toPutRequest(m model) client.PutDynamicIPRequest {
	onDemand := m.OnDemand.ValueBool()
	return client.PutDynamicIPRequest{
		Schedule:    m.Schedule.ValueString(),
		Connections: m.Connections.ValueString(),
		OnDemand:    &onDemand,
	}
}

// fromAPIResponse builds state from the API object, preserving the timeouts
// block from prev.
func fromAPIResponse(prev model, d *client.DynamicIP) model {
	id := d.InstanceID
	if id == "" && !prev.InstanceID.IsNull() && !prev.InstanceID.IsUnknown() {
		id = prev.InstanceID.ValueString()
	}
	return model{
		ID:               types.StringValue(id),
		InstanceID:       types.StringValue(id),
		Schedule:         types.StringValue(d.Schedule),
		Connections:      types.StringValue(d.Connections),
		OnDemand:         types.BoolValue(d.OnDemand),
		State:            types.StringValue(d.State),
		CurrentAddress:   resourcekit.OptionalString(d.CurrentAddress),
		NextRotationAt:   resourcekit.Timestamp(d.NextRotationAt),
		LastRotatedAt:    resourcekit.Timestamp(d.LastRotatedAt),
		SurchargePercent: types.Float64Value(d.SurchargePercent),
		OnDemandUsed:     types.Int64Value(d.OnDemandUsed),
		OnDemandLimit:    types.Int64Value(d.OnDemandLimit),
		FailureReason:    resourcekit.OptionalString(d.FailureReason),
		Timeouts:         prev.Timeouts,
	}
}
