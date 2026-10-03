package securitygroup

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// addAPIError reports a client error. A 422 with field errors points at each
// invalid attribute. Anything else becomes one diagnostic that includes the
// request_id.
func addAPIError(diags *diag.Diagnostics, summary string, err error) {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && len(apiErr.Errors) > 0 {
		for _, fe := range apiErr.Errors {
			diags.AddAttributeError(path.Root(fe.Field), summary, fe.Message+" ("+fe.Code+")")
		}
		return
	}
	diags.AddError(summary, err.Error())
}

// addCreateError reports a failed create. The backend answers a name it has
// seen before with a bare 500, even when the old group was deleted, so a plain
// INTERNAL error gets a hint about the likeliest cause.
func addCreateError(diags *diag.Diagnostics, err error) {
	if client.HasCode(err, "INTERNAL") {
		diags.AddError("Error creating security group",
			err.Error()+"\n\nThe API returns this error when the name was used before, even by a group that has since been deleted. "+
				"Try a different name. If the name is new, retry later and quote the request_id to support.")
		return
	}
	addAPIError(diags, "Error creating security group", err)
}

// addDeleteError explains the two refusals a user can act on.
func addDeleteError(diags *diag.Diagnostics, err error) {
	switch {
	case client.HasCode(err, client.CodeDefaultSecurityGroupUndeletable):
		diags.AddError("The default security group cannot be deleted",
			err.Error()+"\n\nThe default group exists on every account and the API never removes it. Remove it from Terraform state with `terraform state rm`, or stop managing it.")
	case client.HasCode(err, client.CodeInvalidResourceState):
		diags.AddError("Security group is still in use",
			err.Error()+"\n\nAn instance still uses this group. Move those instances to another security group, or delete them, then try again.")
	default:
		addAPIError(diags, "Error deleting security group", err)
	}
}

// addWaitError reports a failed or interrupted wait. The group id is in the
// message because, after an interruption, the group may still finish on its own.
func addWaitError(diags *diag.Diagnostics, summary, id string, err error) {
	var opErr *client.OperationError
	switch {
	case errors.As(err, &opErr):
		diags.AddError(summary, err.Error())
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		diags.AddError(summary, "Stopped waiting for security group "+id+": "+err.Error()+
			"\n\nThe backend may still complete the change. Run `terraform refresh` to see the current state.")
	default:
		diags.AddError(summary, err.Error())
	}
}
