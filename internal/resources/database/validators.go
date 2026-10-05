package database

import (
	"context"
	"fmt"
	"net/netip"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Limits from the API spec and docs/adr/0108 (amendments 6 and 7, 2026-10-05).
const (
	minPasswordLength = 16
	maxPasswordLength = 128
	maxAccessRules    = 50
	maxSecurityGroups = 5
	minAccessPrefix   = 8
)

// adminUsernamePattern is the API's rule: 3 to 32 characters, lowercase letters,
// digits and underscores, starting with a letter.
var adminUsernamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{2,31}$`)

// reservedAdminUsernames and reservedAdminPrefixes are refused by the API with
// INVALID_DATABASE_ADMIN_USERNAME.
var (
	reservedAdminUsernames = []string{
		"root", "postgres", "public", "none", "all", "user", "sys", "system",
		"replication", "current_user", "current_role", "session_user",
	}
	reservedAdminPrefixes = []string{"pg_", "mysql", "mariadb", "pantech"}
)

// adminUsernameProblem returns why the API would refuse name, or "".
func adminUsernameProblem(name string) string {
	if !adminUsernamePattern.MatchString(name) {
		return "must be 3 to 32 characters of lowercase letters, digits and underscores, starting with a letter"
	}
	for _, r := range reservedAdminUsernames {
		if name == r {
			return fmt.Sprintf("%q is reserved", name)
		}
	}
	for _, p := range reservedAdminPrefixes {
		if strings.HasPrefix(name, p) {
			return fmt.Sprintf("names starting with %q are reserved", p)
		}
	}
	return ""
}

// passwordProblem returns why the API would refuse a password, or "". It never
// echoes the password.
func passwordProblem(pw string) string {
	if n := len(pw); n < minPasswordLength || n > maxPasswordLength {
		return fmt.Sprintf("must be %d to %d characters, got %d", minPasswordLength, maxPasswordLength, n)
	}
	for _, c := range pw {
		if c <= ' ' || c > '~' || c == '\'' || c == '"' || c == '\\' {
			return "must be printable ASCII with no spaces, quotes or backslashes"
		}
	}
	return ""
}

// accessRuleProblem returns why the API would refuse a CIDR, or "".
func accessRuleProblem(cidr string) string {
	p, err := netip.ParsePrefix(cidr)
	if err != nil || !p.Addr().Is4() || p.Masked() != p {
		return fmt.Sprintf("%q is not an IPv4 CIDR with its host bits clear, for example \"10.0.0.0/16\"", cidr)
	}
	if p.Bits() < minAccessPrefix {
		return fmt.Sprintf("%q is too wide: the prefix must be /%d or longer, and 0.0.0.0/0 is never allowed", cidr, minAccessPrefix)
	}
	return ""
}

type adminUsernameValidator struct{}

func (adminUsernameValidator) Description(context.Context) string {
	return "must be 3 to 32 lowercase letters, digits or underscores, starting with a letter, and not a reserved name"
}

func (v adminUsernameValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (adminUsernameValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if problem := adminUsernameProblem(req.ConfigValue.ValueString()); problem != "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid admin username", "The admin username "+problem+".")
	}
}

type passwordValidator struct{}

func (passwordValidator) Description(context.Context) string {
	return fmt.Sprintf("must be %d to %d printable ASCII characters with no spaces, quotes or backslashes", minPasswordLength, maxPasswordLength)
}

func (v passwordValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (passwordValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if problem := passwordProblem(req.ConfigValue.ValueString()); problem != "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid database password", "The password "+problem+".")
	}
}

type accessRulesValidator struct{}

func (accessRulesValidator) Description(context.Context) string {
	return fmt.Sprintf("must be at most %d IPv4 CIDRs, each /%d to /32 with its host bits clear", maxAccessRules, minAccessPrefix)
}

func (v accessRulesValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (accessRulesValidator) ValidateSet(_ context.Context, req validator.SetRequest, resp *validator.SetResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	elems := req.ConfigValue.Elements()
	if len(elems) > maxAccessRules {
		resp.Diagnostics.AddAttributeError(req.Path, "Too many access rules",
			fmt.Sprintf("A database takes at most %d access rules, got %d.", maxAccessRules, len(elems)))
	}
	for _, e := range elems {
		s, ok := e.(types.String)
		if !ok || s.IsNull() || s.IsUnknown() {
			continue
		}
		if problem := accessRuleProblem(s.ValueString()); problem != "" {
			resp.Diagnostics.AddAttributeError(req.Path.AtSetValue(s), "Invalid access rule", problem+".")
		}
	}
}

type securityGroupIDsValidator struct{}

func (securityGroupIDsValidator) Description(context.Context) string {
	return fmt.Sprintf("must be at most %d security group ids starting with sg_", maxSecurityGroups)
}

func (v securityGroupIDsValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (securityGroupIDsValidator) ValidateSet(_ context.Context, req validator.SetRequest, resp *validator.SetResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	elems := req.ConfigValue.Elements()
	if len(elems) > maxSecurityGroups {
		resp.Diagnostics.AddAttributeError(req.Path, "Too many security groups",
			fmt.Sprintf("A database takes at most %d security groups, got %d.", maxSecurityGroups, len(elems)))
	}
	for _, e := range elems {
		s, ok := e.(types.String)
		if !ok || s.IsNull() || s.IsUnknown() {
			continue
		}
		if !strings.HasPrefix(s.ValueString(), "sg_") {
			resp.Diagnostics.AddAttributeError(req.Path.AtSetValue(s), "Invalid security group id",
				fmt.Sprintf("Expected an id starting with \"sg_\", got %q.", s.ValueString()))
		}
	}
}

// pathPassword is where a password diagnostic points.
var pathPassword = path.Root("password_wo")
