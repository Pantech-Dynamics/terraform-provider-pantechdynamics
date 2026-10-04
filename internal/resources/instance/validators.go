package instance

import (
	"context"
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// hostnameLabel is an RFC 1123 label: letters, digits and hyphens, not starting
// or ending with a hyphen, 1 to 63 characters.
var hostnameLabel = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

// nameValidator checks the instance name. The API says the name becomes the
// hostname, 1 to 63 characters. The backend itself accepted spaces and 64
// characters on rename, so this is stricter than the backend on purpose: an
// instance whose hostname is not a valid label is a trap, and a failed order
// still costs money.
type nameValidator struct{}

var _ validator.String = nameValidator{}

func (nameValidator) Description(context.Context) string {
	return "must be 1 to 63 letters, digits or hyphens, and cannot start or end with a hyphen, because it becomes the hostname"
}

func (v nameValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (nameValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if name := req.ConfigValue.ValueString(); !hostnameLabel.MatchString(name) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid name",
			fmt.Sprintf("The name becomes the hostname, so it must be 1 to 63 letters, digits or hyphens and cannot start or end with a hyphen, got %q.", name))
	}
}
