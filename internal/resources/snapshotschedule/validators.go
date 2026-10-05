package snapshotschedule

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

const (
	minRetention = 1
	maxRetention = 168
)

// oneOf rejects a string that is not in a fixed set. It is a few lines, so it lives
// here rather than adding the framework-validators module.
type oneOf struct{ allowed []string }

var _ validator.String = oneOf{}

func (v oneOf) Description(context.Context) string {
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

// retentionRange keeps retention_count within what the API accepts.
type retentionRange struct{}

var _ validator.Int64 = retentionRange{}

func (retentionRange) Description(context.Context) string {
	return fmt.Sprintf("must be from %d to %d", minRetention, maxRetention)
}

func (v retentionRange) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (retentionRange) ValidateInt64(_ context.Context, req validator.Int64Request, resp *validator.Int64Response) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if n := req.ConfigValue.ValueInt64(); n < minRetention || n > maxRetention {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid retention count",
			fmt.Sprintf("retention_count must be from %d to %d, got %d.", minRetention, maxRetention, n))
	}
}

// prefixCheck catches an id of the wrong kind at plan time.
type prefixCheck struct{ prefix, kind string }

var _ validator.String = prefixCheck{}

func (v prefixCheck) Description(context.Context) string {
	return "must be " + v.kind + " id starting with " + v.prefix
}

func (v prefixCheck) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (v prefixCheck) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if !strings.HasPrefix(req.ConfigValue.ValueString(), v.prefix) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid value",
			fmt.Sprintf("Expected %s id starting with %q, got %q.", v.kind, v.prefix, req.ConfigValue.ValueString()))
	}
}
