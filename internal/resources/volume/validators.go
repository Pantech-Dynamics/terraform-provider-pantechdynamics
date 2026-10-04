package volume

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

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

// nameCheck rejects an empty name, which the API refuses anyway.
var nameCheck = stringCheck{
	description: "must not be empty",
	problem: func(v string) string {
		if strings.TrimSpace(v) == "" {
			return "The name must not be empty."
		}
		return ""
	},
}

// mountPointCheck mirrors the API rule: an absolute path such as /data. The
// platform stores it as a label and does not mount anything.
var mountPointCheck = stringCheck{
	description: "must be an absolute path such as /data",
	problem: func(v string) string {
		if len(v) < 2 || !strings.HasPrefix(v, "/") || strings.ContainsAny(v, " \t\n") {
			return "The mount point must be an absolute path without spaces, such as /data, got " + quote(v) + "."
		}
		return ""
	},
}

// instanceIDCheck catches an id of the wrong kind at plan time.
var instanceIDCheck = stringCheck{
	description: "must be an instance id starting with vm_",
	problem: func(v string) string {
		if !strings.HasPrefix(v, "vm_") {
			return "Expected an instance id starting with \"vm_\", got " + quote(v) + "."
		}
		return ""
	},
}

func quote(s string) string { return `"` + s + `"` }
