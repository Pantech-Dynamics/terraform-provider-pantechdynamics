package instance

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

var pathPrivateNetwork = path.Root("private_network")

// onPrivateNetwork reports whether the instance has, or is getting, the private
// database network interface. One being removed counts as off.
func onPrivateNetwork(state string) bool {
	return state == client.PrivateNetworkAttached || state == client.PrivateNetworkAttaching
}

// setPrivateNetwork attaches or detaches the private database network interface
// and waits until the instance reports it. The platform accepts either on a
// running or a stopped instance, so the power state is left alone.
func (r *Resource) setPrivateNetwork(ctx context.Context, id string, on bool) error {
	var (
		ref  *client.OperationReference
		err  error
		want = client.PrivateNetworkNone
	)
	if on {
		want = client.PrivateNetworkAttached
		ref, err = r.api.AttachInstancePrivateNetwork(ctx, id)
	} else {
		ref, err = r.api.DetachInstancePrivateNetwork(ctx, id)
	}
	if err != nil {
		return err
	}
	return r.api.WaitForOperation(ctx, ref.OperationID, r.hasPrivateNetwork(id, want))
}

// hasPrivateNetwork finishes when the interface reaches want and, once
// attached, has its address.
func (r *Resource) hasPrivateNetwork(id, want string) client.DoneCheck {
	return func(ctx context.Context) (bool, error) {
		inst, err := r.api.GetInstance(ctx, id)
		if err != nil {
			return false, err
		}
		if inst.ObservedState == client.InstanceFailed {
			return false, failedInstanceError(inst)
		}
		if want == client.PrivateNetworkAttached {
			return inst.PrivateNetworkState == want && inst.PrivateNetworkIP != nil && *inst.PrivateNetworkIP != "", nil
		}
		return inst.PrivateNetworkState == want || inst.PrivateNetworkState == "", nil
	}
}

// privateNetworkHint says what to do about the refusals of an attach, or of a
// security group change while attached. The API's own message, which names the
// rules to narrow, comes first.
func privateNetworkHint(err error) (string, bool) {
	switch {
	case client.HasCode(err, client.CodeSecurityGroupAllowsPrivateNetwork),
		client.HasFieldCode(err, "security_group_id", client.CodeSecurityGroupAllowsPrivateNetwork):
		return "A security group applies to every interface of the instance, so it must let in nothing from the private network's range. " +
			"Narrow the ingress rules the message names in the instance's pantechdynamics_security_group: replace 0.0.0.0/0 with the addresses you need, or with ranges that leave out the private range. Then apply again.", true
	case client.HasCode(err, client.CodePrivateNetworkNotAvailable):
		return "The private database network is available only to standard instances (no subnet_id) in a zone that has one. Set private_network = false.", true
	case client.HasCode(err, "INSTANCE_NOT_SETTLED"):
		return "The instance must be running or stopped with no other change in progress. Wait for it to settle, then apply again.", true
	}
	return "", false
}

// addPrivateNetworkError reports a failed attach or detach on private_network.
func addPrivateNetworkError(diags *diag.Diagnostics, summary, id string, err error) {
	if hint, ok := privateNetworkHint(err); ok {
		diags.AddAttributeError(pathPrivateNetwork, summary, err.Error()+"\n\n"+hint)
		return
	}
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		diags.AddAttributeError(pathPrivateNetwork, summary, err.Error())
		return
	}
	addWaitError(diags, summary, id, err)
}

// privateIPFollowsAttachment keeps private_network_ip from the state unless
// private_network changes, when the address is known only after apply.
type privateIPFollowsAttachment struct{}

func (privateIPFollowsAttachment) Description(context.Context) string {
	return "Keeps the address unless private_network changes."
}

func (m privateIPFollowsAttachment) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (privateIPFollowsAttachment) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() || !req.PlanValue.IsUnknown() {
		return
	}
	var configured, stored types.Bool
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, pathPrivateNetwork, &configured)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, pathPrivateNetwork, &stored)...)
	if resp.Diagnostics.HasError() || configured.IsUnknown() {
		return
	}
	// Unset keeps the interface as it is, so the address stays too.
	if configured.IsNull() || configured.ValueBool() == stored.ValueBool() {
		resp.PlanValue = req.StateValue
	}
}

// privateNetworkProblem is why a configuration cannot have the interface, or "".
func privateNetworkProblem(subnet types.String, on types.Bool) string {
	if !subnet.IsNull() && !on.IsNull() && !on.IsUnknown() && on.ValueBool() {
		return fmt.Sprintf("The private database network is for standard instances. An instance in a subnet reaches a database in its VPC directly. Remove %s, or remove subnet_id.", "private_network")
	}
	return ""
}
