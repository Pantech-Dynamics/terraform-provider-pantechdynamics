package sshkey

import (
	"errors"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

func TestAddAPIErrorAttachesOnlyToKnownFields(t *testing.T) {
	validation := func(fields ...string) error {
		apiErr := &client.APIError{Status: 422, Code: "VALIDATION_FAILED"}
		for _, f := range fields {
			apiErr.Errors = append(apiErr.Errors, client.FieldError{Field: f, Code: "X", Message: "bad"})
		}
		return apiErr
	}

	tests := []struct {
		name         string
		err          error
		wantAttached []path.Path // one per diagnostic, in order; empty means one general error
	}{
		{"known field", validation("public_key"), []path.Path{path.Root("public_key")}},
		{"two known fields", validation("name", "public_key"), []path.Path{path.Root("name"), path.Root("public_key")}},
		{"unknown field becomes a general error", validation("bogus"), nil},
		{"one unknown field among known ones becomes a general error", validation("name", "bogus"), nil},
		{"a plain error is general", errors.New("boom"), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var diags diag.Diagnostics
			addAPIError(&diags, "Error", tt.err)

			errs := diags.Errors()
			if len(tt.wantAttached) == 0 {
				if len(errs) != 1 {
					t.Fatalf("diags = %v, want exactly one general error", diags)
				}
				if _, attached := errs[0].(diag.DiagnosticWithPath); attached {
					t.Fatal("a diagnostic on a path the schema does not have would be rejected by Terraform")
				}
				return
			}
			if len(errs) != len(tt.wantAttached) {
				t.Fatalf("diags = %v", diags)
			}
			for i, want := range tt.wantAttached {
				withPath, ok := errs[i].(diag.DiagnosticWithPath)
				if !ok || !withPath.Path().Equal(want) {
					t.Fatalf("diagnostic %d = %#v, want it attached to %s", i, errs[i], want)
				}
			}
		})
	}
}
