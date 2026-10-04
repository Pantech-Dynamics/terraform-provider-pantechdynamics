package volume

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// settableFields are the attributes a 422 may point at. A field name the schema
// does not have would make Terraform reject the diagnostic, so anything else is
// reported as one general error.
var settableFields = map[string]bool{
	"name": true, "disk_offering_slug": true, "size_gb": true,
	"region": true, "mount_point": true, "instance_id": true,
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

// addWaitError reports a failed or interrupted wait. The volume id is in the
// message because, after an interruption, the change may still complete.
func addWaitError(diags *diag.Diagnostics, summary, id string, err error) {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		diags.AddError(summary, "Stopped waiting for volume "+id+": "+err.Error()+
			"\n\nThe change may still complete. Run `terraform refresh` to see the current state before applying again.")
	default:
		diags.AddError(summary, err.Error())
	}
}

// addAttachError explains a failed attach. On staging every attach of a shared
// volume failed with a bare provider job failure while a local volume attached,
// so the storage type is named in the message.
func addAttachError(diags *diag.Diagnostics, id, storageType string, err error) {
	var opErr *client.OperationError
	if errors.As(err, &opErr) {
		hint := ""
		if storageType == "shared" {
			hint = "\n\nThis volume's storage type is \"shared\". On the staging platform, attaching a shared volume to an instance failed in every test while a local volume attached. Try a disk offering whose storage_type is \"local\"."
		}
		diags.AddError("Error attaching the volume", err.Error()+hint+"\n\nThe volume "+id+" exists and is not attached. It can still be deleted.")
		return
	}
	addWaitError(diags, "Error attaching the volume", id, err)
}

// addDeleteError explains the refusals a user can act on.
func addDeleteError(diags *diag.Diagnostics, err error) {
	if client.HasCode(err, client.CodeInvalidResourceState) {
		diags.AddError("The volume cannot be deleted in its current state",
			err.Error()+"\n\nThe platform refuses to delete a volume that is attached, or that has an attach request pending. Detach it first, then try again.")
		return
	}
	addAPIError(diags, "Error deleting volume", err)
}
