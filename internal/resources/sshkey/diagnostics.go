package sshkey

import (
	"errors"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// addAPIError reports a client error. A 422 points at each invalid attribute,
// anything else becomes one diagnostic that includes the request_id.
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
