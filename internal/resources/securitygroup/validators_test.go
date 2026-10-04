package securitygroup

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func rule(direction, protocol, port, cidr string) ruleModel {
	m := ruleModel{
		Direction: types.StringValue(direction), Protocol: types.StringValue(protocol),
		PortRange: types.StringNull(), CIDR: types.StringValue(cidr),
	}
	if port != "" {
		m.PortRange = types.StringValue(port)
	}
	return m
}

func TestValidateRule(t *testing.T) {
	tests := []struct {
		name string
		rule ruleModel
		want string // substring of the expected problem, "" for none
	}{
		{"tcp single port", rule("ingress", "tcp", "22", "10.0.0.0/8"), ""},
		{"tcp range", rule("egress", "udp", "8000-8080", "0.0.0.0/0"), ""},
		{"tcp all ports", rule("ingress", "tcp", "", "10.0.0.0/8"), ""},
		{"icmp", rule("ingress", "icmp", "", "0.0.0.0/0"), ""},
		{"all", rule("egress", "all", "", "0.0.0.0/0"), ""},
		{"max port", rule("ingress", "tcp", "65535", "10.0.0.0/8"), ""},
		{"bad direction", rule("sideways", "tcp", "22", "10.0.0.0/8"), "direction"},
		{"bad protocol", rule("ingress", "gre", "", "10.0.0.0/8"), "protocol"},
		{"bad cidr", rule("ingress", "tcp", "22", "nope"), "cidr"},
		{"ipv6 cidr", rule("ingress", "tcp", "22", "::/0"), "cidr"},
		{"cidr without mask", rule("ingress", "tcp", "22", "10.0.0.1"), "cidr"},
		{"port zero", rule("ingress", "tcp", "0", "10.0.0.0/8"), "port_range"},
		{"port too big", rule("ingress", "tcp", "65536", "10.0.0.0/8"), "port_range"},
		{"port not a number", rule("ingress", "tcp", "ssh", "10.0.0.0/8"), "port_range"},
		{"reversed range", rule("ingress", "tcp", "90-80", "10.0.0.0/8"), "port_range"},
		{"range with bad end", rule("ingress", "tcp", "80-99999", "10.0.0.0/8"), "port_range"},
		{"leading zero", rule("ingress", "tcp", "022", "10.0.0.0/8"), "port_range"},
		{"icmp with port", rule("ingress", "icmp", "22", "10.0.0.0/8"), "omitted"},
		{"all with port", rule("ingress", "all", "22", "10.0.0.0/8"), "omitted"},
		{
			"explicit empty port_range",
			ruleModel{Direction: types.StringValue("ingress"), Protocol: types.StringValue("tcp"), PortRange: types.StringValue(""), CIDR: types.StringValue("10.0.0.0/8")},
			"empty string",
		},
		{
			"unknown values are skipped",
			ruleModel{Direction: types.StringUnknown(), Protocol: types.StringUnknown(), PortRange: types.StringUnknown(), CIDR: types.StringUnknown()},
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			problems := validateRule(tt.rule)
			if tt.want == "" {
				if len(problems) != 0 {
					t.Fatalf("problems = %v", problems)
				}
				return
			}
			if len(problems) == 0 || !strings.Contains(strings.Join(problems, " "), tt.want) {
				t.Fatalf("problems = %v, want one mentioning %q", problems, tt.want)
			}
		})
	}
}

func setOf(t *testing.T, rules ...ruleModel) types.Set {
	t.Helper()
	set, diags := types.SetValueFrom(ctx, ruleType, rules)
	if diags.HasError() {
		t.Fatal(diags)
	}
	return set
}

func TestRulesValidator(t *testing.T) {
	good := rule("ingress", "tcp", "22", "10.0.0.0/8")
	tests := []struct {
		name    string
		value   types.Set
		wantErr bool
	}{
		{"one valid rule", setOf(t, good), false},
		{"two valid rules", setOf(t, good, rule("ingress", "icmp", "", "0.0.0.0/0")), false},
		{"empty set", types.SetValueMust(ruleType, []attr.Value{}), true},
		{"one invalid rule", setOf(t, good, rule("ingress", "tcp", "99999", "10.0.0.0/8")), true},
		{"null is left to the required check", types.SetNull(ruleType), false},
		{"unknown is skipped", types.SetUnknown(ruleType), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resp validator.SetResponse
			rulesValidator{}.ValidateSet(ctx, validator.SetRequest{ConfigValue: tt.value}, &resp)
			if resp.Diagnostics.HasError() != tt.wantErr {
				t.Fatalf("diags = %v", resp.Diagnostics)
			}
		})
	}
}

func TestNameValidator(t *testing.T) {
	tests := []struct {
		name    string
		value   types.String
		wantErr bool
	}{
		{"short", types.StringValue("web"), false},
		{"exactly 255", types.StringValue(strings.Repeat("a", 255)), false},
		{"256 is too long", types.StringValue(strings.Repeat("a", 256)), true},
		{"the 300 that made the backend answer 500", types.StringValue(strings.Repeat("a", 300)), true},
		{"empty", types.StringValue(""), true},
		{"null", types.StringNull(), false},
		{"unknown", types.StringUnknown(), false},
		{"multibyte counts characters", types.StringValue(strings.Repeat("é", 255)), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resp validator.StringResponse
			nameValidator{}.ValidateString(ctx, validator.StringRequest{ConfigValue: tt.value}, &resp)
			if resp.Diagnostics.HasError() != tt.wantErr {
				t.Fatalf("diags = %v", resp.Diagnostics)
			}
		})
	}
}
