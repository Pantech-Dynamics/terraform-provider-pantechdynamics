package database

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

// attributeFor maps a 422 field to a schema attribute. The API's password and
// access rule fields have different names from the schema's.
func attributeFor(field string) (path.Path, bool) {
	switch field {
	case "name", "engine", "version", "plan_slug", "zone_id", "subnet_id", "admin_username", "access_rules", "storage_gb":
		return path.Root(field), true
	case "password":
		return pathPassword, true
	case "rules":
		return path.Root("access_rules"), true
	}
	return path.Path{}, false
}

// addCreateError explains the failures a user can act on.
func addCreateError(diags *diag.Diagnostics, err error) {
	if client.HasCode(err, client.CodeInsufficientCredit) {
		diags.AddError("Not enough credit to create the database",
			err.Error()+"\n\nNothing was ordered and nothing was charged. Top up your credit or add a card to the account, then apply again.")
		return
	}
	resourcekit.AddHintedAPIError(diags, "Error creating database", err, attributeFor, map[string]string{
		"DATABASES_UNAVAILABLE":        "Managed databases are not available right now. Nothing was ordered. Try again later.",
		"DATABASE_STORAGE_UNAVAILABLE": "A chosen storage size is not available in this zone yet. Nothing was ordered. Remove storage_gb to use the plan's size, or try again later.",
	})
}

// addWaitError reports a failed or interrupted wait during create. The id is in
// the message because, after an interruption, the order may still complete.
func addWaitError(diags *diag.Diagnostics, summary, id string, err error) {
	var orderErr *client.DatabaseOrderError
	switch {
	case errors.As(err, &orderErr):
		diags.AddError(summary, err.Error()+"\n\nThe database "+id+" was not provisioned. Quote the order id to support if the payment was not returned.")
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		diags.AddError(summary, "Stopped waiting for database "+id+": "+err.Error()+
			"\n\nThe order may still complete, and may already be paid for. Run `terraform refresh` to see the current state before applying again.")
	default:
		diags.AddError(summary, err.Error())
	}
}

// updateHints say what to do about the refusals of an in-place change.
var updateHints = map[string]string{
	"DATABASE_STORAGE_RESIZE_IN_PROGRESS": "Another storage resize of this database is still running. Wait for it to finish, then apply again.",
	"DATABASE_STORAGE_UNAVAILABLE":        "Storage resizing is not available in this database's zone yet. Nothing was changed. Try again later.",
}

// addUpdateError reports an in-place change that failed.
func addUpdateError(diags *diag.Diagnostics, summary, id string, err error) {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		resourcekit.AddHintedAPIError(diags, summary, err, attributeFor, updateHints)
		return
	}
	resourcekit.AddWaitError(diags, summary, "database", id, err)
}
