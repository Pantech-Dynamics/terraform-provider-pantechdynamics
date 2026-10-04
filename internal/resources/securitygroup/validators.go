package securitygroup

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const maxNameLength = 255

var (
	directions = []string{"ingress", "egress"}
	protocols  = []string{"tcp", "udp", "icmp", "all"}
)

// nameValidator rejects a name the backend would refuse. The backend answers an
// over-long name with an opaque 500 after a slow call, so this fails early with a
// clear message instead.
type nameValidator struct{}

var _ validator.String = nameValidator{}

func (nameValidator) Description(context.Context) string {
	return fmt.Sprintf("must be 1 to %d characters", maxNameLength)
}

func (v nameValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (nameValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if n := utf8.RuneCountInString(req.ConfigValue.ValueString()); n < 1 || n > maxNameLength {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid name",
			fmt.Sprintf("The name must be 1 to %d characters, got %d.", maxNameLength, n))
	}
}

// rulesValidator checks the whole rule set at plan time, so users see the error
// before any API call. It checks only what the backend would reject anyway.
type rulesValidator struct{}

var _ validator.Set = rulesValidator{}

func (rulesValidator) Description(context.Context) string {
	return "needs at least one rule, each with a valid direction, protocol, port_range and cidr"
}

func (v rulesValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (rulesValidator) ValidateSet(ctx context.Context, req validator.SetRequest, resp *validator.SetResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if len(req.ConfigValue.Elements()) == 0 {
		resp.Diagnostics.AddAttributeError(req.Path, "No rules",
			"A security group needs at least one rule. The API rejects an empty rule set.")
		return
	}

	var rules []ruleModel
	if d := req.ConfigValue.ElementsAs(ctx, &rules, false); d.HasError() {
		resp.Diagnostics.Append(d...)
		return
	}
	for _, r := range rules {
		for _, msg := range validateRule(r) {
			resp.Diagnostics.AddAttributeError(req.Path, "Invalid rule", msg)
		}
	}
}

// validateRule returns one message per problem. Values that are not known yet
// are skipped, because they are checked again once they are.
func validateRule(r ruleModel) []string {
	var problems []string

	if known(r.Direction) && !slices.Contains(directions, r.Direction.ValueString()) {
		problems = append(problems, fmt.Sprintf("direction must be one of %s, got %q.", strings.Join(directions, ", "), r.Direction.ValueString()))
	}
	if known(r.Protocol) && !slices.Contains(protocols, r.Protocol.ValueString()) {
		problems = append(problems, fmt.Sprintf("protocol must be one of %s, got %q.", strings.Join(protocols, ", "), r.Protocol.ValueString()))
	}
	if known(r.CIDR) && !isIPv4CIDR(r.CIDR.ValueString()) {
		problems = append(problems, fmt.Sprintf("cidr must be an IPv4 CIDR such as 10.0.0.0/8 or 0.0.0.0/0, got %q.", r.CIDR.ValueString()))
	}
	if msg := validatePortRange(r.Protocol, r.PortRange); msg != "" {
		problems = append(problems, msg)
	}
	return problems
}

func known(v types.String) bool { return !v.IsNull() && !v.IsUnknown() }

func isIPv4CIDR(s string) bool {
	p, err := netip.ParsePrefix(s)
	return err == nil && p.Addr().Is4()
}

// validatePortRange enforces the port_range rules. Omitting it means all ports.
// icmp and all have no ports, so a value is an error there. An explicit empty
// string is rejected: it would read back as null and cause a permanent diff.
func validatePortRange(protocol, portRange types.String) string {
	if portRange.IsNull() || portRange.IsUnknown() {
		return ""
	}
	value := portRange.ValueString()
	if value == "" {
		return "port_range must not be an empty string. Omit it to mean all ports (or for icmp)."
	}
	if known(protocol) && (protocol.ValueString() == "icmp" || protocol.ValueString() == "all") {
		return fmt.Sprintf("port_range must be omitted for protocol %q.", protocol.ValueString())
	}

	first, last, isRange := strings.Cut(value, "-")
	lo, errLo := parsePort(first)
	hi, errHi := lo, errLo
	if isRange {
		hi, errHi = parsePort(last)
	}
	if errLo != nil || errHi != nil || lo > hi {
		return fmt.Sprintf("port_range must be a port from 1 to 65535 or a range such as 8000-8080, got %q.", value)
	}
	return ""
}

func parsePort(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != s {
		return 0, fmt.Errorf("invalid port %q", s)
	}
	return n, nil
}
