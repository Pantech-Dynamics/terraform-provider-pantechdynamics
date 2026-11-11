package dnsrecord

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/dnsname"
)

// relativeName checks a record name that is relative to the zone. A name
// ending in a dot is fully qualified and is left to the backend, which knows
// the zone's name.
type relativeName struct{}

func (relativeName) Description(context.Context) string {
	return "must be \"@\", or labels such as \"www\", \"*.dev\" or \"_dmarc\""
}

func (v relativeName) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (relativeName) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if problem := dnsname.RelativeNameProblem(req.ConfigValue.ValueString()); problem != "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid record name", fmt.Sprintf("The name %s, got %q.", problem, req.ConfigValue.ValueString()))
	}
}

// valueCount checks that a record set has 1 to 100 values.
type valueCount struct{}

func (valueCount) Description(context.Context) string {
	return fmt.Sprintf("must hold 1 to %d values", maxValues)
}

func (v valueCount) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (valueCount) ValidateSet(_ context.Context, req validator.SetRequest, resp *validator.SetResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if n := len(req.ConfigValue.Elements()); n < 1 || n > maxValues {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid number of values", fmt.Sprintf("A record set holds 1 to %d values, got %d.", maxValues, n))
	}
}

// validateRecord checks what depends on more than one attribute: each value
// against the record type, private addresses against allow_private, and the
// CNAME and NS rules. Anything unknown is skipped; the backend checks it at
// apply.
func validateRecord(ctx context.Context, m model, diags *diag.Diagnostics) {
	if m.Type.IsNull() || m.Type.IsUnknown() || m.Values.IsNull() || m.Values.IsUnknown() {
		return
	}
	recordType := m.Type.ValueString()
	values, d := valuesOf(ctx, m)
	if d.HasError() {
		return // an unknown element; checked at apply
	}
	allowPrivate := !m.AllowPrivate.IsUnknown() && m.AllowPrivate.ValueBool()
	for _, v := range values {
		if _, problem := dnsname.CanonicalValue(recordType, v); problem != "" {
			diags.AddAttributeError(path.Root("values"), "Invalid "+recordType+" value", fmt.Sprintf("The value %q %s.", v, problem))
			continue
		}
		if !allowPrivate && !m.AllowPrivate.IsUnknown() {
			if problem := dnsname.PrivateAddressProblem(recordType, v); problem != "" {
				diags.AddAttributeError(path.Root("values"), "Private address", fmt.Sprintf("The value %q %s.", v, problem))
			}
		}
	}
	if recordType == "CNAME" && len(values) != 1 {
		diags.AddAttributeError(path.Root("values"), "Invalid CNAME", fmt.Sprintf("A CNAME has exactly one value, got %d.", len(values)))
	}
	if (recordType == "CNAME" || recordType == "NS") && isApex(m) {
		diags.AddAttributeError(path.Root("name"), "Invalid "+recordType+" name",
			"A "+recordType+" record cannot be at the zone apex (\"@\"). The apex NS records are managed by Pantech DNS.")
	}
}

// isApex reports whether the configured name is the zone apex. A fully
// qualified name may be the apex too, but only the backend knows the zone's
// name, so it checks that one.
func isApex(m model) bool {
	if m.Name.IsNull() || m.Name.IsUnknown() {
		return false
	}
	name := strings.TrimSpace(m.Name.ValueString())
	return name == "@" || name == ""
}
