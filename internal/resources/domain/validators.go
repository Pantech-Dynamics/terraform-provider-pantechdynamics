package domain

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/dnsname"
)

// mustBeTrue refuses accept_terms = false at plan time: the backend refuses
// the order without it, and the plan should say why before anything runs.
type mustBeTrue struct{}

func (mustBeTrue) Description(context.Context) string { return "must be true" }

func (v mustBeTrue) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (mustBeTrue) ValidateBool(_ context.Context, req validator.BoolRequest, resp *validator.BoolResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if !req.ConfigValue.ValueBool() {
		resp.Diagnostics.AddAttributeError(req.Path, "Terms not accepted",
			"Set accept_terms = true to register the domain. "+warnNoUndo)
	}
}

// nameServerList checks 2 to 6 valid host names.
type nameServerList struct{}

func (nameServerList) Description(context.Context) string {
	return fmt.Sprintf("must hold %d to %d host names", minCustomNSCount, maxCustomNSCount)
}

func (v nameServerList) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (nameServerList) ValidateSet(ctx context.Context, req validator.SetRequest, resp *validator.SetResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if n := len(req.ConfigValue.Elements()); n < minCustomNSCount || n > maxCustomNSCount {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid name servers",
			fmt.Sprintf("Set %d to %d custom name servers, got %d. Leave name_servers unset to use Pantech DNS.", minCustomNSCount, maxCustomNSCount, n))
		return
	}
	for _, e := range req.ConfigValue.Elements() {
		s, ok := e.(types.String)
		if !ok || s.IsUnknown() {
			continue
		}
		if _, valid := dnsname.CanonicalHost(s.ValueString()); !valid {
			resp.Diagnostics.AddAttributeError(req.Path, "Invalid name server", fmt.Sprintf("%q is not a host name such as ns1.example.net.", s.ValueString()))
		}
	}
}

// phoneFormat checks +CC.NUMBER, as the registries require.
type phoneFormat struct{}

func (phoneFormat) Description(context.Context) string {
	return "must be +CC.NUMBER, such as +234.8012345678"
}

func (v phoneFormat) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (phoneFormat) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if !validPhone(strings.TrimSpace(req.ConfigValue.ValueString())) {
		// The value is personal data: the message does not repeat it.
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid phone number", "Write the phone number as +CC.NUMBER, such as +234.8012345678.")
	}
}

func validPhone(p string) bool {
	rest, ok := strings.CutPrefix(p, "+")
	if !ok {
		return false
	}
	cc, number, ok := strings.Cut(rest, ".")
	return ok && len(cc) >= 1 && len(cc) <= 3 && len(number) >= 4 && len(number) <= 15 && digits(cc) && digits(number)
}

// countryCode checks a two-letter ISO code in capitals.
type countryCode struct{}

func (countryCode) Description(context.Context) string {
	return "must be a two-letter ISO country code in capitals, such as NG"
}

func (v countryCode) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (countryCode) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	c := req.ConfigValue.ValueString()
	if len(c) != 2 || c[0] < 'A' || c[0] > 'Z' || c[1] < 'A' || c[1] > 'Z' {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid country code", fmt.Sprintf("Expected a two-letter ISO country code in capitals, such as \"NG\", got %q.", c))
	}
}

func digits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// ngSuffix ends every .ng name, including the second levels Pantech sells
// (com.ng, org.ng, net.ng, name.ng, mobi.ng, i.ng).
const ngSuffix = ".ng"

// checkNG reports what the .ng registry refuses, before anything is bought.
func checkNG(m model, diags *diag.Diagnostics) {
	name := m.Name.ValueString()
	if !strings.HasSuffix(name, ngSuffix) {
		return
	}
	if !m.Privacy.IsNull() && !m.Privacy.IsUnknown() && !m.Privacy.ValueBool() {
		diags.AddAttributeError(path.Root("privacy"), "Privacy is always on for .ng",
			"The .ng registry hides every registrant's contact details, so privacy cannot be turned off for "+name+". Remove privacy, or set it to true.")
	}
	c := m.Contact
	if c == nil {
		return
	}
	for attr, v := range map[string]types.String{"state": c.State, "postal_code": c.PostalCode} {
		if !v.IsUnknown() && strings.TrimSpace(v.ValueString()) == "" {
			diags.AddAttributeError(path.Root("contact").AtName(attr), "Missing "+attr, "A .ng registrant needs "+attr+".")
		}
	}
	if strings.HasSuffix(name, ".name.ng") && !c.Type.IsUnknown() && c.Type.ValueString() == "company" {
		diags.AddAttributeError(path.Root("contact").AtName("type"), "name.ng is for people",
			"A name.ng domain can only be registered to a person: set contact.type = \"person\".")
	}
}
