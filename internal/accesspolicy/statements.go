package accesspolicy

import (
	"context"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// StatementModel is one statement block. Actions, resources and source_ip
// are sets: their order means nothing, so a reordered list never shows a
// change.
type StatementModel struct {
	Sid        types.String     `tfsdk:"sid"`
	Effect     types.String     `tfsdk:"effect"`
	Actions    types.Set        `tfsdk:"actions"`
	Resources  types.Set        `tfsdk:"resources"`
	Conditions *ConditionsModel `tfsdk:"conditions"`
}

// ConditionsModel is a statement's conditions. A null MFA is no condition;
// false on a deny means "only without MFA".
type ConditionsModel struct {
	SourceIP types.Set  `tfsdk:"source_ip"`
	MFA      types.Bool `tfsdk:"mfa"`
}

// Known reports whether every value in the blocks is known, so they can be
// checked or sent. A plan built from other resources may hold unknowns.
func Known(statements []StatementModel) bool {
	for _, s := range statements {
		if s.Sid.IsUnknown() || s.Effect.IsUnknown() || !setKnown(s.Actions) || !setKnown(s.Resources) {
			return false
		}
		if c := s.Conditions; c != nil && (!setKnown(c.SourceIP) || c.MFA.IsUnknown()) {
			return false
		}
	}
	return true
}

func setKnown(s types.Set) bool {
	if s.IsUnknown() {
		return false
	}
	for _, e := range s.Elements() {
		if e.IsUnknown() {
			return false
		}
	}
	return true
}

// ToAPI turns statement blocks into API statements. Call it only when Known.
func ToAPI(ctx context.Context, statements []StatementModel) ([]client.PolicyStatement, diag.Diagnostics) {
	var diags diag.Diagnostics
	out := make([]client.PolicyStatement, 0, len(statements))
	for _, s := range statements {
		st := client.PolicyStatement{Sid: s.Sid.ValueString(), Effect: s.Effect.ValueString()}
		diags.Append(s.Actions.ElementsAs(ctx, &st.Actions, false)...)
		diags.Append(s.Resources.ElementsAs(ctx, &st.Resources, false)...)
		slices.Sort(st.Actions)
		slices.Sort(st.Resources)
		if c := s.Conditions; c != nil && (!c.SourceIP.IsNull() || !c.MFA.IsNull()) {
			st.Conditions = &client.PolicyConditions{MFA: c.MFA.ValueBoolPointer()}
			if !c.SourceIP.IsNull() {
				st.Conditions.SourceIP = []string{}
				diags.Append(c.SourceIP.ElementsAs(ctx, &st.Conditions.SourceIP, false)...)
				slices.Sort(st.Conditions.SourceIP)
			}
		}
		out = append(out, st)
	}
	return out, diags
}

// FromAPI turns API statements into statement blocks.
func FromAPI(ctx context.Context, statements []client.PolicyStatement) ([]StatementModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	out := make([]StatementModel, 0, len(statements))
	for _, s := range statements {
		actions, d := types.SetValueFrom(ctx, types.StringType, nonNil(s.Actions))
		diags.Append(d...)
		resources, d := types.SetValueFrom(ctx, types.StringType, nonNil(s.Resources))
		diags.Append(d...)
		m := StatementModel{Sid: optionalString(s.Sid), Effect: types.StringValue(s.Effect), Actions: actions, Resources: resources}
		if c := s.Conditions; c != nil && (len(c.SourceIP) > 0 || c.MFA != nil) {
			cm := &ConditionsModel{SourceIP: types.SetNull(types.StringType), MFA: types.BoolPointerValue(c.MFA)}
			if len(c.SourceIP) > 0 {
				cm.SourceIP, d = types.SetValueFrom(ctx, types.StringType, c.SourceIP)
				diags.Append(d...)
			}
			m.Conditions = cm
		}
		out = append(out, m)
	}
	return out, diags
}

// ProblemPath is where a problem belongs: the statement attribute (blocks
// named blocksAttr), or the attribute itself for the list.
func ProblemPath(blocksAttr string, p Problem) path.Path {
	root := path.Root(blocksAttr)
	if p.Statement < 0 {
		return root
	}
	at := root.AtListIndex(p.Statement)
	switch p.Field {
	case "":
		return at
	case "conditions.source_ip":
		return at.AtName("conditions").AtName("source_ip")
	default:
		return at.AtName(p.Field)
	}
}

// ObjectType is the type of one statement, for computed lists of
// statements in data sources.
var ObjectType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"sid":       types.StringType,
	"effect":    types.StringType,
	"actions":   types.SetType{ElemType: types.StringType},
	"resources": types.SetType{ElemType: types.StringType},
	"conditions": types.ObjectType{AttrTypes: map[string]attr.Type{
		"source_ip": types.SetType{ElemType: types.StringType},
		"mfa":       types.BoolType,
	}},
}}

// Descriptions shared by every schema that shows statements.
const (
	SidDescription        = "An optional label for the statement, unique within the policy: 1 to 64 letters, digits, '_' or '-'."
	EffectDescription     = "\"allow\" or \"deny\". A matching deny always wins, for admins and members alike; owners are never limited by a policy."
	ActionsDescription    = "1 to 50 actions, such as \"instances:Stop\", or wildcards in the service or the verb: \"instances:*\", \"*:Read\", \"*\". The pantechdynamics_iam_actions data source lists every action."
	ResourcesDescription  = "1 to 50 resources: \"*\", a resource id such as \"vm_01JD...\", or \"name:<pattern>\" matched against the resource's name (\"*\" any run of characters, \"?\" one character, case-insensitive), such as \"name:staging-*\"."
	ConditionsDescription = "Conditions that must all hold for the statement to apply. Leave it out for none."
	SourceIPDescription   = "The request must come from one of these 1 to 20 IPv4 or IPv6 CIDR blocks (a single address is its /32 or /128)."
	MFADescription        = "true: the statement applies only to a session signed in with MFA, never to an API key. false on a deny: the deny applies only without MFA (\"nothing without MFA\"). false on an allow is the same as leaving it out."
)

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func optionalString(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}
