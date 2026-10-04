package firewallrule

import (
	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// icmpAny is how the API's request schema spells "any ICMP type or code". The
// attributes use null for it, as the docs say to omit the field.
const icmpAny = -1

// model maps the resource's attributes to Go.
type model struct {
	ID            types.String   `tfsdk:"id"`
	SubnetID      types.String   `tfsdk:"subnet_id"`
	Number        types.Int64    `tfsdk:"number"`
	Direction     types.String   `tfsdk:"direction"`
	Protocol      types.String   `tfsdk:"protocol"`
	PortStart     types.Int64    `tfsdk:"port_start"`
	PortEnd       types.Int64    `tfsdk:"port_end"`
	ICMPType      types.Int64    `tfsdk:"icmp_type"`
	ICMPCode      types.Int64    `tfsdk:"icmp_code"`
	CIDR          types.String   `tfsdk:"cidr"`
	Action        types.String   `tfsdk:"action"`
	ObservedState types.String   `tfsdk:"observed_state"`
	CreatedAt     types.String   `tfsdk:"created_at"`
	UpdatedAt     types.String   `tfsdk:"updated_at"`
	Timeouts      timeouts.Value `tfsdk:"timeouts"`
}

// toCreateRequest builds the create body. Unset ports and ICMP fields are
// omitted, because the backend wants ports only for tcp and udp and ICMP fields
// only for icmp.
func toCreateRequest(plan model) client.CreateFirewallRuleRequest {
	return client.CreateFirewallRuleRequest{
		Number:    plan.Number.ValueInt64(),
		Direction: plan.Direction.ValueString(),
		Protocol:  plan.Protocol.ValueString(),
		PortStart: resourcekit.IntPtr(plan.PortStart),
		PortEnd:   resourcekit.IntPtr(plan.PortEnd),
		ICMPType:  resourcekit.IntPtr(plan.ICMPType),
		ICMPCode:  resourcekit.IntPtr(plan.ICMPCode),
		CIDR:      plan.CIDR.ValueString(),
		Action:    plan.Action.ValueString(),
	}
}

// fromAPIResponse builds the state from the API object. prev supplies the
// timeouts block, which only exists in Terraform.
func fromAPIResponse(prev model, r *client.FirewallRule) model {
	return model{
		ID:            types.StringValue(r.ID),
		SubnetID:      types.StringValue(r.SubnetID),
		Number:        types.Int64Value(r.Number),
		Direction:     types.StringValue(r.Direction),
		Protocol:      types.StringValue(r.Protocol),
		PortStart:     resourcekit.OptionalInt64(r.PortStart),
		PortEnd:       resourcekit.OptionalInt64(r.PortEnd),
		ICMPType:      icmpValue(r.ICMPType),
		ICMPCode:      icmpValue(r.ICMPCode),
		CIDR:          types.StringValue(r.CIDR),
		Action:        types.StringValue(r.Action),
		ObservedState: types.StringValue(r.ObservedState),
		CreatedAt:     resourcekit.Timestamp(r.CreatedAt),
		UpdatedAt:     resourcekit.Timestamp(r.UpdatedAt),
		Timeouts:      prev.Timeouts,
	}
}

// icmpValue maps the API's "any" (null or -1) to null.
func icmpValue(n *int64) types.Int64 {
	if n == nil || *n == icmpAny {
		return types.Int64Null()
	}
	return types.Int64Value(*n)
}

// pendingModel is the state saved the moment the backend accepts a create. It
// holds the id and no unknown values, which state may not contain.
func pendingModel(plan model, id string) model {
	return model{
		ID:            types.StringValue(id),
		SubnetID:      plan.SubnetID,
		Number:        plan.Number,
		Direction:     plan.Direction,
		Protocol:      plan.Protocol,
		PortStart:     plan.PortStart,
		PortEnd:       knownOrNull(plan.PortEnd),
		ICMPType:      plan.ICMPType,
		ICMPCode:      plan.ICMPCode,
		CIDR:          plan.CIDR,
		Action:        plan.Action,
		ObservedState: types.StringNull(),
		CreatedAt:     types.StringNull(),
		UpdatedAt:     types.StringNull(),
		Timeouts:      plan.Timeouts,
	}
}

func knownOrNull(v types.Int64) types.Int64 {
	if v.IsUnknown() {
		return types.Int64Null()
	}
	return v
}
