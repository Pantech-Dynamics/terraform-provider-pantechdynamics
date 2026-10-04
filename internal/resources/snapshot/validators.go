package snapshot

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

const maxNameLength = 255

// stringCheck is a validator built from a function that returns a problem, or "".
// It keeps each rule to a few lines without adding the framework-validators module.
type stringCheck struct {
	description string
	problem     func(value string) string
}

var _ validator.String = stringCheck{}

func (v stringCheck) Description(context.Context) string { return v.description }

func (v stringCheck) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (v stringCheck) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if msg := v.problem(req.ConfigValue.ValueString()); msg != "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid value", msg)
	}
}

// nameCheck rejects an empty or over-long name. The API refuses both, an over-long
// one with a bare validation error.
var nameCheck = stringCheck{
	description: "must be 1 to 255 characters",
	problem: func(v string) string {
		if n := utf8.RuneCountInString(v); strings.TrimSpace(v) == "" || n > maxNameLength {
			return "The name must be 1 to 255 characters and not blank."
		}
		return ""
	},
}

// prefixCheck catches an id of the wrong kind at plan time.
func prefixCheck(prefix, kind string) stringCheck {
	return stringCheck{
		description: "must be " + kind + " id starting with " + prefix,
		problem: func(v string) string {
			if !strings.HasPrefix(v, prefix) {
				return "Expected " + kind + " id starting with \"" + prefix + "\", got \"" + v + "\"."
			}
			return ""
		},
	}
}
