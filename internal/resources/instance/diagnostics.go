package instance

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// settableFields are the attributes a 422 may point at. A field name the schema
// does not have would make Terraform reject the diagnostic, so anything else is
// reported as one general error.
var settableFields = map[string]bool{
	"name": true, "plan_slug": true, "image_slug": true,
	"ssh_key_id": true, "region": true, "security_group_id": true, "tags": true,
}

// addAPIError reports a client error. A 422 whose fields the schema has points at
// each attribute. Anything else becomes one diagnostic that includes the
// request_id.
func addAPIError(diags *diag.Diagnostics, summary string, err error) {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && len(apiErr.Errors) > 0 && allSettable(apiErr.Errors) {
		for _, fe := range apiErr.Errors {
			diags.AddAttributeError(path.Root(fe.Field), summary, fe.Message+" ("+fe.Code+")")
		}
		return
	}
	diags.AddError(summary, err.Error())
}

func allSettable(fields []client.FieldError) bool {
	for _, fe := range fields {
		if !settableFields[fe.Field] {
			return false
		}
	}
	return true
}

// addCreateError explains the failures a user can act on. Running out of credit
// is the common one, and the API's detail already states the amounts.
func addCreateError(diags *diag.Diagnostics, err error) {
	if client.HasCode(err, client.CodeInsufficientCredit) {
		diags.AddError("Not enough credit to create the instance",
			err.Error()+"\n\nNothing was ordered and nothing was charged. Top up your credit or add a card to the account, then apply again.")
		return
	}
	addAPIError(diags, "Error creating instance", err)
}

// addDeleteError explains the refusals a user can act on.
func addDeleteError(diags *diag.Diagnostics, err error) {
	if client.HasCode(err, client.CodeInstanceHasPublicIP) || client.HasCode(err, client.CodeInstanceHasPortForwards) {
		diags.AddError("Instance still has a public IP or port forwards",
			err.Error()+"\n\nDetach the static_nat public IP (remove instance_id from its pantechdynamics_public_ip, which keeps the address) or release it, or delete the port forwarding rules that point at this instance, then try again.")
		return
	}
	if client.HasCode(err, client.CodeInstanceBehindLoadBalancer) {
		diags.AddError("Instance is behind a load balancer",
			err.Error()+"\n\nRemove the instance from the instance_ids of every pantechdynamics_load_balancer that targets it, or delete those load balancers, then try again.")
		return
	}
	addAPIError(diags, "Error deleting instance", err)
}

// addWaitError reports a failed or interrupted wait. The instance id is in the
// message because, after an interruption, the order may still complete and
// charge, so the user must be able to find it.
func addWaitError(diags *diag.Diagnostics, summary, id string, err error) {
	var orderErr *client.OrderError
	switch {
	case errors.As(err, &orderErr):
		diags.AddError(summary, err.Error()+"\n\nThe instance "+id+" was not provisioned. Check whether the account was charged, and quote the order id to support if it was.")
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		diags.AddError(summary, "Stopped waiting for instance "+id+": "+err.Error()+
			"\n\nThe order may still complete, and credit may already be reserved. Run `terraform refresh` to see the current state before applying again.")
	default:
		diags.AddError(summary, resourcekit.WithFailureHint(err))
	}
}

// addUpdateError explains the failures of a resize or a security group change
// that a user can act on. A plan that is not bigger is the common one.
func addUpdateError(diags *diag.Diagnostics, summary, id string, err error) {
	if hint, ok := privateNetworkHint(err); ok {
		diags.AddAttributeError(path.Root("security_group_id"), summary, err.Error()+"\n\n"+hint)
		return
	}
	if client.HasFieldCode(err, "plan_slug", client.FieldCodePlanNotBigger) {
		diags.AddAttributeError(path.Root("plan_slug"), "Instances can only be resized to a bigger plan",
			"The platform refused the resize because the new plan is not bigger than the current one.\n\n"+
				"To move to a smaller plan, replace the instance with `terraform apply -replace=<address>`. That destroys the instance and its disk.")
		return
	}
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		addAPIError(diags, summary, err)
		return
	}
	addWaitError(diags, summary, id, err)
}
