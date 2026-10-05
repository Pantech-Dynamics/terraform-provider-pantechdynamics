package securitygroup

import (
	"context"
	"errors"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// attributeFor maps a 422 field to a schema attribute. A field inside a rule,
// such as "rules[0].port_range", points at the rules attribute as a whole. A
// field the schema does not have returns false, because Terraform rejects a
// diagnostic on a path that does not exist.
func attributeFor(field string) (path.Path, bool) {
	switch {
	case field == "name":
		return path.Root("name"), true
	case field == "rules" || strings.HasPrefix(field, "rules[") || strings.HasPrefix(field, "rules."):
		return path.Root("rules"), true
	default:
		return path.Path{}, false
	}
}

// addAPIError reports a client error. A 422 whose fields the schema has points at
// each invalid attribute. Anything else becomes one diagnostic that includes the
// request_id.
func addAPIError(diags *diag.Diagnostics, summary string, err error) {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && len(apiErr.Errors) > 0 && allMapped(apiErr.Errors) {
		for _, fe := range apiErr.Errors {
			p, _ := attributeFor(fe.Field)
			diags.AddAttributeError(p, summary, fe.Message+" ("+fe.Code+")")
		}
		return
	}
	diags.AddError(summary, err.Error())
}

func allMapped(fields []client.FieldError) bool {
	for _, fe := range fields {
		if _, ok := attributeFor(fe.Field); !ok {
			return false
		}
	}
	return true
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

// addDeleteError explains the refusals a user can act on.
func addDeleteError(diags *diag.Diagnostics, err error) {
	switch {
	case client.HasCode(err, client.CodeDefaultSecurityGroupUndeletable):
		diags.AddError("The default security group cannot be deleted",
			err.Error()+"\n\nThe default group exists on every account and the API never removes it. Remove it from Terraform state with `terraform state rm`, or stop managing it.")
	case client.HasCode(err, client.CodeInvalidResourceState):
		diags.AddError("Security group is still in use",
			err.Error()+"\n\nAn instance still uses this group. Move those instances to another security group, or delete them, then try again.")
	case client.HasCode(err, client.CodeSecurityGroupAttachedToDatabase):
		diags.AddError("Security group is attached to a database",
			err.Error()+"\n\nRemove the group from the database's security_group_ids (pantechdynamics_database) first, then try again.")
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
