package loadbalancer

import (
	"context"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// memberRemoved is the desired state of a target that is being taken out.
const memberRemoved = "deleted"

// model maps the resource's attributes to Go.
type model struct {
	ID              types.String   `tfsdk:"id"`
	Name            types.String   `tfsdk:"name"`
	PublicIPID      types.String   `tfsdk:"public_ip_id"`
	SubnetID        types.String   `tfsdk:"subnet_id"`
	Algorithm       types.String   `tfsdk:"algorithm"`
	PublicPort      types.Int64    `tfsdk:"public_port"`
	PrivatePort     types.Int64    `tfsdk:"private_port"`
	CIDRList        types.Set      `tfsdk:"cidr_list"`
	InstanceIDs     types.Set      `tfsdk:"instance_ids"`
	PublicIPAddress types.String   `tfsdk:"public_ip_address"`
	NetworkID       types.String   `tfsdk:"network_id"`
	Protocol        types.String   `tfsdk:"protocol"`
	ObservedState   types.String   `tfsdk:"observed_state"`
	CreatedAt       types.String   `tfsdk:"created_at"`
	UpdatedAt       types.String   `tfsdk:"updated_at"`
	Timeouts        timeouts.Value `tfsdk:"timeouts"`
}

// toCreateRequest builds the create body. Empty lists are omitted, which the
// backend reads as "any source" and "no targets yet".
func toCreateRequest(ctx context.Context, plan model) (client.CreateLoadBalancerRequest, diag.Diagnostics) {
	cidrs, diags := stringsOf(ctx, plan.CIDRList)
	ids, d := stringsOf(ctx, plan.InstanceIDs)
	diags.Append(d...)
	return client.CreateLoadBalancerRequest{
		Name:        plan.Name.ValueString(),
		PublicIPID:  plan.PublicIPID.ValueString(),
		SubnetID:    plan.SubnetID.ValueString(),
		Algorithm:   plan.Algorithm.ValueString(),
		PublicPort:  plan.PublicPort.ValueInt64(),
		PrivatePort: resourcekit.IntPtr(plan.PrivatePort),
		CIDRList:    cidrs,
		InstanceIDs: ids,
	}, diags
}

// toUpdateRequest sends only what changed between state and plan. changed is
// false when nothing the API stores differs, for example when only the timeouts
// block changed.
func toUpdateRequest(ctx context.Context, state, plan model) (req client.UpdateLoadBalancerRequest, changed bool, diags diag.Diagnostics) {
	if !plan.Name.Equal(state.Name) {
		name := plan.Name.ValueString()
		req.Name, changed = &name, true
	}
	if !plan.Algorithm.Equal(state.Algorithm) {
		algorithm := plan.Algorithm.ValueString()
		req.Algorithm, changed = &algorithm, true
	}
	if !plan.InstanceIDs.Equal(state.InstanceIDs) {
		ids, d := stringsOf(ctx, plan.InstanceIDs)
		diags.Append(d...)
		if ids == nil {
			ids = []string{} // the whole new set is empty: remove every target
		}
		req.InstanceIDs, changed = &ids, true
	}
	return req, changed, diags
}

// fromAPIResponse builds the state from the API object. prev supplies the
// timeouts block, which only exists in Terraform. Targets being removed are left
// out of instance_ids, because they are on their way out.
func fromAPIResponse(ctx context.Context, prev model, lb *client.LoadBalancer) (model, diag.Diagnostics) {
	cidrs, diags := types.SetValueFrom(ctx, types.StringType, nonNil(lb.CIDRList))
	ids, d := types.SetValueFrom(ctx, types.StringType, targetIDs(lb))
	diags.Append(d...)
	return model{
		ID:              types.StringValue(lb.ID),
		Name:            types.StringValue(lb.Name),
		PublicIPID:      types.StringValue(lb.PublicIPID),
		SubnetID:        types.StringValue(lb.SubnetID),
		Algorithm:       types.StringValue(lb.Algorithm),
		PublicPort:      types.Int64Value(lb.PublicPort),
		PrivatePort:     types.Int64Value(lb.PrivatePort),
		CIDRList:        cidrs,
		InstanceIDs:     ids,
		PublicIPAddress: resourcekit.OptionalString(lb.PublicIPAddress),
		NetworkID:       resourcekit.OptionalString(lb.NetworkID),
		Protocol:        types.StringValue(lb.Protocol),
		ObservedState:   types.StringValue(lb.ObservedState),
		CreatedAt:       resourcekit.Timestamp(lb.CreatedAt),
		UpdatedAt:       resourcekit.Timestamp(lb.UpdatedAt),
		Timeouts:        prev.Timeouts,
	}, diags
}

// pendingModel is the state saved the moment the backend accepts a create. It
// holds the id and no unknown values, which state may not contain.
func pendingModel(plan model, id string) model {
	m := plan
	m.ID = types.StringValue(id)
	m.PublicIPAddress = types.StringNull()
	m.NetworkID = types.StringNull()
	m.Protocol = types.StringNull()
	m.ObservedState = types.StringNull()
	m.CreatedAt = types.StringNull()
	m.UpdatedAt = types.StringNull()
	if m.PrivatePort.IsUnknown() {
		m.PrivatePort = types.Int64Null()
	}
	return m
}

// targetIDs lists the instances that are, or are becoming, targets, sorted.
func targetIDs(lb *client.LoadBalancer) []string {
	ids := []string{}
	for _, m := range lb.Members {
		if m.DesiredState != memberRemoved {
			ids = append(ids, m.InstanceID)
		}
	}
	slices.Sort(ids)
	return ids
}

// stringsOf reads a set of strings. A null or unknown set gives nil.
func stringsOf(ctx context.Context, set types.Set) ([]string, diag.Diagnostics) {
	if set.IsNull() || set.IsUnknown() {
		return nil, nil
	}
	var out []string
	diags := set.ElementsAs(ctx, &out, false)
	if len(out) == 0 {
		return nil, diags
	}
	return out, diags
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
