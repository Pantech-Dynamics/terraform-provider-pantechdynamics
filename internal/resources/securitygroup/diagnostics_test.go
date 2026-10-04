package securitygroup

import (
	"errors"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

func TestAttributeFor(t *testing.T) {
	tests := []struct {
		field  string
		want   path.Path
		wantOK bool
	}{
		{"name", path.Root("name"), true},
		{"rules", path.Root("rules"), true},
		{"rules[0].port_range", path.Root("rules"), true},
		{"rules.0.cidr", path.Root("rules"), true},
		{"rulesets", path.Path{}, false},
		{"bogus", path.Path{}, false},
		{"", path.Path{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			got, ok := attributeFor(tt.field)
			if ok != tt.wantOK || (ok && !got.Equal(tt.want)) {
				t.Fatalf("attributeFor(%q) = %v, %v", tt.field, got, ok)
			}
		})
	}
}

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
		wantAttached []path.Path
	}{
		{"known field", validation("name"), []path.Path{path.Root("name")}},
		{"a nested rule field points at rules", validation("rules[0].port_range"), []path.Path{path.Root("rules")}},
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
