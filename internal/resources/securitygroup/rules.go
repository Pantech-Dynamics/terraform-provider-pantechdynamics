package securitygroup

import (
	"context"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// ruleModel is one element of the rules set.
type ruleModel struct {
	Direction types.String `tfsdk:"direction"`
	Protocol  types.String `tfsdk:"protocol"`
	PortRange types.String `tfsdk:"port_range"`
	CIDR      types.String `tfsdk:"cidr"`
}

// ruleType is the element type of the rules set, needed to build a set value.
var ruleType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"direction":  types.StringType,
	"protocol":   types.StringType,
	"port_range": types.StringType,
	"cidr":       types.StringType,
}}

// rulesFromSet converts the planned set into API rules. An omitted port_range
// is sent as the empty string, which is how the API says "all ports".
func rulesFromSet(ctx context.Context, set types.Set) ([]client.SecurityGroupRule, diag.Diagnostics) {
	var models []ruleModel
	diags := set.ElementsAs(ctx, &models, false)

	rules := make([]client.SecurityGroupRule, 0, len(models))
	for _, m := range models {
		rules = append(rules, client.SecurityGroupRule{
			Direction: m.Direction.ValueString(),
			Protocol:  m.Protocol.ValueString(),
			PortRange: m.PortRange.ValueString(), // null reads as ""
			CIDR:      m.CIDR.ValueString(),
		})
	}
	return rules, diags
}

// rulesToSet converts API rules into the state set. The API's empty port_range
// becomes null, so a rule written without port_range stays equal to what is read
// back. The schema validator rejects an explicit empty string for that reason.
func rulesToSet(ctx context.Context, rules []client.SecurityGroupRule) (types.Set, diag.Diagnostics) {
	models := make([]ruleModel, 0, len(rules))
	for _, r := range rules {
		portRange := types.StringNull()
		if r.PortRange != "" {
			portRange = types.StringValue(r.PortRange)
		}
		models = append(models, ruleModel{
			Direction: types.StringValue(r.Direction),
			Protocol:  types.StringValue(r.Protocol),
			PortRange: portRange,
			CIDR:      types.StringValue(r.CIDR),
		})
	}
	return types.SetValueFrom(ctx, ruleType, models)
}

// sameRules reports whether two rule lists hold the same rules, ignoring order.
// Rules have no ids, so identity is the whole rule.
func sameRules(a, b []client.SecurityGroupRule) bool {
	return slices.Equal(ruleKeys(a), ruleKeys(b))
}

func ruleKeys(rules []client.SecurityGroupRule) []string {
	keys := make([]string, 0, len(rules))
	for _, r := range rules {
		keys = append(keys, strings.Join([]string{r.Direction, r.Protocol, r.PortRange, r.CIDR}, "|"))
	}
	slices.Sort(keys)
	return keys
}
