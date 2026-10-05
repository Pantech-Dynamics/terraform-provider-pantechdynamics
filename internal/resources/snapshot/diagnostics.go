package snapshot

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
var settableFields = map[string]bool{"name": true, "instance_id": true, "volume_id": true}

// addAPIError reports a client error. A 422 whose fields the schema has points at
// each attribute. Anything else becomes one diagnostic that includes the request_id.
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

// addCreateError explains the failures a user can act on. A failed snapshot keeps
// its name, so a name clash often means an earlier attempt that failed.
func addCreateError(diags *diag.Diagnostics, err error) {
	if client.HasCode(err, client.CodeSnapshotNameTaken) {
		diags.AddError("Snapshot name already in use",
			err.Error()+"\n\nThis instance or volume already has, or had, a snapshot with this name. On the staging platform a snapshot that failed and a snapshot that was deleted both keep their name. Choose a new name, or bring a live snapshot under Terraform with `terraform import`.")
		return
	}
	addAPIError(diags, "Error creating snapshot", err)
}

// addWaitError reports a failed or interrupted wait. The snapshot id is in the
// message because, after an interruption, the snapshot may still complete.
func addWaitError(diags *diag.Diagnostics, summary, id, hint string, err error) {
	var opErr *client.OperationError
	switch {
	case errors.As(err, &opErr):
		diags.AddError(summary, resourcekit.WithFailureHint(err)+hint+"\n\nThe failed snapshot "+id+" still exists and holds its name. The next apply deletes it and tries again, but a deleted snapshot's name stays reserved on staging, so that retry can be refused with SNAPSHOT_NAME_TAKEN. If it is, choose a new name.")
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		diags.AddError(summary, "Stopped waiting for snapshot "+id+": "+err.Error()+
			"\n\nThe snapshot may still complete. Run `terraform refresh` to see the current state before applying again.")
	default:
		diags.AddError(summary, err.Error()+hint)
	}
}

// failureHint names what staging showed about when a snapshot fails, by source.
func failureHint(m model) string {
	switch {
	case knownString(m.InstanceID) != "":
		return "\n\nOn the staging platform a snapshot of a running instance's root disk failed, and a snapshot of a stopped instance succeeded. Stop the instance (desired_state = \"stopped\") and try again."
	case knownString(m.VolumeID) != "":
		return "\n\nOn the staging platform a snapshot of a local volume attached to an instance succeeded, and a snapshot of a detached shared volume failed."
	}
	return ""
}

func knownString(v interface {
	IsNull() bool
	IsUnknown() bool
	ValueString() string
}) string {
	if v.IsNull() || v.IsUnknown() {
		return ""
	}
	return v.ValueString()
}
