// Package resourcekit holds the small helpers the VPC resources share: timeouts,
// timestamps and error diagnostics. The older resources carry their own copies;
// new ones use this package instead of adding another.
package resourcekit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// Resource states the VPC resources share.
const (
	StateActive  = "active"
	StateFailed  = "failed"
	StateDeleted = "deleted"
)

// DefaultTimeout bounds each create and delete wait of a VPC resource.
const DefaultTimeout = 15 * time.Minute

// Timeout is the shape of a timeouts block's Create or Delete getter.
type Timeout func(ctx context.Context, def time.Duration) (time.Duration, diag.Diagnostics)

// WithTimeout applies the configured timeout, or DefaultTimeout, to ctx. It
// reports false, with diagnostics added, if the configured value is invalid.
func WithTimeout(ctx context.Context, get Timeout, diags *diag.Diagnostics) (context.Context, context.CancelFunc, bool) {
	d, timeoutDiags := get(ctx, DefaultTimeout)
	diags.Append(timeoutDiags...)
	if diags.HasError() {
		return ctx, func() {}, false
	}
	ctx, cancel := context.WithTimeout(ctx, d)
	return ctx, cancel, true
}

// Timestamp formats a time to whole seconds, which keeps state stable if the
// backend varies the fractional digits.
func Timestamp(t *time.Time) types.String {
	if t == nil {
		return types.StringNull()
	}
	return types.StringValue(t.UTC().Format(time.RFC3339))
}

// OptionalString maps an API string that may be null or empty to a Terraform
// string, null when absent.
func OptionalString(s *string) types.String {
	if s == nil || *s == "" {
		return types.StringNull()
	}
	return types.StringValue(*s)
}

// OptionalInt64 maps an API integer that may be null to a Terraform number.
func OptionalInt64(n *int64) types.Int64 {
	if n == nil {
		return types.Int64Null()
	}
	return types.Int64Value(*n)
}

// IntPtr returns nil for a null or unknown Terraform number, so an unset
// attribute is omitted from the request.
func IntPtr(v types.Int64) *int64 {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	n := v.ValueInt64()
	return &n
}

// CheckID fails an import whose id lacks the resource's prefix, so a wrong id
// stops at plan time instead of as a 404.
func CheckID(diags *diag.Diagnostics, kind, prefix, id string) bool {
	if strings.HasPrefix(id, prefix) {
		return true
	}
	diags.AddError("Invalid "+kind+" id", fmt.Sprintf("Expected an id starting with %q, got %q.", prefix, id))
	return false
}

// AttributeFor maps a 422 field name to a schema attribute, or false if the
// schema has none (Terraform rejects a diagnostic on a path that does not exist).
type AttributeFor func(field string) (path.Path, bool)

// AddAPIError reports a client error. A 422 whose fields the schema all has
// points at each invalid attribute. Anything else becomes one diagnostic that
// carries the request_id.
func AddAPIError(diags *diag.Diagnostics, summary string, err error, attr AttributeFor) {
	var apiErr *client.APIError
	if attr != nil && errors.As(err, &apiErr) && len(apiErr.Errors) > 0 && allMapped(apiErr.Errors, attr) {
		for _, fe := range apiErr.Errors {
			p, _ := attr(fe.Field)
			diags.AddAttributeError(p, summary, fe.Message+" ("+fe.Code+")")
		}
		return
	}
	diags.AddError(summary, err.Error())
}

func allMapped(fields []client.FieldError, attr AttributeFor) bool {
	for _, fe := range fields {
		if _, ok := attr(fe.Field); !ok {
			return false
		}
	}
	return true
}

// AddWaitError reports a failed or interrupted wait. The id is in the message
// because, after an interruption, the backend may still finish on its own.
func AddWaitError(diags *diag.Diagnostics, summary, kind, id string, err error) {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		diags.AddError(summary, "Stopped waiting for "+kind+" "+id+": "+err.Error()+
			"\n\nThe backend may still complete the change. Run `terraform refresh` to see the current state.")
		return
	}
	diags.AddError(summary, err.Error())
}

// AddHintedError reports an error and, for the given problem codes, appends a
// hint the user can act on.
func AddHintedError(diags *diag.Diagnostics, summary string, err error, hints map[string]string) {
	for code, hint := range hints {
		if client.HasCode(err, code) {
			diags.AddError(summary, err.Error()+"\n\n"+hint)
			return
		}
	}
	diags.AddError(summary, err.Error())
}

// ConfigureError reports provider data of the wrong type, which is a bug.
func ConfigureError(diags *diag.Diagnostics, data any) {
	diags.AddError("Unexpected provider data", fmt.Sprintf("Expected an API client, got %T. This is a bug in the provider.", data))
}

// AddHintedAPIError reports a create or delete error: field errors from a 422
// point at their attributes, and any other error gets the hint for its problem
// code, if there is one.
func AddHintedAPIError(diags *diag.Diagnostics, summary string, err error, attr AttributeFor, hints map[string]string) {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && len(apiErr.Errors) > 0 {
		AddAPIError(diags, summary, err, attr)
		return
	}
	AddHintedError(diags, summary, err, hints)
}
