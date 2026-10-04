package plans

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// oneOf rejects a string that is not in a fixed set. It is a few lines, so it
// lives here rather than adding the framework-validators module as a dependency.
type oneOf struct {
	allowed []string
}

var _ validator.String = oneOf{}

func (v oneOf) Description(_ context.Context) string {
	return "value must be one of: " + strings.Join(v.allowed, ", ")
}

func (v oneOf) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (v oneOf) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if !slices.Contains(v.allowed, req.ConfigValue.ValueString()) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid value",
			fmt.Sprintf("Expected one of %s, got %q.", strings.Join(v.allowed, ", "), req.ConfigValue.ValueString()))
	}
}
