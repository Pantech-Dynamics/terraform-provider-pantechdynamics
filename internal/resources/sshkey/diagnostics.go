package sshkey

import (
	"errors"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// settableFields are the attributes a 422 may point at. A field name the schema
// does not have would make Terraform reject the diagnostic, so anything else is
// reported as one general error.
var settableFields = map[string]bool{"name": true, "public_key": true}

// addAPIError reports a client error. A 422 whose fields the schema has points at
// each invalid attribute. Anything else becomes one diagnostic that includes the
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

// addCreateError explains the create failures a user can act on, and falls back
// to addAPIError for the rest (including per-field 422s).
func addCreateError(diags *diag.Diagnostics, err error) {
	switch {
	case client.HasCode(err, client.CodeSSHKeyNameTaken):
		diags.AddError("SSH key name already in use",
			err.Error()+"\n\nA key with this name already exists on the account. Choose another name, or bring the existing key under Terraform with `terraform import`.")
	case client.HasCode(err, client.CodeSSHKeyAlreadyExists):
		diags.AddError("SSH public key already registered",
			err.Error()+"\n\nThis public key is already registered under another name. Import that key with `terraform import`, or use a different key.")
	default:
		addAPIError(diags, "Error creating SSH key", err)
	}
}
