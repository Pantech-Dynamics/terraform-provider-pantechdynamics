package resourcekit

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"unicode/utf8"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/dnsname"
)

// NameLength returns a validator for a name of 1 to maxLen characters. kind names
// the resource in the error, for example "network".
func NameLength(kind string, maxLen int) validator.String {
	return nameLength{kind: kind, max: maxLen}
}

type nameLength struct {
	kind string
	max  int
}

func (v nameLength) Description(context.Context) string {
	return fmt.Sprintf("must be 1 to %d characters", v.max)
}

func (v nameLength) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (v nameLength) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if n := len(req.ConfigValue.ValueString()); n < 1 || n > v.max {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid "+v.kind+" name",
			fmt.Sprintf("The name must be 1 to %d characters, got %d.", v.max, n))
	}
}

// IPv4CIDR returns a validator for an IPv4 prefix whose host bits are clear.
func IPv4CIDR() validator.String { return ipv4CIDR{} }

type ipv4CIDR struct{}

func (ipv4CIDR) Description(context.Context) string {
	return "must be an IPv4 CIDR such as 10.0.0.0/16"
}

func (v ipv4CIDR) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (ipv4CIDR) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	p, err := netip.ParsePrefix(req.ConfigValue.ValueString())
	if err != nil || !p.Addr().Is4() || p.Masked() != p {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid CIDR",
			fmt.Sprintf("%q is not an IPv4 CIDR with its host bits clear, for example \"10.0.0.0/16\".", req.ConfigValue.ValueString()))
	}
}

// IDPrefix returns a validator that a referenced id starts with prefix, so a
// wrong id (an instance id where a network id belongs) fails at plan time.
func IDPrefix(kind, prefix string) validator.String { return idPrefix{kind: kind, prefix: prefix} }

type idPrefix struct{ kind, prefix string }

func (v idPrefix) Description(context.Context) string {
	return fmt.Sprintf("must be a %s id starting with %s", v.kind, v.prefix)
}

func (v idPrefix) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (v idPrefix) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if got := req.ConfigValue.ValueString(); !strings.HasPrefix(got, v.prefix) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid "+v.kind+" id",
			fmt.Sprintf("Expected an id starting with %q, got %q.", v.prefix, got))
	}
}

// OneOf returns a validator that the value is one of the allowed strings.
func OneOf(allowed ...string) validator.String { return oneOf{allowed: allowed} }

type oneOf struct{ allowed []string }

func (v oneOf) Description(context.Context) string {
	return "must be one of: " + strings.Join(v.allowed, ", ")
}

func (v oneOf) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (v oneOf) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	got := req.ConfigValue.ValueString()
	for _, a := range v.allowed {
		if got == a {
			return
		}
	}
	resp.Diagnostics.AddAttributeError(req.Path, "Invalid value",
		fmt.Sprintf("Expected one of %s, got %q.", strings.Join(v.allowed, ", "), got))
}

// IntBetween returns a validator that the number is within lo and hi, inclusive.
func IntBetween(lo, hi int64) validator.Int64 { return intBetween{min: lo, max: hi} }

type intBetween struct{ min, max int64 }

func (v intBetween) Description(context.Context) string {
	return fmt.Sprintf("must be between %d and %d", v.min, v.max)
}

func (v intBetween) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (v intBetween) ValidateInt64(_ context.Context, req validator.Int64Request, resp *validator.Int64Response) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if got := req.ConfigValue.ValueInt64(); got < v.min || got > v.max {
		resp.Diagnostics.AddAttributeError(req.Path, "Value out of range",
			fmt.Sprintf("The value must be between %d and %d, got %d.", v.min, v.max, got))
	}
}

// DomainName returns a validator for a zone or domain name in the form the
// backend stores it: lower case, no trailing dot, such as "example.com".
// kind names the resource in the error, for example "dns zone".
func DomainName(kind string) validator.String { return domainName{kind: kind} }

type domainName struct{ kind string }

func (v domainName) Description(context.Context) string {
	return "must be a lower-case domain name without a trailing dot, such as example.com"
}

func (v domainName) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (v domainName) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if problem := dnsname.ZoneNameProblem(req.ConfigValue.ValueString()); problem != "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid "+v.kind+" name",
			fmt.Sprintf("The name %s, got %q.", problem, req.ConfigValue.ValueString()))
	}
}

// MaxLength returns a validator for a string of at most maxLen characters; an
// empty string is fine.
func MaxLength(maxLen int) validator.String { return maxLength{max: maxLen} }

type maxLength struct{ max int }

func (v maxLength) Description(context.Context) string {
	return fmt.Sprintf("must be at most %d characters", v.max)
}

func (v maxLength) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (v maxLength) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if n := utf8.RuneCountInString(req.ConfigValue.ValueString()); n > v.max {
		resp.Diagnostics.AddAttributeError(req.Path, "Value too long", fmt.Sprintf("Got %d characters; at most %d are allowed.", n, v.max))
	}
}

// maxRepositoryNameLength is the registry's limit on a repository name.
const maxRepositoryNameLength = 255

// RepositoryName returns a validator for a container repository name inside
// the namespace, such as "api/server": path components of lower-case letters
// and digits, separated within a component by single ".", "_" or "-".
func RepositoryName() validator.String { return repositoryName{} }

type repositoryName struct{}

func (repositoryName) Description(context.Context) string {
	return "must be lower-case path components such as api/server, at most 255 characters"
}

func (v repositoryName) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (repositoryName) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if name := req.ConfigValue.ValueString(); !ValidRepositoryName(name) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid repository name",
			fmt.Sprintf("A repository name is lower-case letters and digits, with single \".\", \"_\" or \"-\" between them, in path components separated by \"/\" (such as \"api/server\"), at most %d characters; got %q.", maxRepositoryNameLength, name))
	}
}

// ValidRepositoryName reports whether name follows the repository grammar
// [a-z0-9]+([._-][a-z0-9]+)* for each "/"-separated component.
func ValidRepositoryName(name string) bool {
	if name == "" || len(name) > maxRepositoryNameLength {
		return false
	}
	for _, component := range strings.Split(name, "/") {
		if !validRepositoryComponent(component) {
			return false
		}
	}
	return true
}

func validRepositoryComponent(c string) bool {
	if c == "" {
		return false
	}
	alnum := func(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') }
	if !alnum(c[0]) || !alnum(c[len(c)-1]) {
		return false
	}
	for i := 1; i < len(c); i++ {
		if !alnum(c[i]) && !alnum(c[i-1]) {
			return false // two separators in a row
		}
		if !alnum(c[i]) && c[i] != '.' && c[i] != '_' && c[i] != '-' {
			return false
		}
	}
	return true
}
