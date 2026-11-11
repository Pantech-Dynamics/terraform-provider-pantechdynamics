package certificate

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/dnsname"
)

const maxNames = 100

// CertificateNames returns the validator for a certificate's names: 1 to 100
// distinct lower-case DNS names, a wildcard only as the whole left-most label.
func CertificateNames() validator.List { return certificateNames{} }

type certificateNames struct{}

func (certificateNames) Description(context.Context) string {
	return fmt.Sprintf("must be 1 to %d distinct lower-case DNS names", maxNames)
}

func (v certificateNames) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (certificateNames) ValidateList(_ context.Context, req validator.ListRequest, resp *validator.ListResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	elems := req.ConfigValue.Elements()
	if len(elems) == 0 || len(elems) > maxNames {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid certificate names", fmt.Sprintf("A certificate has 1 to %d names, got %d.", maxNames, len(elems)))
		return
	}
	seen := map[string]bool{}
	for i, e := range elems {
		s, ok := e.(types.String)
		if !ok || s.IsUnknown() || s.IsNull() {
			continue
		}
		name := s.ValueString()
		if problem := NameProblem(name); problem != "" {
			resp.Diagnostics.AddAttributeError(req.Path.AtListIndex(i), "Invalid certificate name", problem)
			continue
		}
		if seen[name] {
			resp.Diagnostics.AddAttributeError(req.Path.AtListIndex(i), "Invalid certificate name", fmt.Sprintf("%q is listed more than once.", name))
		}
		seen[name] = true
	}
}

// NameProblem returns why a name cannot be on a certificate, or "".
func NameProblem(name string) string {
	base, wildcard := strings.CutPrefix(name, "*.")
	if strings.Contains(base, "*") {
		return fmt.Sprintf("%q: a wildcard is only the whole left-most label, such as *.example.com.", name)
	}
	if problem := dnsname.ZoneNameProblem(base); problem != "" {
		what := "a name"
		if wildcard {
			what = "the name after *."
		}
		return fmt.Sprintf("%q: %s %s.", name, what, problem)
	}
	return ""
}
